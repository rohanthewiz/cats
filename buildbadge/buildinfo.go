// Package buildbadge reports the git identity of the running binary — the short
// hash of the commit it was built from, that commit's subject line, when that
// commit was made, and whether the working tree was modified at build time —
// and ships the small web badge that shows it: the hash as a quiet label, and
// on hover a card with the hash, the subject, and the commit time in the
// viewer's zone with its age ("2026-10-15 09:41 · 3h ago").
//
// It exists to answer "is this install stale?" at a glance, and is written to
// be lifted into any Go web project: stdlib only, no imports from the rest of
// cats, its JS and CSS embedded. See README.md for the three-step setup.
//
//	link time      stamp.sh / Makefile ──-ldflags -X──▶ hash, subjectB64, when
//	run time       Get() ──▶ Info{Hash, Subject, When, Dirty}
//	page render    DataScript / Head ──▶ <script>window.__buildInfo={…}</script>
//	browser        buildbadge.js ──▶ BuildBadge.mount(el) ──▶ badge + hover card
//
// The stamp is three -X values set at link time. The subject rides
// base64-encoded on purpose: it is free to contain spaces, quotes and '$', none
// of which survive a make recipe's -ldflags string intact, while base64's
// alphabet is inert in every layer in between.
//
// Nothing here is required. An unstamped build (plain `go build ./...`, `go run`)
// falls back to the VCS revision and commit time the toolchain records in the
// binary's build info, so the hash and time are still right — only the subject
// goes missing, and callers treat an empty field as "unknown" rather than
// showing a placeholder.
package buildbadge

import (
	"encoding/base64"
	"runtime/debug"
	"strings"
	"sync"
	"time"
)

// Stamped via -ldflags -X; unexported so Get is the only reader and the decode
// happens exactly once.
//
// when is git's strict ISO 8601 committer date (%cI), e.g.
// "2026-10-15T09:41:07-05:00". It needs no encoding: the alphabet is digits,
// '-', ':', '+' and 'T', all inert in -ldflags.
var (
	hash       string
	subjectB64 string
	when       string
)

// Info is a binary's build identity. Empty fields mean "unknown".
type Info struct {
	Hash    string `json:"hash"`    // short commit hash, e.g. "e251787"
	Subject string `json:"subject"` // that commit's first line
	// When is the commit's committer time as RFC 3339 in UTC. It travels as a
	// string rather than pre-formatted text so the page can render it in the
	// viewer's own zone and compute its age against the viewer's clock.
	When  string `json:"when"`
	Dirty bool   `json:"dirty"` // built from a modified working tree
}

// String is the one-line form for a CLI's `version` output or a startup log:
// "e251787* · fix the thing · 2026-10-15T14:41:07Z". The '*' marks a dirty
// tree, the same mark the badge uses. Unknown fields drop out rather than
// leaving empty separators; a binary that knows nothing says "unknown".
func (i Info) String() string {
	var parts []string
	if i.Hash != "" {
		h := i.Hash
		if i.Dirty {
			h += "*"
		}
		parts = append(parts, h)
	}
	for _, s := range []string{i.Subject, i.When} {
		if s != "" {
			parts = append(parts, s)
		}
	}
	if len(parts) == 0 {
		return "unknown"
	}
	return strings.Join(parts, " · ")
}

var (
	once     sync.Once
	resolved Info
)

// Get returns the build identity of this binary, resolved on first call.
func Get() Info {
	once.Do(resolve)
	return resolved
}

func resolve() {
	resolved.Hash = hash
	if b, err := base64.StdEncoding.DecodeString(subjectB64); err == nil {
		// Keep it to one line: the Makefile asks git for %s, but a hand-set
		// value shouldn't be able to smuggle a second row into the display.
		resolved.Subject, _, _ = strings.Cut(strings.TrimSpace(string(b)), "\n")
	}
	resolved.When = normWhen(when)

	// The toolchain's own VCS stamping covers the unstamped case, and is the
	// only source for the dirty flag either way.
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if resolved.Hash == "" && len(s.Value) >= 7 {
				resolved.Hash = s.Value[:7]
			}
		case "vcs.time":
			// The toolchain records the same committer time (already UTC
			// RFC 3339); it only fills in when the stamp didn't.
			if resolved.When == "" {
				resolved.When = normWhen(s.Value)
			}
		case "vcs.modified":
			resolved.Dirty = s.Value == "true"
		}
	}
}

// normWhen validates a commit time and re-renders it as RFC 3339 UTC, so the
// stamp (committer's local offset) and the toolchain value (already UTC) reach
// the page in one shape. Anything unparseable becomes "" — unknown — rather
// than a string the page would render as "Invalid Date".
func normWhen(s string) string {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
