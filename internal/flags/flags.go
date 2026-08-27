// Package flags is the vocabulary, the validation and the normalization for
// user flags — the small persistent annotations a person pins to a workspace
// or to a pane so they can find it again tomorrow.
//
// A flag is two things at once, and the split is the whole design:
//
//	kind ── what it MEANS, drawn as a glyph in a colour  ── "followup", "⚑", red
//	note ── what it means TO YOU, free text              ── "waiting on the API review"
//
// The kind is what a glance reads and what a script can filter on; the note is
// what the glance cannot carry. Either half stands alone: a bare ⚑ with no note
// is a legitimate "come back here", and the note is what a tooltip and a future
// listing show once the glyph has done its job of catching the eye.
//
// # Named kinds, and glyphs that are not
//
// Kind holds EITHER one of the six named kinds below or a literal glyph the user
// typed. Both are stored, sent and persisted as the same string field, and the
// clients render them through one path — the named kind resolves to its glyph
// through Lookup, and anything else IS its glyph. That is why the two shapes are
// kept disjoint by ParseKind: a bare word made of ASCII letters, digits, '-' and
// '_' is reserved for the vocabulary, so a misspelled "folloup" is refused
// rather than silently becoming a flag that renders the word "folloup".
//
//	"followup"  → named   → ⚑ in red, with a documented meaning
//	"🍕"         → glyph   → 🍕, meaning whatever the note says
//	"folloup"   → refused → unknown flag kind "folloup"
//
// Nothing here knows about colours or SVG: the clients own the drawing. What
// this package owns is the set of names, so the server, the CLI, the browser and
// the generated mobile client cannot drift on what "followup" is.
package flags

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Kind is a flag's meaning: one of the named kinds, or a literal glyph.
type Kind string

// The named kinds. Six, chosen so that each has a distinct monochrome glyph and
// a distinct colour — a sidebar row is a dozen pixels of colour and shape, and
// two flags that read alike at that size are one flag with extra steps.
const (
	KindFollowup Kind = "followup" // ⚑ red    — come back to this
	KindQuestion Kind = "question" // ?  amber  — waiting on an answer
	KindStar     Kind = "star"     // ★ gold   — important, worth finding again
	KindWarn     Kind = "warn"     // ⚠ orange — something is wrong here
	KindDone     Kind = "done"     // ✓ green  — handled, nothing left to do
	KindNote     Kind = "note"     // ✎ muted  — just a note
)

// Def describes one named kind for the clients that draw it and the CLI that
// documents it. Glyph is the fallback rendering — a client with a real icon for
// the kind may use it instead, but every client must be able to fall back here,
// because the custom-glyph case has nothing else to draw.
type Def struct {
	Kind    Kind
	Glyph   string // the monochrome character clients render when they have no icon
	Label   string // short human name, for menus ("follow-up")
	Meaning string // what setting it says, for tooltips and `catctl flags`
}

// defs is the vocabulary, in the order menus and help output list it: the ones
// that ask for attention first, the ones that record a state last.
var defs = []Def{
	{KindFollowup, "⚑", "follow-up", "come back to this"},
	{KindQuestion, "?", "question", "waiting on an answer"},
	{KindStar, "★", "important", "worth finding again"},
	{KindWarn, "⚠", "problem", "something is wrong here"},
	{KindDone, "✓", "done", "handled — nothing left to do"},
	{KindNote, "✎", "note", "just a note"},
}

// byKind indexes defs for Lookup. Built once; the vocabulary is a compile-time
// constant in everything but spelling.
var byKind = func() map[Kind]Def {
	m := make(map[Kind]Def, len(defs))
	for _, d := range defs {
		m[d.Kind] = d
	}
	return m
}()

// Defs returns the named vocabulary in menu order. The slice is a copy, so a
// caller sorting or trimming it cannot corrupt the table.
func Defs() []Def {
	out := make([]Def, len(defs))
	copy(out, defs)
	return out
}

// Lookup resolves a named kind. ok is false for a custom glyph, which is the
// signal to render the kind string itself.
func Lookup(k Kind) (Def, bool) {
	d, ok := byKind[k]
	return d, ok
}

// Names lists the named kinds as plain strings, sorted, for error messages and
// shell completion.
func Names() []string {
	out := make([]string, 0, len(defs))
	for _, d := range defs {
		out = append(out, string(d.Kind))
	}
	sort.Strings(out)
	return out
}

// Glyph is what a client draws for a kind: the named kind's glyph, or the kind
// itself when it is already one. The single rendering path both halves of the
// vocabulary go through.
func Glyph(k Kind) string {
	if d, ok := byKind[k]; ok {
		return d.Glyph
	}
	return string(k)
}

// Describe is Glyph's companion for prose — a menu label, a tooltip, a CLI
// line. A named kind gets its label; a custom glyph is its own description,
// because nothing here knows what the user meant by it.
func Describe(k Kind) string {
	if d, ok := byKind[k]; ok {
		return d.Label
	}
	return string(k)
}

const (
	// MaxGlyphRunes bounds a custom kind. One grapheme is the intent; the
	// allowance is for the multi-codepoint ones — a ZWJ emoji sequence, a base
	// character plus a variation selector or a skin-tone modifier — which are
	// several runes and one glyph. Past that it is a word, not a mark, and a
	// word does not fit where these are drawn.
	MaxGlyphRunes = 8
	// MaxNoteRunes bounds the note. Generous enough for a sentence with a
	// ticket number in it, short enough that a tooltip and a sidebar row stay
	// readable — and, being a bound at all, short enough that a runaway script
	// cannot grow the session snapshot without limit.
	MaxNoteRunes = 500
)

// ParseKind validates and normalizes what a caller asked for. The empty string
// is returned as-is and means "no flag" — clearing is spelled the same way
// clearing a custom name is, so every mutation in the vocabulary reads alike.
//
// The two accepted shapes are deliberately disjoint (see the package doc): a
// bare ASCII word must be a known kind, and anything else must look like a mark.
func ParseKind(s string) (Kind, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	// A named kind is matched case-insensitively — "Followup" off a script or a
	// shell history is the same intent — but only for the word shape, so
	// lowercasing can never rewrite somebody's glyph.
	if isWordShaped(s) {
		k := Kind(strings.ToLower(s))
		if _, ok := byKind[k]; ok {
			return k, nil
		}
		return "", fmt.Errorf("unknown flag kind %q (one of: %s, or a single glyph)",
			s, strings.Join(Names(), ", "))
	}
	n := 0
	for _, r := range s {
		// Control characters and whitespace are refused rather than stripped: a
		// glyph is one visible mark, and something that contains a newline or a
		// terminal escape is not one. Stripping would leave the user with a flag
		// they did not ask for; refusing tells them so.
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", fmt.Errorf("flag glyph %q contains whitespace or a control character", s)
		}
		n++
	}
	if n > MaxGlyphRunes {
		return "", fmt.Errorf("flag glyph %q is too long (max %d code points)", s, MaxGlyphRunes)
	}
	return Kind(s), nil
}

// isWordShaped reports whether s is the shape reserved for named kinds: ASCII
// letters, digits, '-' and '_'. It is the discriminator between the two halves
// of the vocabulary, so it is spelled out here rather than inlined.
func isWordShaped(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// CleanNote normalizes a note for storage. Unlike a glyph this is sanitized
// rather than refused: the note is free text a person typed or a script pasted,
// so a stray tab or a pasted newline is a formatting accident, not a mistake
// worth failing a command over. What it must not do is stay multi-line — the
// note is drawn in a one-line row and a tooltip — so line breaks become spaces
// and every other control character is dropped.
func CleanNote(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case unicode.IsControl(r):
			// dropped
		default:
			b.WriteRune(r)
		}
	}
	// Collapse the runs the substitution above can create, then trim: "a\r\nb"
	// must read as "a b", not "a  b".
	out := strings.Join(strings.Fields(b.String()), " ")
	if r := []rune(out); len(r) > MaxNoteRunes {
		out = strings.TrimSpace(string(r[:MaxNoteRunes]))
	}
	return out
}

// Flag is one user annotation. It travels as a value inside the model (a nil
// *Flag is "unflagged") and as three flat fields on the wire — see app.FlagInfo,
// which is what the browser and the generated mobile client actually decode.
type Flag struct {
	Kind Kind   `json:"kind"`
	Note string `json:"note,omitempty"`
	// AtMs is when the flag was last set or edited, in Unix milliseconds.
	//
	// Milliseconds rather than a time.Time for two reasons that point the same
	// way: the wire structs are reflected into Dart by cmd/catgen-dart, which has
	// no mapping for time.Time, and a persisted integer cannot acquire a timezone
	// or a format between one release and the next. Clients that want "flagged 3d
	// ago" subtract it from their own clock, exactly as they already do with the
	// agents rollup's SinceMs.
	AtMs int64 `json:"at_ms,omitempty"`
}

// New builds a validated flag from what a command carried. A blank kind yields
// (nil, nil): "no flag", which is how every clear is spelled.
//
// The note survives on its own only when there is a kind to hang it from —
// a note with no mark is invisible in every surface that draws these, so
// accepting one would be accepting a write nobody can see.
func New(kind, note string, atMs int64) (*Flag, error) {
	k, err := ParseKind(kind)
	if err != nil {
		return nil, err
	}
	if k == "" {
		return nil, nil
	}
	return &Flag{Kind: k, Note: CleanNote(note), AtMs: atMs}, nil
}

// Equal reports whether two flags say the same thing, nils included. The
// mutations use it to answer "did this actually change?", so a no-op set can
// skip the broadcast and the save the way the workspace lock does.
//
// AtMs is part of the comparison: re-flagging with the same kind and note is a
// deliberate "still true, as of now", and the timestamp is the only thing that
// records it.
func (f *Flag) Equal(g *Flag) bool {
	if f == nil || g == nil {
		return f == g
	}
	return *f == *g
}

// Clone returns an independent copy (nil stays nil), so a snapshot and the live
// model never share a flag a later edit would mutate under one of them.
func (f *Flag) Clone() *Flag {
	if f == nil {
		return nil
	}
	c := *f
	return &c
}

// Glyph and Describe on the value, for the callers that hold a flag rather than
// a bare kind. A nil flag has nothing to draw and nothing to say.
func (f *Flag) Glyph() string {
	if f == nil {
		return ""
	}
	return Glyph(f.Kind)
}

func (f *Flag) Describe() string {
	if f == nil {
		return ""
	}
	return Describe(f.Kind)
}

// String renders a flag for a terminal listing: the glyph, its meaning, and the
// note when there is one — "⚑ follow-up: waiting on the API review".
func (f *Flag) String() string {
	if f == nil {
		return ""
	}
	s := f.Glyph()
	if d := f.Describe(); d != "" && d != s {
		s += " " + d
	}
	if f.Note != "" {
		s += ": " + f.Note
	}
	return s
}
