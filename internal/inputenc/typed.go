// typed.go — turning pane.send_keys strokes into the browser key events the
// encoder already knows how to make bytes from.
//
// pane.send_keys exists so a caller can TYPE into a pane rather than paste
// (wire.SendKeysParams says why). The encoder, though, speaks one language:
// browserproto.Key, a KeyboardEvent as the browser reports it — a physical
// Code, the produced Key text, a modifier mask. So a typed rune has to become
// the event a person's keyboard would have produced for it, and a named key
// has to get the .key value a browser would have paired with its .code.
//
//	{"text":"Hi!"}            {"code":"Enter","mods":ModShift}
//	      │                                 │
//	 RuneKey per rune                  NamedKey(code, mods)
//	      ▼                                 ▼
//	KeyH+shift "H" · KeyI "i" ·       Enter "Enter" +shift
//	Digit1+shift "!"
//	      └──────────────┬──────────────────┘
//	                     ▼
//	        Encoder.Key(press), Encoder.Key(release)
//
// It is pure Go (builds untagged) so the mapping is testable without
// libghostty; the encoder half is encoder.go's.
//
// The keyboard assumed is a US layout, the same assumption unshiftedCodepoint
// makes for punctuation, and the same table catctl probe's `type:` op uses
// (cmd/catctl/probe.go keyFor, which predates this and stays the probe's own:
// it is a test client's convenience, this is the server's contract). A rune a
// US keyboard has no key for — "é", "—", "✓", an emoji — is sent the way an
// IME or a compose sequence delivers it: no physical code, the rune as its
// text. The encoder then emits the rune's UTF-8 under legacy and kitty
// "disambiguate" modes, which is what an app sees when a person types it.

package inputenc

import (
	"strings"

	"github.com/rohanthewiz/cats/internal/browserproto"
)

// punctCodes maps the unshifted punctuation of a US keyboard to its W3C code.
var punctCodes = map[rune]string{
	'-': "Minus", '=': "Equal", '[': "BracketLeft", ']': "BracketRight",
	'\\': "Backslash", ';': "Semicolon", '\'': "Quote", '`': "Backquote",
	',': "Comma", '.': "Period", '/': "Slash",
}

// shiftedPunct maps a shifted symbol to the unshifted character on the same
// key, so "!" is shift+Digit1 and "?" is shift+Slash.
var shiftedPunct = map[rune]rune{
	'!': '1', '@': '2', '#': '3', '$': '4', '%': '5', '^': '6', '&': '7',
	'*': '8', '(': '9', ')': '0', '_': '-', '+': '=', '{': '[', '}': ']',
	'|': '\\', ':': ';', '"': '\'', '~': '`', '<': ',', '>': '.', '?': '/',
}

// RuneKey is the key event a person types r with. ok is false for control
// runes, which wire.SendKeysParams.Validate has already refused — a newline or
// a tab in typed text is a choice of key the caller makes, not this table.
func RuneKey(r rune) (code, key string, mods uint8, ok bool) {
	switch {
	case r < 0x20 || (r >= 0x7f && r < 0xa0):
		return "", "", 0, false
	case r >= 'a' && r <= 'z':
		return "Key" + strings.ToUpper(string(r)), string(r), 0, true
	case r >= 'A' && r <= 'Z':
		return "Key" + string(r), string(r), browserproto.ModShift, true
	case r >= '0' && r <= '9':
		return "Digit" + string(r), string(r), 0, true
	case r == ' ':
		return "Space", " ", 0, true
	}
	if c, found := punctCodes[r]; found {
		return c, string(r), 0, true
	}
	if base, found := shiftedPunct[r]; found {
		if c, f2 := punctCodes[base]; f2 {
			return c, string(r), browserproto.ModShift, true
		}
		return "Digit" + string(base), string(r), browserproto.ModShift, true
	}
	// No key on a US board: the rune rides as text with no physical code.
	return "", string(r), 0, true
}

// NamedKey is the KeyboardEvent.key a browser pairs with code: the letter for
// a KeyX (upper-cased under shift), the digit for an unshifted DigitN, and the
// code's own name for everything else, which is exactly the .key of the named
// keys (Enter, Tab, Escape, ArrowDown, F5, Backspace…). Space is the one named
// key whose .key is text.
func NamedKey(code string, mods uint8) string {
	if rest, ok := strings.CutPrefix(code, "Key"); ok && len(rest) == 1 {
		if mods&browserproto.ModShift != 0 {
			return rest
		}
		return strings.ToLower(rest)
	}
	if rest, ok := strings.CutPrefix(code, "Digit"); ok && len(rest) == 1 && mods&browserproto.ModShift == 0 {
		return rest
	}
	if code == "Space" {
		return " "
	}
	return code
}

// StrokeKeys expands one pane.send_keys stroke into the key presses it stands
// for, in order: one per rune of Text, or the one named key. Releases are the
// caller's to add — every press is followed by its release in the same write.
func StrokeKeys(s browserproto.KeyStroke) ([]browserproto.Key, bool) {
	if s.Text == "" {
		key := s.Key
		if key == "" {
			key = NamedKey(s.Code, s.Mods)
		}
		return []browserproto.Key{{Code: s.Code, Key: key, Mods: s.Mods}}, true
	}
	out := make([]browserproto.Key, 0, len(s.Text))
	for _, r := range s.Text {
		code, key, mods, ok := RuneKey(r)
		if !ok {
			return nil, false
		}
		out = append(out, browserproto.Key{Code: code, Key: key, Mods: mods})
	}
	return out, true
}
