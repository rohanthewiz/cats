//go:build ghostty

// Golden byte tests for the ghostty-backed encoders, keyed to the same
// dimensions as herdr's WS0-B2 differential matrix (mouse mode × mouse
// encoding × kitty flags) — now including kitty bits 2/8, which the Rust pure
// encoders deliberately degraded (the divergence WS9 retires). Run with:
//
//	PKG_CONFIG_PATH=<herdr>/vendor/libghostty-vt/zig-out/share/pkgconfig \
//	  go test -tags ghostty ./internal/inputenc/

package inputenc

import (
	"testing"

	"github.com/rohanthewiz/herdr-web/internal/browserproto"
	"github.com/rohanthewiz/herdr-web/internal/terminal"
)

func newEnc(t *testing.T, m terminal.InputModes) *Encoder {
	t.Helper()
	e, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(e.Close)
	e.SetModes(m)
	return e
}

func key(code, keyStr string, mods uint8, kind string) browserproto.Key {
	return browserproto.Key{T: browserproto.MsgKey, Code: code, Key: keyStr, Mods: mods, Kind: kind}
}

func press(code, keyStr string, mods uint8) browserproto.Key {
	return key(code, keyStr, mods, browserproto.KeyDown)
}

func TestKeyLegacy(t *testing.T) {
	const (
		shift = browserproto.ModShift
		ctrl  = browserproto.ModCtrl
		alt   = browserproto.ModAlt
	)
	tests := []struct {
		name string
		k    browserproto.Key
		want string
	}{
		{"enter", press("Enter", "Enter", 0), "\r"},
		{"backspace", press("Backspace", "Backspace", 0), "\x7f"},
		{"tab", press("Tab", "Tab", 0), "\t"},
		{"escape", press("Escape", "Escape", 0), "\x1b"},
		{"up", press("ArrowUp", "ArrowUp", 0), "\x1b[A"},
		{"down", press("ArrowDown", "ArrowDown", 0), "\x1b[B"},
		{"home", press("Home", "Home", 0), "\x1b[H"},
		{"page up", press("PageUp", "PageUp", 0), "\x1b[5~"},
		{"delete", press("Delete", "Delete", 0), "\x1b[3~"},
		{"f5", press("F5", "F5", 0), "\x1b[15~"},
		{"plain a", press("KeyA", "a", 0), "a"},
		{"space", press("Space", " ", 0), " "},
		{"shift a", press("KeyA", "A", shift), "A"},
		{"unicode text", press("KeyC", "ш", 0), "ш"},
		{"ctrl c", press("KeyC", "c", ctrl), "\x03"},
		{"ctrl space", press("Space", " ", ctrl), "\x00"},
		{"alt a", press("KeyA", "a", alt), "\x1ba"},
		{"macos option a", press("KeyA", "å", alt), "\x1ba"}, // option-as-alt undoes the compose
		{"shift up", press("ArrowUp", "ArrowUp", shift), "\x1b[1;2A"},
		{"ctrl delete", press("Delete", "Delete", ctrl), "\x1b[3;5~"},
		{"shift tab", press("Tab", "Tab", shift), "\x1b[Z"},
		{"ctrl shift f5", press("F5", "F5", ctrl|shift), "\x1b[15;6~"},
		{"release a", key("KeyA", "a", 0, browserproto.KeyUp), ""},
		{"repeat a", key("KeyA", "a", 0, browserproto.KeyRepeat), "a"},
		{"bare shift", press("ShiftLeft", "Shift", shift), ""},
		{"unknown kind", key("KeyA", "a", 0, "x"), ""},
	}
	e := newEnc(t, terminal.InputModes{})
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.Key(tc.k)
			if err != nil {
				t.Fatalf("Key: %v", err)
			}
			if string(got) != tc.want {
				t.Fatalf("Key(%+v) = %q, want %q", tc.k, got, tc.want)
			}
		})
	}
}

func TestKeyApplicationCursor(t *testing.T) {
	e := newEnc(t, terminal.InputModes{ApplicationCursor: true})
	tests := []struct {
		name string
		k    browserproto.Key
		want string
	}{
		{"up", press("ArrowUp", "ArrowUp", 0), "\x1bOA"},
		{"home", press("Home", "Home", 0), "\x1bOH"},
		{"shift up keeps modified form", press("ArrowUp", "ArrowUp", browserproto.ModShift), "\x1b[1;2A"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.Key(tc.k)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestKeyModifyOtherKeys pins the encoding that retires the Rust InputMirror's
// XTMODKEYS Enter special-case: with modifyOtherKeys active, modified
// otherwise-legacy keys use CSI 27;mods;code~.
func TestKeyModifyOtherKeys(t *testing.T) {
	e := newEnc(t, terminal.InputModes{ModifyOtherKeys: true})
	tests := []struct {
		name string
		k    browserproto.Key
		want string
	}{
		{"ctrl enter", press("Enter", "Enter", browserproto.ModCtrl), "\x1b[27;5;13~"},
		{"shift enter", press("Enter", "Enter", browserproto.ModShift), "\x1b[27;2;13~"},
		{"plain enter stays legacy", press("Enter", "Enter", 0), "\r"},
		{"plain a stays text", press("KeyA", "a", 0), "a"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := e.Key(tc.k)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestKeyKitty(t *testing.T) {
	const (
		disambiguate = 1
		reportEvents = 2
		alternates   = 4
		reportAll    = 8
	)
	const (
		shift = browserproto.ModShift
		ctrl  = browserproto.ModCtrl
		meta  = browserproto.ModMeta
	)
	tests := []struct {
		name  string
		flags uint16
		k     browserproto.Key
		want  string
	}{
		// Bit 1 — disambiguate.
		{"esc", disambiguate, press("Escape", "Escape", 0), "\x1b[27u"},
		{"ctrl c", disambiguate, press("KeyC", "c", ctrl), "\x1b[99;5u"},
		{"ctrl a", disambiguate, press("KeyA", "a", ctrl), "\x1b[97;5u"},
		{"super a", disambiguate, press("KeyA", "a", meta), "\x1b[97;9u"},
		{"ctrl enter", disambiguate, press("Enter", "Enter", ctrl), "\x1b[13;5u"},
		{"plain a stays text", disambiguate, press("KeyA", "a", 0), "a"},
		{"plain enter stays legacy", disambiguate, press("Enter", "Enter", 0), "\r"},
		// Bit 2 — report event types (the retired degradation).
		{"ctrl c press", disambiguate | reportEvents, press("KeyC", "c", ctrl), "\x1b[99;5u"},
		{"ctrl c repeat", disambiguate | reportEvents, key("KeyC", "c", ctrl, browserproto.KeyRepeat), "\x1b[99;5:2u"},
		{"ctrl c release", disambiguate | reportEvents, key("KeyC", "c", ctrl, browserproto.KeyUp), "\x1b[99;5:3u"},
		// Bit 4 — report alternate keys.
		{"shift a alternates", disambiguate | alternates | reportAll, press("KeyA", "A", shift), "\x1b[97:65;2u"},
		// Bit 8 — report all keys as escape codes (the retired degradation).
		{"plain a as escape", disambiguate | reportAll, press("KeyA", "a", 0), "\x1b[97u"},
		{"plain enter as escape", disambiguate | reportAll, press("Enter", "Enter", 0), "\x1b[13u"},
		{"arrow under kitty", disambiguate | reportAll, press("ArrowUp", "ArrowUp", 0), "\x1b[A"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnc(t, terminal.InputModes{KittyKeyboardFlags: tc.flags})
			got, err := e.Key(tc.k)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("flags %#b: got %q, want %q", tc.flags, got, tc.want)
			}
		})
	}
}

// TestKittyIgnoresDECCKM: kitty-mode arrows do not switch to SS3 application
// sequences (protocol rule; the Rust encoder pinned the same).
func TestKittyIgnoresDECCKM(t *testing.T) {
	e := newEnc(t, terminal.InputModes{ApplicationCursor: true, KittyKeyboardFlags: 1})
	got, err := e.Key(press("ArrowUp", "ArrowUp", 0))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "\x1b[A" {
		t.Fatalf("kitty + DECCKM arrow = %q, want \\x1b[A", got)
	}
}

func mouse(kind string, btn uint8, x, y uint16, mods uint8) browserproto.Mouse {
	return browserproto.Mouse{T: browserproto.MsgMouse, Pane: 1, X: x, Y: y, Btn: btn, Kind: kind, Mods: mods}
}

func TestMouseSGR(t *testing.T) {
	sgr := terminal.InputModes{MouseMode: terminal.MousePressRelease, MouseEncoding: terminal.MouseEncodingSGR}
	tests := []struct {
		name  string
		modes terminal.InputModes
		m     browserproto.Mouse
		want  string
	}{
		{"left down", sgr, mouse(browserproto.MouseDown, browserproto.BtnLeft, 4, 8, 0), "\x1b[<0;5;9M"},
		{"left up", sgr, mouse(browserproto.MouseUp, browserproto.BtnLeft, 4, 8, 0), "\x1b[<0;5;9m"},
		{"right down", sgr, mouse(browserproto.MouseDown, browserproto.BtnRight, 4, 8, 0), "\x1b[<2;5;9M"},
		{"middle down", sgr, mouse(browserproto.MouseDown, browserproto.BtnMiddle, 4, 8, 0), "\x1b[<1;5;9M"},
		{"shift left down", sgr, mouse(browserproto.MouseDown, browserproto.BtnLeft, 4, 8, browserproto.ModShift), "\x1b[<4;5;9M"},
		{"ctrl alt down", sgr, mouse(browserproto.MouseDown, browserproto.BtnLeft, 4, 8, browserproto.ModCtrl|browserproto.ModAlt), "\x1b[<24;5;9M"},
		{"move not reported in press-release", sgr, mouse(browserproto.MouseMove, browserproto.BtnNone, 4, 8, 0), ""},
		{"drag in button-motion",
			terminal.InputModes{MouseMode: terminal.MouseButtonMotion, MouseEncoding: terminal.MouseEncodingSGR},
			mouse(browserproto.MouseMove, browserproto.BtnLeft, 4, 8, 0), "\x1b[<32;5;9M"},
		{"bare move in any-motion",
			terminal.InputModes{MouseMode: terminal.MouseAnyMotion, MouseEncoding: terminal.MouseEncodingSGR},
			mouse(browserproto.MouseMove, browserproto.BtnNone, 4, 8, 0), "\x1b[<35;5;9M"},
		{"none mode reports nothing", terminal.InputModes{MouseEncoding: terminal.MouseEncodingSGR},
			mouse(browserproto.MouseDown, browserproto.BtnLeft, 4, 8, 0), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnc(t, tc.modes)
			got, err := e.Mouse(tc.m)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMouseLegacyFormats(t *testing.T) {
	x10fmt := terminal.InputModes{MouseMode: terminal.MousePressRelease, MouseEncoding: terminal.MouseEncodingDefault}
	tests := []struct {
		name  string
		modes terminal.InputModes
		m     browserproto.Mouse
		want  string
	}{
		// Default (X10 bytes): \x1b[M cb+32 col+1+32 row+1+32.
		{"left down", x10fmt, mouse(browserproto.MouseDown, browserproto.BtnLeft, 4, 8, 0), "\x1b[M %)"},
		{"left up encodes release as 3", x10fmt, mouse(browserproto.MouseUp, browserproto.BtnLeft, 4, 8, 0), "\x1b[M#%)"},
		{"shift adds 4", x10fmt, mouse(browserproto.MouseDown, browserproto.BtnLeft, 4, 8, browserproto.ModShift), "\x1b[M$%)"},
		// X10 tracking mode strips modifiers.
		{"x10 mode strips mods",
			terminal.InputModes{MouseMode: terminal.MouseX10},
			mouse(browserproto.MouseDown, browserproto.BtnLeft, 4, 8, browserproto.ModShift|browserproto.ModCtrl), "\x1b[M %)"},
		// UTF-8 (1005): coordinates >95 become multibyte codepoints.
		{"utf8 wide coordinate",
			terminal.InputModes{MouseMode: terminal.MousePressRelease, MouseEncoding: terminal.MouseEncodingUTF8},
			mouse(browserproto.MouseDown, browserproto.BtnLeft, 200, 8, 0), "\x1b[M é)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnc(t, tc.modes)
			got, err := e.Mouse(tc.m)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWheel(t *testing.T) {
	sgr := terminal.InputModes{MouseMode: terminal.MousePressRelease, MouseEncoding: terminal.MouseEncodingSGR}
	wheel := func(dx, dy int) browserproto.Mouse {
		m := mouse(browserproto.MouseWheel, browserproto.BtnNone, 4, 8, 0)
		m.DX, m.DY = dx, dy
		return m
	}
	tests := []struct {
		name  string
		modes terminal.InputModes
		m     browserproto.Mouse
		want  string
	}{
		{"wheel up", sgr, wheel(0, -1), "\x1b[<64;5;9M"},
		{"wheel down twice", sgr, wheel(0, 2), "\x1b[<65;5;9M\x1b[<65;5;9M"},
		{"wheel left", sgr, wheel(-1, 0), "\x1b[<66;5;9M"},
		{"wheel right", sgr, wheel(1, 0), "\x1b[<67;5;9M"},
		{"no reporting, no alt scroll", terminal.InputModes{}, wheel(0, 2), ""},
		{"alternate scroll",
			terminal.InputModes{AlternateScreen: true, MouseAlternateScroll: true},
			wheel(0, 2), "\x1b[B\x1b[B"},
		{"alternate scroll application cursor",
			terminal.InputModes{AlternateScreen: true, MouseAlternateScroll: true, ApplicationCursor: true},
			wheel(0, -1), "\x1bOA"},
		{"alt screen without 1007",
			terminal.InputModes{AlternateScreen: true},
			wheel(0, 2), ""},
		{"reporting beats alternate scroll",
			terminal.InputModes{AlternateScreen: true, MouseAlternateScroll: true,
				MouseMode: terminal.MousePressRelease, MouseEncoding: terminal.MouseEncodingSGR},
			wheel(0, -1), "\x1b[<64;5;9M"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnc(t, tc.modes)
			got, err := e.Mouse(tc.m)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPaste(t *testing.T) {
	e := newEnc(t, terminal.InputModes{BracketedPaste: true})
	got, err := e.Paste("echo hi")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "\x1b[200~echo hi\x1b[201~" {
		t.Fatalf("bracketed paste = %q", got)
	}

	plain := newEnc(t, terminal.InputModes{})
	got, err = plain.Paste("a\nb")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a\rb" {
		t.Fatalf("unbracketed paste should convert newlines, got %q", got)
	}
}

// TestModeSwitchMidStream: an encoder tracks the pane's mode changes (the β
// pane_modes mirror updates it between keystrokes).
func TestModeSwitchMidStream(t *testing.T) {
	e := newEnc(t, terminal.InputModes{})
	up := press("ArrowUp", "ArrowUp", 0)

	got, _ := e.Key(up)
	if string(got) != "\x1b[A" {
		t.Fatalf("legacy arrow = %q", got)
	}
	e.SetModes(terminal.InputModes{ApplicationCursor: true})
	got, _ = e.Key(up)
	if string(got) != "\x1bOA" {
		t.Fatalf("after DECCKM on, arrow = %q", got)
	}
	e.SetModes(terminal.InputModes{KittyKeyboardFlags: 1})
	got, _ = e.Key(press("Escape", "Escape", 0))
	if string(got) != "\x1b[27u" {
		t.Fatalf("after kitty on, esc = %q", got)
	}
}
