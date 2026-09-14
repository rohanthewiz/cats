package orchestration

import (
	"errors"
	"testing"
	"time"
)

// Frames come out in the order they went in, across wakes, and the budget is
// released only once the writer reports them written.
func TestOutboxKeepsOrderAndAccountsForTheBacklog(t *testing.T) {
	b := NewOutbox(10)
	for _, f := range []string{"ab", "cd", "ef"} {
		if err := b.Push([]byte(f)); err != nil {
			t.Fatalf("push %q: %v", f, err)
		}
	}
	batch := b.Take()
	if len(batch) != 3 || string(batch[0]) != "ab" || string(batch[2]) != "ef" {
		t.Fatalf("take = %q, want [ab cd ef] in order", batch)
	}
	// 6 bytes are taken but not written, so they still count: 6 + 5 > 10.
	if got := b.Queued(); got != 6 {
		t.Fatalf("Queued with a batch in flight = %d, want 6", got)
	}
	if err := b.Push([]byte("ghijk")); !errors.Is(err, ErrOutboxFull) {
		t.Fatalf("push past the budget = %v, want ErrOutboxFull", err)
	}
	b.Sent(6)
	if err := b.Push([]byte("ghijk")); err != nil {
		t.Fatalf("push after the writer caught up: %v", err)
	}
}

// Push never blocks — the property every producer depends on — even with no
// writer taking anything and far more frames than any channel buffer.
func TestOutboxPushNeverBlocks(t *testing.T) {
	b := NewOutbox(0)
	done := make(chan struct{})
	go func() {
		for range 100_000 {
			_ = b.Push([]byte("x"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("push blocked with no writer draining")
	}
}

// Close drops the backlog, refuses more, and releases a writer waiting in Take.
func TestOutboxCloseReleasesAWaitingWriter(t *testing.T) {
	b := NewOutbox(0)
	got := make(chan [][]byte, 1)
	go func() { got <- b.Take() }()
	time.Sleep(10 * time.Millisecond) // let Take start waiting
	b.Close()
	select {
	case batch := <-got:
		if batch != nil {
			t.Fatalf("take after close = %q, want nil", batch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close did not release a writer waiting in take")
	}
	if err := b.Push([]byte("x")); !errors.Is(err, ErrOutboxClosed) {
		t.Fatalf("push after close = %v, want ErrOutboxClosed", err)
	}
	b.Close() // idempotent
}

// CloseWhenDrained hands over what is already queued, refuses anything new,
// and then ends — including for a writer that was idle when it was called.
func TestOutboxCloseWhenDrainedFlushesThenEnds(t *testing.T) {
	b := NewOutbox(0)
	_ = b.Push([]byte("close_pane"))
	b.CloseWhenDrained()
	if err := b.Push([]byte("late")); !errors.Is(err, ErrOutboxClosed) {
		t.Fatalf("push while draining = %v, want ErrOutboxClosed", err)
	}
	if batch := b.Take(); len(batch) != 1 || string(batch[0]) != "close_pane" {
		t.Fatalf("take while draining = %q, want the queued frame", batch)
	}
	if b.Take() != nil || !b.IsClosed() {
		t.Fatal("a drained outbox should close once its last frame is taken")
	}

	idle := NewOutbox(0)
	got := make(chan [][]byte, 1)
	go func() { got <- idle.Take() }()
	time.Sleep(10 * time.Millisecond)
	idle.CloseWhenDrained()
	select {
	case batch := <-got:
		if batch != nil {
			t.Fatalf("idle drain returned %q, want nil", batch)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("an idle writer was not woken to notice the drain")
	}
}
