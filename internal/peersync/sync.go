package peersync

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Local is what this package needs from the running catway. The workspace
// model belongs to the orchestrator goroutine, so the two workspace methods
// are the seam where cmd/catway hops onto that goroutine and back; everything
// else a sync touches is a file this package can open itself.
type Local interface {
	// Instance describes this catway.
	Instance() Instance
	// Workspaces lists the workspaces whose folder is on this machine (a
	// workspace pinned to a remote cathost names a folder on that machine and
	// is neither offered nor matched).
	Workspaces() ([]WorkspaceEntry, error)
	// CreateWorkspaces adds one workspace per entry, asleep — in the list
	// with no terminal, the way a restored-but-untouched workspace is — and
	// leaves the active workspace where it was. One error slot per entry; nil
	// where it landed.
	CreateWorkspaces(entries []WorkspaceEntry) []error
}

// Collect gathers this side's bundle over the wanted categories. Only what
// was asked for is read, so a todos-only sync never touches the plugins root
// and a plugins-only sync never opens a backlog.
func Collect(local Local, want Want) (Bundle, error) {
	if !want.Any() {
		return Bundle{}, errors.New("nothing selected to sync")
	}
	b := Bundle{Schema: Schema, Instance: local.Instance(), Want: want}
	var wss []WorkspaceEntry
	if want.Workspaces || want.Todos {
		var err error
		if wss, err = local.Workspaces(); err != nil {
			return b, fmt.Errorf("workspaces: %w", err)
		}
	}
	if want.Workspaces {
		b.Workspaces = wss
	}
	if want.Todos {
		dir, err := GlobalTodoDir()
		if err != nil {
			return b, fmt.Errorf("global backlog: %w", err)
		}
		if g, ok, err := collectBacklog(dir); err != nil {
			return b, fmt.Errorf("global backlog: %w", err)
		} else if ok {
			b.Global = &g
		}
		seen := map[string]bool{}
		for _, ws := range wss {
			if ws.Cwd == "" || seen[ws.Cwd] {
				continue
			}
			seen[ws.Cwd] = true
			bl, ok, err := collectBacklog(ProjectTodoDir(ws.Cwd))
			if err != nil {
				// One unreadable backlog must not stop the rest from
				// travelling; it is reported by the OTHER side as absent,
				// and by this side's next sync-pull as a failed merge.
				continue
			}
			if !ok {
				continue
			}
			bl.Cwd = ws.Cwd
			b.Backlogs = append(b.Backlogs, bl)
		}
	}
	if want.Plugins {
		ps, err := collectPlugins()
		if err != nil {
			return b, fmt.Errorf("plugins: %w", err)
		}
		b.Plugins = ps
	}
	return b, nil
}

// Apply folds a peer's bundle into this side and reports every item. The
// categories applied are the intersection of what the bundle carries and
// what the caller wants, so a peer that answered with more than was asked
// for cannot install anything the user did not select.
func Apply(local Local, b Bundle, want Want) ApplyReport {
	rep := ApplyReport{Instance: local.Instance(), Items: []Item{}}
	if b.Schema != Schema {
		rep.Error = fmt.Sprintf("bundle schema %d, want %d", b.Schema, Schema)
		return rep
	}
	home := local.Instance().Home
	if want.Workspaces && b.Want.Workspaces {
		applyWorkspaces(local, b, home, &rep)
	}
	if want.Todos && b.Want.Todos {
		applyTodos(b, home, &rep)
	}
	if want.Plugins && b.Want.Plugins {
		applyPlugins(b.Plugins, &rep)
	}
	return rep
}

// applyWorkspaces creates a workspace for every folder in the bundle that
// exists here and is not already a workspace. The match is on the folder
// after home-directory translation, cleaned; a symlinked folder and its
// target count as different workspaces, as they do everywhere else in cats.
func applyWorkspaces(local Local, b Bundle, home string, rep *ApplyReport) {
	have, err := local.Workspaces()
	if err != nil {
		rep.Error = "workspaces: " + err.Error()
		return
	}
	existing := make(map[string]bool, len(have))
	for _, ws := range have {
		existing[filepath.Clean(ws.Cwd)] = true
	}
	var create []WorkspaceEntry
	var names []string
	for _, in := range b.Workspaces {
		if in.Cwd == "" {
			continue
		}
		cwd := filepath.Clean(TranslatePath(in.Cwd, b.Instance.Home, home))
		label := in.Name
		if label == "" {
			label = filepath.Base(cwd)
		}
		name := label + " (" + cwd + ")"
		if existing[cwd] {
			rep.add(KindWorkspace, name, StatusUnchanged, "already a workspace here")
			continue
		}
		if !dirExists(cwd) {
			rep.add(KindWorkspace, name, StatusSkipped, "no such folder on this machine")
			continue
		}
		existing[cwd] = true // two peer workspaces on one folder create one here
		create = append(create, WorkspaceEntry{Cwd: cwd, Name: in.Name})
		names = append(names, name)
	}
	if len(create) == 0 {
		return
	}
	errs := local.CreateWorkspaces(create)
	for i := range create {
		if i < len(errs) && errs[i] != nil {
			rep.add(KindWorkspace, names[i], StatusFailed, errs[i].Error())
			continue
		}
		rep.add(KindWorkspace, names[i], StatusSynced, "added, asleep")
	}
}

// applyTodos merges the global backlog and every project backlog whose
// folder exists here. A project backlog is created when its folder exists
// but has never had one — the folder's existence is the whole equivalence
// rule, and a backlog the peer keeps for a project this machine also holds is
// a backlog this machine wants; that is exactly what `cats-todo init` there
// would do, minus the question.
func applyTodos(b Bundle, home string, rep *ApplyReport) {
	if b.Global != nil {
		dir, err := GlobalTodoDir()
		if err != nil {
			rep.add(KindTodos, "global", StatusFailed, err.Error())
		} else {
			mergeInto(dir, "global", *b.Global, rep)
		}
	}
	// Deterministic order, so two reports of the same sync read the same.
	backlogs := append([]Backlog(nil), b.Backlogs...)
	sort.SliceStable(backlogs, func(i, j int) bool { return backlogs[i].Cwd < backlogs[j].Cwd })
	for _, bl := range backlogs {
		if bl.Cwd == "" {
			continue
		}
		project := filepath.Clean(TranslatePath(bl.Cwd, b.Instance.Home, home))
		name := filepath.Base(project) + " (" + project + ")"
		if !dirExists(project) {
			rep.add(KindTodos, name, StatusSkipped, "no such folder on this machine")
			continue
		}
		mergeInto(ProjectTodoDir(project), name, bl, rep)
	}
}

// mergeInto merges one incoming backlog into the backlog directory dir and
// records the outcome as one report item, with the merge arithmetic in its
// detail.
func mergeInto(dir, name string, in Backlog, rep *ApplyReport) {
	file := filepath.Join(dir, todoFileName)
	local, err := readTodos(file)
	if err != nil {
		rep.add(KindTodos, name, StatusFailed, "could not read the backlog here: "+err.Error())
		return
	}
	merged, res := mergeTodos(local, in.Todos, in.Files, dir)
	if res.changed() {
		if err := writeTodos(file, merged); err != nil {
			rep.add(KindTodos, name, StatusFailed, "could not write the backlog: "+err.Error())
			return
		}
	} else if _, err := os.Stat(file); errors.Is(err, os.ErrNotExist) && len(in.Todos) == 0 {
		// Nothing to merge and nothing here: do not create an empty backlog
		// for a project the peer's is empty on as well.
		rep.add(KindTodos, name, StatusUnchanged, "both backlogs are empty")
		return
	}
	detail := fmt.Sprintf("%d added, %d completed, %d unchanged", res.Added, res.Completed, res.Unchanged)
	if res.Dupes > 0 {
		detail += fmt.Sprintf(", %d duplicate(s) not added", res.Dupes)
	}
	if res.NoFiles > 0 {
		detail += fmt.Sprintf(", %d without their attachment(s)", res.NoFiles)
	}
	st := StatusUnchanged
	if res.changed() {
		st = StatusSynced
	}
	rep.add(KindTodos, name, st, detail)
	// Conflicts are their own lines, one per row: they are the part of a
	// merge that needs a human, and a count would send the reader to diff
	// two files to find out which rows.
	for _, note := range res.Notes {
		rep.add(KindTodos, name, StatusSkipped, note)
	}
}

// Sync runs one sync against a peer, in the given direction, and tells the
// whole story. It never returns an error: a failure to reach or read the
// peer is the report's Error, because the caller in every case is about to
// show the user a report and "could not connect" is the first line of one.
func Sync(ctx context.Context, local Local, c *Client, peerID string, want Want, dir Direction) SyncReport {
	rep := SyncReport{Peer: peerID, Direction: dir, Want: want}
	if !want.Any() {
		rep.Error = "nothing selected to sync (choose workspaces, todos and/or plugins)"
		return rep
	}
	theirs, err := c.Fetch(ctx, want)
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	rep.Remote = theirs.Instance
	if dir == DirectionBoth || dir == DirectionPull {
		pulled := Apply(local, theirs, want)
		rep.Pulled = &pulled
	}
	if dir == DirectionBoth || dir == DirectionPush {
		mine, err := Collect(local, want)
		if err != nil {
			rep.Error = "collecting this side: " + err.Error()
			return rep
		}
		pushed, err := c.Apply(ctx, mine)
		if err != nil {
			rep.Error = "push: " + err.Error()
			return rep
		}
		rep.Pushed = &pushed
	}
	return rep
}
