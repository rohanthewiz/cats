//go:build darwin

package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// What pbpaste printed for `“é” x` before the locale was pinned: MacRoman.
// The ObjC bridge turns invalid UTF-8 into nil and the page into "clipboard has
// no text", so validUTF8 must hand back valid UTF-8 that still carries the
// readable part of the text.
func TestValidUTF8RepairsMacRomanBytes(t *testing.T) {
	in := "\xd2\x8e\xd3 x"
	got := validUTF8(in)
	if !utf8.ValidString(got) {
		t.Fatalf("validUTF8(%q) = %q, still invalid", in, got)
	}
	if !strings.HasSuffix(got, " x") || !strings.Contains(got, "�") {
		t.Errorf("validUTF8(%q) = %q, want the ASCII kept and the bad bytes as U+FFFD", in, got)
	}
}

// Valid text — including the non-ASCII a pinned-locale pbpaste returns — passes
// through untouched.
func TestValidUTF8PassesValidText(t *testing.T) {
	for _, s := range []string{"", "plain", "smart “quotes” — café 🐱"} {
		if got := validUTF8(s); got != s {
			t.Errorf("validUTF8(%q) = %q, want it unchanged", s, got)
		}
	}
}
