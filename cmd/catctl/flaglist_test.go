package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/flags"
)

// flags [kind]: no argument lists everything (no params at all, the shape the
// dispatcher reads as the zero value), one argument filters, two is a usage
// error. The kind is NOT validated here — the server owns the vocabulary.
func TestBuildFlagList(t *testing.T) {
	sc, ok := lookupSubcommand("flags")
	if !ok {
		t.Fatal("no such verb flags")
	}
	raw, err := sc.build(nil)
	if err != nil || raw != nil {
		t.Errorf("flags with no args: want (nil, nil), got (%s, %v)", raw, err)
	}
	buildOK(t, "flags", []string{"followup"}, app.FlagListParams{Kind: "followup"})
	buildOK(t, "flags", []string{"🍕"}, app.FlagListParams{Kind: "🍕"})
	// A typo reaches the server, which is what makes the CLI's error text and
	// the browser's the same text.
	buildOK(t, "flags", []string{"folloup"}, app.FlagListParams{Kind: "folloup"})
	buildErr(t, "flags", []string{"followup", "extra"})
}

// The mark teaches the vocabulary for a named kind and gets out of the way for
// a glyph the user invented — Describe hands a custom kind back unchanged, so
// the naive spelling would render "🍕 🍕".
func TestFlagMark(t *testing.T) {
	if got := flagMark("followup"); got != "⚑ follow-up" {
		t.Errorf("flagMark(followup) = %q, want %q", got, "⚑ follow-up")
	}
	if got := flagMark("🍕"); got != "🍕" {
		t.Errorf("flagMark(🍕) = %q, want %q", got, "🍕")
	}
	// Every named kind must render as "<glyph> <label>", or the listing teaches
	// a vocabulary the server does not have.
	for _, d := range flags.Defs() {
		if got, want := flagMark(string(d.Kind)), d.Glyph+" "+d.Label; got != want {
			t.Errorf("flagMark(%s) = %q, want %q", d.Kind, got, want)
		}
	}
}

// paneWhere always leads with the handle, then the most specific thing known
// about the pane: a name the user chose, else the agent (with its state), else
// the live title, else nothing.
func TestPaneWhere(t *testing.T) {
	cases := []struct {
		name string
		in   app.PaneInfo
		want string
	}{
		{"handle only", app.PaneInfo{Handle: "w1:p3"}, "w1:p3"},
		{"custom name wins", app.PaneInfo{Handle: "w1:p3", Name: "build",
			PaneMeta: app.PaneMeta{Agent: "claude", Title: "zsh"}}, "w1:p3 build"},
		{"agent with state", app.PaneInfo{Handle: "w1:p3",
			PaneMeta: app.PaneMeta{Agent: "claude", AgentState: "idle", Title: "zsh"}}, "w1:p3 claude · idle"},
		{"agent without state", app.PaneInfo{Handle: "w1:p3",
			PaneMeta: app.PaneMeta{Agent: "codex"}}, "w1:p3 codex"},
		{"title is the fallback", app.PaneInfo{Handle: "w1:p3",
			PaneMeta: app.PaneMeta{Title: "make test"}}, "w1:p3 make test"},
	}
	for _, tc := range cases {
		if got := paneWhere(tc.in); got != tc.want {
			t.Errorf("%s: paneWhere = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestHumanAge(t *testing.T) {
	now := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }
	cases := []struct {
		in   int64
		want string
	}{
		{0, ""}, // no timestamp: a row from before AtMs existed
		{ago(3 * time.Second), "3s ago"},
		{ago(90 * time.Second), "1m ago"},
		{ago(3 * time.Hour), "3h ago"},
		{ago(50 * time.Hour), "2d ago"},
		// A clock that moved backwards says "now" rather than a negative age:
		// the timestamp is evidence about the flag, not about the clock.
		{now.Add(time.Hour).UnixMilli(), "now"},
	}
	for _, tc := range cases {
		if got := humanAge(tc.in, now); got != tc.want {
			t.Errorf("humanAge(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The width estimate is what keeps the columns lined up. Only three answers
// matter: a joiner or variation selector adds nothing, an emoji is two, and
// every named kind's glyph is one.
func TestDispWidth(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"w1:p3", 5},
		{"⚑", 1},
		{"🍕", 2},
		{"🍕️", 2},  // variation selector rides along
		{"👩‍💻", 4}, // ZWJ sequence: two wide glyphs, one joiner
		{"é", 1},  // combining acute
		{"漢字", 4},  // fullwidth CJK
	}
	for _, tc := range cases {
		if got := dispWidth(tc.in); got != tc.want {
			t.Errorf("dispWidth(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
	// Every glyph the vocabulary can draw must measure at least one column, or
	// pad would produce a negative-width run.
	for _, d := range flags.Defs() {
		if got := dispWidth(d.Glyph); got < 1 {
			t.Errorf("dispWidth(%q) = %d for kind %s", d.Glyph, got, d.Kind)
		}
	}
}

// nothingFlagged names the filter when there was one: "nothing flagged" is a
// misleading answer to `catctl flags done`.
func TestNothingFlagged(t *testing.T) {
	if got := nothingFlagged(nil); got != "nothing flagged" {
		t.Errorf("unfiltered = %q", got)
	}
	raw, err := json.Marshal(app.FlagListParams{Kind: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if got := nothingFlagged(raw); !strings.Contains(got, `"done"`) {
		t.Errorf("filtered = %q, want it to name the kind", got)
	}
}
