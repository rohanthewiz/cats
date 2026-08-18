//go:build ghostty

package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/config"
	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// Hot attach/detach (Phase 5). The promises under test are the ones an operator
// is trusting when they type the command into a running session: the roster and
// the config file always agree, a refusal changes neither, and a host that goes
// away never leaves a pane pointing at a machine nobody is attached to.

// hostEditResponder captures one synchronous host.attach / host.detach reply.
type hostEditResponder struct {
	ok, fail bool
	data     any
	errMsg   string
}

func (*hostEditResponder) WantsReply() bool  { return true }
func (r *hostEditResponder) OK(data any)     { r.ok, r.data = true, data }
func (r *hostEditResponder) Fail(msg string) { r.fail, r.errMsg = true, msg }

// editOrch is a single-host orch with a writable config path, which is what
// every roster edit needs: the command's second half is a config.Save, and a
// test that skipped it would be exercising half the command.
//
// Attaching starts a real dial loop, so every daemon is stopped at teardown —
// a leaked one would keep redialing (and posting onto a mailbox nobody drains)
// for the rest of the run.
func editOrch(t *testing.T) (*orch, string) {
	t.Helper()
	dir := t.TempDir()
	o, err := newOrch(filepath.Join(dir, "cathost.sock"), dir)
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	o.cfg, o.cfgPath = config.Default(), cfgPath
	stopHostsAtExit(t, o)
	return o, cfgPath
}

// editOrchWith is editOrch over a configured roster (the local host plus the
// given entries), for the detach cases — which need a host that already exists
// and, usually, panes on it. No dial loops are running: main starts those, and
// a test that wants one starts it itself.
func editOrchWith(t *testing.T, hosts ...config.Host) (*orch, string) {
	t.Helper()
	dir := t.TempDir()
	o, err := newOrchHosts(config.EffectiveHosts(filepath.Join(dir, "cathost.sock"), hosts), dir)
	if err != nil {
		t.Fatalf("newOrchHosts: %v", err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := config.Default()
	cfg.Hosts = hosts
	o.cfg, o.cfgPath = cfg, cfgPath
	stopHostsAtExit(t, o)
	return o, cfgPath
}

func stopHostsAtExit(t *testing.T, o *orch) {
	t.Cleanup(func() {
		for _, d := range o.hosts {
			d.stop()
		}
	})
}

// savedHosts reads the hosts: block back off disk. The file is the half of the
// edit that outlives the process, so asserting on o.cfg alone would pass for a
// command that never wrote anything.
func savedHosts(t *testing.T, path string) []config.Host {
	t.Helper()
	cfg, _, err := config.Load(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	return cfg.Hosts
}

func hostIDs(o *orch) []string { return o.hostOrder }

// An attach lands in three places at once: the live roster, the in-memory
// config, and the file. All three, or the command is a lie the next restart
// exposes.
func TestHostAttachAddsToRosterAndConfig(t *testing.T) {
	o, cfgPath := editOrch(t)
	addr := "unix://" + filepath.Join(t.TempDir(), "devbox.sock")

	r := &hostEditResponder{}
	o.HostAttach(r, app.HostAttachParams{ID: "devbox", Label: "Dev Box", Addr: addr})
	if !r.ok {
		t.Fatalf("host.attach failed: %q", r.errMsg)
	}

	roster, _ := r.data.(app.HostListResult)
	if len(roster.Hosts) != 2 || roster.Hosts[1].ID != "devbox" {
		t.Fatalf("reply roster = %+v; want [local devbox]", roster.Hosts)
	}
	if roster.Hosts[1].Label != "Dev Box" || roster.Hosts[1].Default || roster.Hosts[1].Local {
		t.Fatalf("attached host = %+v; want a labelled, non-default, non-local host", roster.Hosts[1])
	}
	if o.hosts["devbox"] == nil || o.defaultHost != localHostID {
		t.Fatalf("roster = %v, default = %q", hostIDs(o), o.defaultHost)
	}
	saved := savedHosts(t, cfgPath)
	if len(saved) != 1 || saved[0].ID != "devbox" || saved[0].Addr != addr {
		t.Fatalf("saved hosts: = %+v", saved)
	}
	// The synthesized local host is never written: it is derived from
	// server.cathost_socket, and a copy of it in hosts: would go stale the first
	// time that setting (or the --socket flag) changed.
	for _, h := range saved {
		if h.ID == localHostID {
			t.Fatalf("the synthesized local host was written to the file: %+v", saved)
		}
	}
}

// Attaching with is_default moves where unqualified new panes land, and moves
// it in the file too — the flag is single-valued, so claiming it has to release
// whoever held it.
func TestHostAttachCanClaimDefault(t *testing.T) {
	o, cfgPath := editOrch(t)
	first := "unix://" + filepath.Join(t.TempDir(), "a.sock")
	second := "unix://" + filepath.Join(t.TempDir(), "b.sock")

	r := &hostEditResponder{}
	o.HostAttach(r, app.HostAttachParams{ID: "boxa", Addr: first, Default: true})
	if !r.ok {
		t.Fatalf("attach boxa: %q", r.errMsg)
	}
	if o.defaultHost != "boxa" {
		t.Fatalf("default host = %q, want boxa", o.defaultHost)
	}

	r = &hostEditResponder{}
	o.HostAttach(r, app.HostAttachParams{ID: "boxb", Addr: second, Default: true})
	if !r.ok {
		t.Fatalf("attach boxb: %q", r.errMsg)
	}
	if o.defaultHost != "boxb" {
		t.Fatalf("default host = %q, want boxb", o.defaultHost)
	}
	saved := savedHosts(t, cfgPath)
	if len(saved) != 2 {
		t.Fatalf("saved hosts = %+v", saved)
	}
	if saved[0].Default || !saved[1].Default {
		t.Fatalf("exactly boxb should be marked default on disk: %+v", saved)
	}
}

// A refused attach must leave nothing behind — no roster entry, and above all
// no config file naming a host catway could not dial, which would turn one bad
// command into a startup that keeps complaining.
func TestHostAttachRefusals(t *testing.T) {
	cases := []struct {
		name string
		p    app.HostAttachParams
		want string
	}{
		{"local is always attached", app.HostAttachParams{ID: localHostID, Addr: "unix:///tmp/x.sock"}, "always attached"},
		{"cleartext off the loopback", app.HostAttachParams{ID: "far", Addr: "tcp://example.com:8422"}, "loopback"},
		{"unknown scheme", app.HostAttachParams{ID: "weird", Addr: "http://example.com"}, "scheme"},
		{"no scheme at all", app.HostAttachParams{ID: "bare", Addr: "/tmp/cathost.sock"}, "scheme://target"},
		{"a local host must stay unix", app.HostAttachParams{ID: "x", Addr: "tls://box:1"}, ""}, // placeholder, replaced below
	}
	// The last case is about the id, not the address; state it plainly.
	cases[4] = struct {
		name string
		p    app.HostAttachParams
		want string
	}{"tls fingerprint must be a hash", app.HostAttachParams{ID: "box", Addr: "tls://box:8422", Fingerprint: "nonsense"}, "SHA-256"}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o, cfgPath := editOrch(t)
			r := &hostEditResponder{}
			o.HostAttach(r, tc.p)
			if !r.fail {
				t.Fatalf("attach %+v was accepted", tc.p)
			}
			if !strings.Contains(r.errMsg, tc.want) {
				t.Fatalf("error %q does not mention %q", r.errMsg, tc.want)
			}
			if len(o.hostOrder) != 1 {
				t.Fatalf("roster changed: %v", hostIDs(o))
			}
			if _, _, err := config.Load(cfgPath); err == nil {
				t.Fatalf("a refused attach wrote %s", cfgPath)
			}
		})
	}
}

func TestHostAttachRefusesDuplicate(t *testing.T) {
	o, _ := editOrch(t)
	addr := "unix://" + filepath.Join(t.TempDir(), "devbox.sock")
	r := &hostEditResponder{}
	o.HostAttach(r, app.HostAttachParams{ID: "devbox", Addr: addr})
	if !r.ok {
		t.Fatalf("first attach: %q", r.errMsg)
	}
	r = &hostEditResponder{}
	o.HostAttach(r, app.HostAttachParams{ID: "devbox", Addr: addr})
	if !r.fail || !strings.Contains(r.errMsg, "already attached") {
		t.Fatalf("second attach: fail=%v err=%q", r.fail, r.errMsg)
	}
	if len(o.hostOrder) != 2 {
		t.Fatalf("roster = %v", hostIDs(o))
	}
}

// The local host is the fallback every other host's panes land on, so it is the
// one entry with nowhere to go.
func TestHostDetachRefusesLocal(t *testing.T) {
	o, _ := editOrch(t)
	r := &hostEditResponder{}
	o.HostDetach(r, app.HostDetachParams{ID: localHostID})
	if !r.fail || !strings.Contains(r.errMsg, "cannot be detached") {
		t.Fatalf("detach local: fail=%v err=%q", r.fail, r.errMsg)
	}
	if len(o.hostOrder) != 1 {
		t.Fatalf("roster = %v", hostIDs(o))
	}
}

// remoteHostEntry is a config entry for a second host at a socket that will
// never answer — every detach test cares about the roster, not the wire.
func remoteHostEntry(t *testing.T, id string) config.Host {
	t.Helper()
	return config.Host{ID: id, Addr: "unix://" + filepath.Join(t.TempDir(), id+".sock")}
}

// A host with no panes is an ordinary removal: no force, no warning, nothing to
// mourn.
func TestHostDetachEmptyHost(t *testing.T) {
	o, cfgPath := editOrchWith(t, remoteHostEntry(t, "devbox"))

	r := &hostEditResponder{}
	o.HostDetach(r, app.HostDetachParams{ID: "devbox"})
	if !r.ok {
		t.Fatalf("detach: %q", r.errMsg)
	}
	if len(o.hostOrder) != 1 || o.hosts["devbox"] != nil {
		t.Fatalf("roster = %v", hostIDs(o))
	}
	if saved := savedHosts(t, cfgPath); len(saved) != 0 {
		t.Fatalf("saved hosts = %+v; want none", saved)
	}
}

// The refusal is the default because the alternative loses running work
// silently. It must also change nothing at all — a half-applied refusal is
// worse than either outcome.
func TestHostDetachRefusesHostWithPanes(t *testing.T) {
	o, cfgPath := editOrchWith(t, remoteHostEntry(t, "devbox"))
	if _, err := o.session.CreateWorkspaceAtOn(t.TempDir(), "devbox"); err != nil {
		t.Fatalf("CreateWorkspaceAtOn: %v", err)
	}
	o.syncDaemon()

	r := &hostEditResponder{}
	o.HostDetach(r, app.HostDetachParams{ID: "devbox"})
	if !r.fail {
		t.Fatalf("detach with panes was accepted")
	}
	for _, want := range []string{"1 pane", "force"} {
		if !strings.Contains(r.errMsg, want) {
			t.Fatalf("error %q does not mention %q", r.errMsg, want)
		}
	}
	if o.hosts["devbox"] == nil {
		t.Fatalf("roster lost the host anyway: %v", hostIDs(o))
	}
	if _, _, err := config.Load(cfgPath); err == nil {
		t.Fatalf("a refused detach wrote %s", cfgPath)
	}
}

// The whole point of force: the pane survives the machine leaving. It keeps its
// id (and so its place in the layout), stops naming the departed host, and is
// spawned again on the default one — while the departing cathost, if it is
// still reachable, is told to shut the PTY it will no longer be asked about.
func TestHostDetachForceRehomesPanes(t *testing.T) {
	o, cfgPath := editOrchWith(t, remoteHostEntry(t, "devbox"))
	if _, err := o.session.CreateWorkspaceAtOn(t.TempDir(), "devbox"); err != nil {
		t.Fatalf("CreateWorkspaceAtOn: %v", err)
	}
	pdLocal := newPipeDaemonFor(t, o, localHostID)
	pdRemote := newPipeDaemonFor(t, o, "devbox")
	o.syncDaemon()
	o.refreshViewport()

	pane := uint32(o.session.ActiveWorkspace().Tabs[0].RootPane)
	if got := o.panes[pane].host; got != "devbox" {
		t.Fatalf("pane host = %q, want devbox", got)
	}
	pdRemote.expect(t, orchestration.MsgCreatePane) // its original spawn
	pdLocal.collect(100 * time.Millisecond)         // settle: only what the detach sends matters below

	r := &hostEditResponder{}
	o.HostDetach(r, app.HostDetachParams{ID: "devbox", Force: true})
	if !r.ok {
		t.Fatalf("forced detach: %q", r.errMsg)
	}

	// The departing host is told to close what it holds, so a persistent cathost
	// nobody is attached to any more does not keep the shell running forever.
	var closed orchestration.ClosePane
	if err := json.Unmarshal(pdRemote.expect(t, orchestration.MsgClosePane), &closed); err != nil {
		t.Fatalf("close_pane: %v", err)
	}
	if closed.PaneID != pane {
		t.Fatalf("closed pane %d, want %d", closed.PaneID, pane)
	}

	if o.hosts["devbox"] != nil || len(o.hostOrder) != 1 {
		t.Fatalf("roster = %v", hostIDs(o))
	}
	// "" rather than "local": the pane was displaced, not deliberately placed,
	// so it should follow the default host wherever it goes next.
	if got := o.session.PaneHost(layout.PaneID(pane)); got != "" {
		t.Fatalf("stored pane host = %q, want the empty (default) form", got)
	}
	if got := o.panes[pane].host; got != localHostID {
		t.Fatalf("runtime pane host = %q, want %q", got, localHostID)
	}
	// And it is a live pane again: the default host was asked to spawn it.
	pdLocal.expect(t, orchestration.MsgCreatePane)
	if saved := savedHosts(t, cfgPath); len(saved) != 0 {
		t.Fatalf("saved hosts = %+v; want none", saved)
	}
}

// A re-homed pane must not be spawned in the departed machine's directory. The
// workspace still says it lives on devbox — that is its identity, and detaching
// is not a decision to move the project — but its start path describes a
// filesystem this catway can no longer reach, so the pane that landed here gets
// the ordinary local default instead.
func TestRehomedPaneDoesNotInheritTheDepartedHostsPath(t *testing.T) {
	o, _ := editOrchWith(t, remoteHostEntry(t, "devbox"))
	const remotePath = "/srv/app-that-only-exists-on-devbox"
	if _, err := o.session.CreateWorkspaceAtOn(remotePath, "devbox"); err != nil {
		t.Fatalf("CreateWorkspaceAtOn: %v", err)
	}
	o.syncDaemon()
	pane := uint32(o.session.ActiveWorkspace().Tabs[0].RootPane)
	if got := o.paneCwd(pane); got != remotePath {
		t.Fatalf("before the detach the pane should spawn in the workspace path, got %q", got)
	}

	r := &hostEditResponder{}
	o.HostDetach(r, app.HostDetachParams{ID: "devbox", Force: true})
	if !r.ok {
		t.Fatalf("forced detach: %q", r.errMsg)
	}
	if got := o.paneCwd(pane); got == remotePath {
		t.Fatalf("the re-homed pane is still being spawned in %q, which is another machine's directory", got)
	}
	// The workspace keeps naming its host: the pin is a policy, and a host that
	// comes back should get its workspace's new panes again.
	if got := o.session.ActiveWorkspace().HostID; got != "devbox" {
		t.Fatalf("workspace host = %q, want it left alone at devbox", got)
	}
}

// A detached host stops being dialed. Without this the roster would drop a row
// while a goroutine kept reconnecting to it — and, on reconnect, kept reporting
// panes catway had already moved somewhere else.
func TestDetachStopsTheDialLoop(t *testing.T) {
	o, _ := editOrchWith(t, remoteHostEntry(t, "devbox"))
	d := o.hosts["devbox"]

	done := make(chan struct{})
	go func() { d.run(); close(done) }()

	r := &hostEditResponder{}
	o.HostDetach(r, app.HostDetachParams{ID: "devbox"})
	if !r.ok {
		t.Fatalf("detach: %q", r.errMsg)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the dial loop is still running a detach later")
	}
	if !d.stopping() {
		t.Fatal("daemon does not report itself stopped")
	}
}

// A roster edit that only renames a host must not cost it its connection: the
// label is what the sidebar prints, and redialing to change a string would
// interrupt every stream the host is carrying.
func TestApplyHostRosterRenameKeepsConnection(t *testing.T) {
	o, _ := editOrchWith(t, remoteHostEntry(t, "devbox"))
	newPipeDaemonFor(t, o, "devbox")
	before := o.hosts["devbox"]

	renamed := o.cfg.Hosts[0]
	renamed.Label = "the build box"
	if err := o.applyHostRoster([]config.Host{renamed}); err != nil {
		t.Fatalf("applyHostRoster: %v", err)
	}

	if o.hosts["devbox"] != before {
		t.Fatal("a rename rebuilt the daemon")
	}
	if !before.connected() {
		t.Fatal("a rename dropped the connection")
	}
	if before.label != "the build box" {
		t.Fatalf("label = %q", before.label)
	}
}

// Moving a host's address is the opposite case: same host, new route, so the
// daemon is replaced and redialed — but its panes stay its own, because the
// machine on the other end is (as far as catway can tell) the same one.
func TestApplyHostRosterReaddressRedials(t *testing.T) {
	o, _ := editOrchWith(t, remoteHostEntry(t, "devbox"))
	if _, err := o.session.CreateWorkspaceAtOn(t.TempDir(), "devbox"); err != nil {
		t.Fatalf("CreateWorkspaceAtOn: %v", err)
	}
	o.syncDaemon()
	pane := uint32(o.session.ActiveWorkspace().Tabs[0].RootPane)
	before := o.hosts["devbox"]

	moved := o.cfg.Hosts[0]
	moved.Addr = "unix://" + filepath.Join(t.TempDir(), "moved.sock")
	if err := o.applyHostRoster([]config.Host{moved}); err != nil {
		t.Fatalf("applyHostRoster: %v", err)
	}

	if o.hosts["devbox"] == before {
		t.Fatal("a new address did not rebuild the daemon")
	}
	if !before.stopping() {
		t.Fatal("the old daemon was left dialing the old address")
	}
	if got := o.session.PaneHost(layout.PaneID(pane)); got != "devbox" {
		t.Fatalf("stored pane host = %q; a readdressed host keeps its panes", got)
	}
	if got := o.panes[pane].host; got != "devbox" {
		t.Fatalf("runtime pane host = %q; a readdressed host keeps its panes", got)
	}
}

// A config reload can drop two hosts at once, and each must be told about its
// own panes: the orphan list accumulates across the whole edit, so a host handed
// the running total would be asked to close another machine's pane ids — which,
// pane ids being globally unique, is a request to close panes it does not have
// and (worse) a close the other host never receives.
func TestApplyHostRosterDropsTwoHostsIndependently(t *testing.T) {
	o, _ := editOrchWith(t, remoteHostEntry(t, "boxa"), remoteHostEntry(t, "boxb"))
	pdA := newPipeDaemonFor(t, o, "boxa")
	pdB := newPipeDaemonFor(t, o, "boxb")
	paneOn := func(host string) uint32 {
		t.Helper()
		if _, err := o.session.CreateWorkspaceAtOn(t.TempDir(), host); err != nil {
			t.Fatalf("CreateWorkspaceAtOn(%s): %v", host, err)
		}
		return uint32(o.session.ActiveWorkspace().Tabs[0].RootPane)
	}
	paneA, paneB := paneOn("boxa"), paneOn("boxb")
	o.syncDaemon()

	if err := o.applyHostRoster(nil); err != nil { // both gone at once
		t.Fatalf("applyHostRoster: %v", err)
	}

	for _, tc := range []struct {
		pd   *pipeDaemon
		want uint32
	}{{pdA, paneA}, {pdB, paneB}} {
		var closed orchestration.ClosePane
		if err := json.Unmarshal(tc.pd.expect(t, orchestration.MsgClosePane), &closed); err != nil {
			t.Fatalf("close_pane: %v", err)
		}
		if closed.PaneID != tc.want {
			t.Fatalf("host was told to close pane %d, want its own %d", closed.PaneID, tc.want)
		}
	}
	for _, pane := range []uint32{paneA, paneB} {
		if got := o.panes[pane].host; got != localHostID {
			t.Fatalf("pane %d host = %q, want %q", pane, got, localHostID)
		}
	}
}

// An unusable entry anywhere in the roster fails the whole edit: a config
// reload that half-applied would leave the file and the session describing
// different sets of machines, which is the one state nothing else here can
// recover from.
func TestApplyHostRosterIsAllOrNothing(t *testing.T) {
	o, _ := editOrchWith(t, remoteHostEntry(t, "devbox"))
	before := o.hosts["devbox"]

	err := o.applyHostRoster([]config.Host{
		o.cfg.Hosts[0],
		{ID: "bad", Addr: "tcp://example.com:8422"}, // cleartext off the loopback
	})
	if err == nil {
		t.Fatal("a roster with an unusable host was accepted")
	}
	if o.hosts["devbox"] != before || o.hosts["bad"] != nil || len(o.hostOrder) != 2 {
		t.Fatalf("roster changed: %v", hostIDs(o))
	}
	if before.stopping() {
		t.Fatal("a failed edit stopped a healthy host")
	}
}
