//go:build ghostty

package inputenc

import (
	"os"
	"regexp"
	"testing"

	libghostty "go.mitchellh.com/libghostty"
)

// This is the far end of a chain that starts in a Flutter SDK data file and
// ends in ghostty's key encoder:
//
//	PhysicalKeyboardKey.usbHidUsage      (the phone reports this)
//	  → kUsbHidToW3cCode                 (cmd/catgen-dart/keys.go generates it)
//	    → key{code: "KeyA"}              (the wire)
//	      → w3cKeyName → ParseKey        (this file's subject)
//	        → VT bytes                   (encoder.go)
//
// Every link but the last two is generated, and generated code is exactly the
// kind that nobody reads. So the test reads the committed table and puts every
// entry through the real conversion and the real parser. A code that reaches
// ghostty and does not parse is not an error anyone sees — encoder.go falls
// back to KeyUnidentified — it is a key that silently does nothing, which is
// the worst failure mode a keyboard can have.
//
// It runs on every `make check` (test-ghostty / race-ghostty) even though the
// generator's own input needs a Flutter SDK, which is the point: the committed
// table is what ships, so the committed table is what gets checked.

const keyTablePath = "../../cmd/catgen-dart/testdata/golden/keys.g.dart"

// keyTableEntry matches one generated row: `  0x00004: 'KeyA',`
var keyTableEntry = regexp.MustCompile(`(?m)^\s*0x[0-9A-F]+: '([^']+)',$`)

// ghosttyUnknownCodes are the W3C codes ghostty does not model — 107 of
// Flutter's 269, leaving 162 that reach the encoder.
//
// Flutter's table is the whole USB HID keyboard page, which is much larger than
// "keys a terminal can encode": gamepad buttons, the USB error sentinels,
// hardware launch keys, brightness and media transport, and the IME language
// keys. They are NOT filtered out of the generated map, deliberately — the
// phone should report honestly which key was pressed, and catway's
// KeyUnidentified fallback is the correct answer for a key with no VT sequence.
//
// Recording the partition here rather than skipping it is what gives the test
// teeth in both directions. A code that stops parsing is a regression in
// w3cKeyName. A code that starts parsing means ghostty grew support and this
// list should shrink. Either way the build says so.
//
// Regenerate by listing every entry whose ParseKey fails, sorted.
var ghosttyUnknownCodes = []string{
	"Abort", "Again", "BassBoost", "BrightnessAuto", "BrightnessDown", "BrightnessMaximum",
	"BrightnessMinimum", "BrightnessToggle", "BrightnessUp", "ChannelDown", "ChannelUp",
	"Close", "ClosedCaptionToggle", "DisplayToggleIntExt", "Exit", "Find", "GameButton1",
	"GameButton10", "GameButton11", "GameButton12", "GameButton13", "GameButton14",
	"GameButton15", "GameButton16", "GameButton2", "GameButton3", "GameButton4",
	"GameButton5", "GameButton6", "GameButton7", "GameButton8", "GameButton9", "GameButtonA",
	"GameButtonB", "GameButtonC", "GameButtonLeft1", "GameButtonLeft2", "GameButtonMode",
	"GameButtonRight1", "GameButtonRight2", "GameButtonSelect", "GameButtonStart",
	"GameButtonThumbLeft", "GameButtonThumbRight", "GameButtonX", "GameButtonY",
	"GameButtonZ", "Hyper", "Info", "KbdIllumDown", "KbdIllumUp", "KeyboardLayoutSelect",
	"Lang1", "Lang2", "Lang3", "Lang4", "Lang5", "LaunchAssistant", "LaunchAudioBrowser",
	"LaunchCalendar", "LaunchContacts", "LaunchControlPanel", "LaunchDocuments",
	"LaunchInternetBrowser", "LaunchKeyboardLayout", "LaunchPhone", "LaunchScreenSaver",
	"LaunchSpreadsheet", "LaunchWordProcessor", "LockScreen", "LogOff", "MailForward",
	"MailReply", "MailSend", "MediaFastForward", "MediaLast", "MediaPause", "MediaPlay",
	"MediaRecord", "MediaRewind", "MicrophoneMuteToggle", "New", "NumpadSignChange", "Open",
	"Print", "PrivacyScreenToggle", "ProgramGuide", "Props", "Redo", "Resume", "Save",
	"Select", "SelectTask", "ShowAllWindows", "SpeechInputToggle", "SpellCheck", "Super",
	"Suspend", "Turbo", "Undo", "UsbErrorRollOver", "UsbErrorUndefined", "UsbPostFail",
	"UsbReserved", "ZoomIn", "ZoomOut", "ZoomToggle",
}

func TestGeneratedKeyCodesParse(t *testing.T) {
	raw, err := os.ReadFile(keyTablePath)
	if err != nil {
		t.Fatalf("reading the generated key table: %v\n"+
			"regenerate with: go run ./cmd/catgen-dart -out cmd/catgen-dart/testdata/golden "+
			"-flutter-root \"$FLUTTER_ROOT\"", err)
	}
	matches := keyTableEntry.FindAllStringSubmatch(string(raw), -1)
	if len(matches) < 200 {
		t.Fatalf("only %d entries parsed out of %s; the generated shape changed",
			len(matches), keyTablePath)
	}

	expectedUnknown := make(map[string]bool, len(ghosttyUnknownCodes))
	for _, code := range ghosttyUnknownCodes {
		expectedUnknown[code] = true
	}

	unparsed := map[string]bool{}
	reached := 0
	for _, m := range matches {
		code := m[1]
		if _, err := libghostty.ParseKey(w3cKeyName(code)); err != nil {
			unparsed[code] = true
			continue
		}
		reached++
	}

	// Both directions are failures, for different reasons.
	for code := range unparsed {
		if !expectedUnknown[code] {
			t.Errorf("%q → w3cKeyName %q no longer reaches ghostty. A phone pressing "+
				"this key would silently do nothing — encoder.go falls back to "+
				"KeyUnidentified rather than erroring.", code, w3cKeyName(code))
		}
	}
	for _, code := range ghosttyUnknownCodes {
		if !unparsed[code] {
			t.Errorf("%q is listed in ghosttyUnknownCodes but now parses — "+
				"ghostty grew support for it; drop it from the list", code)
		}
	}
	t.Logf("%d of %d generated codes reach libghostty.ParseKey", reached, len(matches))
}

// TestCommonKeysSurviveTheChain spot-checks the keys a phone actually sends,
// naming them explicitly so a table that regenerated to something empty or
// truncated cannot pass by having nothing to check.
func TestCommonKeysSurviveTheChain(t *testing.T) {
	// USB HID usage → the code every one of these must produce. Written out from
	// the HID Usage Tables keyboard page rather than read back from the generated
	// file, so the two sources have to agree rather than agreeing with themselves.
	//
	// The 0x7 nibble is the usage PAGE (0x07, "Keyboard/Keypad") — Flutter's
	// scanCodes.usb is page<<16 | usage, and so is PhysicalKeyboardKey.usbHidUsage.
	// Dropping it would look like a working table and match nothing at runtime.
	want := map[int]string{
		0x70004: "KeyA",
		0x7001D: "KeyZ",
		0x7001E: "Digit1",
		0x70028: "Enter",
		0x70029: "Escape",
		0x7002A: "Backspace",
		0x7002B: "Tab",
		0x7002C: "Space",
		0x7003A: "F1",
		0x70045: "F12",
		0x7004F: "ArrowRight",
		0x70050: "ArrowLeft",
		0x70051: "ArrowDown",
		0x70052: "ArrowUp",
		0x700E0: "ControlLeft",
		0x700E1: "ShiftLeft",
		0x700E2: "AltLeft",
		0x700E3: "MetaLeft",
	}
	table := loadKeyTable(t)
	for usb, code := range want {
		got, ok := table[usb]
		if !ok {
			t.Errorf("USB 0x%05X is absent from the generated table", usb)
			continue
		}
		if got != code {
			t.Errorf("USB 0x%05X: table says %q, want %q", usb, got, code)
			continue
		}
		if _, err := libghostty.ParseKey(w3cKeyName(code)); err != nil {
			t.Errorf("%q does not reach ghostty: %v", code, err)
		}
	}
}

func loadKeyTable(t *testing.T) map[int]string {
	t.Helper()
	raw, err := os.ReadFile(keyTablePath)
	if err != nil {
		t.Fatal(err)
	}
	row := regexp.MustCompile(`(?m)^\s*0x([0-9A-F]+): '([^']+)',$`)
	out := map[int]string{}
	for _, m := range row.FindAllStringSubmatch(string(raw), -1) {
		var usb int
		for _, c := range m[1] {
			usb = usb*16 + hexDigit(c)
		}
		out[usb] = m[2]
	}
	return out
}

func hexDigit(c rune) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return 0
}
