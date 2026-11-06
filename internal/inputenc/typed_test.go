package inputenc

import (
	"testing"

	"github.com/rohanthewiz/cats/internal/browserproto"
)

// RuneKey is a US keyboard: shifted characters carry shift on their base key,
// and a rune with no key on that board rides as text with no physical code.
func TestRuneKey(t *testing.T) {
	const shift = browserproto.ModShift
	tests := []struct {
		r    rune
		code string
		key  string
		mods uint8
	}{
		{'a', "KeyA", "a", 0},
		{'Z', "KeyZ", "Z", shift},
		{'7', "Digit7", "7", 0},
		{' ', "Space", " ", 0},
		{'/', "Slash", "/", 0},
		{'?', "Slash", "?", shift},
		{'!', "Digit1", "!", shift},
		{'@', "Digit2", "@", shift},
		{'"', "Quote", "\"", shift},
		{'é', "", "é", 0},
		{'—', "", "—", 0},
		{'✓', "", "✓", 0},
	}
	for _, tc := range tests {
		code, key, mods, ok := RuneKey(tc.r)
		if !ok || code != tc.code || key != tc.key || mods != tc.mods {
			t.Errorf("RuneKey(%q) = %q %q %d %v, want %q %q %d", tc.r, code, key, mods, ok, tc.code, tc.key, tc.mods)
		}
	}
	for _, r := range []rune{'\n', '\r', '\t', 0x1b, 0x7f, 0x85} {
		if _, _, _, ok := RuneKey(r); ok {
			t.Errorf("RuneKey(%q) should refuse a control rune", r)
		}
	}
}

// A named stroke gets the .key a browser would pair with its code; a typed
// stroke is one key event per rune, in order.
func TestStrokeKeys(t *testing.T) {
	got, ok := StrokeKeys(browserproto.KeyStroke{Code: "Enter", Mods: browserproto.ModShift})
	if !ok || len(got) != 1 || got[0].Code != "Enter" || got[0].Key != "Enter" || got[0].Mods != browserproto.ModShift {
		t.Fatalf("shift+Enter = %+v %v", got, ok)
	}
	got, _ = StrokeKeys(browserproto.KeyStroke{Code: "KeyS"})
	if got[0].Key != "s" {
		t.Fatalf("KeyS .key = %q, want s", got[0].Key)
	}
	got, _ = StrokeKeys(browserproto.KeyStroke{Code: "Space"})
	if got[0].Key != " " {
		t.Fatalf("Space .key = %q, want a space", got[0].Key)
	}
	got, ok = StrokeKeys(browserproto.KeyStroke{Text: "Hi é"})
	if !ok || len(got) != 4 {
		t.Fatalf("Hi é = %+v %v, want 4 events", got, ok)
	}
	if got[0].Code != "KeyH" || got[0].Mods != browserproto.ModShift || got[3].Key != "é" {
		t.Fatalf("Hi é events = %+v", got)
	}
}
