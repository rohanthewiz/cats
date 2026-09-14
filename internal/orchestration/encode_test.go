package orchestration

import (
	"bytes"
	"testing"
)

// EncodeMessage and WriteMessage are two routes onto the same wire. A frame
// queued by catway's writer goroutine must be indistinguishable from one written
// directly, or cathost would read a queued message as garbage.
func TestEncodeMessageMatchesWriteMessage(t *testing.T) {
	msg := NewPing(42)
	var direct bytes.Buffer
	if err := WriteMessage(&direct, msg); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	frame, err := EncodeMessage(msg)
	if err != nil {
		t.Fatalf("EncodeMessage: %v", err)
	}
	if !bytes.Equal(frame, direct.Bytes()) {
		t.Fatalf("encoded frame %q differs from written frame %q", frame, direct.Bytes())
	}
	typ, _, err := ReadMessage(bytes.NewReader(frame))
	if err != nil || typ != MsgPing {
		t.Fatalf("ReadMessage of an encoded frame = %q, %v; want %q", typ, err, MsgPing)
	}
}

// An oversized message is refused at encode time, before it can take a place in
// any queue.
func TestEncodeMessageRefusesOversizedFrames(t *testing.T) {
	big := map[string]string{"type": "input", "data": string(make([]byte, MaxFrameSize))}
	if _, err := EncodeMessage(big); err == nil {
		t.Fatal("a message over MaxFrameSize was encoded")
	}
}
