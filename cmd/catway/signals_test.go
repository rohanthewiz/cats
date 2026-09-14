//go:build ghostty

package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// jammedPost stands in for o.post on a loop whose mailbox is full: it never
// returns and never runs fn.
func jammedPost(func()) { select {} }

// recordExit is a fake os.Exit that reports the code instead of exiting.
func recordExit() (func(int), <-chan int) {
	codes := make(chan int, 4)
	return func(code int) { codes <- code }, codes
}

// The 2026-09-13 failure: with the loop jammed, a second SIGTERM must still end
// the process, and promptly.
func TestSecondSignalExitsWhenTheLoopIsJammed(t *testing.T) {
	exit, codes := recordExit()
	sigc := make(chan os.Signal, 2)
	go handleSignals(sigc, jammedPost, func() {}, exit, time.Hour)

	sigc <- syscall.SIGTERM
	sigc <- syscall.SIGTERM
	select {
	case code := <-codes:
		if code != 1 {
			t.Fatalf("exit code %d, want 1", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a second signal did not exit while the loop was jammed")
	}
}

// One signal is enough, given time: the deadline exits without the loop.
func TestShutdownDeadlineExitsWhenTheLoopNeverRunsShutdown(t *testing.T) {
	exit, codes := recordExit()
	sigc := make(chan os.Signal, 2)
	go handleSignals(sigc, jammedPost, func() {}, exit, 50*time.Millisecond)

	sigc <- syscall.SIGTERM
	select {
	case code := <-codes:
		if code != 1 {
			t.Fatalf("exit code %d, want 1", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the shutdown deadline did not fire")
	}
}

// On a healthy loop the first signal runs the graceful Shutdown, and nothing
// exits early behind its back.
func TestSignalRunsShutdownThroughTheLoop(t *testing.T) {
	exit, codes := recordExit()
	ran := make(chan struct{})
	post := func(fn func()) { fn() }
	sigc := make(chan os.Signal, 2)
	go handleSignals(sigc, post, func() { close(ran) }, exit, time.Hour)

	sigc <- os.Interrupt
	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown was not posted to the loop")
	}
	select {
	case code := <-codes:
		t.Fatalf("exited with %d before a second signal or the deadline", code)
	case <-time.After(50 * time.Millisecond):
	}
}
