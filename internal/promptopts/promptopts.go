// Package promptopts reads an agent's on-screen menu out of a terminal capture.
//
// The problem it solves is narrow and specific. When an agent blocks on a
// permission prompt, catway learns *that* it blocked (the detector, or the
// agent's own hook) but not *what it asked*. The choices are on screen, because
// a human is meant to read them:
//
//	Do you want to proceed?
//	❯ 1. Yes
//	  2. Yes, and don't ask again for rg commands
//	  3. No, and tell Claude what to do differently (esc)
//
// A notification that says "claude needs attention" cannot be answered from a
// phone. One that says "Yes / Yes, don't ask / No" can — and the difference
// between them is this parser.
//
// # Why parse the screen rather than ask the agent
//
// The alternative is to teach every hook asset in internal/integration to
// report its menu. That is a fleet upgrade — every agent, every user's
// installed copy — for a feature that would STILL have to handle the agents
// that never send one, so the screen path has to exist either way. It is also
// the only source that is right by construction: what is on screen is what
// pressing "2" will select, whatever the agent believes it offered.
//
// # The rules, and why each is a rule
//
// A menu is the LAST contiguous run of numbered lines at the bottom of the
// capture. Numbered lines appear in ordinary output all the time — a test
// summary, a stack trace, an agent explaining three options in prose — and the
// one thing that distinguishes a live prompt is that nothing has been printed
// after it. Anchoring at the bottom is therefore not an optimisation; it is the
// check.
//
// Numbering must start at 1 and ascend by exactly 1. A run that starts at 4 is
// the tail of something longer that has scrolled, and offering "4" as the first
// button would answer a menu whose first three entries the user cannot see.
//
// Two options minimum. A single numbered line is a list of one, which is what
// ordinary output looks like far more often than a prompt does.
//
// Nothing parsed means no buttons — never a guess. A wrong button on a lock
// screen sends the wrong keystroke into a real terminal, so the failure mode
// has to be "you have to go and look", not "it pressed something".
package promptopts

import (
	"regexp"
	"strconv"
	"strings"
)

// MaxOptions is how many choices are worth carrying. ntfy renders at most three
// action buttons, and a prompt with more than a handful of branches is one to
// read on a real screen anyway.
const MaxOptions = 3

// maxLabel bounds a rendered label. A menu entry can be a full sentence with a
// path in it ("Yes, and don't ask again for rg commands in /very/long/path"),
// and a notification button is a few words wide.
const maxLabel = 40

// scanLines bounds how far up the capture the search goes. A prompt sits at the
// bottom of the screen; reading further is only a chance to find a numbered
// list in scrollback that has nothing to do with it.
const scanLines = 24

// Option is one choice on the menu: the key that selects it and the text the
// user would read.
type Option struct {
	Key   string // what to send — "1", "2", …
	Label string // the menu entry's own words, trimmed and shortened
}

// optionLine matches a numbered menu entry, tolerating the selection marker
// agents draw on the current row (❯ › > * -) and the indentation that aligns
// the rest with it. The separator may be "." or ")".
//
// The label is required to be non-empty: "3." alone is a numbered line with no
// choice on it, which is what a wrapped paragraph looks like from here.
var optionLine = regexp.MustCompile(`^[\s❯›>*\-|▌│]*(\d{1,2})[.)]\s+(\S.*)$`)

// ansiSeq matches the escape sequences a capture keeps when it was taken with
// ansi styling. A label is display text, so they come out.
var ansiSeq = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

// Parse returns the menu at the bottom of capture, or nil when there is not one
// it can be sure of. See the package comment for what "sure" means.
func Parse(capture string) []Option {
	lines := strings.Split(strings.ReplaceAll(capture, "\r\n", "\n"), "\n")
	// Drop the trailing blank rows a captured viewport ends in, so "the last
	// line" is the last line with anything on it.
	end := len(lines)
	for end > 0 && strings.TrimSpace(clean(lines[end-1])) == "" {
		end--
	}
	if end == 0 {
		return nil
	}
	start := end - scanLines
	if start < 0 {
		start = 0
	}

	// Walk up from the bottom collecting the contiguous run. A non-matching
	// line ends it — including a blank one, since a blank row between the menu
	// and the cursor is still the menu's own end.
	var rev []Option
	for i := end - 1; i >= start; i-- {
		m := optionLine.FindStringSubmatch(clean(lines[i]))
		if m == nil {
			break
		}
		rev = append(rev, Option{Key: m[1], Label: shorten(m[2])})
	}
	if len(rev) < 2 {
		return nil
	}

	// rev is bottom-up; the menu reads top-down.
	opts := make([]Option, 0, len(rev))
	for i := len(rev) - 1; i >= 0; i-- {
		opts = append(opts, rev[i])
	}
	for i, o := range opts {
		n, err := strconv.Atoi(o.Key)
		if err != nil || n != i+1 {
			return nil // not a whole menu — see the package comment
		}
	}
	if len(opts) > MaxOptions {
		opts = opts[:MaxOptions]
	}
	return opts
}

// clean strips ANSI styling and normalises whitespace so the patterns above see
// text rather than presentation.
func clean(line string) string {
	line = ansiSeq.ReplaceAllString(line, "")
	return strings.TrimRight(strings.ReplaceAll(line, "\t", " "), " ")
}

// shorten renders a menu entry as a button label: one line, no trailing
// parenthetical hint, and short enough to read on a lock screen.
func shorten(s string) string {
	s = strings.TrimSpace(s)
	// Agents append the raw key in parentheses ("No, and tell Claude … (esc)").
	// The button IS the key, so the hint is noise that costs a third of the
	// label's width.
	if i := strings.LastIndex(s, " ("); i > 0 && strings.HasSuffix(s, ")") {
		s = strings.TrimSpace(s[:i])
	}
	if len(s) <= maxLabel {
		return s
	}
	// Cut on a rune boundary and, when there is one nearby, on a word boundary:
	// "Yes, and don't ask again for rg com…" reads better than a mid-word cut.
	cut := maxLabel
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	if sp := strings.LastIndexByte(s[:cut], ' '); sp > maxLabel/2 {
		cut = sp
	}
	return strings.TrimSpace(s[:cut]) + "…"
}
