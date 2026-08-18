package ledger

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func openTest(t *testing.T, retention int) *Ledger {
	t.Helper()
	l, err := Open(filepath.Join(t.TempDir(), "ledger.db"), retention)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func entry(at time.Time, host string, pane uint32, cmd string) Entry {
	return Entry{At: at, Host: host, Pane: pane, Cmd: cmd, Cwd: "/tmp", Origin: "human"}
}

// Records come back newest first, which is the order every consumer wants and
// the order the key layout exists to give without a sort.
func TestListIsNewestFirst(t *testing.T) {
	l := openTest(t, 0)
	base := time.Unix(1_700_000_000, 0)
	for i, cmd := range []string{"first", "second", "third"} {
		if err := l.Add(entry(base.Add(time.Duration(i)*time.Second), "local", 1, cmd)); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	got := l.List(Query{})
	if len(got) != 3 {
		t.Fatalf("got %d entries, want 3", len(got))
	}
	if got[0].Cmd != "third" || got[2].Cmd != "first" {
		t.Errorf("order = %q %q %q", got[0].Cmd, got[1].Cmd, got[2].Cmd)
	}
}

// Every filter, and the fact that they AND together.
func TestListFilters(t *testing.T) {
	l := openTest(t, 0)
	base := time.Unix(1_700_000_000, 0)
	add := func(i int, host string, pane uint32, cmd, cwd string, exit *int) {
		e := entry(base.Add(time.Duration(i)*time.Second), host, pane, cmd)
		e.Cwd, e.Exit = cwd, exit
		if err := l.Add(e); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	zero, two := 0, 2
	add(0, "local", 1, "go test ./...", "/p/cats", &zero)
	add(1, "local", 2, "go build ./...", "/p/cats", &two)
	add(2, "devbox", 3, "GO test -race", "/p/other", &zero)
	add(3, "devbox", 3, "make", "/p/other", nil)

	cases := []struct {
		name string
		q    Query
		want []string
	}{
		{"host", Query{Host: "devbox"}, []string{"make", "GO test -race"}},
		{"pane", Query{Pane: 2}, []string{"go build ./..."}},
		{"cwd", Query{Cwd: "/p/cats"}, []string{"go build ./...", "go test ./..."}},
		{"contains is case-insensitive", Query{Contains: "go TEST"}, []string{"GO test -race", "go test ./..."}},
		{"failed", Query{Failed: true}, []string{"go build ./..."}},
		{"and", Query{Host: "local", Contains: "go"}, []string{"go build ./...", "go test ./..."}},
		{"limit", Query{Limit: 1}, []string{"make"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := l.List(c.q)
			if len(got) != len(c.want) {
				t.Fatalf("got %d entries %+v, want %v", len(got), cmds(got), c.want)
			}
			for i := range c.want {
				if got[i].Cmd != c.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i].Cmd, c.want[i])
				}
			}
		})
	}
}

// An unknown exit status is NOT a failure. It is the difference between
// "finished, status unknown" and "succeeded", and this is the field somebody
// filters "what failed" on.
func TestUnknownExitIsNotAFailure(t *testing.T) {
	l := openTest(t, 0)
	base := time.Unix(1_700_000_000, 0)
	e := entry(base, "local", 1, "abandoned")
	if err := l.Add(e); err != nil {
		t.Fatalf("add: %v", err)
	}
	if e.Failed() {
		t.Error("an entry with no exit status reports as failed")
	}
	if got := l.List(Query{Failed: true}); len(got) != 0 {
		t.Errorf("the failed filter matched an unknown status: %+v", cmds(got))
	}
}

// Retention is enforced on write, which is what keeps a backward scan bounded
// by design rather than by a scan budget that would truncate answers silently.
func TestRetentionTrimsTheOldest(t *testing.T) {
	l := openTest(t, 20)
	base := time.Unix(1_700_000_000, 0)
	for i := range 100 {
		if err := l.Add(entry(base.Add(time.Duration(i)*time.Second), "local", 1, "cmd"+strconv.Itoa(i))); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if n := l.Len(); n > 30 {
		t.Fatalf("kept %d entries for a retention of 20", n)
	}
	got := l.List(Query{Limit: MaxLimit})
	if len(got) == 0 || got[0].Cmd != "cmd99" {
		t.Fatalf("newest = %v; the trim took the wrong end", cmds(got)[:min(3, len(got))])
	}
	// And what survives is contiguous from the newest end.
	for i := 1; i < len(got); i++ {
		if got[i].At.After(got[i-1].At) {
			t.Fatalf("order broke after a trim at %d", i)
		}
	}
}

// Two commands starting in the same nanosecond on two machines must both
// survive: a key collision in a history is worse than any duplicate, because
// nothing about the result would look wrong.
func TestKeysDoNotCollideAcrossHosts(t *testing.T) {
	l := openTest(t, 0)
	at := time.Unix(1_700_000_000, 12345)
	if err := l.Add(entry(at, "local", 1, "here")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := l.Add(entry(at, "devbox", 2, "there")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if n := l.Len(); n != 2 {
		t.Fatalf("stored %d entries, want 2", n)
	}
}

// The store survives a reopen — it is a write-ahead log, and the point of the
// ledger is that it outlives the pane.
func TestReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ledger.db")
	l, err := Open(path, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := l.Add(entry(time.Unix(1_700_000_000, 0), "local", 1, "survivor")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	again, err := Open(path, 0)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()
	got := again.List(Query{})
	if len(got) != 1 || got[0].Cmd != "survivor" {
		t.Fatalf("after reopen: %+v", cmds(got))
	}
}

// A nil ledger is a working no-op, so a catway that could not open one holds it
// unconditionally and never branches.
func TestNilLedgerIsANoOp(t *testing.T) {
	var l *Ledger
	if err := l.Add(entry(time.Now(), "local", 1, "x")); err != nil {
		t.Errorf("Add on a nil ledger: %v", err)
	}
	if got := l.List(Query{}); got != nil {
		t.Errorf("List on a nil ledger = %+v", got)
	}
	if l.Len() != 0 || l.Close() != nil {
		t.Error("Len/Close on a nil ledger misbehaved")
	}
}

// A caller that names no bound gets a screenful, not the store: the default
// answer must never be the expensive one, since this list crosses a control
// socket as one JSON document.
func TestLimitDefaults(t *testing.T) {
	l := openTest(t, 0)
	base := time.Unix(1_700_000_000, 0)
	for i := range DefaultLimit + 25 {
		if err := l.Add(entry(base.Add(time.Duration(i)*time.Second), "local", 1, "cmd"+strconv.Itoa(i))); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if got := l.List(Query{}); len(got) != DefaultLimit {
		t.Errorf("unbounded query returned %d entries, want the %d default", len(got), DefaultLimit)
	}
	if got := l.List(Query{Limit: 5}); len(got) != 5 {
		t.Errorf("Limit 5 returned %d entries", len(got))
	}
	// An outsized bound is clamped rather than honoured.
	if got := l.List(Query{Limit: MaxLimit + 1000}); len(got) != DefaultLimit+25 {
		t.Errorf("clamped query returned %d entries, want everything stored (%d)", len(got), DefaultLimit+25)
	}
}

func cmds(es []Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Cmd
	}
	return out
}
