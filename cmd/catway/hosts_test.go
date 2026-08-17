//go:build ghostty

package main

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/rohanthewiz/cats/internal/config"
)

// The roster is what every host-aware surface reads from: the sidebar section,
// the badge gate, catctl hosts, and the settings modal. These tests hold it to
// the two promises those surfaces depend on — it names every configured host in
// configured order, and it reports each one's connectivity and pane count
// truthfully, including for a host that is down but still owns panes.

// A session with no hosts: block has exactly one host, and it is the connected,
// default local one holding every pane. This is the shape a browser uses to
// decide it should draw no HOSTS section and no badges at all, so it is the one
// that must not drift.
func TestHostRosterSingleHost(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	newPipeDaemon(t, o)

	got := o.Hosts()
	if len(got) != 1 {
		t.Fatalf("roster = %+v; want exactly one host", got)
	}
	h := got[0]
	if h.ID != localHostID || !h.Default || !h.Connected || h.AddrKind != "unix" {
		t.Fatalf("local host = %+v", h)
	}
	if h.Panes != len(o.session.AllPaneIDs()) {
		t.Fatalf("local host holds %d panes, session has %d", h.Panes, len(o.session.AllPaneIDs()))
	}
}

// Two hosts, one pane each: the roster carries both, in order, with their own
// pane counts — and when one host's link drops, only that row changes.
func TestHostRosterTwoHosts(t *testing.T) {
	o, _, _, _, _ := twoHostOrch(t)

	got := o.Hosts()
	if len(got) != 2 || got[0].ID != localHostID || got[1].ID != testRemoteHost {
		t.Fatalf("roster = %+v; want [local remote] in configured order", got)
	}
	if !got[0].Default || got[1].Default {
		t.Fatalf("exactly the local host should be the default: %+v", got)
	}
	for _, h := range got {
		if !h.Connected {
			t.Fatalf("host %s should be connected: %+v", h.ID, h)
		}
		if h.Panes != 1 {
			t.Fatalf("host %s holds %d panes, want 1", h.ID, h.Panes)
		}
	}

	// The remote host goes away, with its reason recorded the way the dial loop
	// records it. Its panes are still its own — a disconnected host keeps what
	// it holds, it just cannot be reached — and the other host is untouched.
	o.hosts[testRemoteHost].setLastErr(errors.New("dial: connection refused"))
	o.hosts[testRemoteHost].setConn(nil)

	got = o.Hosts()
	if !got[0].Connected || got[0].Error != "" {
		t.Fatalf("local host should be unaffected: %+v", got[0])
	}
	if got[1].Connected || got[1].Error == "" || got[1].Panes != 1 {
		t.Fatalf("remote host should be down, explained, and still holding its pane: %+v", got[1])
	}
}

// The layout every browser renders from carries each pane's resolved host and
// each workspace's — resolved, so a badge never has to print the model's empty
// "means the default" form or an id the roster no longer lists.
func TestViewportLayoutCarriesHosts(t *testing.T) {
	o, _, remotePane, _, _ := twoHostOrch(t)

	// twoHostOrch leaves the remote workspace active, so the viewport is its.
	msg := o.viewportLayout()
	if len(msg.Panes) != 1 || msg.Panes[0].Pane != remotePane {
		t.Fatalf("viewport panes = %+v; want just the remote workspace's pane", msg.Panes)
	}
	if msg.Panes[0].Host != testRemoteHost {
		t.Fatalf("pane host = %q, want %q", msg.Panes[0].Host, testRemoteHost)
	}
	for _, ws := range msg.Workspaces {
		if ws.Host == "" {
			t.Fatalf("workspace %s carries no host: %+v", ws.ID, ws)
		}
	}
	// The default host's workspace resolves to the default rather than to "".
	if msg.Workspaces[0].Host != o.defaultHost {
		t.Fatalf("first workspace host = %q, want the default %q", msg.Workspaces[0].Host, o.defaultHost)
	}
}

// A configured roster reaches the runtime: two hosts, the marked one is the
// default, and their labels come from the config rather than their ids.
func TestNewOrchHostsBuildsRoster(t *testing.T) {
	dir := t.TempDir()
	hosts := config.EffectiveHosts(filepath.Join(dir, "local.sock"), []config.Host{
		{ID: "devbox", Label: "devbox (ssh)", Addr: "unix://" + filepath.Join(dir, "devbox.sock"), Default: true},
	})
	o, err := newOrchHosts(hosts, dir)
	if err != nil {
		t.Fatalf("newOrchHosts: %v", err)
	}
	if len(o.hosts) != 2 || o.hosts["devbox"] == nil {
		t.Fatalf("hosts = %v", o.hostOrder)
	}
	if o.defaultHost != "devbox" {
		t.Fatalf("default host = %q, want devbox", o.defaultHost)
	}
	if o.hosts["devbox"].label != "devbox (ssh)" {
		t.Fatalf("label = %q", o.hosts["devbox"].label)
	}
	// The session's original pane predates the roster and names no host, so it
	// belongs to whatever the default is — including when that is not local.
	pid := uint32(o.session.AllPaneIDs()[0])
	if got := o.paneHostID(pid); got != "devbox" {
		t.Fatalf("unqualified pane host = %q, want the default devbox", got)
	}
}

// A transport catway cannot dial yet fails at startup with the reason, rather
// than becoming a host that retries forever and spawns nothing.
func TestNewOrchHostsRejectsUnsupportedTransport(t *testing.T) {
	dir := t.TempDir()
	hosts := config.EffectiveHosts(filepath.Join(dir, "local.sock"), []config.Host{
		{ID: "devbox", Addr: "tls://devbox:8422"},
	})
	_, err := newOrchHosts(hosts, dir)
	if err == nil {
		t.Fatal("a tls:// host should fail to build until the transport exists")
	}
}
