package main

// `catctl flags` — the cross-workspace listing the marks exist for.
//
// A flag's whole value is that you can find it again from somewhere else, and
// until this verb the only way to collect them was to read four sidebar lists or
// to grep two JSON payloads. flag.list answers it in one query (internal/app);
// this file is only the rendering.
//
// The output is a table rather than the pretty JSON every other query prints,
// because this is the one listing whose reason for existing is a glance:
//
//	w1     ⚑ follow-up   cats                  2h ago   flaky tests in here
//	3      ★ important   w1:p3 claude · idle   5m ago   still here after a restart?
//
// The first column is the argument the mutating verbs take, which is why it is
// not the same shape on both kinds of row: a workspace is addressed by its
// public id (`flag-ws w1`) and a pane by the internal number (`flag 3`, see
// parsePane). Reading it is `unflag <that>`, with the two shapes telling you
// which verb — the same distinction the verb pair already makes.
//
// `--json` still yields the raw flag.list payload, which is the scripting path
// and the reason this renderer is free to drop fields.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/ctlproto"
	"github.com/rohanthewiz/cats/internal/flags"
)

// flagRow is one rendered line, split into the columns that get aligned. note is
// last and unpadded, so a long one runs off the end rather than widening a
// column nothing else is in.
type flagRow struct {
	target string // the id you pass to flag/unflag: "w1" or "3"
	mark   string // glyph, plus the named kind's label
	where  string // workspace name, or "w1:p3" and what is running in it
	age    string // "2h ago"
	note   string
}

// printFlagList renders a flag.list payload. params is the request's own params
// so an empty listing can say WHICH listing was empty: "nothing flagged" is a
// misleading answer to `catctl flags done`, where the truthful one is that
// nothing is flagged *done*.
//
// An undecodable payload falls back to the generic renderer rather than printing
// nothing, the printBlockOutput rule: better a JSON blob the caller can read
// than a silence that looks like a session with no flags in it.
func printFlagList(resp ctlproto.Response, params json.RawMessage) {
	var data app.FlagListResult
	if err := json.Unmarshal(resp.Data, &data); err != nil {
		printResult(resp)
		return
	}

	now := time.Now()
	rows := make([]flagRow, 0, len(data.Workspaces)+len(data.Panes))
	// Workspaces first, then panes — the sidebar's own top-to-bottom order, so
	// the listing and the UI agree about where a mark sits.
	for _, w := range data.Workspaces {
		rows = append(rows, flagRow{
			target: w.ID,
			mark:   flagMark(w.Flag),
			where:  w.Name,
			age:    humanAge(w.FlagAtMs, now),
			note:   w.FlagNote,
		})
	}
	for _, p := range data.Panes {
		rows = append(rows, flagRow{
			target: fmt.Sprintf("%d", p.Pane),
			mark:   flagMark(p.Flag),
			where:  paneWhere(p),
			age:    humanAge(p.FlagAtMs, now),
			note:   p.FlagNote,
		})
	}

	if len(rows) == 0 {
		fmt.Fprintf(os.Stderr, "catctl: %s\n", nothingFlagged(params))
		return
	}

	// Two passes: widths, then lines. Every column but the last is padded to the
	// widest cell in it.
	w := make([]int, 4)
	for _, r := range rows {
		for i, c := range []string{r.target, r.mark, r.where, r.age} {
			if n := dispWidth(c); n > w[i] {
				w[i] = n
			}
		}
	}
	for _, r := range rows {
		line := pad(r.target, w[0]) + "  " + pad(r.mark, w[1]) + "  " +
			pad(r.where, w[2]) + "  " + pad(r.age, w[3]) + "  " + r.note
		// A row with no note (or no age) would otherwise end in the padding of
		// the columns that carried nothing.
		fmt.Println(strings.TrimRight(line, " "))
	}
}

// flagMark renders the kind: the glyph plus its label for a named kind, and the
// bare glyph for a custom one — because Describe hands a custom glyph back
// unchanged, and "🍕 🍕" teaches nothing.
func flagMark(kind string) string {
	k := flags.Kind(kind)
	if d, ok := flags.Lookup(k); ok {
		return d.Glyph + " " + d.Label
	}
	return flags.Glyph(k)
}

// paneWhere describes a flagged pane: its public handle, then the most specific
// thing known about what is in it. The priority is deliberate — a custom name is
// what the user chose to call it, an agent is what the flag is usually about,
// and a live title is the fallback that is at least true.
func paneWhere(p app.PaneInfo) string {
	parts := []string{p.Handle}
	switch {
	case p.Name != "":
		parts = append(parts, p.Name)
	case p.Agent != "":
		agent := p.Agent
		if p.AgentState != "" {
			agent += " · " + p.AgentState
		}
		parts = append(parts, agent)
	case p.Title != "":
		parts = append(parts, p.Title)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// nothingFlagged phrases the empty listing, naming the filter when there was
// one. The kind is read back out of the request rather than threaded through
// main, so this renderer stays a function of the round trip it is printing.
func nothingFlagged(params json.RawMessage) string {
	var p app.FlagListParams
	if err := json.Unmarshal(params, &p); err == nil && p.Kind != "" {
		return fmt.Sprintf("nothing flagged %q", p.Kind)
	}
	return "nothing flagged"
}

// humanAge renders "when it was last set" as an age. The server sends absolute
// Unix milliseconds precisely so each client can do this against its own clock
// (see FlagInfo.FlagAtMs); catctl and catway share a machine, so there is no
// skew to reason about.
//
// A flag written by a clock that has since moved backwards would give a negative
// age, which is reported as "now" rather than a negative number: the timestamp is
// evidence about the flag, not about the clock, and "-3h ago" tells the reader
// nothing they can act on.
func humanAge(atMs int64, now time.Time) string {
	if atMs <= 0 {
		return "" // a flag from before AtMs existed, or a hand-written snapshot
	}
	d := now.Sub(time.UnixMilli(atMs))
	switch {
	case d < time.Minute:
		if d < 0 {
			return "now"
		}
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// pad right-pads s to w display columns.
func pad(s string, w int) string {
	if n := w - dispWidth(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// dispWidth ESTIMATES how many terminal columns a string occupies. It is an
// estimate on purpose: a full East-Asian-width table is a dependency, and the
// only thing riding on it here is whether one row of a listing sits a column off.
//
// Three cases, which between them cover everything a flag can hold:
//
//   - zero — the pieces that modify the glyph before them rather than taking a
//     cell of their own: a zero-width joiner, an emoji variation selector, a
//     combining mark. Without these a ZWJ emoji sequence (several runes, one
//     glyph) would be measured as several columns.
//   - two — emoji and the CJK/fullwidth blocks, the characters a terminal draws
//     double-wide.
//   - one — everything else, which is every named kind's glyph (⚑ ? ★ ⚠ ✓ ✎;
//     all "ambiguous width", drawn single-wide in the terminals cats runs in)
//     and all of the ASCII the other columns are made of.
func dispWidth(s string) int {
	n := 0
	for _, r := range s {
		switch {
		case r == 0x200D || r == 0xFE0F || unicode.Is(unicode.Mn, r):
			// joiner, variation selector, combining mark: no cell of its own
		case r >= 0x1F300, // emoji and the astral planes above them
			r >= 0x1100 && r <= 0x115F, // Hangul Jamo
			r >= 0x2E80 && r <= 0xA4CF, // CJK radicals through Yi
			r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
			r >= 0xF900 && r <= 0xFAFF, // CJK compatibility ideographs
			r >= 0xFE30 && r <= 0xFE6F, // CJK compatibility forms
			r >= 0xFF00 && r <= 0xFF60, // fullwidth forms
			r >= 0xFFE0 && r <= 0xFFE6: // fullwidth signs
			n += 2
		default:
			n++
		}
	}
	return n
}

// flagKindsHelp renders the named vocabulary for `catctl help flags`, aligned
// with the same estimator the listing uses. The kinds are generated from
// flags.Defs() rather than written out here, for the reason the completion
// candidates are: the table is compiled in, and a second copy of it in a help
// string is a copy that goes stale silently.
func flagKindsHelp() string {
	defs := flags.Defs()
	w := 0
	for _, d := range defs {
		if n := len(string(d.Kind)); n > w {
			w = n
		}
	}
	var kinds strings.Builder
	for _, d := range defs {
		kinds.WriteString("    " + pad(d.Glyph, 2) + " " + pad(string(d.Kind), w) + "  " + d.Meaning + "\n")
	}
	return `Lists every flag in the session — workspaces first, then panes — or, with a
kind, only that kind's. The first column is the argument the mutating verbs
take: ` + "`w1`" + ` for ` + "`flag-ws`/`unflag-ws`" + `, a number for ` + "`flag`/`unflag`" + `.

Kinds:

` + kinds.String() + `
A kind may also be a single glyph you invented, and filtering accepts one —
` + "`catctl flags 🍕`" + ` reads exactly like ` + "`catctl flags followup`" + `. An unknown
kind is refused rather than answered with an empty list, which would look the
same as a session with nothing flagged.

--json prints the raw flag.list payload instead: the flagged subset of
` + "`workspaces`" + ` and ` + "`panes`" + `, carrying those rows' own fields.`
}
