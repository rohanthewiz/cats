//go:build darwin

package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The startup window's last steps come from outside this process — a
// navigation delegate and the page's own report — and the mapping between
// those callbacks and the log is the part with decisions in it. The cgo entry
// points are one line each over the methods tested here (a _test.go file
// cannot import "C", which is why they are split at all).

func newTestWatch(t *testing.T) (*bootWatch, *bootLog) {
	t.Helper()
	return &bootWatch{}, &bootLog{t0: time.Now(), nextID: 1,
		logPath: filepath.Join(t.TempDir(), bootLogFile)}
}

func TestPageLoadSettlesTheWindowStep(t *testing.T) {
	w, b := newTestWatch(t)
	w.start(b, 1)

	if !w.pageLoaded(b) {
		t.Fatal("the first page load was not treated as the startup one")
	}
	snap := b.snapshot()
	if len(snap.Entries) != 1 || snap.Entries[0].State != bootOK {
		t.Fatalf("entries = %+v, want the window step closed", snap.Entries)
	}
	// A second window loading, a reload, a navigation: not startup.
	if w.pageLoaded(b) {
		t.Error("a later page load was treated as startup")
	}
	if n := len(b.snapshot().Entries); n != 1 {
		t.Errorf("%d entries after a later load, want the log left alone", n)
	}
	w.grace.Stop()
}

func TestStartUIWatchNamesTheWindowCount(t *testing.T) {
	w, b := newTestWatch(t)
	w.start(b, 3)
	w.start(b, 9) // once only: the step is already open

	snap := b.snapshot()
	if len(snap.Entries) != 1 {
		t.Fatalf("%d entries, want one step", len(snap.Entries))
	}
	if got := snap.Entries[0].Name; got != "opening 3 windows" {
		t.Errorf("step = %q, want the restored window count", got)
	}
}

func TestSessionPhasesRunFromConnectingToReady(t *testing.T) {
	w, b := newTestWatch(t)
	w.start(b, 1)
	w.pageLoaded(b)

	if done := w.phase(b, "ws-connecting", "127.0.0.1:8422"); done {
		t.Fatal("connecting ended startup")
	}
	// The session step is open and running: this is what ticks on screen while
	// a catway that cannot reach cathost keeps the page waiting.
	sess := b.snapshot().Entries[1]
	if sess.Name != "connecting to the session" || sess.State != bootRunning {
		t.Fatalf("second entry = %+v, want a running session step", sess)
	}

	w.phase(b, "ws-open", "")
	w.phase(b, "welcome", "")
	if done := w.phase(b, "ready", ""); !done {
		t.Fatal("ready did not end startup")
	}
	if got := b.snapshot().Entries[1].State; got != bootOK {
		t.Errorf("the session step is %q after ready, want %q", got, bootOK)
	}
}

func TestAClosedSocketLeavesTheSessionStepRunning(t *testing.T) {
	// A page that connects, drops and retries is the visible form of a broken
	// backend. Closing the step on ws-closed would hide it; leaving it running
	// is what puts a growing number next to it.
	w, b := newTestWatch(t)
	w.phase(b, "ws-connecting", "")
	w.phase(b, "ws-closed", "")

	snap := b.snapshot()
	if snap.Entries[0].State != bootRunning {
		t.Errorf("the session step settled on a dropped socket: %q", snap.Entries[0].State)
	}
	if len(snap.Entries) != 2 || snap.Entries[1].State != bootNote {
		t.Errorf("entries = %+v, want the drop recorded as a note", snap.Entries)
	}
}

func TestTheFirstReportCancelsTheGracePeriod(t *testing.T) {
	// The grace timer exists for a page that cannot report (an older catway, a
	// login form). One report proves this page can, so its "ready" is what ends
	// startup — not a timer that would close the window mid-connect.
	w, b := newTestWatch(t)
	w.pageLoaded(b)
	if w.grace == nil {
		t.Fatal("no grace timer was armed by the page load")
	}
	w.phase(b, "ws-connecting", "")
	if w.grace != nil {
		t.Error("the grace timer survived the page's first report")
	}
	if w.graceExpired(b) {
		t.Error("an expired grace timer ended startup even though the page reports")
	}
}

func TestGraceExpiryEndsStartupForASilentPage(t *testing.T) {
	w, b := newTestWatch(t)
	w.pageLoaded(b)
	w.grace.Stop()

	if !w.graceExpired(b) {
		t.Fatal("a page that never reported did not end startup")
	}
	last := b.snapshot().Entries[len(b.snapshot().Entries)-1]
	if last.State != bootNote {
		t.Errorf("last entry = %+v, want a note saying why startup was called done", last)
	}
}

func TestPhasesAfterStartupAreIgnored(t *testing.T) {
	// The page reconnects for the life of the session. None of that is boot.
	w, b := newTestWatch(t)
	b.done = true
	if w.phase(b, "ready", "") {
		t.Error("a phase after startup ended startup again")
	}
	if w.pageLoaded(b) {
		t.Error("a page load after startup was treated as startup")
	}
	if n := len(b.snapshot().Entries); n != 0 {
		t.Errorf("%d entries recorded after startup, want none", n)
	}
}

func TestSplashPageKnowsEveryLogState(t *testing.T) {
	// The page draws a glyph per entry state. A state added to the log with no
	// glyph here renders as a bare dot with no colour — the log would still be
	// readable, but "failed" would stop looking like failure, which is the one
	// thing the window exists to make obvious.
	page := splashPageHTML()
	var glyphs string
	for _, line := range strings.Split(page, "\n") {
		if strings.Contains(line, "var GLYPH") {
			glyphs = line
		}
	}
	if glyphs == "" {
		t.Fatal("the splash page no longer has a glyph table")
	}
	for _, state := range []string{bootRunning, bootOK, bootWarn, bootFail, bootNote} {
		if !strings.Contains(glyphs, state+":") {
			t.Errorf("no glyph for the %q state: %s", state, glyphs)
		}
	}
	// The hook the native side calls into (see splashPush).
	if !strings.Contains(page, "window.catsBootPush = function") {
		t.Error("the page no longer defines window.catsBootPush; pushes would vanish")
	}
}

func TestOnlyAFirstLoadFailureStopsTheLaunch(t *testing.T) {
	w, b := newTestWatch(t)
	w.start(b, 1)

	err := w.pageFailed(b, "could not connect to the server")
	if err == nil {
		t.Fatal("a page that never loaded did not fail the launch")
	}
	if got := b.snapshot().Entries[0].State; got != bootFail {
		t.Errorf("the window step is %q, want %q", got, bootFail)
	}

	// After the UI is up, a failed navigation is that window's problem — and
	// WKWebView reports a superseded navigation as a failure, so this path is
	// walked by ordinary use.
	w2, b2 := newTestWatch(t)
	w2.start(b2, 1)
	w2.pageLoaded(b2)
	w2.grace.Stop()
	if err := w2.pageFailed(b2, "cancelled"); err != nil {
		t.Errorf("a failure after the UI was up stopped the launch: %v", err)
	}
	last := b2.snapshot().Entries[len(b2.snapshot().Entries)-1]
	if last.State != bootNote {
		t.Errorf("last entry = %+v, want the failure noted and nothing more", last)
	}
}
