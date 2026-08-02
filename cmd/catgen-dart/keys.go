package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// The key table is the one generated artefact whose source is not in this repo.
//
// A Flutter client receives raw keys as PhysicalKeyboardKey.usbHidUsage — a USB
// HID usage code. catway's `key` message wants a W3C KeyboardEvent.code
// ("KeyA", "ArrowLeft"), because internal/inputenc converts exactly that into
// libghostty's key names. Something has to bridge 269 entries.
//
// Hand-writing it would be 269 chances to make a typo that manifests as one
// dead key on one keyboard layout. But the mapping already exists, in the data
// file Flutter itself generates PhysicalKeyboardKey from:
//
//	$FLUTTER_ROOT/dev/tools/gen_keycodes/data/physical_key_data.g.json
//	  entry.names.name    ==  the W3C KeyboardEvent.code, byte for byte
//	  entry.scanCodes.usb ==  PhysicalKeyboardKey.usbHidUsage
//
// So the table is transcribed by a program from the same source of truth the
// Flutter side derives its constants from, and cannot disagree with it.
//
// The generated file is committed, and regenerating it needs -flutter-root.
// That is deliberate: this input changes only on an SDK upgrade, so making
// cats's build depend on a Flutter checkout would buy no drift protection. What
// runs on every build instead is TestGeneratedKeyCodesParse, which feeds the
// COMMITTED table through inputenc's own w3cKeyName → libghostty.ParseKey — the
// check that actually matters, since a code that reaches ghostty and does not
// parse is a key that silently does nothing.

// physicalKeyEntry is the slice of Flutter's data file this generator reads.
type physicalKeyEntry struct {
	Names struct {
		Name string `json:"name"`
	} `json:"names"`
	ScanCodes struct {
		USB *int `json:"usb"`
	} `json:"scanCodes"`
}

// keyDataRelPath is where the table lives inside a Flutter SDK checkout.
var keyDataRelPath = filepath.Join("dev", "tools", "gen_keycodes", "data", "physical_key_data.g.json")

func emitKeys(flutterRoot string) (string, error) {
	path := filepath.Join(flutterRoot, keyDataRelPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading Flutter's key data: %w", err)
	}
	var entries map[string]physicalKeyEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return "", fmt.Errorf("parsing %s: %w", path, err)
	}

	// Index by USB code. Flutter's file is keyed by an internal name; the entry's
	// names.name is the W3C code and is what we emit.
	byUSB := map[int]string{}
	for _, entry := range entries {
		if entry.ScanCodes.USB == nil || entry.Names.Name == "" {
			continue // an entry with no USB usage cannot arrive from a keyboard
		}
		usb := *entry.ScanCodes.USB
		if prev, dup := byUSB[usb]; dup {
			return "", fmt.Errorf("USB usage 0x%X maps to both %q and %q — "+
				"Flutter's data file changed shape", usb, prev, entry.Names.Name)
		}
		byUSB[usb] = entry.Names.Name
	}
	if len(byUSB) == 0 {
		return "", fmt.Errorf("%s yielded no entries", path)
	}

	codes := make([]int, 0, len(byUSB))
	for usb := range byUSB {
		codes = append(codes, usb)
	}
	sort.Ints(codes)

	e := &emitter{}
	e.header("USB HID usage → W3C KeyboardEvent.code, for raw key input.",
		[]string{"$FLUTTER_ROOT/" + filepath.ToSlash(keyDataRelPath) + " (Flutter SDK)"})
	e.p("// Regenerating this file specifically:")
	e.p("//   go run ./cmd/catgen-dart -out <dir> -flutter-root \"$FLUTTER_ROOT\"")
	e.p("//")
	e.p("// Flutter's own PhysicalKeyboardKey constants come from this same file, so")
	e.p("// PhysicalKeyboardKey.usbHidUsage is guaranteed to be a key of this map for")
	e.p("// every key Flutter can report.")
	e.nl()
	e.p("/// Maps `PhysicalKeyboardKey.usbHidUsage` to the W3C `KeyboardEvent.code`")
	e.p("/// catway's `key` message expects.")
	e.p("///")
	e.p("/// A miss means Flutter reported a key this table does not know. Send")
	e.p("/// `Unidentified` rather than guessing: catway falls back to")
	e.p("/// libghostty's KeyUnidentified, which encodes the event's text if it has")
	e.p("/// any and nothing otherwise — the honest outcome for a key we cannot name.")
	e.p("const Map<int, String> kUsbHidToW3cCode = <int, String>{")
	for _, usb := range codes {
		e.p("  0x%05X: %s,", usb, dartString(byUSB[usb]))
	}
	e.p("};")
	e.nl()
	e.p("/// The code to send for a key absent from [kUsbHidToW3cCode].")
	e.p("const String kUnidentifiedKeyCode = 'Unidentified';")
	e.nl()
	e.p("/// The W3C code for a physical key, or [kUnidentifiedKeyCode].")
	e.p("String w3cCodeForUsbHid(int usbHidUsage) =>")
	e.p("    kUsbHidToW3cCode[usbHidUsage] ?? kUnidentifiedKeyCode;")
	return e.b.String(), nil
}
