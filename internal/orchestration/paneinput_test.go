package orchestration

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

// sgrWheelDown is one SGR wheel-down report at (10,5) — what the browser
// encoder emits per notch while the child has mouse reporting on.
const sgrWheelDown = "\x1b[<65;10;5M"

// TestIsDroppable pins which input may be discarded under backpressure: only
// chunks made ENTIRELY of wheel or buttonless-motion SGR reports. A single
// non-droppable byte anywhere makes the whole chunk lossless, because dropping
// it would lose that byte too.
func TestIsDroppable(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"wheel down", sgrWheelDown, true},
		{"wheel up", "\x1b[<64;1;1M", true},
		{"wheel right with ctrl", "\x1b[<83;3;4M", true}, // 67+16
		{"many wheel reports", strings.Repeat(sgrWheelDown, 20), true},
		{"buttonless motion", "\x1b[<35;12;7M", true},
		{"buttonless motion with shift", "\x1b[<39;12;7M", true},
		{"left press", "\x1b[<0;1;1M", false},
		{"left drag (motion with button)", "\x1b[<32;4;4M", false},
		{"release", "\x1b[<0;1;1m", false},
		{"wheel-coded release", "\x1b[<65;1;1m", false},
		{"extended button 8", "\x1b[<128;1;1M", false},
		{"wheel then a key", sgrWheelDown + "a", false},
		{"key", "a", false},
		{"arrow (alternate scroll)", "\x1b[B", false},
		{"legacy X10 mouse", "\x1b[Ma!!", false},
		{"truncated report", "\x1b[<65;10;5", false},
		{"missing field", "\x1b[<65;10M", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		if got := isDroppable([]byte(c.in)); got != c.want {
			t.Errorf("%s: isDroppable(%q) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

// TestPaneInputCoalescesInOrder: the writer gets every queued chunk as ONE
// buffer, in push order — a burst costs one PTY write, and bytes never reorder.
func TestPaneInputCoalescesInOrder(t *testing.T) {
	q := newPaneInput(0)
	for _, s := range []string{"ab", sgrWheelDown, "cd"} {
		if _, err := q.push([]byte(s)); err != nil {
			t.Fatalf("push %q: %v", s, err)
		}
	}
	got := q.take()
	if want := "ab" + sgrWheelDown + "cd"; string(got) != want {
		t.Fatalf("take = %q, want %q", got, want)
	}
}

// TestPaneInputCopiesPushedBytes: the emulator's write callback reuses its
// buffer, so a queue that kept the caller's slice would write whatever the
// buffer held later instead of the reply.
func TestPaneInputCopiesPushedBytes(t *testing.T) {
	q := newPaneInput(0)
	buf := []byte("reply")
	if _, err := q.push(buf); err != nil {
		t.Fatal(err)
	}
	copy(buf, "XXXXX")
	if got := q.take(); string(got) != "reply" {
		t.Fatalf("take = %q, want the bytes as pushed", got)
	}
}

// TestPaneInputDropsWheelUnderBackpressure is the core policy: once the
// backlog passes droppableBacklog (a child that is not reading), wheel reports
// are discarded, while keys queued after them still arrive — in order, after
// the wheel reports that WERE accepted.
func TestPaneInputDropsWheelUnderBackpressure(t *testing.T) {
	q := newPaneInput(0)
	accepted := 0
	for range 5000 { // ~55KB of notches, far past the backlog
		dropped, err := q.push([]byte(sgrWheelDown))
		if err != nil {
			t.Fatalf("push wheel: %v", err)
		}
		if !dropped {
			accepted++
		}
	}
	if q.droppedCount() == 0 {
		t.Fatal("no wheel reports dropped under backpressure")
	}
	if maxAccepted := droppableBacklog/len(sgrWheelDown) + 1; accepted > maxAccepted {
		t.Fatalf("accepted %d wheel reports, want at most %d", accepted, maxAccepted)
	}
	if dropped, err := q.push([]byte("q")); dropped || err != nil {
		t.Fatalf("key under backpressure: dropped=%v err=%v, want it queued", dropped, err)
	}
	got := q.take()
	if !bytes.HasSuffix(got, []byte("q")) {
		t.Fatalf("key missing from the tail of the batch: %q", got[max(0, len(got)-20):])
	}
	if n := bytes.Count(got, []byte(sgrWheelDown)); n != accepted {
		t.Fatalf("batch holds %d wheel reports, want the %d accepted", n, accepted)
	}
}

// TestPaneInputInFlightBatchCountsAsBacklog: a batch the writer has taken but
// whose write has not returned is the stuck-child case itself, so wheel
// reports must keep dropping until sent() says the bytes actually left.
func TestPaneInputInFlightBatchCountsAsBacklog(t *testing.T) {
	q := newPaneInput(0)
	big := bytes.Repeat([]byte("k"), droppableBacklog)
	if _, err := q.push(big); err != nil {
		t.Fatal(err)
	}
	batch := q.take() // the writer is now "blocked" writing this
	if dropped, _ := q.push([]byte(sgrWheelDown)); !dropped {
		t.Fatal("wheel accepted while a full backlog was still in flight")
	}
	q.sent(len(batch))
	if dropped, _ := q.push([]byte(sgrWheelDown)); dropped {
		t.Fatal("wheel dropped after the backlog was written")
	}
}

// TestPaneInputBudgetRefusesLossless: lossless input is never dropped, so the
// only bound on a child that never reads is an error to the caller.
func TestPaneInputBudgetRefusesLossless(t *testing.T) {
	q := newPaneInput(8)
	if _, err := q.push([]byte("12345678")); err != nil {
		t.Fatalf("push within budget: %v", err)
	}
	if _, err := q.push([]byte("9")); err != errPaneInputFull {
		t.Fatalf("push past budget: err=%v, want errPaneInputFull", err)
	}
}

// TestPaneInputCloseReleasesWriter: closing must wake a writer waiting for
// input (or inputPump leaks one goroutine per closed pane) and refuse later
// pushes; a second close is harmless.
func TestPaneInputCloseReleasesWriter(t *testing.T) {
	q := newPaneInput(0)
	done := make(chan []byte, 1)
	go func() { done <- q.take() }()
	time.Sleep(10 * time.Millisecond) // let take park
	q.close()
	select {
	case b := <-done:
		if b != nil {
			t.Fatalf("take after close = %q, want nil", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("take did not return after close")
	}
	if _, err := q.push([]byte("x")); err != errPaneInputClosed {
		t.Fatalf("push after close: err=%v, want errPaneInputClosed", err)
	}
	q.close()
	q.sent(5) // a writer finishing a batch after close must not drive queued negative
}
