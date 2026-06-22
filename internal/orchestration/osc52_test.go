package orchestration

import (
	"bytes"
	"testing"
)

// flatten runs the scanner over one chunk and returns the writes for assertions.
func scan52(in string) [][]byte {
	var s osc52Scanner
	return s.scan([]byte(in))
}

func wantWrites(t *testing.T, got [][]byte, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d writes, want %d (%q)", len(got), len(want), got)
	}
	for i := range want {
		if !bytes.Equal(got[i], []byte(want[i])) {
			t.Fatalf("write %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestOSC52WriteBELandST(t *testing.T) {
	wantWrites(t, scan52("\x1b]52;c;aGVsbG8=\x07"), "hello")
	wantWrites(t, scan52("\x1b]52;c;aGVsbG8=\x1b\\"), "hello")
}

func TestOSC52EmptySelectorForm(t *testing.T) {
	wantWrites(t, scan52("\x1b]52;;aGVsbG8=\x07"), "hello")
}

func TestOSC52ClearClipboard(t *testing.T) {
	// `52;c;` (empty payload) is a clipboard-clear: one write of empty bytes.
	got := scan52("\x1b]52;c;\x07")
	if len(got) != 1 || len(got[0]) != 0 {
		t.Fatalf("clear should yield one empty write, got %q", got)
	}
}

func TestOSC52IgnoresQuery(t *testing.T) {
	if got := scan52("\x1b]52;c;?\x07"); len(got) != 0 {
		t.Fatalf("query should be ignored, got %q", got)
	}
	if got := scan52("\x1b]52;;?\x07"); len(got) != 0 {
		t.Fatalf("empty-selector query should be ignored, got %q", got)
	}
}

func TestOSC52IgnoresOtherSelections(t *testing.T) {
	for _, sel := range []string{"p", "s", "q", "0", "7"} {
		if got := scan52("\x1b]52;" + sel + ";aGk=\x07"); len(got) != 0 {
			t.Fatalf("selection %q should be ignored, got %q", sel, got)
		}
	}
}

func TestOSC52IgnoresInvalidBase64(t *testing.T) {
	if got := scan52("\x1b]52;c;%%%\x07"); len(got) != 0 {
		t.Fatalf("invalid base64 should be ignored, got %q", got)
	}
	// An ESC mid-payload becomes literal bytes; the body then fails to decode.
	if got := scan52("\x1b]52;c;aGVs\x1b[bG8=\x07"); len(got) != 0 {
		t.Fatalf("ESC-tainted base64 should be ignored, got %q", got)
	}
}

func TestOSC52IgnoresNonOSC52(t *testing.T) {
	got := scan52("\x1b]11;?\x07\x1b]0;title\x07\x1b]8;;https://example.com\x1b\\")
	if len(got) != 0 {
		t.Fatalf("non-OSC52 should be ignored, got %q", got)
	}
}

func TestOSC52SplitAcrossReads(t *testing.T) {
	var s osc52Scanner
	chunks := []string{"\x1b]52;c;aGVs", "bG8gd29y", "bGQ=\x07"}
	var got [][]byte
	for _, ch := range chunks {
		got = append(got, s.scan([]byte(ch))...)
	}
	wantWrites(t, got, "hello world")
}

func TestOSC52SplitBetweenEscAndBackslash(t *testing.T) {
	var s osc52Scanner
	var got [][]byte
	got = append(got, s.scan([]byte("\x1b]52;c;aGk=\x1b"))...)
	got = append(got, s.scan([]byte("\\"))...)
	wantWrites(t, got, "hi")
}

func TestOSC52MultipleInOneChunk(t *testing.T) {
	wantWrites(t, scan52("\x1b]52;c;aGk=\x07\x1b]52;c;Ynll\x07"), "hi", "bye")
}

func TestOSC52RecoversAfterGarbage(t *testing.T) {
	wantWrites(t, scan52("\x01\x02random\x7fbytes\x1b]52;c;aGk=\x07tail"), "hi")
}

func TestOSC52PayloadSizeLimit(t *testing.T) {
	var s osc52Scanner
	huge := append([]byte("\x1b]52;c;"), bytes.Repeat([]byte("A"), osc52MaxPayload+16)...)
	huge = append(huge, 0x07)
	if got := s.scan(huge); len(got) != 0 {
		t.Fatalf("oversized payload should be abandoned, got %d writes", len(got))
	}
	// Scanner recovers for a subsequent valid write.
	wantWrites(t, s.scan([]byte("\x1b]52;c;aGk=\x07")), "hi")
}
