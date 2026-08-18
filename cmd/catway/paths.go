//go:build ghostty

package main

import (
	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/orchestration"
	"github.com/rohanthewiz/cats/internal/pathpick"
)

// path.list (app.Backend seam): the directory listing behind the start-path
// picker in the new-workspace dialog.
//
// There are two ways to answer it and one rule that picks between them: the
// listing must be produced by the machine that owns the paths. For a pane on
// this catway's own box that is this process, off the loop goroutine so a cold
// network mount cannot stall the orchestrator. For a pane on another machine it
// is that machine's cathost, over the seam, because "~" is the remote user's
// home, "." is a directory only its kernel can resolve, and whether something
// is a directory at all is not a question this side can answer about a disk it
// cannot see.
//
// A host too old to list still gets the old answer — a listing error naming the
// host — rather than a wrong one. The picker shows it under the field and keeps
// taking keystrokes, exactly as it does for a path that is half typed.

// StartPathList lists one directory's subdirectories and, when asked, the
// directories the user works in most: cdx's frecency memory first (their real
// habits, learned from every shell cd), then this session's own live pane cwds.
// The session half matters because it is the answer that always exists — a
// machine without cdx still gets a useful list, seeded with the projects that
// are open right now.
func (o *orch) StartPathList(r app.Responder, p app.PathListParams) {
	pane := o.anchorPane(p.Pane)
	// Which machine is being asked. The Host param wins because the picker may
	// be completing a path for a host this session has no pane on at all — the
	// new-workspace dialog's host field is chosen before anything exists there —
	// and in that case the anchor pane says nothing about the answer.
	hostID := p.Host
	if hostID == "" {
		hostID = o.paneHostID(pane)
	} else if o.hosts[hostID] == nil {
		r.OK(app.PathListResult{Dir: p.Dir, Error: unknownHostListErr(hostID)})
		return
	}

	// The anchor's cwd is only an anchor if it is on the same filesystem the
	// listing will be taken from. A path from a pane on another box would be
	// resolved against a directory that does not exist there, which is worse
	// than having no anchor: with none, the answering machine falls back to its
	// own home.
	var cwd string
	if o.paneHostID(pane) == hostID {
		cwd = o.anchorPaneCwd(p.Pane)
	}
	var live []string
	if p.Recents {
		live = o.liveCwdsOn(hostID) // loop-owned; stat'ed by whoever owns the disk
	}

	if hostID == localHostID {
		go func() {
			res := fromListing(pathpick.List(p.Dir, cwd, p.Recents, live), cwd)
			o.post(func() { r.OK(res) })
		}()
		return
	}

	d := o.hosts[hostID]
	if !d.supports(orchestration.FeatureListDir) {
		r.OK(app.PathListResult{Dir: p.Dir, Cwd: cwd, Error: cannotListErr(d)})
		return
	}
	// Keyed on the anchor pane like every other daemon round trip, and resolved
	// by the same FIFO: a pane's picker requests go out one at a time, and the
	// daemon answers in order over one connection. It also means a host that
	// drops mid-listing fails this request through flushPendingFor rather than
	// leaving the picker waiting for a reply that cannot come.
	o.registerPending(pathListResponder{r: r, cwd: cwd}, reqKey{pane, reqListDir})
	d.send(orchestration.NewRequestListDir(pane, p.Dir, cwd, p.Recents, live))
}

// pathListResponder turns the daemon's Listing into the command's result on the
// way back. It exists because the pending queue carries one Responder and knows
// nothing about the shape of what it is resolving, while the anchor cwd is
// catway's own knowledge and never crosses the wire in the reply.
type pathListResponder struct {
	r   app.Responder
	cwd string
}

// WantsReply follows the wrapped responder: a caller that cannot receive a
// result should not cause a round trip to another machine either.
func (p pathListResponder) WantsReply() bool { return p.r.WantsReply() }

func (p pathListResponder) OK(data any) {
	l, ok := data.(pathpick.Listing)
	if !ok {
		p.r.Fail("bad directory listing reply")
		return
	}
	p.r.OK(fromListing(l, p.cwd))
}

func (p pathListResponder) Fail(msg string) {
	// A failed round trip is still a picker answer rather than a failed command:
	// the field keeps its text and shows why nothing is listed, which is what it
	// already does for a path half-way through being typed.
	p.r.OK(app.PathListResult{Error: msg})
}

// fromListing maps one machine's answer onto the command's result. Cwd is the
// anchor the request was made against — the client shows it, and it is the one
// field the answering machine was told rather than asked.
func fromListing(l pathpick.Listing, cwd string) app.PathListResult {
	return app.PathListResult{
		Dir:       l.Dir,
		Cwd:       cwd,
		Home:      l.Home,
		Exists:    l.Exists,
		Error:     l.Error,
		Dirs:      l.Dirs,
		Truncated: l.Truncated,
		Recents:   l.Recents,
	}
}

// cannotListErr explains a host that cannot answer. Named rather than inlined
// because it is the one sentence a user sees when a picker goes dark, and the
// reason matters: a cathost that has not been upgraded is a different fix from
// one that is unreachable.
func cannotListErr(d *daemon) string {
	if !d.connected() {
		return "host " + d.label + " is not connected"
	}
	return "host " + d.label + " cannot list directories (its cathost predates the list_dir capability)"
}

func unknownHostListErr(id string) string {
	return "unknown host " + id + " (see host.list)"
}

// liveCwdsOn collects the directories this session is actually sitting in on one
// host — every live pane's cwd plus each workspace's identity cwd — in workspace
// order, deduplicated. Loop-goroutine only.
//
// Scoped by host, because these paths are handed to whichever machine is
// answering and are stat'ed there. An unfiltered list would offer a picker on
// devbox the local machine's project directories, and any of them that happened
// to exist on devbox too would be offered as if they were the ones on screen.
func (o *orch) liveCwdsOn(hostID string) []string {
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
		if o.workspaceHostOwns(ws, hostID) {
			add(ws.IdentityCwd)
		}
		for _, tab := range ws.Tabs {
			for _, id := range tab.Layout.PaneIDs() {
				pid := uint32(id)
				if o.paneHostID(pid) != hostID {
					continue
				}
				if rt := o.panes[pid]; rt != nil {
					add(rt.cwd)
				}
			}
		}
	}
	return out
}
