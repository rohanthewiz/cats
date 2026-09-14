package orchestration

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

// A peer that stops reading must turn into an error, not a goroutine parked
// forever — the parked writer is where the 2026-09-13 freeze started.
func TestStallWriterFailsWhenThePeerStopsReading(t *testing.T) {
	client, server := net.Pipe() // nothing ever reads server
	defer client.Close()
	defer server.Close()

	done := make(chan error, 1)
	go func() {
		_, err := NewStallWriter(client, 50*time.Millisecond).Write(make([]byte, 1<<20))
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("err = %v, want one that wraps os.ErrDeadlineExceeded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a write to a peer that never reads did not give up")
	}
}

// The bound is on silence, not on total time: a large frame crossing a slow
// link that keeps draining must get through even when the whole write takes
// several times the timeout. A whole-call deadline would fail this.
func TestStallWriterToleratesASlowPeerThatKeepsReading(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	const timeout = 150 * time.Millisecond
	total := 8 * stallChunk
	go func() {
		buf := make([]byte, stallChunk)
		for got := 0; got < total; {
			n, err := server.Read(buf)
			if err != nil {
				return
			}
			got += n
			time.Sleep(timeout / 3) // slow, but never silent for a whole timeout
		}
	}()

	start := time.Now()
	if _, err := NewStallWriter(client, timeout).Write(make([]byte, total)); err != nil {
		t.Fatalf("slow-but-moving peer was treated as stalled: %v", err)
	}
	if took := time.Since(start); took <= timeout {
		t.Fatalf("write took %v, not longer than the %v timeout — the test proves nothing", took, timeout)
	}
}

// Writers that cannot carry a deadline, and a disabled bound, pass through
// untouched, so the wrapper can be applied unconditionally.
func TestNewStallWriterLeavesOtherWritersAlone(t *testing.T) {
	var b bytes.Buffer
	if w := NewStallWriter(&b, time.Second); w != io.Writer(&b) {
		t.Fatal("a writer with no deadline support was wrapped")
	}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	if w := NewStallWriter(client, 0); w != io.Writer(client) {
		t.Fatal("a zero timeout should disable the bound, not wrap")
	}
}
