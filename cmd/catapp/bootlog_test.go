//go:build darwin

package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The startup window is Objective-C and cannot be tested here. What can be —
// and what carries the diagnostic value — is the record it renders: that a step
// is still marked running while it is running (the whole point: a hang is a
// step with no end), that a failure is recorded against the step that failed,
// that daemon output lands beside the step it explains, and that a noisy
// daemon cannot push the steps out of the log.

// newTestLog is a log of its own — never the package singleton, which the rest
// of the launcher writes to — with its transcript pointed at a temp file so a
// test run cannot overwrite the record of the user's last real launch.
func newTestLog(t *testing.T) *bootLog {
	t.Helper()
	return &bootLog{t0: time.Now(), nextID: 1, logPath: filepath.Join(t.TempDir(), bootLogFile)}
}

func TestBootLogTracksAStepThroughItsStates(t *testing.T) {
	b := newTestLog(t)

	id := b.begin("starting catway")
	snap := b.snapshot()
	if len(snap.Entries) != 1 {
		t.Fatalf("after begin: %d entries, want 1", len(snap.Entries))
	}
	// Running, and with no end: this is what the window ticks a clock against.
	if got := snap.Entries[0].State; got != bootRunning {
		t.Errorf("state = %q, want %q", got, bootRunning)
	}
	if snap.Entries[0].End != 0 {
		t.Errorf("a running step has an end time (%d); nothing has finished yet", snap.Entries[0].End)
	}

	b.okDetail(id, "pid 4211")
	snap = b.snapshot()
	if got := snap.Entries[0].State; got != bootOK {
		t.Errorf("state after ok = %q, want %q", got, bootOK)
	}
	if got := snap.Entries[0].Detail; got != "pid 4211" {
		t.Errorf("detail = %q, want the one passed to okDetail", got)
	}
	if snap.Failed || snap.Done {
		t.Errorf("a completed step is not a completed startup: failed=%v done=%v", snap.Failed, snap.Done)
	}
}

func TestBootLogFailMarksTheStepAndTheLaunch(t *testing.T) {
	b := newTestLog(t)
	ok := b.begin("starting cathost")
	b.ok(ok)
	bad := b.begin("waiting for the catway")
	b.fail(bad, errors.New("did not become ready within 10s"))

	snap := b.snapshot()
	if !snap.Failed {
		t.Error("the launch is not marked failed")
	}
	if got := snap.Entries[0].State; got != bootOK {
		t.Errorf("the earlier step changed state to %q; a later failure must not rewrite history", got)
	}
	if got := snap.Entries[1].State; got != bootFail {
		t.Errorf("state = %q, want %q", got, bootFail)
	}
	if !strings.Contains(snap.Entries[1].Detail, "10s") {
		t.Errorf("detail = %q, want the error text", snap.Entries[1].Detail)
	}
}

func TestBootLogWarnDoesNotFailTheLaunch(t *testing.T) {
	// The PATH probe is the case: it can fail without stopping anything, and a
	// red startup window for a launch that is going fine would be a lie.
	b := newTestLog(t)
	id := b.begin("reading PATH from the login shell")
	b.warn(id, "the login shell did not answer")

	snap := b.snapshot()
	if snap.Failed {
		t.Error("a warning failed the launch")
	}
	if got := snap.Entries[0].State; got != bootWarn {
		t.Errorf("state = %q, want %q", got, bootWarn)
	}
}

func TestBootLogNotesArriveInPlace(t *testing.T) {
	b := newTestLog(t)
	id := b.begin("waiting for the catway")
	b.note("catway", "  dialing cathost socket…  ")
	b.note("catway", "   ") // whitespace only: not worth a line
	b.ok(id)

	snap := b.snapshot()
	if len(snap.Entries) != 2 {
		t.Fatalf("%d entries, want the step and one note", len(snap.Entries))
	}
	n := snap.Entries[1]
	if n.State != bootNote || n.Name != "catway" || n.Detail != "dialing cathost socket…" {
		t.Errorf("note = %+v, want a trimmed catway note after the step", n)
	}
}

func TestBootLogTrimsNotesAndKeepsSteps(t *testing.T) {
	// A daemon that logs a thousand lines during startup must not cost us the
	// steps: they are what says where the launch is.
	b := newTestLog(t)
	first := b.begin("starting cathost")
	b.ok(first)
	for i := 0; i < bootMaxEntries*2; i++ {
		b.note("cathost", "chatter")
	}
	last := b.begin("waiting for the catway")

	snap := b.snapshot()
	if len(snap.Entries) > bootMaxEntries {
		t.Errorf("%d entries, want at most %d", len(snap.Entries), bootMaxEntries)
	}
	var steps int
	for _, e := range snap.Entries {
		if e.State != bootNote {
			steps++
		}
	}
	if steps != 2 {
		t.Errorf("%d steps survived the trim, want both of them", steps)
	}
	// Both handles still resolve — ids are stable across a trim, which is why
	// they are ids and not slice indices.
	b.ok(last)
	if got := b.snapshot().Entries[len(b.snapshot().Entries)-1].State; got != bootOK {
		t.Errorf("the last step did not settle after a trim: %q", got)
	}
}

func TestBootLogFlushesTheBacklogToANewSink(t *testing.T) {
	// The window opens after the log has already started: the entries recorded
	// before it existed are exactly the ones a hang before the window would
	// otherwise hide, so installing a sink must hand them over immediately.
	b := newTestLog(t)
	id := b.begin("reading app settings")
	b.okDetail(id, "mode: local")

	got := make(chan []byte, 4)
	b.setSink(func(data []byte) { got <- data })

	select {
	case data := <-got:
		var snap bootSnapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			t.Fatalf("the sink was handed something that is not a snapshot: %v", err)
		}
		if len(snap.Entries) != 1 || snap.Entries[0].Name != "reading app settings" {
			t.Errorf("snapshot = %+v, want the backlog", snap.Entries)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("setSink did not flush the backlog")
	}

	// And a later change reaches it too (coalesced, hence the wait).
	b.begin("starting cathost")
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("a change after the sink was installed never arrived")
	}
}

func TestBootLogFinishStopsRecording(t *testing.T) {
	b := newTestLog(t)
	b.done = true // finish() also writes a transcript to disk; this is the half under test
	b.note("catway", "a line from the rest of the session")
	if n := len(b.snapshot().Entries); n != 0 {
		t.Errorf("%d entries after startup ended, want none — the log is for boot only", n)
	}
}

func TestBootTapSplitsWritesIntoLines(t *testing.T) {
	// exec hands us whatever the pipe gives it, which is not lines: a write can
	// carry three of them, or half of one.
	b := newTestLog(t)
	tap := &bootTap{src: "catway", log: b}

	tap.Write([]byte("listening on 127.0.0.1:8422\ndialing cat"))
	if n := len(b.snapshot().Entries); n != 1 {
		t.Fatalf("%d entries, want only the completed line", n)
	}
	tap.Write([]byte("host socket\n"))

	snap := b.snapshot()
	if len(snap.Entries) != 2 {
		t.Fatalf("%d entries, want two lines", len(snap.Entries))
	}
	if got := snap.Entries[1].Detail; got != "dialing cathost socket" {
		t.Errorf("second line = %q, want the halves joined", got)
	}
}

func TestBootTapFlushesAnEndlessLine(t *testing.T) {
	// Output with no newline in it (a progress bar, a binary splat) must not
	// grow the tap without bound, and must still be seen.
	b := newTestLog(t)
	tap := &bootTap{src: "cathost", log: b}
	tap.Write([]byte(strings.Repeat("x", bootTapMaxLine+1)))

	if n := len(b.snapshot().Entries); n != 1 {
		t.Fatalf("%d entries, want the over-long line flushed once", n)
	}
	if len(tap.buf) != 0 {
		t.Errorf("%d bytes still buffered, want the buffer emptied", len(tap.buf))
	}
}

func TestBootTapIsInertOnceStartupIsOver(t *testing.T) {
	b := newTestLog(t)
	b.done = true
	tap := &bootTap{src: "catway", log: b}
	n, err := tap.Write([]byte("a line the session logs an hour later\n"))
	if err != nil || n == 0 {
		t.Fatalf("the tap must keep accepting writes: n=%d err=%v", n, err)
	}
	if got := len(b.snapshot().Entries); got != 0 {
		t.Errorf("%d entries recorded after startup, want none", got)
	}
}

func TestTranscriptReadsAsALog(t *testing.T) {
	b := newTestLog(t)
	id := b.begin("starting catway")
	b.okDetail(id, "pid 91")
	b.note("catway", "listening")
	bad := b.begin("waiting for the catway")
	b.failed = true
	b.settle(bad, bootFail, "timed out")

	got := b.transcript()
	for _, want := range []string{"starting catway", "pid 91", "catway: listening", "timed out", "startup failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("the transcript is missing %q:\n%s", want, got)
		}
	}
}

func TestElapsedSwitchesUnits(t *testing.T) {
	if got := elapsed(940); got != "940ms" {
		t.Errorf("elapsed(940) = %q", got)
	}
	if got := elapsed(9400); got != "9.4s" {
		t.Errorf("elapsed(9400) = %q", got)
	}
}
