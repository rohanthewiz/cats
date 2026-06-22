package orchestration

import (
	"strings"
	"testing"
)

func TestOSCTitleOSC0andOSC2(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"osc0_bel", "\x1b]0;my title\x07", "my title"},
		{"osc2_st", "\x1b]2;vim - file.go\x1b\\", "vim - file.go"},
		{"surrounded", "out\x1b]0;htop\x07more", "htop"},
		{"empty_clear", "\x1b]2;\x07", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s oscTitleScanner
			got, ok := s.scan([]byte(tc.in))
			if !ok || got != tc.want {
				t.Fatalf("scan(%q) = (%q,%v), want (%q,true)", tc.in, got, ok, tc.want)
			}
		})
	}
}

func TestOSCTitleIgnoresOther(t *testing.T) {
	var s oscTitleScanner
	// OSC 1 (icon name only), OSC 7 cwd, OSC 9 progress, plain text → no title.
	if got, ok := s.scan([]byte("\x1b]1;icon\x07\x1b]7;file:///tmp\x07\x1b]9;4;3;\x07plain")); ok {
		t.Fatalf("unexpected title %q from non-title OSC input", got)
	}
}

func TestOSCTitleSplitAcrossReads(t *testing.T) {
	var s oscTitleScanner
	chunks := []string{"\x1b]0;my ", "long ", "title\x07"}
	var got string
	var ok bool
	for _, ch := range chunks {
		if t2, found := s.scan([]byte(ch)); found {
			got, ok = t2, true
		}
	}
	if !ok || got != "my long title" {
		t.Fatalf("split scan = (%q,%v), want (my long title, true)", got, ok)
	}
}

func TestOSCTitleLatestWins(t *testing.T) {
	var s oscTitleScanner
	got, ok := s.scan([]byte("\x1b]0;first\x07\x1b]2;second\x07"))
	if !ok || got != "second" {
		t.Fatalf("scan = (%q,%v), want (second, true)", got, ok)
	}
}

func TestOSCTitleStripsControlChars(t *testing.T) {
	var s oscTitleScanner
	got, ok := s.scan([]byte("\x1b]0;be\x01fore\x07"))
	if !ok || got != "before" {
		t.Fatalf("scan = (%q,%v), want (before, true)", got, ok)
	}
}

func TestOSCTitleCapsLength(t *testing.T) {
	var s oscTitleScanner
	long := strings.Repeat("a", oscProgressMaxChars+50)
	got, ok := s.scan([]byte("\x1b]2;" + long + "\x07"))
	if !ok {
		t.Fatal("expected a title")
	}
	if len(got) != oscProgressMaxChars {
		t.Fatalf("title len = %d, want %d", len(got), oscProgressMaxChars)
	}
}

func TestOSCTitleOverlongAbandoned(t *testing.T) {
	var s oscTitleScanner
	in := []byte("\x1b]0;")
	in = append(in, []byte(strings.Repeat("x", oscTitleMaxLen+10))...)
	in = append(in, 0x07)
	if got, ok := s.scan(in); ok {
		t.Fatalf("overlong OSC title should be abandoned, got %q", got)
	}
	if got, ok := s.scan([]byte("\x1b]0;ok\x07")); !ok || got != "ok" {
		t.Fatalf("recovery scan = (%q,%v), want (ok, true)", got, ok)
	}
}
