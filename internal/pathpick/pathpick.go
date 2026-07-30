// Package pathpick supplies the raw material for a directory picker: the
// subdirectories of one directory, plus the frecency-ranked directories the
// user actually works in.
//
// The recents come from cdx (github.com/rohanthewiz/cdx), the TUI cd-assistant
// whose chpwd hook already records every directory change the user makes in a
// shell. Reading its state file is deliberately the whole integration: no
// subprocess, no shared library, and nothing to install — a machine without cdx
// simply has no recents, and the caller falls back to what it can see itself
// (live pane cwds). We only ever read that file; cdx owns writing it.
//
// Ranking of the *typed query* is not here on purpose. The picker filters as
// the user types, and a round trip per keystroke is a poor deal when the
// browser may be a WAN away from the server (see the web-client topology): the
// front-end fuzzy-matches a directory's contents locally and comes back only
// when the directory it is completing inside changes.
package pathpick

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// MaxDirs caps one directory listing. A node_modules-sized directory would
// otherwise push tens of thousands of names at a picker that shows sixty rows;
// the cap is far above any hand-organized project tree, and Subdirs reports
// when it bites so the UI can say the list is partial.
const MaxDirs = 2000

// Expand resolves a path a user is part-way through typing, leniently: "$VAR"
// and a leading "~" expand, a relative path resolves against base, and the
// result is cleaned. Unlike startdir.Resolve nothing is stat'ed and nothing is
// an error — "/usr/lo" is a perfectly good thing to have typed, and the picker
// answers it with a listing of "/usr", not a complaint.
//
// An empty path is base itself: the picker's "here".
func Expand(path, base string) string {
	p := os.ExpandEnv(strings.TrimSpace(path))
	if p == "" {
		return filepath.Clean(base)
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p[1:], "/"))
		}
	}
	if !filepath.IsAbs(p) {
		if base == "" {
			return filepath.Clean(p) // no anchor to resolve against; hand back what we can
		}
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p)
}

// Subdirs lists the names of dir's subdirectories, sorted, symlinks to
// directories included (a projects dir full of symlinked checkouts is still a
// projects dir). Hidden names are kept: which of them to show depends on what
// the user has typed, and that decision belongs to the filtering front-end.
//
// truncated reports that dir holds more than MaxDirs subdirectories and the
// list is the first MaxDirs of them. A dir that cannot be read (missing,
// unreadable, not a directory) yields nil, false and an error — half-typed
// paths are the common case, so callers report that as "nothing here" rather
// than as a failed command.
func Subdirs(dir string) (names []string, truncated bool, err error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, false, err
	}
	for _, e := range ents {
		if !e.IsDir() {
			if e.Type()&os.ModeSymlink == 0 || !IsDir(filepath.Join(dir, e.Name())) {
				continue
			}
		}
		if len(names) == MaxDirs {
			truncated = true
			break
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, truncated, nil
}

// IsDir reports whether path is an existing directory (symlinks followed).
func IsDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// Recents returns up to limit absolute directories from cdx's memory,
// best-first by frecency, skipping any that no longer exist. No cdx state (not
// installed, never used, unreadable, corrupt) is not an error: the answer is
// simply nil, and the caller's own candidates carry the picker.
func Recents(limit int) []string {
	path, err := StatePath()
	if err != nil {
		return nil
	}
	return recents(path, time.Now().Unix(), limit)
}

// StatePath is cdx's state file — the user config dir cdx itself writes to
// (~/Library/Application Support/cdx/state.json on macOS,
// ~/.config/cdx/state.json on Linux).
func StatePath() (string, error) {
	cfg, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine user config dir: %w", err)
	}
	return filepath.Join(cfg, "cdx", "state.json"), nil
}

// entry is one remembered directory in cdx's state file. Only the fields
// ranking needs are decoded; cdx is free to add more.
type entry struct {
	Path  string `json:"path"`
	Count int    `json:"count"`
	Last  int64  `json:"last"` // unix seconds of the last visit
}

type state struct {
	Entries []entry `json:"entries"`
}

// recents is Recents against an explicit state file and clock, for tests.
func recents(statePath string, now int64, limit int) []string {
	data, err := os.ReadFile(statePath)
	if err != nil {
		return nil
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		return nil // a corrupt file means no habits to offer, not a failed picker
	}
	sort.SliceStable(st.Entries, func(i, j int) bool {
		return frecency(st.Entries[i], now) > frecency(st.Entries[j], now)
	})
	out := make([]string, 0, min(limit, len(st.Entries)))
	for _, e := range st.Entries {
		if len(out) == limit {
			break
		}
		if e.Path == "" || !filepath.IsAbs(e.Path) || !IsDir(e.Path) {
			continue // stale: the directory has been moved or deleted since
		}
		out = append(out, e.Path)
	}
	return out
}

// frecency scores a remembered directory by visit count weighted by recency —
// cdx's ranking (zoxide's, in turn), reproduced so the picker offers the same
// order the user sees in cdx itself.
func frecency(e entry, now int64) float64 {
	age := now - e.Last
	w := 0.25
	switch {
	case age < 3600: // within the hour
		w = 4
	case age < 86400: // within the day
		w = 2
	case age < 7*86400: // within the week
		w = 0.5
	}
	return float64(e.Count) * w
}
