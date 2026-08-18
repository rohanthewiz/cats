//go:build ghostty

package main

import (
	"time"

	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/hostmeter"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// Per-host meters: the sidebar's USAGE section growing one subsection per
// remote cathost, so "is anything about to stop?" can be asked of the machine a
// pane is actually on rather than only of the one the browser is pointed at.
//
// The local host is deliberately NOT part of this. catway measures its own box
// directly (usage.go's hostUsageGroup), which is both cheaper — no round trip,
// no subscription — and necessary: in managed mode the local cathost is a child
// of this process on this machine, and subscribing to it would put two CPU
// samplers, and on darwin two iostats, on one box to draw one row.
//
// The subscription is paced by the same attention tier as the account poll,
// which means it is *cancelled* when the last browser disconnects. That is the
// rule that keeps a cathost from being a monitoring agent: a box in the roster
// that nobody has a sidebar open on measures nothing at all.

// hostStatsInterval is how often a remote host should report, for the current
// attention tier — 0 meaning "stop".
//
// It tracks the account poll's cadence because the two end up beside each other
// in one section: a remote memory row that refreshed on a different clock from
// the local one would make a glance across the section a comparison of two
// different moments. Dark is the exception rather than a third cadence: with no
// browser there is nothing to draw, so there is nothing worth measuring on
// somebody else's machine.
func (o *orch) hostStatsInterval() time.Duration {
	switch o.usageAttention.Load() {
	case usageAttentionWatched:
		return usageInterval
	case usageAttentionIdle:
		return usageIdleInterval
	default:
		return 0
	}
}

// syncHostStats re-paces every remote host's subscription to the current tier.
// Loop-goroutine only (it walks o.hosts), and cheap: a daemon whose interval did
// not change sends nothing.
func (o *orch) syncHostStats() {
	want := o.hostStatsInterval()
	for _, id := range o.hostOrder {
		d := o.hosts[id]
		if d == nil || id == localHostID {
			continue
		}
		d.setStatsInterval(want)
	}
}

// noteHostStats records a reading pushed by one host and republishes the usage
// message. Runs on the loop (posted by the daemon pump).
func (o *orch) noteHostStats(hostID string, rows []hostmeter.Row) {
	if o.hostStats == nil {
		o.hostStats = make(map[string][]hostmeter.Row)
	}
	o.hostStats[hostID] = rows
	o.broadcastUsage()
}

// dropHostStats forgets a host's reading — on disconnect, and on detach.
//
// A stale meter is worse than an absent one here for the same reason a stale
// latency figure is: the numbers describe a machine, and a machine that has
// stopped answering is precisely the one whose last known state should not be
// presented as its current one. Only republishes when something was actually
// dropped, so a disconnect of a host nobody subscribed to is silent.
func (o *orch) dropHostStats(hostID string) {
	if _, ok := o.hostStats[hostID]; !ok {
		return
	}
	delete(o.hostStats, hostID)
	o.broadcastUsage()
}

// usageMsg is the whole USAGE section: the poll's reading with one group per
// remote host that has reported appended to it.
//
// Composed on the way out rather than stored composed, because the two halves
// arrive independently — a poll every couple of minutes, a push per host on its
// own subscription — and merging at each arrival would mean every reading had to
// know about every other one. ok is false before the first poll lands, which is
// the only moment there is nothing to send.
func (o *orch) usageMsg() (browserproto.Usage, bool) {
	if o.usage == nil {
		return browserproto.Usage{}, false
	}
	m := *o.usage
	// The local machine's group is already in there (hostUsageGroup), and the
	// remote ones follow it in roster order: hostOrder is the order the operator
	// wrote the config in, which is the order every other host-shaped list in
	// the UI uses, and a section whose subsections reordered themselves as
	// readings arrived would be unreadable.
	for _, id := range o.hostOrder {
		rows := o.hostStats[id]
		if len(rows) == 0 {
			continue
		}
		d := o.hosts[id]
		if d == nil {
			continue
		}
		label := d.label
		if label == "" {
			label = id
		}
		if g, ok := meterGroup("host:"+id, label, rows); ok {
			m.Groups = append(m.Groups, g)
		}
	}
	return m, true
}

// broadcastUsage pushes the composed section to every client. Used by the two
// paths that change only the remote half; setUsage does its own broadcast for
// the poll's half.
func (o *orch) broadcastUsage() {
	if m, ok := o.usageMsg(); ok {
		o.broadcast(m)
	}
}

// setStatsInterval re-paces (or cancels) this host's reporting subscription.
//
// The interval is remembered whether or not it can be sent right now, because a
// host that is down still has a subscription state — the reconnect is what
// applies it, and re-deriving "what did we want from this machine" at that
// moment would mean the dial loop reaching back into the orchestrator's
// attention tier.
func (d *daemon) setStatsInterval(interval time.Duration) {
	d.mu.Lock()
	changed := d.statsInterval != interval
	d.statsInterval = interval
	d.mu.Unlock()
	if changed {
		d.sendStatsRequest()
	}
}

// sendStatsRequest tells the connected host what it should report. A no-op on a
// host that cannot answer — an older cathost would take the unknown message
// type as an error and toast the user about a request they never made.
func (d *daemon) sendStatsRequest() {
	if !d.supports(orchestration.FeatureHostStats) {
		return
	}
	d.mu.Lock()
	interval := d.statsInterval
	d.mu.Unlock()
	d.send(orchestration.NewRequestHostStats(interval))
}
