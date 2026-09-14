//go:build darwin

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRestartBudgetBacksOffThenRefuses(t *testing.T) {
	r := restartBudget{window: time.Minute, max: 3, base: 100 * time.Millisecond, cap: 250 * time.Millisecond}
	t0 := time.Unix(1000, 0)

	for i, want := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 250 * time.Millisecond} {
		d, ok := r.next(t0.Add(time.Duration(i) * time.Second))
		if !ok || d != want {
			t.Fatalf("launch %d: next = %s, %v; want %s, true", i+1, d, ok, want)
		}
	}
	if _, ok := r.next(t0.Add(3 * time.Second)); ok {
		t.Fatal("a fourth launch within the window was allowed")
	}
	// The first launch (t0) has aged out; the two at +1s and +2s have not, so a
	// slot is free but the delay still reflects them.
	if d, ok := r.next(t0.Add(time.Minute + 500*time.Millisecond)); !ok || d != 250*time.Millisecond {
		t.Fatalf("after the first aged out: next = %s, %v; want 250ms, true", d, ok)
	}
	// Everything aged out: back to the base delay.
	if d, ok := r.next(t0.Add(10 * time.Minute)); !ok || d != 100*time.Millisecond {
		t.Fatalf("after all aged out: next = %s, %v; want 100ms, true", d, ok)
	}
	r.reset()
	if r.used() != 0 {
		t.Fatalf("used after reset = %d", r.used())
	}
}

// recordingUI stands in for the windows.
type recordingUI struct{ events chan string }

func newRecordingUI() *recordingUI { return &recordingUI{events: make(chan string, 32)} }

func (u *recordingUI) catwayDown(detail string, canRetry bool) {
	u.events <- fmt.Sprintf("down retry=%v", canRetry)
}
func (u *recordingUI) catwayRestarting() { u.events <- "restarting" }
func (u *recordingUI) catwayBack()       { u.events <- "back" }

func (u *recordingUI) expect(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-u.events:
		if got != want {
			t.Fatalf("ui event = %q, want %q", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("no %q event within 10s", want)
	}
}

// newTestBackend builds a backend whose "catway" is a shell script, with a
// private daemon log and a millisecond backoff. Ready means still running a
// moment after start — the scripts have no port to dial.
func newTestBackend(t *testing.T, script string, budget restartBudget) *backend {
	t.Helper()
	b := &backend{
		log:        newRotatingLog(filepath.Join(t.TempDir(), "daemons.log"), 1<<20),
		quit:       make(chan struct{}),
		retry:      make(chan struct{}, 1),
		restarts:   budget,
		catwayPath: "/bin/sh",
		catwayArgs: []string{"-c", script},
	}
	b.ready = func(p *daemonProc) error {
		select {
		case <-p.done:
			return fmt.Errorf("exited: %s", p.exitStatus())
		case <-time.After(150 * time.Millisecond):
			return nil
		}
	}
	t.Cleanup(b.stop)
	return b
}

// supervise launches the first catway and starts the supervisor, returning the
// first process and a channel closed when the supervisor returns.
func supervise(t *testing.T, b *backend, ui backendUI) (*daemonProc, chan struct{}) {
	t.Helper()
	first, err := b.launch(&b.catway, b.catwayPath, b.catwayArgs...)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	done := make(chan struct{})
	go func() {
		b.superviseCatway(ui)
		close(done)
	}()
	return first, done
}

func fastBudget(max int) restartBudget {
	return restartBudget{window: time.Minute, max: max, base: time.Millisecond, cap: time.Millisecond}
}

func TestSuperviseRestartsACatwayThatExits(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started-once")
	// Exits the first time, stays up every time after.
	script := fmt.Sprintf(`if [ -e '%s' ]; then exec sleep 30; fi; touch '%s'; exit 3`, marker, marker)
	b := newTestBackend(t, script, fastBudget(5))
	ui := newRecordingUI()
	first, done := supervise(t, b, ui)

	ui.expect(t, "back")
	cur := b.currentCatway()
	if cur == first || cur.pid() == first.pid() {
		t.Fatalf("catway was not replaced (pid %d)", cur.pid())
	}
	select {
	case <-cur.done:
		t.Fatalf("the restarted catway is not running: %s", cur.exitStatus())
	default:
	}

	b.stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the supervisor did not return after stop")
	}
}

func TestSuperviseGivesUpThenRestartsOnRequest(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "fixed")
	// Exits until the marker exists — standing in for whatever a person fixes
	// before pressing the button.
	script := fmt.Sprintf(`if [ -e '%s' ]; then exec sleep 30; fi; exit 3`, marker)
	b := newTestBackend(t, script, fastBudget(3))
	ui := newRecordingUI()
	_, done := supervise(t, b, ui)

	// cathost is nil here, which counts as not exited: the retry is offered.
	ui.expect(t, "down retry=true")
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	activeBackend.Store(b)
	t.Cleanup(func() { activeBackend.Store(nil) })
	requestBackendRestart()
	ui.expect(t, "restarting")
	ui.expect(t, "back")

	b.stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the supervisor did not return after stop")
	}
}

func TestStopEndsTheSupervisorDuringBackoff(t *testing.T) {
	// A backoff far longer than the test: only quit can end the sleep.
	budget := restartBudget{window: time.Minute, max: 5, base: time.Hour, cap: time.Hour}
	b := newTestBackend(t, `exit 3`, budget)
	ui := newRecordingUI()
	first, done := supervise(t, b, ui)

	<-first.done
	time.Sleep(100 * time.Millisecond) // into the backoff
	b.stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the supervisor did not return from its backoff after stop")
	}
	if b.currentCatway() != first {
		t.Fatal("a catway was launched after stop")
	}
	if _, err := b.launch(&b.catway, "/bin/sh", "-c", "exit 0"); !errors.Is(err, errBackendStopped) {
		t.Fatalf("launch after stop: err = %v, want errBackendStopped", err)
	}
}

func TestCatwayDownDetail(t *testing.T) {
	up := catwayDownDetail("exit status 2", true)
	if !strings.Contains(up, "exit status 2") || !strings.Contains(up, "still running") {
		t.Errorf("cathost up: %q", up)
	}
	down := catwayDownDetail("exit status 2", false)
	if !strings.Contains(down, "Quit and reopen") || strings.Contains(down, "still running") {
		t.Errorf("cathost down: %q", down)
	}
}
