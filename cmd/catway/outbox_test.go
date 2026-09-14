//go:build ghostty

package main

import (
	"errors"
	"testing"
	"time"
)

// Frames come out in the order they went in, across wakes, and the budget is
// released only once the writer reports them written.
func TestOutboxKeepsOrderAndAccountsForTheBacklog(t *testing.T) {
	b := newOutbox(10)
	for _, f := range []string{"ab", "cd", "ef"} {
		if err := b.push([]byte(f)); err != nil {
			t.Fatalf("push %q: %v", f, err)
		}
	}
	batch := b.take()
	if len(batch) != 3 || string(batch[0]) != "ab" || string(batch[2]) != "ef" {
		t.Fatalf("take = %q, want [ab cd ef] in order", batch)
	}
	// 6 bytes are taken but not written, so they still count: 6 + 5 > 10.
	if err := b.push([]byte("ghijk")); !errors.Is(err, errOutboxFull) {
		t.Fatalf("push past the budget = %v, want errOutboxFull", err)
	}
	b.sent(6)
	if err := b.push([]byte("ghijk")); err != nil {
		t.Fatalf("push after the writer caught up: %v", err)
	}
}

// push never blocks — the property the orchestrator loop depends on — even with
// no writer taking anything and far more frames than any channel buffer.
func TestOutboxPushNeverBlocks(t *testing.T) {
	b := newOutbox(0)
	done := make(chan struct{})
	go func() {
		for range 100_000 {
			_ = b.push([]byte("x"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("push blocked with no writer draining")
	}
}

// close drops the backlog, refuses more, and releases a writer waiting in take.
func TestOutboxCloseReleasesAWaitingWriter(t *testing.T) {
	b := newOutbox(0)
	got := make(chan [][]byte, 1)
	go func() { got <- b.take() }()
	time.Sleep(10 * time.Millisecond) // let take start waiting
	b.close()
	select {
	case batch := <-got:
		if batch != nil {
			t.Fatalf("take after close = %q, want nil", batch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close did not release a writer waiting in take")
	}
	if err := b.push([]byte("x")); !errors.Is(err, errOutboxClosed) {
		t.Fatalf("push after close = %v, want errOutboxClosed", err)
	}
	b.close() // idempotent
}

// closeWhenDrained hands over what is already queued, refuses anything new,
// and then ends — including for a writer that was idle when it was called.
func TestOutboxCloseWhenDrainedFlushesThenEnds(t *testing.T) {
	b := newOutbox(0)
	_ = b.push([]byte("close_pane"))
	b.closeWhenDrained()
	if err := b.push([]byte("late")); !errors.Is(err, errOutboxClosed) {
		t.Fatalf("push while draining = %v, want errOutboxClosed", err)
	}
	if batch := b.take(); len(batch) != 1 || string(batch[0]) != "close_pane" {
		t.Fatalf("take while draining = %q, want the queued frame", batch)
	}
	if b.take() != nil || !b.isClosed() {
		t.Fatal("a drained outbox should close once its last frame is taken")
	}

	idle := newOutbox(0)
	got := make(chan [][]byte, 1)
	go func() { got <- idle.take() }()
	time.Sleep(10 * time.Millisecond)
	idle.closeWhenDrained()
	select {
	case batch := <-got:
		if batch != nil {
			t.Fatalf("idle drain returned %q, want nil", batch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an idle writer was not woken to notice the drain")
	}
}
