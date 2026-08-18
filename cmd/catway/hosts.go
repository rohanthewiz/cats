//go:build ghostty

package main

import (
	"fmt"
	"log"
	"slices"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/config"
	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// Hot attach/detach: editing the cathost roster of a *running* catway
// (host.attach / host.detach, and a config reload that changed hosts:).
//
// Everything here runs on the orchestrator loop, which is the goroutine that
// owns orch.hosts. The daemons' own dial loops never read that map — they reach
// back only by posting closures — so re-shaping it here needs no lock; what it
// does need is care about ORDER, which is what applyHostRoster is:
//
//	build  →  retire  →  install  →  re-home  →  start  →  announce
//
// Build first so an unusable address fails the edit before anything is torn
// down; retire (flush in-flight work, stop the dial loop) while the departing
// host is still resolvable by pane, since that is how its panes are found;
// install the new map; only then re-home the orphans onto the default host,
// which is now the *new* default.
//
// The config file is the other half of every edit. A roster that vanished on
// restart would make attach a toy, so both commands rewrite the hosts: block
// through the same saveConfig the settings modal uses, and the live roster is
// always re-derived from o.cfg.Hosts + o.cathostSocket rather than patched in
// place — one source of truth, and no way for the file and the running session
// to describe different machines.

// sameDialTarget reports whether two config entries describe the same
// connection. Deliberately not `a == b`: Label and Default are presentation and
// policy, and a roster edit that only renames a host or moves the default flag
// must not drop a live connection (and every PTY's stream with it) to express
// itself.
func sameDialTarget(a, b config.Host) bool {
	return a.ID == b.ID && a.Addr == b.Addr && a.Token == b.Token &&
		a.TokenFile == b.TokenFile && a.Fingerprint == b.Fingerprint
}

// applyHostRoster installs the roster described by `configured` (the config
// file's hosts: block) over the one currently running. The local host is
// synthesized in exactly as it is at startup, so a caller only ever passes the
// file's half.
//
// An error means nothing changed: the only failure is an address that cannot be
// turned into a dialer, and every new daemon is built before the first one is
// retired precisely so that failure is total rather than half-applied.
func (o *orch) applyHostRoster(configured []config.Host) error {
	eff := config.EffectiveHosts(o.cathostSocket, configured)

	// --- build ---------------------------------------------------------------
	// A host is rebuilt when it is new or when its dial target moved; an entry
	// that only changed label/default keeps its daemon (and its connection).
	built := make(map[string]*daemon, len(eff))
	for _, h := range eff {
		if d := o.hosts[h.ID]; d != nil && sameDialTarget(d.spec, h) {
			continue
		}
		d, err := newDaemon(o, h)
		if err != nil {
			return err
		}
		built[h.ID] = d
	}

	// --- retire --------------------------------------------------------------
	// Two different fates, and the difference matters: a host that is GONE has
	// its panes moved to the default machine, while a host that was merely
	// READDRESSED keeps them — same id, same panes, a new way to reach them. So
	// a readdressed host's PTYs are never closed either: if the new address is
	// the same machine by another route (an ssh forward moved, a socket path
	// changed), the new connection's reconcile adopts the survivors, and closing
	// them here would have killed the terminals it was about to re-adopt.
	keep := make(map[string]bool, len(eff))
	for _, h := range eff {
		keep[h.ID] = true
	}
	var orphaned []uint32 // panes whose host left the roster entirely
	for _, id := range o.hostOrder {
		d := o.hosts[id]
		if d == nil {
			continue
		}
		gone := !keep[id]
		if !gone && built[id] == nil {
			continue // unchanged, or changed only in name/default
		}
		reason := "cathost detached"
		if !gone {
			reason = "cathost readdressed — reconnecting"
		}
		// In-flight reads/captures/waits on this host can never be answered by
		// the connection that is going away. Failing them here (while the panes
		// still resolve to this host) is the difference between a caller getting
		// an error and a caller getting a timeout.
		o.flushPendingFor(id, reason)
		o.flushWaitersFor(id, reason)
		if gone {
			// This host's own panes, closed on this host: `orphaned` accumulates
			// across every departing host in the edit, and passing the running
			// total here would ask the second one to close the first one's panes.
			mine := o.panesOnHost(id)
			o.closePanesOn(d, mine)
			orphaned = append(orphaned, mine...)
			// Its meters described a machine that is no longer part of this
			// session; leaving them in the sidebar would keep a subsection for a
			// host with no row in the roster above it.
			o.dropHostStats(id)
		}
		d.stop()
	}

	// --- install -------------------------------------------------------------
	next := make(map[string]*daemon, len(eff))
	order := make([]string, 0, len(eff))
	def := ""
	for _, h := range eff {
		d := built[h.ID]
		if d == nil {
			d = o.hosts[h.ID]
			d.label = h.DisplayLabel() // a rename lands without a redial
			d.spec = h
		}
		next[h.ID] = d
		order = append(order, h.ID)
		if h.Default {
			def = h.ID
		}
	}
	if def == "" && len(order) > 0 {
		def = order[0] // EffectiveHosts marks one, but never depend on it
	}
	o.hosts, o.hostOrder, o.defaultHost = next, order, def

	// --- re-home -------------------------------------------------------------
	for _, pid := range orphaned {
		o.rehomePane(pid)
	}

	// --- start + announce ----------------------------------------------------
	for _, d := range built {
		go d.run()
	}
	if len(orphaned) > 0 {
		// syncDaemon respawns the re-homed panes on their new host and the
		// broadcast repaints their (now different) badges; applyModel is the one
		// call that does both plus the structural events and the save.
		o.applyModel()
	}
	o.broadcastHosts()
	return nil
}

// panesOnHost lists the live panes currently resolving to a host.
func (o *orch) panesOnHost(hostID string) []uint32 {
	var out []uint32
	for _, id := range o.session.AllPaneIDs() {
		if o.paneHostID(uint32(id)) == hostID {
			out = append(out, uint32(id))
		}
	}
	return out
}

// closePanesOn asks a departing host to shut its PTYs down. Best effort by
// design: the usual reason to detach a host is that it is unreachable, and a
// disconnected send is simply dropped. When it *is* reachable this is what
// keeps a detach from leaking shells on a persistent cathost that nobody is
// attached to any more.
func (o *orch) closePanesOn(d *daemon, panes []uint32) {
	if !d.connected() {
		return
	}
	for _, pid := range panes {
		d.send(orchestration.NewClosePane(pid))
	}
}

// rehomePane moves one pane onto the default host after its own host left the
// roster.
//
// A terminal cannot follow: the process is on the other machine, and detaching
// is exactly the loss of the channel that could have talked to it. So the pane
// keeps its place in the layout, its name and its public handle, and gets a
// fresh shell on the default host — which is the difference between a session
// that lost some shells and a session with permanently black rectangles in it.
//
// The stored host becomes "" rather than the default host's id: "" means "the
// default", so a pane that was displaced by an edit tracks later changes to the
// default instead of being pinned to whatever it happened to be at this moment.
// The runtime gets the resolved id, as every runtime does.
//
// Nothing is seeded into the new pane. The scrollback catway holds for it is
// another machine's, and replaying it into a shell on this one would produce a
// convincing history of things that never happened here.
func (o *orch) rehomePane(pid uint32) {
	o.session.SetPaneHost(layout.PaneID(pid), "")
	rt := o.panes[pid]
	if rt == nil {
		return
	}
	rt.host = o.defaultHost
	rt.created = false // syncDaemon spawns it on the new host
	// Restored state that was waiting for the old host would now be applied to
	// the wrong filesystem: a saved cwd and an agent-resume argv both name paths
	// on the machine we just let go of.
	delete(o.restoredCwds, pid)
	delete(o.resumePlans, pid)
	delete(o.seeds, pid)
	rt.cwd = "" // the live cwd was the old machine's; the new host will report its own
	rt.branch = ""
}

// --- §7 commands ---------------------------------------------------------------

// HostAttach adds a cathost to the running session (host.attach) and to the
// config's hosts: block, then starts dialing it. It answers immediately with
// the new roster rather than waiting for the connection: the dial has its own
// retry loop, a first attempt can legitimately take the full timeout, and the
// roster row (with its error, once there is one) is where the outcome shows up
// anyway — the same place a host configured at startup reports it.
func (o *orch) HostAttach(r app.Responder, p app.HostAttachParams) {
	if o.hosts[p.ID] != nil {
		if p.ID == localHostID {
			r.Fail("host " + p.ID + " is this catway's own machine and is always attached")
			return
		}
		r.Fail("host " + p.ID + " is already attached")
		return
	}
	h := config.Host{
		ID: p.ID, Label: p.Label, Addr: p.Addr, Token: p.Token,
		TokenFile: p.TokenFile, Fingerprint: p.Fingerprint, Default: p.Default,
	}
	// Resolve the dialer up front so a bad address is refused before the config
	// file is written. applyHostRoster checks it again — this is the check that
	// keeps a rejected host out of the file.
	if _, _, err := dialerFor(h); err != nil {
		r.Fail(err.Error())
		return
	}

	cfg := o.cfg
	cfg.Hosts = slices.Clone(o.cfg.Hosts)
	if p.Default {
		// At most one entry may carry the flag (config.Validate), so claiming it
		// means releasing it everywhere else.
		for i := range cfg.Hosts {
			cfg.Hosts[i].Default = false
		}
	}
	cfg.Hosts = append(cfg.Hosts, h)
	if msg := o.saveConfig(cfg); msg != "" { // validates, writes, adopts o.cfg
		r.Fail(msg)
		return
	}
	if err := o.applyHostRoster(cfg.Hosts); err != nil {
		// Unreachable in practice (the dialer resolved above), and reported
		// rather than rolled back: the file now names the host, which is the
		// state a restart would come up in.
		r.Fail(err.Error())
		return
	}
	log.Printf("catway: host.attach %s (%s)", h.ID, h.Addr)
	r.OK(app.HostListResult{Hosts: o.Hosts()})
}

// HostDetach drops a cathost from the running session (host.detach) and from
// the config's hosts: block.
//
// A host that still holds panes is refused unless Force, because detaching it
// abandons those terminals — see rehomePane for what Force actually does with
// them. The refusal names the count and the destination so the operator can
// judge the trade before repeating the command.
func (o *orch) HostDetach(r app.Responder, p app.HostDetachParams) {
	if p.ID == localHostID {
		// Not a policy choice: the local host is synthesized from
		// server.cathost_socket rather than configured, so there is no hosts:
		// entry to remove, and a session with no default machine to fall back
		// to has nowhere to put anything.
		r.Fail("the local host cannot be detached — it is this catway's own cathost")
		return
	}
	if o.hosts[p.ID] == nil {
		r.Fail("no such host " + p.ID)
		return
	}
	panes := o.panesOnHost(p.ID)
	if len(panes) > 0 && !p.Force {
		r.Fail(fmt.Sprintf("host %s still holds %d %s — detach with force to move %s to %s "+
			"(the terminals there are abandoned and respawn as new shells)",
			p.ID, len(panes), plural(len(panes), "pane", "panes"),
			plural(len(panes), "it", "them"), o.detachFallbackLabel(p.ID)))
		return
	}

	cfg := o.cfg
	cfg.Hosts = slices.DeleteFunc(slices.Clone(o.cfg.Hosts), func(h config.Host) bool {
		return h.ID == p.ID
	})
	if msg := o.saveConfig(cfg); msg != "" {
		r.Fail(msg)
		return
	}
	if err := o.applyHostRoster(cfg.Hosts); err != nil {
		r.Fail(err.Error())
		return
	}
	log.Printf("catway: host.detach %s (%d %s re-homed on %s)",
		p.ID, len(panes), plural(len(panes), "pane", "panes"), o.defaultHost)
	r.OK(app.HostListResult{Hosts: o.Hosts()})
}

// detachFallbackLabel names the host a forced detach would move panes to: the
// default host, unless the host being detached IS the default, in which case
// the roster's fallback (the local host) is what they will land on.
func (o *orch) detachFallbackLabel(detaching string) string {
	id := o.defaultHost
	if id == detaching {
		id = localHostID
	}
	if d := o.hosts[id]; d != nil {
		return d.label
	}
	return id
}

// plural picks a word for a count. Small, but the alternative is an error
// message that says "1 panes" at the one moment the operator is being asked to
// weigh exactly how much they are about to throw away.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
