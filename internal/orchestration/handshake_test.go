//go:build ghostty

package orchestration

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The v3 handshake is the first one that judges its client: before a cathost
// could be reached over a port, the socket's file permissions were the whole
// access-control story and the hello payload was ignored outright. These tests
// hold the three answers a client can get — accepted, accepted at an older
// version, refused with a reason — and the one property a refusal must have:
// the client is told *why* before the connection goes, because a silent close
// is indistinguishable from a daemon that never started.

// dialHost starts a Host over a pipe and returns the client end. Unlike
// startTestHost it performs no handshake, since the handshake is the subject.
func dialHost(t *testing.T, configure func(*Host)) net.Conn {
	t.Helper()
	serverEnd, clientEnd := net.Pipe()

	h := NewHost()
	h.FlushInterval = 5 * time.Millisecond
	if configure != nil {
		configure(h)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go h.Serve(ctx, serverEnd)
	t.Cleanup(func() {
		cancel()
		clientEnd.Close()
	})
	_ = clientEnd.SetDeadline(time.Now().Add(15 * time.Second))
	return clientEnd
}

// handshake sends one hello and returns the welcome it is answered with.
func handshake(t *testing.T, c net.Conn, hello Hello) Welcome {
	t.Helper()
	if err := WriteMessage(c, hello); err != nil {
		t.Fatalf("send hello: %v", err)
	}
	typ, payload := readEvent(t, c)
	if typ != MsgWelcome {
		t.Fatalf("first event = %q, want welcome", typ)
	}
	var w Welcome
	if err := json.Unmarshal(payload, &w); err != nil {
		t.Fatalf("decode welcome: %v", err)
	}
	return w
}

func TestHandshakeAcceptsCurrentVersion(t *testing.T) {
	c := dialHost(t, nil)
	w := handshake(t, c, NewHello())
	if w.Error != "" {
		t.Fatalf("welcome carried an error: %q", w.Error)
	}
	if w.ProtocolVersion != ProtocolVersion {
		t.Fatalf("welcome version = %d, want %d", w.ProtocolVersion, ProtocolVersion)
	}
}

// A v2 orchestrator — a catway that has not been upgraded, which over a network
// is the normal state of affairs for a while — is served, and is answered with
// the version it asked for so its own equality check passes.
func TestHandshakeAcceptsOlderPeerAndEchoesItsVersion(t *testing.T) {
	c := dialHost(t, nil)
	w := handshake(t, c, Hello{Type: MsgHello, ProtocolVersion: MinProtocolVersion})
	if w.Error != "" {
		t.Fatalf("a v%d client should be served: %q", MinProtocolVersion, w.Error)
	}
	if w.ProtocolVersion != MinProtocolVersion {
		t.Fatalf("welcome version = %d, want the negotiated %d", w.ProtocolVersion, MinProtocolVersion)
	}
	// Still a working session, not a courtesy welcome.
	cp := NewCreatePane(1, 20, 4)
	cp.Command = "/bin/sh"
	cp.Args = []string{"-c", "printf OLDPEER"}
	if err := WriteMessage(c, cp); err != nil {
		t.Fatalf("create_pane: %v", err)
	}
	waitFor(t, c, MsgPaneExited)
}

// A version this build cannot serve, and a missing or wrong token, both end the
// session — after a welcome that names the reason.
func TestHandshakeRejections(t *testing.T) {
	for name, tc := range map[string]struct {
		configure func(*Host)
		hello     Hello
		want      string
	}{
		"version below the floor": {
			hello: Hello{Type: MsgHello, ProtocolVersion: 1},
			want:  "protocol version 1 unsupported",
		},
		"version from the future": {
			hello: Hello{Type: MsgHello, ProtocolVersion: ProtocolVersion + 1},
			want:  "unsupported",
		},
		"missing token": {
			configure: func(h *Host) { h.RequireToken = "s3cret" },
			hello:     NewHello(),
			want:      "authentication failed",
		},
		"wrong token": {
			configure: func(h *Host) { h.RequireToken = "s3cret" },
			hello:     NewHelloWithToken("s3cret-but-not-quite"),
			want:      "authentication failed",
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := dialHost(t, tc.configure)
			w := handshake(t, c, tc.hello)
			if !strings.Contains(w.Error, tc.want) {
				t.Fatalf("welcome error = %q, want it to mention %q", w.Error, tc.want)
			}
			if len(w.Panes) != 0 {
				t.Fatalf("a refused client learned about panes: %v", w.Panes)
			}
			// The reason arrives *before* the hang-up: that ordering is the
			// entire point of queueing the welcome ahead of the sentinel.
			if _, _, err := ReadMessage(c); err == nil {
				t.Fatal("connection stayed open after a refused handshake")
			}
		})
	}
}

func TestHandshakeAcceptsCorrectToken(t *testing.T) {
	c := dialHost(t, func(h *Host) { h.RequireToken = "s3cret" })
	w := handshake(t, c, NewHelloWithToken("s3cret"))
	if w.Error != "" {
		t.Fatalf("welcome carried an error: %q", w.Error)
	}
}

// A cwd chosen on the orchestrator's machine may simply not exist on this one —
// the routine outcome of splitting a pane or restoring a session across hosts.
// Before v3 that was a dead pane; now it is a live pane in $HOME plus an error
// event saying where it actually landed.
func TestCreatePaneFallsBackWhenCwdIsMissing(t *testing.T) {
	c := startTestHost(t)

	cp := NewCreatePane(11, 40, 5)
	cp.Cwd = filepath.Join(t.TempDir(), "only-on-the-other-machine")
	cp.Command = "/bin/sh"
	cp.Args = []string{"-c", "pwd"}
	if err := WriteMessage(c, cp); err != nil {
		t.Fatalf("create_pane: %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory to fall back to")
	}
	var sawCwd, sawNote bool
	deadline := time.Now().Add(10 * time.Second)
	for !(sawCwd && sawNote) && time.Now().Before(deadline) {
		typ, payload := readEvent(t, c)
		switch typ {
		case MsgPaneCwd:
			var ev PaneCwd
			if err := json.Unmarshal(payload, &ev); err != nil {
				t.Fatal(err)
			}
			if ev.Cwd != home {
				t.Fatalf("pane spawned in %q, want the home fallback %q", ev.Cwd, home)
			}
			sawCwd = true
		case MsgError:
			var ev Error
			if err := json.Unmarshal(payload, &ev); err != nil {
				t.Fatal(err)
			}
			// Attributed to the pane, and it names both directories: a pane
			// that silently started somewhere else is how the next command
			// runs in the wrong tree.
			if ev.PaneID != 11 || !strings.Contains(ev.Message, "not a directory on this host") {
				t.Fatalf("error event = %+v", ev)
			}
			sawNote = true
		case MsgPaneExited:
			if !sawCwd || !sawNote {
				t.Fatalf("pane exited before both the cwd and the note arrived (cwd=%v note=%v)", sawCwd, sawNote)
			}
		}
	}
	if !sawCwd || !sawNote {
		t.Fatalf("missing events: cwd=%v note=%v", sawCwd, sawNote)
	}
}

// A cwd that does exist is used verbatim and produces no note — the fallback
// must not become a thing that fires on the ordinary path.
func TestCreatePaneKeepsAnExistingCwd(t *testing.T) {
	c := startTestHost(t)

	dir := t.TempDir()
	cp := NewCreatePane(12, 40, 5)
	cp.Cwd = dir
	cp.Command = "/bin/sh"
	cp.Args = []string{"-c", "sleep 0.2"}
	if err := WriteMessage(c, cp); err != nil {
		t.Fatalf("create_pane: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		typ, payload := readEvent(t, c)
		switch typ {
		case MsgPaneCwd:
			var ev PaneCwd
			if err := json.Unmarshal(payload, &ev); err != nil {
				t.Fatal(err)
			}
			if ev.Cwd != dir {
				t.Fatalf("pane cwd = %q, want the requested %q", ev.Cwd, dir)
			}
		case MsgError:
			t.Fatalf("an existing cwd produced an error event: %s", payload)
		case MsgPaneExited:
			return
		}
	}
	t.Fatal("pane never exited")
}

// The branch is resolved by the daemon from v3 on, because the pane's cwd is a
// path on the daemon's filesystem — for a pane on another machine that is the
// only place the answer exists.
func TestHostReportsPaneBranch(t *testing.T) {
	c := startTestHost(t)

	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/remote-dream\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cp := NewCreatePane(13, 40, 5)
	cp.Cwd = repo
	cp.Command = "/bin/sh"
	cp.Args = []string{"-c", "sleep 2"}
	if err := WriteMessage(c, cp); err != nil {
		t.Fatalf("create_pane: %v", err)
	}

	payload := waitFor(t, c, MsgPaneBranch)
	var ev PaneBranch
	if err := json.Unmarshal(payload, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.PaneID != 13 || ev.Branch != "remote-dream" {
		t.Fatalf("pane_branch = %+v, want pane 13 on remote-dream", ev)
	}
}

// waitFor reads until an event of type want arrives and returns its payload.
func waitFor(t *testing.T, c net.Conn, want MessageType) []byte {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		typ, payload := readEvent(t, c)
		if typ == want {
			return payload
		}
	}
	t.Fatalf("timed out waiting for %q", want)
	return nil
}

// Capabilities: the welcome advertises what this daemon can answer beyond the
// negotiated version's base set, and the one capability that exists today
// actually works. Both halves matter — a feature list a client believes and a
// daemon that then errors on the request is worse than no list at all.
func TestWelcomeAdvertisesFeaturesAndAnswersPing(t *testing.T) {
	c := dialHost(t, nil)
	w := handshake(t, c, NewHello())
	if w.Error != "" {
		t.Fatalf("welcome carried an error: %q", w.Error)
	}
	if !slices.Contains(w.Features, FeaturePing) {
		t.Fatalf("welcome features = %v, want %q among them", w.Features, FeaturePing)
	}

	if err := WriteMessage(c, NewPing(77)); err != nil {
		t.Fatalf("send ping: %v", err)
	}
	typ, payload := readEvent(t, c)
	if typ != MsgPong {
		t.Fatalf("answer to a ping = %q, want pong", typ)
	}
	var p Pong
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatalf("decode pong: %v", err)
	}
	// The id is echoed so a client can tell this answer from a late one to a
	// probe it already gave up on — the difference between a measurement and a
	// number.
	if p.ID != 77 {
		t.Fatalf("pong id = %d, want the ping's 77", p.ID)
	}
}

// An older peer is still served the daemon's own capabilities: the feature list
// says what this process can do, which does not shrink because the client asked
// for an older version. A client that predates the field ignores it.
func TestFeaturesAreAdvertisedToAnOlderPeerToo(t *testing.T) {
	c := dialHost(t, nil)
	w := handshake(t, c, Hello{Type: MsgHello, ProtocolVersion: MinProtocolVersion})
	if !slices.Contains(w.Features, FeaturePing) {
		t.Fatalf("welcome features = %v, want %q among them", w.Features, FeaturePing)
	}
}

// A refused handshake advertises nothing. There is no session to use a
// capability in, and listing one would invite a client to keep talking to a
// connection that is already ending.
func TestRejectedHandshakeAdvertisesNoFeatures(t *testing.T) {
	c := dialHost(t, func(h *Host) { h.RequireToken = "secret" })
	w := handshake(t, c, NewHello())
	if w.Error == "" {
		t.Fatal("a tokenless hello should have been refused")
	}
	if len(w.Features) != 0 {
		t.Fatalf("rejection features = %v, want none", w.Features)
	}
}

// Host stats: a subscription, not a request/reply, because a CPU figure is a
// rate and does not exist until something has been measuring. These hold the
// three things that makes true — nothing is sampled until asked, the first
// reading arrives immediately with the rows that CAN be read now, and asking
// for nothing stops it.
func TestHostStatsSubscription(t *testing.T) {
	c := dialHost(t, nil)
	w := handshake(t, c, NewHello())
	if !slices.Contains(w.Features, FeatureHostStats) {
		t.Fatalf("welcome features = %v, want %q among them", w.Features, FeatureHostStats)
	}

	if err := WriteMessage(c, NewRequestHostStats(time.Second)); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	typ, payload := readEvent(t, c)
	if typ != MsgHostStats {
		t.Fatalf("first event after subscribing = %q, want host_stats", typ)
	}
	var st HostStats
	if err := json.Unmarshal(payload, &st); err != nil {
		t.Fatalf("decode host_stats: %v", err)
	}
	// The first reading is deliberately sent before the CPU sampler has
	// anything: memory and disk are readable now, and a section that stays blank
	// until the first tick reads as a machine that cannot be measured. On a host
	// where neither is readable an empty set is the honest answer, so only the
	// shape is asserted.
	for _, r := range st.Rows {
		if !r.Known() {
			t.Errorf("row %q was reported with no reading", r.Name)
		}
		if r.Name == "" {
			t.Error("a row arrived with no name; the sidebar keys its scales on it")
		}
	}
}

// Cancelling stops the sampler, which on darwin is the whole cost of the
// feature: a cathost nobody is watching must not keep an iostat alive.
func TestHostStatsUnsubscribeStopsTheSampler(t *testing.T) {
	h := NewHost()
	h.FlushInterval = 5 * time.Millisecond
	h.requestHostStats(NewRequestHostStats(time.Second))
	h.statsMu.Lock()
	sub := h.stats
	h.statsMu.Unlock()
	if sub == nil {
		t.Fatal("subscribing installed nothing")
	}

	h.requestHostStats(NewRequestHostStats(0))
	h.statsMu.Lock()
	still := h.stats
	h.statsMu.Unlock()
	if still != nil {
		t.Fatal("an interval of 0 must cancel the subscription")
	}
	select {
	case <-sub.stop:
	default:
		t.Fatal("the pump was not told to stop")
	}
	// The sampler goes with it: it is the thing that costs a subprocess.
	if !sub.sampler.Stopped() {
		t.Fatal("the sampler is still running after the subscription ended")
	}
}

// A second request re-paces the first rather than adding to it. One daemon
// serves one client, so two live pumps would be two readings per interval of
// the same machine.
func TestHostStatsResubscribeReplaces(t *testing.T) {
	h := NewHost()
	h.FlushInterval = 5 * time.Millisecond
	h.requestHostStats(NewRequestHostStats(time.Second))
	h.statsMu.Lock()
	first := h.stats
	h.statsMu.Unlock()

	h.requestHostStats(NewRequestHostStats(2 * time.Second))
	h.statsMu.Lock()
	second := h.stats
	h.statsMu.Unlock()
	defer h.stopHostStats()

	if first == second {
		t.Fatal("re-pacing kept the old subscription")
	}
	select {
	case <-first.stop:
	default:
		t.Fatal("the superseded pump was left running")
	}
}

// Listing a directory is answered by the machine that owns the paths, which is
// the whole reason this request exists: "~" is the daemon's user's home and "."
// is a directory only its kernel can resolve, so expanding either on the client
// would complete a path against the wrong filesystem.
func TestListDirAnswersFromTheDaemonsFilesystem(t *testing.T) {
	c := dialHost(t, nil)
	w := handshake(t, c, NewHello())
	if !slices.Contains(w.Features, FeatureListDir) {
		t.Fatalf("welcome features = %v, want %q among them", w.Features, FeatureListDir)
	}

	root := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// A relative path against a base, so the resolution is the daemon's too.
	if err := WriteMessage(c, NewRequestListDir(42, ".", root, false, nil)); err != nil {
		t.Fatalf("request: %v", err)
	}
	typ, payload := readEvent(t, c)
	if typ != MsgDirListing {
		t.Fatalf("answer = %q, want dir_listing", typ)
	}
	var dl DirListing
	if err := json.Unmarshal(payload, &dl); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// The pane id is a correlation handle: the client matches replies to
	// requests per pane, so a reply that lost it could not be matched at all.
	if dl.PaneID != 42 {
		t.Errorf("pane id = %d, want the request's 42", dl.PaneID)
	}
	if !dl.Listing.Exists {
		t.Fatalf("listing = %+v, want the temp dir to exist", dl.Listing)
	}
	if len(dl.Listing.Dirs) != 2 || dl.Listing.Dirs[0] != "alpha" || dl.Listing.Dirs[1] != "beta" {
		t.Fatalf("dirs = %v, want [alpha beta]", dl.Listing.Dirs)
	}
	if dl.Listing.Home == "" {
		t.Error("no home in the listing; a client cannot shorten ~ without the daemon's")
	}
}

// A path half-way through being typed is the common case, not a failure: the
// listing says it does not exist and why, and the session carries on.
func TestListDirReportsAMissingPathWithoutFailing(t *testing.T) {
	c := dialHost(t, nil)
	handshake(t, c, NewHello())

	missing := filepath.Join(t.TempDir(), "nope")
	if err := WriteMessage(c, NewRequestListDir(1, missing, "", false, nil)); err != nil {
		t.Fatalf("request: %v", err)
	}
	typ, payload := readEvent(t, c)
	if typ != MsgDirListing {
		t.Fatalf("answer = %q, want dir_listing (an error event would end the picker)", typ)
	}
	var dl DirListing
	if err := json.Unmarshal(payload, &dl); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if dl.Listing.Exists || !strings.Contains(dl.Listing.Error, "no such directory") {
		t.Fatalf("listing = %+v, want exists=false with a reason", dl.Listing)
	}
}
