//go:build darwin

package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newCathostTestBackend builds a backend whose "cathost" is a shell script,
// with a private daemon log and a millisecond backoff. Ready means still
// running a moment after start, the same rule as the real settle, only shorter.
func newCathostTestBackend(t *testing.T, script string, budget restartBudget) *backend {
	t.Helper()
	b := &backend{
		log:             newRotatingLog(filepath.Join(t.TempDir(), "daemons.log"), 1<<20),
		quit:            make(chan struct{}),
		retry:           make(chan struct{}, 1),
		cathostRestarts: budget,
		cathostPath:     "/bin/sh",
		cathostArgs:     []string{"-c", script},
	}
	b.cathostReady = func(p *daemonProc) error { return waitAlive(p, 150*time.Millisecond) }
	t.Cleanup(b.stop)
	return b
}

// superviseHost launches the first cathost and starts its supervisor, returning
// the first process and a channel closed when the supervisor returns.
func superviseHost(t *testing.T, b *backend, ui backendUI) (*daemonProc, chan struct{}) {
	t.Helper()
	first, err := b.launch(&b.cathost, b.cathostPath, b.cathostArgs...)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	done := make(chan struct{})
	go func() {
		b.superviseCathost(ui)
		close(done)
	}()
	return first, done
}

func waitClosed(t *testing.T, done chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not happen within 5s", what)
	}
}

// The N-068 case: a cathost that dies once (the grapheme-overflow panic) is
// relaunched and the windows are told what was lost.
func TestSuperviseCathostRestartsAndSaysPanesWereLost(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started-once")
	script := fmt.Sprintf(`if [ -e '%s' ]; then exec sleep 30; fi; touch '%s'; exit 2`, marker, marker)
	b := newCathostTestBackend(t, script, fastBudget(5))
	ui := newRecordingUI()
	first, done := superviseHost(t, b, ui)

	ui.expect(t, "cathost lost")
	cur := b.currentCathost()
	if cur == first || cur.pid() == first.pid() {
		t.Fatalf("cathost was not replaced (pid %d)", cur.pid())
	}
	if b.cathostExited() {
		t.Fatalf("the restarted cathost is not running: %s", cur.exitStatus())
	}

	b.stop()
	waitClosed(t, done, "the supervisor returning after stop")
}

// A cathost that dies on every start spends the budget and puts up the
// no-button card, and the supervisor ends rather than spinning.
func TestSuperviseCathostGivesUpWhenTheBudgetIsSpent(t *testing.T) {
	b := newCathostTestBackend(t, `exit 2`, fastBudget(3))
	ui := newRecordingUI()
	_, done := superviseHost(t, b, ui)

	ui.expect(t, "cathost down")
	waitClosed(t, done, "the supervisor returning once the budget was spent")
	if n := b.cathostRestarts.used(); n != 3 {
		t.Fatalf("restarts used = %d, want 3", n)
	}
}

// Quitting the app stops cathost; that exit is ours and must not be answered
// with a relaunch or a card.
func TestSuperviseCathostIgnoresTheExitStopCauses(t *testing.T) {
	b := newCathostTestBackend(t, `exec sleep 30`, fastBudget(5))
	ui := newRecordingUI()
	first, done := superviseHost(t, b, ui)

	b.stop()
	waitClosed(t, done, "the supervisor returning after stop")
	if b.currentCathost() != first {
		t.Fatal("a cathost was launched after stop")
	}
	select {
	case ev := <-ui.events:
		t.Fatalf("unexpected ui event %q on a requested stop", ev)
	default:
	}
	if _, err := b.launch(&b.cathost, "/bin/sh", "-c", "exit 0"); !errors.Is(err, errBackendStopped) {
		t.Fatalf("launch after stop: err = %v, want errBackendStopped", err)
	}
}

func TestStopEndsTheCathostSupervisorDuringBackoff(t *testing.T) {
	budget := restartBudget{window: time.Minute, max: 5, base: time.Hour, cap: time.Hour}
	b := newCathostTestBackend(t, `exit 2`, budget)
	ui := newRecordingUI()
	first, done := superviseHost(t, b, ui)

	<-first.done
	time.Sleep(100 * time.Millisecond) // into the backoff
	b.stop()
	waitClosed(t, done, "the supervisor returning from its backoff after stop")
	if b.currentCathost() != first {
		t.Fatal("a cathost was launched after stop")
	}
}

func TestWaitAliveReportsAnEarlyExit(t *testing.T) {
	l := newRotatingLog(filepath.Join(t.TempDir(), "daemons.log"), 1<<20)
	p, err := startDaemon(l, "/bin/sh", "-c", "exit 4")
	if err != nil {
		t.Fatal(err)
	}
	if err := waitAlive(p, 5*time.Second); err == nil || !strings.Contains(err.Error(), "exit status 4") {
		t.Fatalf("waitAlive on an exiting process = %v, want its exit status", err)
	}
	q, err := startDaemon(l, "/bin/sh", "-c", "exec sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.stop(time.Second) })
	if err := waitAlive(q, 50*time.Millisecond); err != nil {
		t.Fatalf("waitAlive on a running process = %v", err)
	}
}

func TestCathostDetails(t *testing.T) {
	lost := cathostLostDetail("exit status 2")
	if !strings.Contains(lost, "exit status 2") || !strings.Contains(lost, "fresh shells") ||
		!strings.Contains(lost, "started again") {
		t.Errorf("lost: %q", lost)
	}
	down := cathostDownDetail("exit status 2")
	if !strings.Contains(down, "exit status 2") || !strings.Contains(down, "Quit and reopen") {
		t.Errorf("down: %q", down)
	}
}
