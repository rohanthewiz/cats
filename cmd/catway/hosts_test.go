//go:build ghostty

package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/config"
	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/orchestration"
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

// Restore has to put every pane back on the machine it was on. This is the one
// promise a multi-host session cannot fudge: a pane restored onto the wrong
// host is a shell in the wrong filesystem with the previous machine's
// scrollback replayed above it, which reads as a working pane until the first
// command lands somewhere unexpected.
func TestRestorePlacesPanesOnTheirOwnHost(t *testing.T) {
	o, localPane, remotePane, _, _ := twoHostOrch(t)
	snap := o.session.Snapshot()

	sess, err := app.RestoreSession(modelSpawner{}, snap)
	if err != nil {
		t.Fatalf("RestoreSession: %v", err)
	}
	// A fresh orchestrator over the same roster — the cold start a catway
	// restart performs, where nothing but the snapshot says where a pane lives.
	r, err := newOrchHostsWith(twoHostConfig(), t.TempDir(), sess)
	if err != nil {
		t.Fatalf("newOrchHostsWith: %v", err)
	}
	if got := r.panes[remotePane].host; got != testRemoteHost {
		t.Fatalf("restored remote pane host = %q, want %q", got, testRemoteHost)
	}
	if got := r.panes[localPane].host; got != localHostID {
		t.Fatalf("restored local pane host = %q, want %q", got, localHostID)
	}

	// And the respawn goes down that host's connection, not the default one.
	pdLocal := newPipeDaemonFor(t, r, r.defaultHost)
	pdRemote := newPipeDaemonFor(t, r, testRemoteHost)
	for _, rt := range r.panes {
		rt.created = false // cold start: no PTY exists on either host yet
	}
	synced := make(chan struct{})
	go func() { r.syncDaemon(); close(synced) }() // pipe writes block until pumped

	// Both pipes have to be drained concurrently: syncDaemon writes to each host
	// in turn and a full pipe would block it (and so the create the other host
	// is waiting for).
	remoteCreates := createdPanes(t, pdRemote, 100*time.Millisecond)
	localCreates := createdPanes(t, pdLocal, 100*time.Millisecond)
	<-synced

	if !(<-remoteCreates)[remotePane] {
		t.Fatalf("remote host was not asked to create pane %d", remotePane)
	}
	local := <-localCreates
	if local[remotePane] {
		t.Fatalf("the remote pane's PTY was created on the local host: %v", local)
	}
	if !local[localPane] {
		t.Fatalf("local host was not asked to create pane %d (got %v)", localPane, local)
	}
}

// createdPanes drains a pipe for a settling window and reports which pane ids
// that host was told to create. Returned over a channel so both hosts' pipes
// can be pumped at once — see the call site.
func createdPanes(t *testing.T, pd *pipeDaemon, d time.Duration) <-chan map[uint32]bool {
	t.Helper()
	out := make(chan map[uint32]bool, 1)
	go func() {
		got := map[uint32]bool{}
		deadline := time.After(d)
		for {
			select {
			case m, ok := <-pd.msgs:
				if !ok {
					out <- got
					return
				}
				if m.mt != orchestration.MsgCreatePane {
					continue
				}
				var cp orchestration.CreatePane
				if err := json.Unmarshal(m.payload, &cp); err == nil {
					got[cp.PaneID] = true
				}
			case <-deadline:
				out <- got
				return
			}
		}
	}()
	return out
}

// A pane on a remote host must not be handed this machine's process cwd. It is
// where catway was started, a path that either does not exist over there or is
// a different directory with the same name; naming nothing instead lets cathost
// spawn the pane in its own default directory — a working shell rather than a
// dead pane. A workspace identity cwd, being a path chosen *for* that host, is
// still sent.
func TestPaneCwdSkipsLocalProcessDirForRemotePanes(t *testing.T) {
	o, localPane, remotePane, _, _ := twoHostOrch(t)

	if got := o.paneCwd(localPane); got != o.cwd {
		t.Fatalf("local pane cwd = %q, want the process cwd %q", got, o.cwd)
	}
	// twoHostOrch's remote workspace carries an identity cwd, which is the
	// host's own path and travels as-is.
	ws := o.session.PaneWorkspace(layout.PaneID(remotePane))
	if got := o.paneCwd(remotePane); got != ws.IdentityCwd {
		t.Fatalf("remote pane cwd = %q, want the workspace identity %q", got, ws.IdentityCwd)
	}
	ws.IdentityCwd = "" // a remote workspace with nothing recorded
	if got := o.paneCwd(remotePane); got != "" {
		t.Fatalf("remote pane cwd = %q, want empty (let cathost choose)", got)
	}

	// A pane placed on another host *inside a local workspace* — what "split
	// here on devbox" produces — must not inherit that workspace's directory
	// either: it is a path in this machine's filesystem, and the workspace is
	// not where the pane is.
	local := o.session.PaneWorkspace(layout.PaneID(localPane))
	if local.IdentityCwd == "" {
		t.Fatal("the local workspace should have an identity cwd to test with")
	}
	target := layout.PaneID(localPane)
	guest, err := o.session.SplitPaneOn(&target, layout.Horizontal, testRemoteHost)
	if err != nil {
		t.Fatalf("SplitPaneOn: %v", err)
	}
	o.syncDaemon() // resolves the new runtime's host
	if got := o.paneCwd(uint32(guest)); got != "" {
		t.Fatalf("cross-host pane cwd = %q, want empty rather than the workspace's local path", got)
	}
}

// The browser's roster message is a translation of the same roster host.list
// answers with, and the fields the UI *gates* on have to survive it: local
// decides whether the start-path picker is offered, is_default which host an
// unqualified create lands on.
func TestHostsMsgCarriesLocalAndDefault(t *testing.T) {
	o, _, _, _, _ := twoHostOrch(t)

	msg := o.hostsMsg()
	if len(msg.Items) != 2 {
		t.Fatalf("hosts message = %+v", msg.Items)
	}
	local, remote := msg.Items[0], msg.Items[1]
	if !local.Local || !local.Default {
		t.Fatalf("local host item = %+v, want local+default", local)
	}
	if remote.Local || remote.Default {
		t.Fatalf("remote host item = %+v, want neither local nor default", remote)
	}
}
