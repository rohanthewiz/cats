//go:build ghostty

package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/hostmeter"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// The sidebar's USAGE section is assembled from two halves that arrive on
// different clocks: our own poll, and a push from each remote machine. These
// hold the seam between them — that the halves compose, that a machine which
// stopped answering stops being shown, and that nobody is asked to measure
// anything while no browser is open.

func testRows() []hostmeter.Row {
	return []hostmeter.Row{
		{Name: "Memory", Pct: 61, Detail: "9.8G/16.0G"},
		{Name: "CPU", Pct: 12, Detail: "load 1.20", Spark: []float64{8, 12}},
	}
}

// groupIDs is the section as a client would read it.
func groupIDs(m browserproto.Usage) []string {
	var out []string
	for _, g := range m.Groups {
		out = append(out, g.ID)
	}
	return out
}

// A remote host's reading becomes its own subsection, named for the host and
// keyed so the browser gives it the machine scales rather than the rate-limit
// ones.
func TestUsageMsgAppendsRemoteHostGroups(t *testing.T) {
	o, _, _, _, _ := twoHostOrch(t)

	if _, ok := o.usageMsg(); ok {
		t.Fatal("there is nothing to send before the first poll lands")
	}
	o.setUsage(browserproto.NewUsage([]browserproto.UsageGroup{{ID: "claude", Name: "Claude"}}))

	o.noteHostStats(testRemoteHost, testRows())
	m, ok := o.usageMsg()
	if !ok {
		t.Fatal("no usage message after a poll and a push")
	}
	ids := groupIDs(m)
	if len(ids) != 2 || ids[0] != "claude" || ids[1] != "host:"+testRemoteHost {
		t.Fatalf("groups = %v, want [claude host:%s] — the remote host follows the poll's groups", ids, testRemoteHost)
	}
	g := m.Groups[1]
	if g.Name != testRemoteHost {
		t.Errorf("group name = %q, want the host's label %q", g.Name, testRemoteHost)
	}
	if len(g.Windows) != 2 || g.Windows[0].Name != "Memory" || g.Windows[1].Name != "CPU" {
		t.Fatalf("rows = %+v, want the reported rows in order", g.Windows)
	}
	if !g.Windows[0].Headline || g.Windows[1].Headline {
		t.Error("memory is the row a folded group quotes, and the only one")
	}
	if len(g.Windows[1].Spark) != 2 {
		t.Error("the CPU history did not survive the crossing; the row is a shape, not a point")
	}

	// The stored poll reading is untouched — the composition happens on the way
	// out, so the next poll does not have to know about any of this.
	if len(o.usage.Groups) != 1 {
		t.Fatalf("stored poll groups = %v, want only the poll's own", groupIDs(*o.usage))
	}
}

// A machine that stopped answering stops being reported. Its last reading is
// exactly the one not to leave on screen as if it were current.
func TestHostStatsDroppedOnDisconnect(t *testing.T) {
	o, _, _, _, _ := twoHostOrch(t)
	o.setUsage(browserproto.NewUsage(nil))
	o.noteHostStats(testRemoteHost, testRows())

	o.dropHostStats(testRemoteHost)
	m, _ := o.usageMsg()
	if ids := groupIDs(m); len(ids) != 0 {
		t.Fatalf("groups = %v after the host went away, want none", ids)
	}
	// Dropping a host nobody reported for is a no-op rather than an error.
	o.dropHostStats("never-reported")
}

// A host that has reported but has since left the roster contributes nothing,
// even if its reading is still in the map: a subsection under no roster row is
// a machine the session no longer has.
func TestUsageMsgSkipsHostsNoLongerInTheRoster(t *testing.T) {
	o, _, _, _, _ := twoHostOrch(t)
	o.setUsage(browserproto.NewUsage(nil))
	o.noteHostStats(testRemoteHost, testRows())
	delete(o.hosts, testRemoteHost)

	m, _ := o.usageMsg()
	if ids := groupIDs(m); len(ids) != 0 {
		t.Fatalf("groups = %v, want none for a host that has left", ids)
	}
}

// The subscription follows the same attention tier as the account poll, and its
// dark tier is "stop": a box in the roster that nobody has a sidebar open on
// measures nothing at all.
func TestHostStatsIntervalFollowsAttention(t *testing.T) {
	o, _, _, _, _ := twoHostOrch(t)
	for _, tc := range []struct {
		tier int
		want time.Duration
	}{
		{usageAttentionWatched, usageInterval},
		{usageAttentionIdle, usageIdleInterval},
		{usageAttentionDark, 0},
	} {
		o.usageAttention.Store(int32(tc.tier))
		if got := o.hostStatsInterval(); got != tc.want {
			t.Errorf("tier %d: interval = %v, want %v", tc.tier, got, tc.want)
		}
	}
}

// syncHostStats asks the remote hosts and leaves the local one alone — catway
// measures its own machine directly, and in managed mode the local cathost is a
// child of this process on this box, so subscribing would put two CPU samplers
// (and on darwin two iostats) on one machine to draw one row.
func TestSyncHostStatsSubscribesRemotesOnly(t *testing.T) {
	o, _, _, pdLocal, pdRemote := twoHostOrch(t)
	o.hosts[testRemoteHost].setFeatures([]string{orchestration.FeatureHostStats})
	o.hosts[o.defaultHost].setFeatures([]string{orchestration.FeatureHostStats})

	o.usageAttention.Store(int32(usageAttentionWatched))
	o.syncHostStats()

	payload := pdRemote.expect(t, orchestration.MsgRequestHostStats)
	var req orchestration.RequestHostStats
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if got := time.Duration(req.IntervalMs) * time.Millisecond; got != usageInterval {
		t.Fatalf("interval = %v, want %v", got, usageInterval)
	}
	if hasType(pdLocal.collect(50*time.Millisecond), orchestration.MsgRequestHostStats) {
		t.Fatal("the local host was asked to measure itself for us")
	}

	// Re-running at the same tier sends nothing: the daemon only speaks when the
	// answer changed.
	o.syncHostStats()
	if hasType(pdRemote.collect(50*time.Millisecond), orchestration.MsgRequestHostStats) {
		t.Fatal("an unchanged interval was re-sent")
	}

	// The last browser going dark cancels it outright.
	o.usageAttention.Store(int32(usageAttentionDark))
	o.syncHostStats()
	payload = pdRemote.expect(t, orchestration.MsgRequestHostStats)
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("decode cancel: %v", err)
	}
	if req.IntervalMs != 0 {
		t.Fatalf("dark interval = %d ms, want 0 — nobody is looking", req.IntervalMs)
	}
}

// A host that cannot answer is never asked. An older cathost takes an unknown
// message type as an error and reports it, which would reach the user as a
// toast about a request they never made.
func TestStatsRequestNotSentToAnUnadvertisedHost(t *testing.T) {
	o, _, _, _, pdRemote := twoHostOrch(t)
	o.usageAttention.Store(int32(usageAttentionWatched))
	o.syncHostStats()

	if hasType(pdRemote.collect(50*time.Millisecond), orchestration.MsgRequestHostStats) {
		t.Fatal("a host that advertised no host_stats capability was asked anyway")
	}
	// The interval is still remembered, so the reconnect that finds a capable
	// daemon establishes it without the orchestrator being asked again.
	if got := o.hosts[testRemoteHost].statsInterval; got != usageInterval {
		t.Fatalf("remembered interval = %v, want %v", got, usageInterval)
	}
}
