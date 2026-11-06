package wire

import (
	"strings"
	"testing"
)

// pane.send_keys refuses, with the reason, every call the server could only
// guess at: nothing to send, a stroke that is both shapes or neither, a control
// character hiding in typed text (a newline is the caller's choice of key), and
// a single call so large it is the unpaced burst the verb exists to avoid.
func TestSendKeysValidate(t *testing.T) {
	ok := []SendKeysParams{
		{Pane: 1, Keys: []KeyStroke{{Text: "fix the test"}}},
		{Pane: 1, Keys: []KeyStroke{{Code: "ArrowDown"}, {Code: "KeyS"}}},
		{Pane: 1, Keys: []KeyStroke{{Text: "a"}, {Code: "Enter", Mods: ModShift}, {Text: "b — é"}}},
		{Pane: 1, Keys: []KeyStroke{{Key: "s"}}},
	}
	for _, p := range ok {
		if err := p.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want ok", p, err)
		}
	}
	bad := []struct {
		p    SendKeysParams
		want string
	}{
		{SendKeysParams{Pane: 1}, "nothing to send"},
		{SendKeysParams{Pane: 1, Keys: []KeyStroke{{}}}, "is empty"},
		{SendKeysParams{Pane: 1, Keys: []KeyStroke{{Text: "a", Code: "Enter"}}}, "both text and a named key"},
		{SendKeysParams{Pane: 1, Keys: []KeyStroke{{Mods: ModCtrl}}}, "modifiers but no key"},
		{SendKeysParams{Pane: 1, Keys: []KeyStroke{{Text: "one\ntwo"}}}, "named keys"},
		{SendKeysParams{Pane: 1, Keys: []KeyStroke{{Text: "a\tb"}}}, "named keys"},
		{SendKeysParams{Pane: 1, Keys: []KeyStroke{{Text: "\xff"}}}, "not valid UTF-8"},
		{SendKeysParams{Pane: 1, Keys: []KeyStroke{{Text: strings.Repeat("x", maxSendKeysRunes)}}}, "split a long text"},
	}
	for _, tc := range bad {
		err := tc.p.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Validate(%+v) = %v, want an error with %q", tc.p, err, tc.want)
		}
	}
}
