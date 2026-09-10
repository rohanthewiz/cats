//go:build darwin

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The startup log: what the launcher is doing, in order, and how long each step
// has taken so far.
//
// Why it exists: everything between a double-click and a window is invisible.
// The launcher asks the user's login shell for a PATH (running their whole rc
// chain), spawns two daemons, waits for a TCP listener, then waits for a page
// to load and for that page's WebSocket to bring a session back. Any one of
// those can be slow, and any one can hang outright — an rc file that blocks, a
// cathost that never comes up, a catway wedged on a stale socket. All the user
// sees is a Dock icon that never becomes a window.
//
// So each step is recorded as it starts and as it ends, the record is pushed to
// the splash window (splash_darwin.go) whenever it changes, and the splash ticks
// the running step's elapsed time on screen. A hang stops being a mystery: it is
// the last line, still running, with a number going up.
//
//	boot.begin("starting catway") ──► entry{state: running, start: +412ms}
//	     │                                   │ pushed to the splash (coalesced)
//	     │                                   ▼
//	     └─ boot.ok(id) ──────────────► entry{state: ok, end: +503ms}
//
// The same record is written to boot.log beside app.json when startup ends, so
// a launch that was merely slow can still be looked at afterwards.
//
// The log is a package-level singleton (boot) because its writers are spread
// across the whole launcher — main, supervise, shellenv, the cgo callbacks the
// page reaches us through — and threading a handle through all of them would
// buy nothing: there is exactly one startup per process.

// Entry states. A step is "running" until it lands on one of the other three;
// "warn" is a step that did not succeed but did not stop the launch either (the
// PATH probe is the one that matters — its failure costs a plugin build, not
// the app). "note" is not a step at all: it is one line of context, most often
// a line a daemon wrote to its own stderr.
const (
	bootRunning = "running"
	bootOK      = "ok"
	bootWarn    = "warn"
	bootFail    = "fail"
	bootNote    = "note"
)

// bootMaxEntries caps the log so a chatty daemon cannot grow it without bound —
// the whole snapshot is re-pushed to the splash on every change, so its size is
// paid repeatedly. Steps are never dropped (there are a dozen at most); notes
// are, oldest first, because the useful ones are the ones next to whatever is
// happening now.
const bootMaxEntries = 400

// bootPushDelay coalesces pushes to the splash. Daemon output arrives in bursts
// of many lines, and each push re-renders the whole list; 40ms is below the
// threshold where the log stops looking live and well above the burst.
const bootPushDelay = 40 * time.Millisecond

// bootEntry is one line of the log. Times are milliseconds since the log was
// created (process start, near enough — boot is a package-level var), which
// keeps the wire format independent of the clock the splash reads.
type bootEntry struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Detail string `json:"detail,omitempty"`
	State  string `json:"state"`
	Start  int64  `json:"start"`
	End    int64  `json:"end,omitempty"` // 0 while the step is still running
}

// bootSnapshot is what the splash renders: the entries, the clock they are
// measured against, and whether startup is over. Now lets the page work out its
// own offset from our clock, so it can tick a running step between pushes
// instead of waiting for one.
type bootSnapshot struct {
	Now     int64       `json:"now"`
	Failed  bool        `json:"failed"`
	Done    bool        `json:"done"`
	LogPath string      `json:"log_path,omitempty"`
	Entries []bootEntry `json:"entries"`
}

// bootLog is the record itself. Every method is safe from any goroutine: the
// writers are the main thread, the goroutine that supervises the backend, the
// io.Writer goroutines exec starts for daemon output, and cgo callbacks from
// the page.
type bootLog struct {
	mu           sync.Mutex
	t0           time.Time
	nextID       int
	entries      []bootEntry
	failed       bool
	done         bool
	flushPending bool         // a coalesced push is already scheduled
	sink         func([]byte) // where snapshots go; nil until the splash opens
	// logPath overrides where the transcript is written. Empty means the
	// default beside app.json; a test points it at a temp file so running the
	// suite cannot clobber the record of the user's last real launch.
	logPath string
}

// boot is the launcher's startup log. Created at package init so t0 is as close
// to process start as we can get without threading a value out of main.
var boot = &bootLog{t0: time.Now(), nextID: 1}

// sinceStart is the log's clock: milliseconds since t0.
func (b *bootLog) sinceStart(t time.Time) int64 {
	return t.Sub(b.t0).Milliseconds()
}

// begin records a step that has just started and returns its id. The id is a
// stable handle rather than an index because trimming drops entries from the
// middle of the slice.
func (b *bootLog) begin(name string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextID
	b.nextID++
	b.entries = append(b.entries, bootEntry{
		ID:    id,
		Name:  name,
		State: bootRunning,
		Start: b.sinceStart(time.Now()),
	})
	log.Printf("boot: %s…", name)
	b.trimLocked()
	b.touchLocked()
	return id
}

// ok closes a step successfully. okDetail is the same with something to show
// beside it — a resolved path, a port, a pid: the values you want when the same
// launch goes wrong on someone else's machine.
func (b *bootLog) ok(id int)                      { b.settle(id, bootOK, "") }
func (b *bootLog) okDetail(id int, detail string) { b.settle(id, bootOK, detail) }

// warn closes a step that did not do what it set out to do but did not stop the
// launch either.
func (b *bootLog) warn(id int, detail string) { b.settle(id, bootWarn, detail) }

// fail closes a step that stopped the launch. The log stays on screen after
// this — a failed startup is exactly when the user needs to read it.
func (b *bootLog) fail(id int, err error) {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	b.mu.Lock()
	b.failed = true
	b.mu.Unlock()
	b.settle(id, bootFail, detail)
	b.writeTranscript()
}

// settle moves a step out of "running".
func (b *bootLog) settle(id int, state, detail string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.entries {
		if b.entries[i].ID != id {
			continue
		}
		e := &b.entries[i]
		e.State = state
		e.End = b.sinceStart(time.Now())
		if detail != "" {
			e.Detail = detail
		}
		log.Printf("boot: %s — %s (%dms)%s", e.Name, state, e.End-e.Start, formatDetail(detail))
		break
	}
	b.touchLocked()
}

// note files one line of context: a daemon's own output, or something the
// launcher wants on the record without it being a step of its own. src labels
// where it came from ("catway", "cathost", "path", "ui").
func (b *bootLog) note(src, line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	// Once startup is over the splash is gone and nobody is reading; the daemons
	// keep writing for the life of the session, so stop accumulating.
	if b.done {
		return
	}
	now := b.sinceStart(time.Now())
	b.entries = append(b.entries, bootEntry{
		ID:     b.nextID,
		Name:   src,
		Detail: line,
		State:  bootNote,
		Start:  now,
		End:    now,
	})
	b.nextID++
	b.trimLocked()
	b.touchLocked()
}

// finish marks startup complete: the UI is up and the log stops taking notes.
// The transcript is written here so a launch that worked but took a while is
// still on disk to look at.
func (b *bootLog) finish() {
	b.mu.Lock()
	if b.done {
		b.mu.Unlock()
		return
	}
	b.done = true
	total := b.sinceStart(time.Now())
	b.mu.Unlock()
	log.Printf("boot: ready in %dms", total)
	b.touch()
	b.writeTranscript()
}

// elapsedMs is how long the launch has been going. Read by the startup window's
// own policy (when to come back in front), not by the log itself.
func (b *bootLog) elapsedMs() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sinceStart(time.Now())
}

// isDone reports whether startup has finished — read by the daemon output tap,
// which is on a hot path for the rest of the session.
func (b *bootLog) isDone() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.done
}

// trimLocked enforces bootMaxEntries by dropping the oldest notes. Steps are
// kept whatever happens: they are the spine of the log, and a startup that
// produced 400 lines of daemon output is precisely the one whose steps you want
// to be able to read.
func (b *bootLog) trimLocked() {
	over := len(b.entries) - bootMaxEntries
	if over <= 0 {
		return
	}
	out := b.entries[:0]
	for _, e := range b.entries {
		if over > 0 && e.State == bootNote {
			over--
			continue
		}
		out = append(out, e)
	}
	b.entries = out
}

// --- pushing to the splash -------------------------------------------------

// setSink installs the surface that renders the log (the splash window), and
// hands it the backlog immediately: the log starts filling before there is a
// window to show it in, and those first entries — reading app.json, probing the
// shell — are the ones a hang before the window would otherwise hide.
func (b *bootLog) setSink(fn func([]byte)) {
	b.mu.Lock()
	b.sink = fn
	b.mu.Unlock()
	if fn != nil {
		b.flush()
	}
}

func (b *bootLog) touch() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.touchLocked()
}

// touchLocked schedules a coalesced push. Called with b.mu held; the timer runs
// flush on its own goroutine, which takes the lock then.
func (b *bootLog) touchLocked() {
	if b.sink == nil || b.flushPending {
		return
	}
	b.flushPending = true
	time.AfterFunc(bootPushDelay, b.flush)
}

func (b *bootLog) flush() {
	b.mu.Lock()
	b.flushPending = false
	sink := b.sink
	snap := b.snapshotLocked()
	b.mu.Unlock()
	if sink == nil {
		return
	}
	data, err := json.Marshal(snap)
	if err != nil { // a marshal failure here is not worth failing a launch over
		log.Printf("boot: could not encode the startup log: %v", err)
		return
	}
	sink(data)
}

// snapshot copies the log out for rendering.
func (b *bootLog) snapshot() bootSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snapshotLocked()
}

func (b *bootLog) snapshotLocked() bootSnapshot {
	entries := make([]bootEntry, len(b.entries))
	copy(entries, b.entries)
	return bootSnapshot{
		Now:     b.sinceStart(time.Now()),
		Failed:  b.failed,
		Done:    b.done,
		LogPath: b.transcriptPath(),
		Entries: entries,
	}
}

// --- the transcript on disk ------------------------------------------------

// bootLogFile is the transcript's name, written beside app.json.
const bootLogFile = "boot.log"

// bootLogPath is the default transcript location, or "" when the app-data dir
// cannot be located (in which case the transcript is simply skipped — a
// launcher that refused to start because it could not write a log would be a
// poor trade).
func bootLogPath() string {
	dir, err := appDataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, bootLogFile)
}

// transcriptPath is where this log writes itself.
func (b *bootLog) transcriptPath() string {
	if b.logPath != "" {
		return b.logPath
	}
	return bootLogPath()
}

// transcript renders the log as text: one line per entry, elapsed time first.
func (b *bootLog) transcript() string {
	snap := b.snapshot()
	var sb strings.Builder
	fmt.Fprintf(&sb, "cats startup log — %s\n\n", time.Now().Format(time.RFC3339))
	for _, e := range snap.Entries {
		switch e.State {
		case bootNote:
			fmt.Fprintf(&sb, "%8s  ·        %s: %s\n", elapsed(e.Start), e.Name, e.Detail)
		case bootRunning:
			fmt.Fprintf(&sb, "%8s  running  %s%s\n", elapsed(e.Start), e.Name, formatDetail(e.Detail))
		default:
			fmt.Fprintf(&sb, "%8s  %-7s  %s (%dms)%s\n",
				elapsed(e.Start), e.State, e.Name, e.End-e.Start, formatDetail(e.Detail))
		}
	}
	if snap.Failed {
		fmt.Fprintf(&sb, "\nstartup failed after %s\n", elapsed(snap.Now))
	} else if snap.Done {
		fmt.Fprintf(&sb, "\nready in %s\n", elapsed(snap.Now))
	}
	return sb.String()
}

// writeTranscript saves the log beside app.json, replacing the previous
// launch's. Best-effort by design: it is called from the failure path, where
// the error we already have is the one worth reporting.
func (b *bootLog) writeTranscript() {
	b.mu.Lock()
	path := b.transcriptPath()
	b.mu.Unlock()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.Printf("boot: could not create %s: %v", filepath.Dir(path), err)
		return
	}
	if err := os.WriteFile(path, []byte(b.transcript()), 0o600); err != nil {
		log.Printf("boot: could not write %s: %v", path, err)
	}
}

// elapsed renders a millisecond offset the way the splash does, so the
// transcript and the window read the same.
func elapsed(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

func formatDetail(detail string) string {
	if detail == "" {
		return ""
	}
	return " — " + detail
}

// --- daemon output ---------------------------------------------------------

// bootTap turns a daemon's stdout/stderr into note entries, one per line, while
// still passing everything through to our own stdio (see command()).
//
// This is the difference between "waiting for the catway to accept connections
// — 9.4s" and knowing WHY: the catway's own log says what it is retrying. The
// tap is only interesting during startup, so once the log is finished it
// degrades to a plain counter and stops splitting lines at all — the daemons
// write for the life of the session and none of that is boot.
type bootTap struct {
	src string
	log *bootLog // the log to file lines in; a field so tests need no singleton
	mu  sync.Mutex
	buf []byte
}

// bootTapMaxLine bounds the partial-line buffer, so output with no newline in
// it (a progress bar, a binary splat) cannot grow the tap without limit.
const bootTapMaxLine = 8 << 10

func newBootTap(src string) *bootTap { return &bootTap{src: src, log: boot} }

func (t *bootTap) Write(p []byte) (int, error) {
	if t.log.isDone() {
		return len(p), nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	for {
		i := indexByte(t.buf, '\n')
		if i < 0 {
			break
		}
		t.log.note(t.src, string(t.buf[:i]))
		t.buf = t.buf[i+1:]
	}
	// A line that never ends still deserves to be seen once.
	if len(t.buf) > bootTapMaxLine {
		t.log.note(t.src, string(t.buf))
		t.buf = t.buf[:0]
	}
	return len(p), nil
}

// indexByte keeps the loop above readable without pulling bytes into the
// import list for one call.
func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}
