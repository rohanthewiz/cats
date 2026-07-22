// Package inputenc encodes the browser's structured input events
// (browserproto key/mouse/paste — WS9 D4) into VT byte sequences for a pane's
// PTY, driven by the pane's live mode state (β pane_modes mirror,
// terminal.InputModes). The server owns encoding; the browser never
// pre-encodes (ai_docs/phase-c-ws9-protocol.md §6).
//
// The heavy lifting is ghostty's own key/mouse/paste encoders via
// go-libghostty (encoder.go, `-tags ghostty` + libghostty-vt on
// PKG_CONFIG_PATH) — the same library the cathost daemon embeds for VT
// emulation, so the encoder and the emulator interpreting its bytes can never
// drift apart. Wrapping it (WS9 task 3.1's spike outcome) supersedes the Rust
// InputMirror's pure encoders and retires their known kitty bits-2/8
// degradation: the full kitty protocol (disambiguate, report-event-types,
// report-alternates, report-all-keys, report-associated-text), xterm
// modifyOtherKeys, and DECCKM are all encoded natively.
//
// This file is pure Go (builds untagged): the W3C KeyboardEvent.code mapping
// and the alternate-scroll fallback (mode 1007), which is a policy above the
// encoders — ghostty implements it in its Surface, not its encoder.
package inputenc

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rohanthewiz/cats/internal/terminal"
)

// w3cKeyName converts a W3C KeyboardEvent.code ("KeyA", "Digit0", "ArrowLeft",
// "F1", "LaunchApp1") to libghostty's canonical snake_case key name ("key_a",
// "digit_0", "arrow_left", "f1", "launch_app_1") for libghostty.ParseKey. The
// conversion is mechanical — underscore before each uppercase letter and each
// digit run, then lowercase — except F-keys, whose names keep the digit
// attached ("f1", not "f_1").
func w3cKeyName(code string) string {
	if len(code) >= 2 && code[0] == 'F' && allASCIIDigits(code[1:]) {
		return "f" + code[1:]
	}
	var b strings.Builder
	b.Grow(len(code) + 4)
	prevDigit := false
	for i, r := range code {
		switch {
		case r >= 'A' && r <= 'Z':
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r + ('a' - 'A'))
			prevDigit = false
		case r >= '0' && r <= '9':
			if i > 0 && !prevDigit {
				b.WriteByte('_')
			}
			b.WriteRune(r)
			prevDigit = true
		default:
			b.WriteRune(r)
			prevDigit = false
		}
	}
	return b.String()
}

func allASCIIDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// keyText returns the text a key event produced: W3C KeyboardEvent.key when
// it is a single printable rune ("a", "A", "!", "ш", " "). Named keys
// ("Enter", "ArrowLeft", "Dead", "Unidentified") produce none — the encoder
// works from the logical key instead (libghostty SetUTF8 contract: no C0
// bytes, no function-key names).
func keyText(key string) string {
	r, n := utf8.DecodeRuneInString(key)
	if n == 0 || n != len(key) || r == utf8.RuneError || unicode.IsControl(r) {
		return ""
	}
	return key
}

// unshiftedCodepoint derives the key's unshifted codepoint (kitty
// report-alternates and macOS option-as-alt need it). With no alt held,
// letters use the browser's translated key text lowercased — honoring the
// active layout ("й" on a Russian layout, not the physical 'q'). With alt
// held, the text may be an option-composed character ("å" for option+a on
// macOS), so the physical code wins — option-as-alt uses this codepoint to
// emit ESC-a. Digits and punctuation can't be un-shifted from the text
// ("!" → '1'), so they come from the physical code, assuming a US layout for
// punctuation (the true unshifted character of another layout is unknowable
// from a KeyboardEvent; a wrong alternate is harmless advisory data).
// Fallback: the lowercased key text itself.
func unshiftedCodepoint(code, key string, altHeld bool) rune {
	if !altHeld {
		if t := keyText(key); t != "" {
			if r, _ := utf8.DecodeRuneInString(t); unicode.IsLetter(r) {
				return unicode.ToLower(r)
			}
		}
	}
	if len(code) == 4 && strings.HasPrefix(code, "Key") {
		return rune(code[3] - 'A' + 'a')
	}
	if len(code) == 6 && strings.HasPrefix(code, "Digit") {
		return rune(code[5])
	}
	switch code {
	case "Backquote":
		return '`'
	case "Minus":
		return '-'
	case "Equal":
		return '='
	case "BracketLeft":
		return '['
	case "BracketRight":
		return ']'
	case "Backslash", "IntlBackslash":
		return '\\'
	case "Semicolon":
		return ';'
	case "Quote":
		return '\''
	case "Comma":
		return ','
	case "Period":
		return '.'
	case "Slash":
		return '/'
	case "Space":
		return ' '
	}
	if t := keyText(key); t != "" {
		r, _ := utf8.DecodeRuneInString(t)
		return unicode.ToLower(r)
	}
	return 0
}

// AlternateScrollActive reports whether wheel input must become cursor keys
// instead of mouse reports or viewport scrolling: alt screen, no mouse
// reporting, and mode 1007 enabled (ghostty Surface rule).
func AlternateScrollActive(m terminal.InputModes) bool {
	return m.MouseMode == terminal.MouseNone && m.AlternateScreen && m.MouseAlternateScroll
}

// EncodeAlternateScroll encodes a wheel delta as cursor-key presses, one per
// line: positive deltaLines (wheel toward the user / scroll down) emits Down,
// negative emits Up. DECCKM selects SS3 application sequences, matching
// ghostty's Surface behavior byte-for-byte.
func EncodeAlternateScroll(deltaLines int, applicationCursor bool) []byte {
	if deltaLines == 0 {
		return nil
	}
	var seq string
	switch {
	case deltaLines < 0 && applicationCursor:
		seq = "\x1bOA"
	case deltaLines < 0:
		seq = "\x1b[A"
	case applicationCursor:
		seq = "\x1bOB"
	default:
		seq = "\x1b[B"
	}
	n := deltaLines
	if n < 0 {
		n = -n
	}
	out := make([]byte, 0, n*len(seq))
	for range n {
		out = append(out, seq...)
	}
	return out
}
