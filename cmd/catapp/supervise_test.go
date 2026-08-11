//go:build darwin

package main

import (
	"fmt"
	"net"
	"testing"
)

// The UI's localStorage is scoped by origin, and the port is part of it, so a
// stable port is the whole reason the preferred band exists. These tests pin the
// two properties that guarantee it: the band is preferred when free, and it is
// walked in a fixed order so a given instance lands on the same port every run.

func TestPickPortPrefersStableBand(t *testing.T) {
	// Hold nothing: the first port in the band should win, and should win again
	// on a second call — an ephemeral port would differ between the two.
	p1, err := pickPort()
	if err != nil {
		t.Fatalf("pickPort: %v", err)
	}
	p2, err := pickPort()
	if err != nil {
		t.Fatalf("pickPort (second): %v", err)
	}
	if p1 != p2 {
		t.Fatalf("pickPort is not stable across calls: %d then %d", p1, p2)
	}
	// Skip rather than fail if the developer's machine genuinely has the whole
	// band occupied — that is the documented fallback, not a broken function.
	if p1 < appPortBase || p1 >= appPortBase+appPortSpan {
		t.Skipf("port band %d..%d occupied on this host; got ephemeral %d",
			appPortBase, appPortBase+appPortSpan-1, p1)
	}
}

func TestPickPortWalksPastAnOccupiedPort(t *testing.T) {
	// Occupy the base port for the duration, standing in for a second running
	// instance, and check we deterministically step to the next one rather than
	// falling back to an ephemeral port.
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", appPortBase))
	if err != nil {
		t.Skipf("cannot occupy %d to run this test: %v", appPortBase, err)
	}
	defer l.Close()

	p, err := pickPort()
	if err != nil {
		t.Fatalf("pickPort: %v", err)
	}
	if p == appPortBase {
		t.Fatalf("pickPort returned the occupied base port %d", p)
	}
	if p < appPortBase || p >= appPortBase+appPortSpan {
		t.Skipf("rest of the band occupied on this host; got ephemeral %d", p)
	}
	if p != appPortBase+1 {
		t.Errorf("expected the next port in the band (%d), got %d", appPortBase+1, p)
	}
}

func TestPortFree(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port

	if portFree(port) {
		t.Errorf("portFree(%d) = true while it is bound", port)
	}
	l.Close()
	if !portFree(port) {
		t.Errorf("portFree(%d) = false after the listener closed", port)
	}
}
