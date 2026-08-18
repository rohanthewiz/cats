package promptopts

import (
	"reflect"
	"testing"
)

// The real shapes: claude's permission prompt with its selection marker, and a
// codex-style boxed one. Both must come out as the menu a phone can answer.
func TestParseRealPrompts(t *testing.T) {
	cases := []struct {
		name    string
		capture string
		want    []Option
	}{
		{
			name: "claude permission prompt",
			capture: "● Bash(rg --files)\n" +
				"  ⎿  Running…\n" +
				"\n" +
				"Do you want to proceed?\n" +
				"❯ 1. Yes\n" +
				"  2. Yes, and don't ask again for rg commands\n" +
				"  3. No, and tell Claude what to do differently (esc)\n",
			want: []Option{
				{Key: "1", Label: "Yes"},
				{Key: "2", Label: "Yes, and don't ask again for rg commands"},
				{Key: "3", Label: "No, and tell Claude what to do…"},
			},
		},
		{
			name: "boxed prompt with a gutter",
			capture: "▌ Allow this command?\n" +
				"▌ 1) Yes\n" +
				"▌ 2) No\n",
			want: []Option{{Key: "1", Label: "Yes"}, {Key: "2", Label: "No"}},
		},
		{
			name:    "ansi styling is not part of the label",
			capture: "\x1b[1mChoose:\x1b[0m\n\x1b[32m 1. Approve\x1b[0m\n 2. Deny\n",
			want:    []Option{{Key: "1", Label: "Approve"}, {Key: "2", Label: "Deny"}},
		},
		{
			// A viewport capture ends in blank rows; the menu is still the last
			// thing printed.
			name:    "trailing blank rows",
			capture: " 1. Yes\n 2. No\n\n\n   \n",
			want:    []Option{{Key: "1", Label: "Yes"}, {Key: "2", Label: "No"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Parse(c.capture); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Parse =\n %+v\nwant\n %+v", got, c.want)
			}
		})
	}
}

// Everything that must yield NO buttons. Each of these is a way an ordinary
// screen can look like a menu, and a wrong button sends a real keystroke into a
// real terminal.
func TestParseRefusesWhatIsNotAMenu(t *testing.T) {
	cases := []struct {
		name    string
		capture string
	}{
		{"empty", ""},
		{"no numbers", "Do you want to proceed?\nyes/no\n"},
		{"one option", "Only one thing:\n 1. Yes\n"},
		{
			// The classic false positive: a numbered list that finished, with a
			// prompt drawn after it. Nothing is being asked.
			name:    "list followed by output",
			capture: " 1. first\n 2. second\nAll done.\n$ \n",
		},
		{
			// A menu whose head has scrolled off. Offering "4" as the first
			// button would answer a prompt whose other options are invisible.
			name:    "run that does not start at one",
			capture: "…\n 4. Four\n 5. Five\n",
		},
		{
			name:    "numbers out of order",
			capture: " 1. one\n 3. three\n",
		},
		{
			// A wrapped paragraph can end in a bare number.
			name:    "numbered lines with no text",
			capture: " 1.\n 2.\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Parse(c.capture); got != nil {
				t.Errorf("Parse = %+v; want no menu", got)
			}
		})
	}
}

// A menu longer than a notification can carry is truncated, not dropped: the
// first three choices are still the ones a prompt puts first.
func TestParseCapsAtMaxOptions(t *testing.T) {
	got := Parse(" 1. a\n 2. b\n 3. c\n 4. d\n 5. e\n")
	if len(got) != MaxOptions {
		t.Fatalf("got %d options, want %d", len(got), MaxOptions)
	}
	if got[0].Key != "1" || got[2].Key != "3" {
		t.Errorf("kept the wrong end: %+v", got)
	}
}

// A label is display text: the raw-key hint an agent appends is dropped, and a
// long entry is cut on a word boundary with an ellipsis.
func TestShortenLabels(t *testing.T) {
	cases := []struct{ in, want string }{
		{"No (esc)", "No"},
		{"Yes", "Yes"},
		{"Yes, and don't ask again for rg commands in /some/deep/path",
			"Yes, and don't ask again for rg…"},
		{"unbrokenlabelthatgoesonandonandonandonandonandonandon",
			"unbrokenlabelthatgoesonandonandonandonan…"},
	}
	for _, c := range cases {
		if got := shorten(c.in); got != c.want {
			t.Errorf("shorten(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
