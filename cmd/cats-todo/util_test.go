package main

import (
	"encoding/hex"
	"testing"
)

func TestFirstNonEmpty(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want string
	}{
		{"first wins", []string{"a", "b"}, "a"},
		{"skips leading empties", []string{"", "", "x", "y"}, "x"},
		{"all empty", []string{"", "", ""}, ""},
		{"no args", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstNonEmpty(tt.in...); got != tt.want {
				t.Errorf("firstNonEmpty(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"under limit untouched", "abc", 5, "abc"},
		{"exactly at limit untouched", "abc", 3, "abc"},
		{"cuts and ellipsizes", "abcdef", 3, "ab…"},
		{"max of one returns one rune, no ellipsis", "abc", 1, "a"},
		{"max of zero returns empty", "abc", 0, ""},
		// truncate counts runes, not bytes: each accented char is one rune, so a
		// five-rune string over a three-rune cap keeps two runes plus the ellipsis.
		{"multibyte counted by rune", "héllo", 3, "hé…"},
		{"empty stays empty", "", 5, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := truncate(tt.s, tt.max); got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.max, got, tt.want)
			}
			// The result must never exceed the cap in runes.
			if n := len([]rune(truncate(tt.s, tt.max))); tt.max >= 0 && n > tt.max {
				t.Errorf("truncate(%q, %d) is %d runes, exceeds cap", tt.s, tt.max, n)
			}
		})
	}
}

func TestFirstLine(t *testing.T) {
	tests := []struct {
		name string
		s    string
		max  int
		want string
	}{
		{"trims and returns first non-blank", "  \n   hello world  \nsecond", 100, "hello world"},
		{"skips leading blank lines", "\n\n\nfirst real", 100, "first real"},
		{"all blank yields empty", "\n   \n\t\n", 10, ""},
		{"empty yields empty", "", 10, ""},
		{"truncates the chosen line", "a very long first line here", 10, "a very lo…"},
		{"single line", "just one line", 100, "just one line"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstLine(tt.s, tt.max); got != tt.want {
				t.Errorf("firstLine(%q, %d) = %q, want %q", tt.s, tt.max, got, tt.want)
			}
		})
	}
}

func TestNewID(t *testing.T) {
	// 8 random bytes hex-encoded is 16 characters.
	id := newID()
	if len(id) != 16 {
		t.Errorf("newID() = %q, want 16 hex chars, got %d", id, len(id))
	}
	if _, err := hex.DecodeString(id); err != nil {
		t.Errorf("newID() = %q is not valid hex: %v", id, err)
	}

	// Two ids should differ — the whole point is keying a todo across edits.
	// Collisions are astronomically unlikely for 64 bits of randomness.
	seen := make(map[string]bool, 100)
	for range 100 {
		v := newID()
		if seen[v] {
			t.Fatalf("newID() produced a duplicate %q within 100 calls", v)
		}
		seen[v] = true
	}
}
