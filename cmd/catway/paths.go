//go:build ghostty

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/pathpick"
	"github.com/rohanthewiz/cats/internal/startdir"
)

// path.list (app.Backend seam): the directory listing behind the start-path
// picker in the new-workspace dialog. Same shape as the worktree and plugin
// commands — resolve loop-owned state (the anchor cwd, the session's live pane
// cwds) on the loop goroutine, do the filesystem work on its own goroutine, and
// post the reply back — so a listing of a cold network mount cannot stall the
// orchestrator.

// maxPickerRecents caps the frecency list sent to a picker. It sees sixty rows
// at a time and fuzzy-filters the rest locally, so a few hundred directories is
// already more history than anyone scrolls through.
const maxPickerRecents = 200

// StartPathList lists one directory's subdirectories and, when asked, the
// directories the user works in most: cdx's frecency memory first (their real
// habits, learned from every shell cd), then this session's own live pane cwds.
// The session half matters because it is the answer that always exists — a
// machine without cdx still gets a useful list, seeded with the projects that
// are open right now.
func (o *orch) StartPathList(r app.Responder, p app.PathListParams) {
	cwd := o.anchorPaneCwd(p.Pane)
	var live []string
	if p.Recents {
		live = o.liveCwds() // loop-owned; gathered here, stat'ed off-loop below
	}
	go func() {
		res := app.PathListResult{
			Dir:  pathpick.Expand(p.Dir, cwd),
			Cwd:  cwd,
			Home: startdir.Home(),
		}
		dirs, truncated, err := pathpick.Subdirs(res.Dir)
		if err != nil {
			// A path half-way through being typed is not a failed command: the
			// picker shows the reason and keeps taking keystrokes.
			res.Error = listErr(res.Dir, err)
		} else {
			res.Exists, res.Dirs, res.Truncated = true, dirs, truncated
		}
		if p.Recents {
			res.Recents = mergeDirs(pathpick.Recents(maxPickerRecents), live)
		}
		o.post(func() { r.OK(res) })
	}()
}

// listErr turns a ReadDir failure into something worth showing under a text
// field, keeping the raw error only for cases we have no better words for.
func listErr(dir string, err error) string {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "no such directory: " + dir
	case errors.Is(err, fs.ErrPermission):
		return "not readable: " + dir
	}
	if fi, serr := os.Stat(dir); serr == nil && !fi.IsDir() {
		return "not a directory: " + dir // a file whose name shares a prefix with one
	}
	return err.Error()
}

// liveCwds collects the directories this session is actually sitting in — every
// live pane's cwd plus each workspace's identity cwd — in workspace order,
// deduplicated. Loop-goroutine only.
func (o *orch) liveCwds() []string {
	var out []string
	seen := map[string]bool{}
	add := func(dir string) {
		if dir == "" || seen[dir] {
			return
		}
		seen[dir] = true
		out = append(out, dir)
	}
	for _, ws := range o.session.Workspaces() {
		add(ws.IdentityCwd)
		for _, tab := range ws.Tabs {
			for _, id := range tab.Layout.PaneIDs() {
				if rt := o.panes[uint32(id)]; rt != nil {
					add(rt.cwd)
				}
			}
		}
	}
	return out
}

// mergeDirs appends extra onto base, dropping duplicates and anything that is no
// longer a directory. Order is significant: base is cdx's frecency ranking, and
// the session's own directories fill in behind it rather than displacing it.
func mergeDirs(base, extra []string) []string {
	out := make([]string, 0, len(base)+len(extra))
	seen := make(map[string]bool, len(base)+len(extra))
	for _, list := range [][]string{base, extra} {
		for _, dir := range list {
			dir = filepath.Clean(dir)
			if !filepath.IsAbs(dir) || seen[dir] || !pathpick.IsDir(dir) {
				continue
			}
			seen[dir] = true
			out = append(out, dir)
		}
	}
	return out
}
