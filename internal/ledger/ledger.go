// Package ledger is cats's command history: one durable record per command a
// shell ran in any pane, on any host.
//
// A terminal's own scrollback answers "what did I just do" for exactly as long
// as the pane lives and nobody clears it, and it answers nothing at all about
// the other four panes or the other machine. The ledger is the same information
// kept as data — what ran, where, on which host, how long it took, whether it
// worked, and whether a human or an agent typed it.
//
// # What it is not
//
// It is not the shell's history file. Those record what was TYPED, per shell,
// per machine, with no cwd, no exit status, no duration and no notion of a pane
// — and an agent's commands are absent from them entirely, because an agent does
// not type into a shell that writes one. The two overlap only in the `cmd`
// field.
//
// # Storage
//
// btypedb, a typed embedded KV store: the whole dataset in a B-tree in memory
// with a write-ahead log on disk. Keys are time-ordered, which makes the two
// questions this is actually asked — "what did I run recently" and "what did I
// run recently HERE" — a backward range scan and a filtered backward range
// scan, rather than a query planner.
//
// Retention is a count, enforced on write. That bound is what keeps a scan
// honest: the alternative is a query that walks a million rows and a
// scan-budget that silently truncates the answer, which is the same bug as
// having no bound but harder to see.
package ledger

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rohanthewiz/btypedb"
)

// Entry is one recorded command. It is JSON-encoded into the store, so every
// field is additive: a field added later reads as its zero value out of records
// written before it, which is the same discipline internal/persist keeps.
type Entry struct {
	// At is when the command started running, on the machine that ran it. It is
	// the primary sort key and the identity half of the store key.
	At time.Time `json:"at"`
	// Host and Pane say where. Handle is the pane's public label at the time
	// ("w1:p3"); it is stored rather than resolved later because a pane that has
	// since closed still has to be nameable in a history.
	Host   string `json:"host"`
	Pane   uint32 `json:"pane"`
	Handle string `json:"handle,omitempty"`

	Cmd string `json:"cmd"`
	Cwd string `json:"cwd,omitempty"`
	// Exit is nil when the shell reported no status — a bare OSC 133;D, or a
	// command abandoned when its pane's shell was replaced. Deliberately
	// distinct from 0: "finished, status unknown" is true and "succeeded" is
	// not, and this is the field somebody filters "what failed" on.
	Exit       *int  `json:"exit,omitempty"`
	DurationMs int64 `json:"ms,omitempty"`

	// Origin is who ran it: "human", or the label of the agent that held the
	// pane when it started. It is the field that makes an agent's work
	// reviewable — "what did claude actually run while I was away" is not a
	// question any shell history can answer.
	Origin string `json:"origin,omitempty"`
}

// Failed reports whether the command is known to have failed. An unknown status
// is not a failure: see Exit.
func (e Entry) Failed() bool { return e.Exit != nil && *e.Exit != 0 }

// Query filters a listing. Every field is optional and they AND together; Limit
// bounds the answer and defaults to DefaultLimit.
//
// Contains is a plain case-insensitive substring over the command, not a regexp
// or a fuzzy match, because the caller doing the interesting matching is a
// palette that wants the whole recent list and will rank it itself — the same
// division path.list draws for directory completion.
type Query struct {
	Host     string
	Pane     uint32
	Cwd      string
	Contains string
	Failed   bool
	Limit    int
}

const (
	// DefaultLimit is what a listing returns when the caller names no bound. A
	// screenful for a palette, and small enough that the default answer is never
	// the expensive one.
	DefaultLimit = 100
	// MaxLimit caps a caller's own bound. The whole store fits in memory, so a
	// caller asking for all of it would get it — over a control socket, as one
	// JSON document.
	MaxLimit = 5000
	// DefaultRetention is how many records are kept. About a fortnight of heavy
	// use, and a few megabytes.
	DefaultRetention = 20000
)

// Ledger is the store. A nil *Ledger is a working no-op — every method is
// nil-safe — so a caller that could not open one holds it unconditionally and
// never branches, exactly as it holds a possibly-nil push bridge.
type Ledger struct {
	db        *btypedb.DB[string, Entry]
	retention int

	// mu guards count and the prune, which is the one operation that has to be
	// serialized against itself: two concurrent writes both deciding to trim
	// would trim twice.
	mu    sync.Mutex
	count int
}

// Open opens (or creates) the ledger at path. retention 0 means DefaultRetention.
func Open(path string, retention int) (*Ledger, error) {
	if retention <= 0 {
		retention = DefaultRetention
	}
	// SyncEverySecond, not the default SyncAlways: a ledger row is a record of
	// something that already happened, and fsyncing per command would put a disk
	// flush in the path of every prompt on every pane. Losing the last second of
	// history to a power cut costs a line in a list nobody had read yet.
	db, err := btypedb.Open(path, btypedb.StringCodec, btypedb.JSONCodec[Entry](),
		btypedb.WithSyncPolicy(btypedb.SyncEverySecond))
	if err != nil {
		return nil, fmt.Errorf("ledger %s: %w", path, err)
	}
	l := &Ledger{db: db, retention: retention}
	l.count = db.Len()
	return l, nil
}

// Close flushes and closes the store. Safe on nil and idempotent.
func (l *Ledger) Close() error {
	if l == nil || l.db == nil {
		return nil
	}
	return l.db.Close()
}

// Add records one command and trims the store back to its retention bound.
func (l *Ledger) Add(e Entry) error {
	if l == nil || l.db == nil {
		return nil
	}
	if err := l.db.Set(key(e), e); err != nil {
		return err
	}
	l.mu.Lock()
	l.count++
	over := l.count - l.retention
	l.mu.Unlock()
	if over > 0 {
		l.trim(over)
	}
	return nil
}

// trim deletes the n oldest records. Keys are time-ordered, so "oldest" is a
// prefix of the key space and one DeleteRange does it — no scan, no sort.
//
// It over-trims on purpose: taking a tenth of the retention at once turns a
// per-command delete into an occasional one, and the bound is a budget rather
// than a promise about the exact row count.
func (l *Ledger) trim(n int) {
	batch := n + l.retention/10
	var cutoff string
	i := 0
	for k := range l.db.Keys() {
		cutoff = k
		if i++; i >= batch {
			break
		}
	}
	if cutoff == "" {
		return
	}
	// The half-open range ends AT cutoff's successor, so the record at cutoff
	// goes too; "\x00" is the smallest byte that can follow it.
	removed, err := l.db.DeleteRange("", cutoff+"\x00")
	if err != nil {
		return
	}
	l.mu.Lock()
	l.count -= removed
	if l.count < 0 {
		l.count = 0
	}
	l.mu.Unlock()
}

// List answers a query, newest first.
//
// The scan runs inside a read transaction rather than over the DB directly.
// btypedb's DB-level iterators hold a read lock for the whole loop, and this
// loop is as long as the store is deep on a query that matches nothing — a
// transaction iterates its own O(1) snapshot lock-free, so a command finishing
// mid-listing is never blocked by somebody browsing their history.
func (l *Ledger) List(q Query) []Entry {
	if l == nil || l.db == nil {
		return nil
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	needle := strings.ToLower(q.Contains)
	out := make([]Entry, 0, min(limit, 64))
	_ = l.db.View(func(tx *btypedb.Tx[string, Entry]) error {
		for _, e := range tx.Backward() {
			if !matches(e, q, needle) {
				continue
			}
			out = append(out, e)
			if len(out) >= limit {
				break
			}
		}
		return nil
	})
	return out
}

// Len reports how many records are stored.
func (l *Ledger) Len() int {
	if l == nil || l.db == nil {
		return 0
	}
	return l.db.Len()
}

func matches(e Entry, q Query, needle string) bool {
	if q.Host != "" && e.Host != q.Host {
		return false
	}
	if q.Pane != 0 && e.Pane != q.Pane {
		return false
	}
	if q.Cwd != "" && e.Cwd != q.Cwd {
		return false
	}
	if q.Failed && !e.Failed() {
		return false
	}
	if needle != "" && !strings.Contains(strings.ToLower(e.Cmd), needle) {
		return false
	}
	return true
}

// key builds the store key: the start time to nanosecond precision, zero-padded
// so lexicographic order IS time order, then the host and pane to break ties.
//
// Two commands can genuinely start in the same nanosecond on two machines, and
// a key collision would silently overwrite one of them — which in a history is
// worse than any duplicate, because nothing about the result would look wrong.
func key(e Entry) string {
	return fmt.Sprintf("%019d/%s/%d", e.At.UnixNano(), e.Host, e.Pane)
}
