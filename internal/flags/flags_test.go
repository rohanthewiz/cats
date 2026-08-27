package flags

import (
	"encoding/json"
	"strings"
	"testing"
)

// The two halves of the vocabulary must stay disjoint: a bare ASCII word is a
// named kind or nothing, and everything else is a glyph. The failure this
// guards against is the quiet one — a misspelled "folloup" silently becoming a
// flag that renders the word "folloup" in the sidebar.
func TestParseKind(t *testing.T) {
	cases := []struct {
		in      string
		want    Kind
		wantErr string // substring; "" = must succeed
	}{
		{"", "", ""},                          // the clear
		{"   ", "", ""},                       // whitespace is still the clear
		{"followup", KindFollowup, ""},        //
		{"  star  ", KindStar, ""},            // trimmed
		{"FOLLOWUP", KindFollowup, ""},        // named kinds are case-insensitive
		{"WaRn", KindWarn, ""},                //
		{"folloup", "", "unknown flag kind"},  // a typo is refused, not stored
		{"todo", "", "unknown flag kind"},     // a plausible word we do not know
		{"🍕", "🍕", ""},                        // a custom glyph
		{"★", "★", ""},                        // one that happens to be a kind's own glyph
		{"❗", "❗", ""},                        //
		{"a b", "", "whitespace"},             // two words are not a mark
		{"x\ny", "", "whitespace"},            //
		{"\x1b[31m", "", "control character"}, // an escape sequence is refused outright
		{"🍕🍕🍕🍕🍕🍕🍕🍕🍕", "", "too long"},         // 9 code points
	}
	for _, c := range cases {
		got, err := ParseKind(c.in)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("ParseKind(%q) = error %v, want %q", c.in, err, c.want)
		case c.wantErr != "" && err == nil:
			t.Errorf("ParseKind(%q) = %q, want an error containing %q", c.in, got, c.wantErr)
		case c.wantErr != "" && !strings.Contains(err.Error(), c.wantErr):
			t.Errorf("ParseKind(%q) error = %v, want it to mention %q", c.in, err, c.wantErr)
		case c.wantErr == "" && got != c.want:
			t.Errorf("ParseKind(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The error for an unknown kind has to name the alternatives: the caller is
// often a script or an agent that never saw a menu.
func TestUnknownKindErrorListsTheVocabulary(t *testing.T) {
	_, err := ParseKind("nope")
	if err == nil {
		t.Fatal("ParseKind(nope) succeeded")
	}
	for _, d := range Defs() {
		if !strings.Contains(err.Error(), string(d.Kind)) {
			t.Errorf("error %q does not name %q", err, d.Kind)
		}
	}
}

// A named kind resolves to its glyph; anything else IS its glyph. One rendering
// path is the whole reason the two shapes share a field.
func TestGlyphAndDescribe(t *testing.T) {
	if got := Glyph(KindFollowup); got != "⚑" {
		t.Errorf("Glyph(followup) = %q", got)
	}
	if got := Glyph("🍕"); got != "🍕" {
		t.Errorf("Glyph(🍕) = %q", got)
	}
	if got := Describe(KindWarn); got != "problem" {
		t.Errorf("Describe(warn) = %q", got)
	}
	// A custom glyph describes itself: nothing here knows what the user meant.
	if got := Describe("🍕"); got != "🍕" {
		t.Errorf("Describe(🍕) = %q", got)
	}
}

// Every named kind must have a distinct glyph and a distinct name. Two flags
// that read alike at 12px are one flag with extra steps.
func TestVocabularyIsDistinct(t *testing.T) {
	seenKind, seenGlyph := map[Kind]bool{}, map[string]bool{}
	for _, d := range Defs() {
		if seenKind[d.Kind] {
			t.Errorf("duplicate kind %q", d.Kind)
		}
		if seenGlyph[d.Glyph] {
			t.Errorf("kind %q reuses glyph %q", d.Kind, d.Glyph)
		}
		if d.Label == "" || d.Meaning == "" {
			t.Errorf("kind %q has no label or meaning", d.Kind)
		}
		// A named kind's own name must survive a round trip through the parser,
		// or the table names something the CLI cannot spell.
		if got, err := ParseKind(string(d.Kind)); err != nil || got != d.Kind {
			t.Errorf("ParseKind(%q) = %q, %v", d.Kind, got, err)
		}
		seenKind[d.Kind], seenGlyph[d.Glyph] = true, true
	}
}

// A note is sanitized rather than refused — it is prose someone typed or pasted
// — but it must come out single-line and bounded, because it is drawn in a
// one-line row and stored in the session snapshot.
func TestCleanNote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"  waiting on review  ", "waiting on review"},
		{"a\r\nb", "a b"},
		{"a\tb", "a b"},
		{"drop\x00the\x07bells", "dropthebells"},
		{"a   b", "a b"},
	}
	for _, c := range cases {
		if got := CleanNote(c.in); got != c.want {
			t.Errorf("CleanNote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	long := strings.Repeat("é", MaxNoteRunes+50)
	if got := []rune(CleanNote(long)); len(got) != MaxNoteRunes {
		t.Errorf("CleanNote(long) kept %d runes, want %d", len(got), MaxNoteRunes)
	}
}

// New is the one constructor the dispatcher uses: a blank kind is the clear,
// and a note with no kind is dropped rather than stored invisibly.
func TestNew(t *testing.T) {
	f, err := New("followup", "  hold\tfor review  ", 42)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if f.Kind != KindFollowup || f.Note != "hold for review" || f.AtMs != 42 {
		t.Fatalf("New = %+v", f)
	}
	if f, err := New("", "orphaned note", 42); err != nil || f != nil {
		t.Fatalf("New(\"\") = %+v, %v; want nil, nil", f, err)
	}
	if _, err := New("nope", "", 42); err == nil {
		t.Fatal("New(nope) succeeded")
	}
}

// Equal is what lets a no-op set skip the broadcast and the save. Re-flagging
// with the same kind and note is NOT a no-op: it is a deliberate "still true,
// as of now", and the timestamp is the only record of it.
func TestEqual(t *testing.T) {
	a := &Flag{Kind: KindStar, Note: "n", AtMs: 1}
	if !a.Equal(&Flag{Kind: KindStar, Note: "n", AtMs: 1}) {
		t.Error("identical flags compared unequal")
	}
	if a.Equal(&Flag{Kind: KindStar, Note: "n", AtMs: 2}) {
		t.Error("a re-flag at a new time compared equal")
	}
	if a.Equal(nil) || (*Flag)(nil).Equal(a) {
		t.Error("a flag compared equal to nil")
	}
	if !(*Flag)(nil).Equal(nil) {
		t.Error("nil compared unequal to nil")
	}
}

// Clone must break the aliasing, or a snapshot and the live model share a flag
// that a later edit rewrites under both.
func TestClone(t *testing.T) {
	if (*Flag)(nil).Clone() != nil {
		t.Error("Clone(nil) is not nil")
	}
	a := &Flag{Kind: KindNote, Note: "before"}
	c := a.Clone()
	c.Note = "after"
	if a.Note != "before" {
		t.Errorf("Clone aliased the original: %q", a.Note)
	}
}

// The persisted shape is part of the contract: a session snapshot written today
// has to load a year from now. Note and AtMs are omitempty; kind never is,
// because a flag without one is not a flag.
func TestJSONShape(t *testing.T) {
	b, err := json.Marshal(&Flag{Kind: KindFollowup})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"kind":"followup"}` {
		t.Errorf("bare flag marshalled as %s", b)
	}
	b, _ = json.Marshal(&Flag{Kind: "🍕", Note: "n", AtMs: 7})
	if string(b) != `{"kind":"🍕","note":"n","at_ms":7}` {
		t.Errorf("full flag marshalled as %s", b)
	}
}

func TestString(t *testing.T) {
	if got := (&Flag{Kind: KindFollowup, Note: "ping the API team"}).String(); got != "⚑ follow-up: ping the API team" {
		t.Errorf("String = %q", got)
	}
	// A custom glyph is its own description, so it is not printed twice.
	if got := (&Flag{Kind: "🍕"}).String(); got != "🍕" {
		t.Errorf("String = %q", got)
	}
	if got := (*Flag)(nil).String(); got != "" {
		t.Errorf("nil String = %q", got)
	}
}
