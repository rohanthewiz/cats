package orchestration

import (
	"strings"
	"testing"
)

func TestOSC9ProgressBELandST(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"active_bel", "\x1b]9;4;3;\x07", "4;3;"},
		{"clear_st", "\x1b]9;4;0;\x1b\\", "4;0;"},
		{"surrounded", "out\x1b]9;4;1;50\x07more", "4;1;50"},
		{"empty_payload", "\x1b]9;\x07", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s osc9Scanner
			got, ok := s.scan([]byte(tc.in))
			if !ok || got != tc.want {
				t.Fatalf("scan(%q) = (%q,%v), want (%q,true)", tc.in, got, ok, tc.want)
			}
		})
	}
}

func TestOSC9IgnoresOther(t *testing.T) {
	var s osc9Scanner
	// OSC 0 title, OSC 7 cwd, and plain text must not yield progress.
	if p, ok := s.scan([]byte("\x1b]0;title\x07\x1b]7;file:///tmp\x07plain")); ok {
		t.Fatalf("unexpected progress %q from non-OSC9 input", p)
	}
}

func TestOSC9SplitAcrossReads(t *testing.T) {
	var s osc9Scanner
	chunks := []string{"\x1b]9;4", ";3", ";\x07"}
	var got string
	var ok bool
	for _, ch := range chunks {
		if p, found := s.scan([]byte(ch)); found {
			got, ok = p, true
		}
	}
	if !ok || got != "4;3;" {
		t.Fatalf("split scan = (%q,%v), want (4;3;, true)", got, ok)
	}
}

func TestOSC9LatestWins(t *testing.T) {
	var s osc9Scanner
	// Two progress reports in one chunk: the most recent is returned.
	got, ok := s.scan([]byte("\x1b]9;4;3;\x07\x1b]9;4;0;\x07"))
	if !ok || got != "4;0;" {
		t.Fatalf("scan = (%q,%v), want (4;0;, true)", got, ok)
	}
}

func TestOSC9StripsControlChars(t *testing.T) {
	var s osc9Scanner
	got, ok := s.scan([]byte("\x1b]9;4;\x013;\x07"))
	if !ok || got != "4;3;" {
		t.Fatalf("scan = (%q,%v), want (4;3;, true)", got, ok)
	}
}

func TestOSC9CapsLength(t *testing.T) {
	var s osc9Scanner
	long := strings.Repeat("a", oscProgressMaxChars+50)
	got, ok := s.scan([]byte("\x1b]9;" + long + "\x07"))
	if !ok {
		t.Fatal("expected a progress payload")
	}
	if len(got) != oscProgressMaxChars {
		t.Fatalf("payload len = %d, want %d", len(got), oscProgressMaxChars)
	}
}

func TestOSC9OverlongAbandoned(t *testing.T) {
	var s osc9Scanner
	in := append([]byte("\x1b]9;"), make([]byte, osc9MaxLen+10)...)
	for i := 2; i < len(in); i++ {
		if in[i] == 0 {
			in[i] = 'x'
		}
	}
	in = append(in, 0x07)
	if p, ok := s.scan(in); ok {
		t.Fatalf("overlong OSC 9 should be abandoned, got %q", p)
	}
	// Recovers for a subsequent valid sequence.
	if p, ok := s.scan([]byte("\x1b]9;4;3;\x07")); !ok || p != "4;3;" {
		t.Fatalf("recovery scan = (%q,%v), want (4;3;, true)", p, ok)
	}
}
