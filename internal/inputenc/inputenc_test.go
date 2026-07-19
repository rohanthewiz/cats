package inputenc

import (
	"testing"

	"github.com/rohanthewiz/herdr-web/internal/terminal"
)

func TestW3CKeyName(t *testing.T) {
	tests := []struct{ code, want string }{
		{"KeyA", "key_a"},
		{"KeyZ", "key_z"},
		{"Digit0", "digit_0"},
		{"Digit9", "digit_9"},
		{"ArrowLeft", "arrow_left"},
		{"F1", "f1"},
		{"F12", "f12"},
		{"F25", "f25"},
		{"Fn", "fn"},
		{"FnLock", "fn_lock"},
		{"Numpad0", "numpad_0"},
		{"NumpadMemoryAdd", "numpad_memory_add"},
		{"NumpadClearEntry", "numpad_clear_entry"},
		{"PageUp", "page_up"},
		{"IntlBackslash", "intl_backslash"},
		{"Escape", "escape"},
		{"Enter", "enter"},
		{"Space", "space"},
		{"ContextMenu", "context_menu"},
		{"LaunchApp1", "launch_app_1"},
		{"AudioVolumeUp", "audio_volume_up"},
		{"BracketLeft", "bracket_left"},
		{"Backquote", "backquote"},
		{"MetaLeft", "meta_left"},
		{"PrintScreen", "print_screen"},
	}
	for _, tc := range tests {
		if got := w3cKeyName(tc.code); got != tc.want {
			t.Errorf("w3cKeyName(%q) = %q, want %q", tc.code, got, tc.want)
		}
	}
}

func TestKeyText(t *testing.T) {
	tests := []struct{ key, want string }{
		{"a", "a"},
		{"A", "A"},
		{"!", "!"},
		{" ", " "},
		{"ш", "ш"},
		{"Enter", ""},
		{"ArrowLeft", ""},
		{"Dead", ""},
		{"Unidentified", ""},
		{"", ""},
		{"\x1b", ""}, // control rune is never text
	}
	for _, tc := range tests {
		if got := keyText(tc.key); got != tc.want {
			t.Errorf("keyText(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

func TestUnshiftedCodepoint(t *testing.T) {
	tests := []struct {
		code, key string
		alt       bool
		want      rune
	}{
		{"KeyA", "a", false, 'a'},
		{"KeyA", "A", false, 'a'},   // shift undone via letter lowering
		{"KeyQ", "й", false, 'й'},   // layout wins over physical code for letters
		{"KeyA", "å", true, 'a'},    // alt held: option-composed text loses to the physical code
		{"KeyQ", "й", true, 'q'},    // alt held: physical code wins even without compose
		{"Digit1", "!", false, '1'}, // shift undone via physical code
		{"Digit1", "1", false, '1'},
		{"Comma", "<", false, ','},
		{"Minus", "_", false, '-'},
		{"BracketLeft", "{", false, '['},
		{"Space", " ", false, ' '},
		{"NumpadAdd", "+", false, '+'}, // fallback: the text itself
		{"ArrowLeft", "ArrowLeft", false, 0},
		{"Enter", "Enter", false, 0},
	}
	for _, tc := range tests {
		if got := unshiftedCodepoint(tc.code, tc.key, tc.alt); got != tc.want {
			t.Errorf("unshiftedCodepoint(%q, %q, alt=%v) = %q, want %q", tc.code, tc.key, tc.alt, got, tc.want)
		}
	}
}

func TestAlternateScrollActive(t *testing.T) {
	on := terminal.InputModes{AlternateScreen: true, MouseAlternateScroll: true}
	if !AlternateScrollActive(on) {
		t.Error("alt screen + 1007 + no mouse reporting should be active")
	}
	for name, m := range map[string]terminal.InputModes{
		"mouse reporting on": {AlternateScreen: true, MouseAlternateScroll: true, MouseMode: terminal.MousePressRelease},
		"primary screen":     {MouseAlternateScroll: true},
		"1007 off":           {AlternateScreen: true},
	} {
		if AlternateScrollActive(m) {
			t.Errorf("%s: should not be active", name)
		}
	}
}

func TestEncodeAlternateScroll(t *testing.T) {
	tests := []struct {
		name  string
		delta int
		app   bool
		want  string
	}{
		{"down normal", 2, false, "\x1b[B\x1b[B"},
		{"up normal", -1, false, "\x1b[A"},
		{"down application", 2, true, "\x1bOB\x1bOB"},
		{"up application", -3, true, "\x1bOA\x1bOA\x1bOA"},
		{"zero", 0, false, ""},
	}
	for _, tc := range tests {
		if got := string(EncodeAlternateScroll(tc.delta, tc.app)); got != tc.want {
			t.Errorf("%s: EncodeAlternateScroll(%d, %v) = %q, want %q", tc.name, tc.delta, tc.app, got, tc.want)
		}
	}
}
