//go:build ghostty

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/buildinfo"
	"github.com/rohanthewiz/cats/internal/config"
	"github.com/rohanthewiz/cats/internal/peersync"
	"github.com/rohanthewiz/rweb"
)

// Peer sync: the catway half of internal/peersync.
//
// Two surfaces meet here. Outbound, the §7 peer.* commands (peers dialog,
// `catctl sync`) run a sync against a configured peer. Inbound, the
// /peer/v1/* HTTP routes answer another catway's sync with this one's bundle
// and apply what it pushes. Both ends are the same package doing the same
// work; what this file adds is the hop onto the orchestrator goroutine for
// the one store that lives there — the workspace model — and the config
// roster the peers come from.
//
//	peer.sync ──▶ StartPeerSync (loop) ──▶ goroutine: peersync.Sync
//	                                          │  fetch / apply / push over HTTP
//	                                          │  Local.Workspaces ─┐
//	                                          │  Local.Create… ────┤ onLoop
//	                                          └─▶ o.post(r.OK(report))
//
//	GET  /peer/v1/bundle ──▶ (rweb goroutine) peersync.Collect(local) ─┘
//	POST /peer/v1/apply  ──▶ (rweb goroutine) peersync.Apply(local)  ─┘
//
// # Trust
//
// The inbound routes sit behind the ordinary auth guard: the caller presents
// this catway's shared secret as a bearer token, exactly as a headless catctl
// would. There is no separate peer permission, deliberately — the argument
// config.Host.ControlRelay makes applies unchanged. A caller holding the
// secret already has /ws, and with it tab.create and pane.send_input on every
// pane; a plugin install through /peer/v1/apply grants nothing that could not
// be typed into a shell through that door. A second credential would only be
// a second thing to leak. Under `auth: none` the routes are as open as the
// rest of the server, which is the operator's stated choice.

// peerLocal is peersync.Local over the running orchestrator. Its workspace
// methods hop onto the loop goroutine (onLoop) because the session model is
// owned there; everything else peersync touches is a file.
type peerLocal struct{ o *orch }

func (l peerLocal) Instance() peersync.Instance {
	return peersync.LocalInstance(buildinfo.Get().Hash)
}

// Workspaces lists the workspaces whose folder is on this machine: those on
// the local host, which is the recorded host or, for a workspace that names
// none, the roster's default. A workspace on a remote cathost identifies with
// a folder over there and is neither offered to a peer nor matched against
// one — the peer's folder test would be run on the wrong machine.
func (l peerLocal) Workspaces() ([]peersync.WorkspaceEntry, error) {
	var out []peersync.WorkspaceEntry
	l.o.onLoop(func() {
		for _, ws := range l.o.session.Workspaces() {
			host := ws.HostID
			if host == "" {
				host = l.o.defaultHost
			}
			if host != localHostID || ws.IdentityCwd == "" {
				continue
			}
			out = append(out, peersync.WorkspaceEntry{Cwd: ws.IdentityCwd, Name: ws.CustomName})
		}
	})
	return out, nil
}

// CreateWorkspaces adds the entries asleep and leaves the viewport where it
// was. Creating a workspace makes it active and gives it a root pane, which
// on the next ApplyModel would become a shell — one per synced workspace,
// fifteen new terminals nobody asked for. So each is put to sleep at once
// (its root pane closed before it ever spawned, the placeholder kept), and
// the previously active workspace is re-focused before the single ApplyModel
// that publishes the lot. A sleeping workspace is exactly what the sidebar
// shows for a restored-but-untouched one: in the list, no terminal, woken by
// a click.
func (l peerLocal) CreateWorkspaces(entries []peersync.WorkspaceEntry) []error {
	errs := make([]error, len(entries))
	l.o.onLoop(func() {
		s := l.o.session
		prev := s.ActiveWorkspace().ID
		for i, e := range entries {
			id, err := s.CreateWorkspaceAtOn(e.Cwd, "")
			if err != nil {
				errs[i] = err
				continue
			}
			if e.Name != "" {
				_ = s.RenameWorkspace(id, e.Name) // only fails for a vanished id, which it is not
			}
			if err := s.SleepWorkspace(id); err != nil {
				// ErrLastAwake cannot happen (prev is awake); anything else is
				// worth the report line but not worth losing the workspace.
				log.Printf("catway: peer sync: sleep new workspace %s: %v", id, err)
			}
		}
		_ = s.FocusWorkspace(prev)
		l.o.ApplyModel()
	})
	return errs
}

// onLoop runs fn on the orchestrator goroutine and waits for it. It is for
// goroutines that are NOT the loop — a sync worker, an rweb handler; calling
// it from the loop would deadlock, the same rule the notification-action
// handler follows with its done channel.
func (o *orch) onLoop(fn func()) {
	done := make(chan struct{})
	o.post(func() {
		defer close(done)
		fn()
	})
	<-done
}

// --- §7 commands ----------------------------------------------------------------

// PeerList answers peer.list from the config roster. Loop goroutine.
func (o *orch) PeerList(r app.Responder) {
	r.OK(app.PeerListResult{Peers: o.peerInfos()})
}

func (o *orch) peerInfos() []app.PeerInfo {
	out := make([]app.PeerInfo, 0, len(o.cfg.Peers))
	for _, p := range o.cfg.Peers {
		out = append(out, app.PeerInfo{
			ID: p.ID, Label: p.DisplayLabel(), URL: p.URL, Fingerprint: p.Fingerprint,
			HasToken: p.Token != "" || p.TokenFile != "",
		})
	}
	return out
}

// findPeer resolves a roster entry by id. Loop goroutine.
func (o *orch) findPeer(id string) (config.Peer, bool) {
	for _, p := range o.cfg.Peers {
		if p.ID == id {
			return p, true
		}
	}
	return config.Peer{}, false
}

// peerToken is the credential to present to a peer: the literal from the
// config, or the current contents of token_file, read per sync so a rotated
// file is picked up without a reload.
func peerToken(p config.Peer) (string, error) {
	if p.TokenFile == "" {
		if p.Token == "" {
			return "", peersync.ErrNoToken
		}
		return p.Token, nil
	}
	path := p.TokenFile
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = home + path[1:]
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token_file: %w", err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("token_file %s is empty", p.TokenFile)
	}
	return tok, nil
}

// StartPeerSync runs peer.sync. The roster lookup and the client build happen
// here on the loop (they read o.cfg); the sync itself — network round trips,
// backlog merges, possibly plugin installs — runs on its own goroutine and
// resolves r back on the loop, like every other Start* command. A sync that
// could not begin (unknown peer, no credential) is still answered as a
// report with an Error, because the caller is about to show a report and
// that is the first line of one.
func (o *orch) StartPeerSync(r app.Responder, p app.PeerSyncParams) {
	want := peersync.Want{Workspaces: p.Workspaces, Todos: p.Todos, Plugins: p.Plugins}
	dir, err := peersync.ParseDirection(p.Direction)
	if err != nil {
		r.Fail("peer.sync: " + err.Error())
		return
	}
	peer, ok := o.findPeer(p.Peer)
	if !ok {
		r.Fail(fmt.Sprintf("peer.sync: no peer %q (peers: %s)", p.Peer, o.peerIDs()))
		return
	}
	fail := func(msg string) {
		rep := peersync.SyncReport{Peer: peer.ID, Direction: dir, Want: want, Error: msg}
		r.OK(toWireReport(rep))
	}
	tok, err := peerToken(peer)
	if err != nil {
		fail(err.Error())
		return
	}
	client, err := peersync.NewClient(peer.URL, tok, peer.Fingerprint)
	if err != nil {
		fail(err.Error())
		return
	}
	local := peerLocal{o}
	go func() {
		start := time.Now()
		rep := peersync.Sync(context.Background(), local, client, peer.ID, want, dir)
		log.Printf("catway: peer.sync %s (%s, %s) took %s%s", peer.ID, dir, want, time.Since(start).Round(time.Millisecond), syncLogSuffix(rep))
		o.post(func() { r.OK(toWireReport(rep)) })
	}()
}

func syncLogSuffix(rep peersync.SyncReport) string {
	if rep.Error != "" {
		return ": " + rep.Error
	}
	return ""
}

func (o *orch) peerIDs() string {
	ids := make([]string, 0, len(o.cfg.Peers))
	for _, p := range o.cfg.Peers {
		ids = append(ids, p.ID)
	}
	if len(ids) == 0 {
		return "none configured"
	}
	return strings.Join(ids, ", ")
}

// toWireReport flattens a SyncReport into the §7 result: both sides' items
// in one list tagged by side, the totals, and the rendered lines.
func toWireReport(rep peersync.SyncReport) app.PeerSyncResult {
	res := app.PeerSyncResult{
		Peer: rep.Peer, Direction: string(rep.Direction), Items: []app.PeerSyncItem{},
		Lines: rep.Render(), Error: rep.Error,
	}
	if rep.Remote.Hostname != "" {
		res.Remote = rep.Remote.Name()
	}
	add := func(side string, a *peersync.ApplyReport) {
		if a == nil {
			return
		}
		s, u, k, f := a.Counts()
		res.Synced += s
		res.Unchanged += u
		res.Skipped += k
		res.Failed += f
		for _, it := range a.Items {
			res.Items = append(res.Items, app.PeerSyncItem{Side: side, Kind: it.Kind, Name: it.Name, Status: string(it.Status), Detail: it.Detail})
		}
	}
	add("here", rep.Pulled)
	add("peer", rep.Pushed)
	return res
}

// PeerAttach adds a peers: entry (peer.attach) and writes the config, the
// way HostAttach does for hosts:. The URL is validated with the same rule
// the config loader applies, so a string this refuses is one the file would
// have refused at the next start.
func (o *orch) PeerAttach(r app.Responder, p app.PeerAttachParams) {
	if _, ok := o.findPeer(p.ID); ok {
		r.Fail("peer " + p.ID + " is already configured")
		return
	}
	peer := config.Peer{ID: p.ID, Label: p.Label, URL: strings.TrimRight(p.URL, "/"), Token: p.Token, TokenFile: p.TokenFile, Fingerprint: p.Fingerprint}
	cfg := o.cfg
	cfg.Peers = append(slices.Clone(o.cfg.Peers), peer)
	if msg := o.saveConfig(cfg); msg != "" { // validates (id shape, URL, one credential), writes, adopts o.cfg
		r.Fail(msg)
		return
	}
	log.Printf("catway: peer.attach %s (%s)", peer.ID, peer.URL)
	r.OK(app.PeerListResult{Peers: o.peerInfos()})
}

// PeerDetach removes a peers: entry (peer.detach). Nothing synced is undone.
func (o *orch) PeerDetach(r app.Responder, p app.PeerDetachParams) {
	if _, ok := o.findPeer(p.ID); !ok {
		r.Fail(fmt.Sprintf("no peer %q (peers: %s)", p.ID, o.peerIDs()))
		return
	}
	cfg := o.cfg
	cfg.Peers = slices.DeleteFunc(slices.Clone(o.cfg.Peers), func(q config.Peer) bool { return q.ID == p.ID })
	if msg := o.saveConfig(cfg); msg != "" {
		r.Fail(msg)
		return
	}
	log.Printf("catway: peer.detach %s", p.ID)
	r.OK(app.PeerListResult{Peers: o.peerInfos()})
}

// --- inbound HTTP -----------------------------------------------------------------

// registerPeerRoutes mounts the /peer/v1 surface. Registered unconditionally:
// the auth guard (when there is one) already covers these paths, since only
// the login page, the favicon and the notification callback are public.
func (o *orch) registerPeerRoutes(s *rweb.Server) {
	s.Get(peersync.PathHello, o.handlePeerHello)
	s.Get(peersync.PathBundle, o.handlePeerBundle)
	s.Post(peersync.PathApply, o.handlePeerApply)
}

func (o *orch) handlePeerHello(ctx rweb.Context) error {
	return ctx.WriteJSON(peerLocal{o}.Instance())
}

// handlePeerBundle answers with this side's state over the wanted categories.
// rweb goroutine; the workspace read hops onto the loop.
func (o *orch) handlePeerBundle(ctx rweb.Context) error {
	want := peersync.ParseWant(ctx.Request().QueryParam("want"))
	if !want.Any() {
		return ctx.Status(http.StatusBadRequest).WriteText("want: name at least one of workspaces, todos, plugins")
	}
	b, err := peersync.Collect(peerLocal{o}, want)
	if err != nil {
		return ctx.Status(http.StatusInternalServerError).WriteText(err.Error())
	}
	return ctx.WriteJSON(b)
}

// handlePeerApply takes a peer's bundle and applies it here, answering with
// the report. The categories applied are the ones the BUNDLE says it carries
// (its Want): the sender chose them, and this side has no narrower wish to
// impose — a pushed bundle is the peer's user pressing "sync" with these
// boxes ticked. rweb goroutine; may run for as long as a plugin build.
func (o *orch) handlePeerApply(ctx rweb.Context) error {
	body := ctx.Request().Body()
	if len(body) == 0 {
		return ctx.Status(http.StatusBadRequest).WriteText("empty body")
	}
	if len(body) > peersync.MaxBodyBytes {
		return ctx.Status(http.StatusRequestEntityTooLarge).WriteText("bundle too large")
	}
	var b peersync.Bundle
	if err := json.Unmarshal(body, &b); err != nil {
		return ctx.Status(http.StatusBadRequest).WriteText("bundle: " + err.Error())
	}
	if !b.Want.Any() {
		return ctx.Status(http.StatusBadRequest).WriteText("bundle names no categories")
	}
	start := time.Now()
	rep := peersync.Apply(peerLocal{o}, b, b.Want)
	s, u, k, f := rep.Counts()
	log.Printf("catway: peer apply from %s (%s): %d synced, %d unchanged, %d skipped, %d failed in %s",
		b.Instance.Name(), b.Want, s, u, k, f, time.Since(start).Round(time.Millisecond))
	return ctx.WriteJSON(rep)
}
