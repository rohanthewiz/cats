//go:build ghostty

package main

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/config"
	"github.com/rohanthewiz/cats/internal/orchestration"
	"github.com/rohanthewiz/cats/internal/terminal"
)

// daemon manages the orchestrator's connection to one cathost: dial +
// hello/welcome, reconciling that host's surviving panes against the model,
// then pumping events until the connection drops — and redialing. All state
// that the pump touches beyond the raw socket lives in orch and is reached by
// posting closures onto the orchestrator loop (never a lock).
//
// One orch holds a daemon per configured host (orch.hosts), each with its own
// dial loop and its own slice of the pane set. Everything host-scoped hangs off
// the daemon's id: reconcile only judges panes whose runtime says they are
// this host's, and a disconnect only fails the requests and waiters belonging
// to it, so one machine going away leaves the others streaming.
type daemon struct {
	o *orch
	// id is the stable host id used across the model, the runtimes, and (later)
	// the wire ("local" for the always-present default host). label is the
	// human-facing name that appears in log lines and error toasts; kind names
	// the transport ("unix", "tcp" or "tls").
	id    string
	label string
	kind  string
	// socket is the dial target (a unix path, or host:port for tcp/tls), kept
	// for logging and for tests that build a daemon directly; dial is the
	// transport-agnostic way to open a fresh connection, which is why adding
	// tcp and tls meant adding two dialers and no branch in run().
	socket string
	dial   func() (net.Conn, error)
	// spec is the config entry this daemon was built from. It is kept whole so
	// the roster diff (applyHostRoster) can ask the only question that matters
	// when a config is re-read or edited live: is this still the same host, or
	// does it need redialing? Comparing the entry beats comparing the fields
	// pulled out of it, because a field added to config.Host later is then
	// part of the comparison automatically rather than silently ignored.
	spec config.Host
	// quit is closed by stop() to end the dial loop: a detached host must stop
	// redialing, and must do it without the disconnect toast and pending-flush
	// that a real connection loss earns — the panes it held have already been
	// dealt with by the detach itself.
	quit chan struct{}

	// token/tokenFile authenticate us to a cathost started with -token-file.
	// The file is read at each handshake rather than at startup, so rotating a
	// token takes effect on the next reconnect instead of on a catway restart —
	// which matters because the reconnect is exactly what a rotation causes.
	token     string
	tokenFile string

	mu sync.Mutex // serializes writes; guards conn, peerVersion, lastErr and stopped
	// peerVersion is the negotiated protocol version this cathost's welcome
	// agreed to, 0 while disconnected. It is what decides who resolves a pane's
	// git branch: a v3 daemon does it itself (the cwd is on its filesystem), a
	// v2 one cannot, and a v2 daemon is by definition the local one, so catway
	// keeps reading HEAD for it. See resolvesBranch.
	peerVersion int
	// lastErr is why this host is currently unreachable — the dial or session
	// error the retry loop last saw, cleared on a successful handshake. It is
	// what the roster shows beside a disconnected host: "not connected" alone
	// leaves the operator guessing between a stopped daemon, a wrong path and a
	// forward that died, which are three different fixes.
	lastErr string
	conn    net.Conn
	// stopped marks a detached daemon. Checked by the dial loop at both of its
	// waiting points so a detach that lands mid-dial or mid-session ends the
	// goroutine at the next boundary instead of one backoff later.
	stopped bool
	// features is what the connected cathost said it can answer beyond the base
	// protocol (orchestration.Welcome.Features), reset on every disconnect. A
	// request that is not in here is never sent: an older daemon answers an
	// unknown message type with an error event, which would reach the user as a
	// toast about a message they did not send.
	features map[string]bool
	// latency is the last round trip measured by the ping probe, 0 when unknown
	// (never measured, not connected, or a daemon that cannot answer a ping).
	// pingID/pingAt are the outstanding probe: one at a time, because the point
	// is a current reading rather than a distribution, and a single outstanding
	// request is also what makes a missed answer detectable.
	latency time.Duration
	pingID  uint64
	pingAt  time.Time
}

// unixDialer builds the dial func for a unix-socket cathost — the only
// transport catway speaks today, and the one a `ssh -L` forward turns into a
// genuinely remote host with no protocol work at all.
func unixDialer(path string) func() (net.Conn, error) {
	return func() (net.Conn, error) {
		return net.DialTimeout("unix", path, dialTimeout)
	}
}

// dialTimeout bounds one connection attempt. Short, because failing fast is
// what puts the reason on the host's roster row; the retry loop is what keeps
// trying.
const dialTimeout = 3 * time.Second

// tcpDialer builds the dial func for a cleartext cathost. Cleartext is only
// ever defensible on the loopback (an agent sandbox, a container sharing the
// network namespace), so a non-loopback target is refused here as well as at
// the daemon's bind: catway is the half that would be sending the keystrokes.
func tcpDialer(target string) (func() (net.Conn, error), error) {
	if err := requireLoopbackTarget(target); err != nil {
		return nil, err
	}
	return func() (net.Conn, error) {
		return net.DialTimeout("tcp", target, dialTimeout)
	}, nil
}

// tlsDialer builds the dial func for cathost's native remote transport.
//
// With a fingerprint configured the certificate is pinned: chain verification
// is turned off and the peer's leaf must hash to exactly the value the operator
// copied from the daemon's startup log. That is *stronger* than the default for
// this shape of deployment — a personal fleet of boxes has no CA, so the
// alternative to pinning is not a verified chain, it is a skipped check — and
// it is the only reason self-signed certificates are safe here.
//
// With no fingerprint the standard chain+hostname verification applies, which
// is the right behaviour for an operator who fronted their cathost with a real
// certificate. There is no third option: an unpinned, unverified TLS session
// authenticates nobody, and would hand a shell to whatever answered the port.
func tlsDialer(target, fingerprint string) (func() (net.Conn, error), error) {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return nil, err
	}
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	want := normalizeFingerprint(fingerprint)
	if want != "" {
		if len(want) != sha256.Size*2 {
			return nil, fmt.Errorf("fingerprint %q: want a hex SHA-256 (%d characters, as cathost prints it)", fingerprint, sha256.Size*2)
		}
		cfg.InsecureSkipVerify = true // replaced, not waived, by the pin below
		cfg.VerifyPeerCertificate = pinnedVerifier(want)
	}
	return func() (net.Conn, error) {
		d := &net.Dialer{Timeout: dialTimeout}
		return tls.DialWithDialer(d, "tcp", target, cfg)
	}, nil
}

// pinnedVerifier checks the peer's leaf certificate against a pinned SHA-256 of
// its DER bytes — the same value gwtls.Fingerprint computes and cathost logs.
// rawCerts[0] is the leaf; nothing else in the chain is consulted, because a
// self-signed certificate *is* the chain.
func pinnedVerifier(want string) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("cathost presented no certificate")
		}
		sum := sha256.Sum256(rawCerts[0])
		got := hex.EncodeToString(sum[:])
		if got != want {
			return fmt.Errorf("cathost certificate fingerprint %s does not match the pinned %s", got, want)
		}
		return nil
	}
}

// normalizeFingerprint accepts the value in the shapes it gets pasted in:
// lower/upper hex, and the colon-separated form openssl and certificate viewers
// print. Anything else fails the length check in tlsDialer.
func normalizeFingerprint(fp string) string {
	return strings.ToLower(strings.NewReplacer(":", "", " ", "", "\n", "").Replace(strings.TrimSpace(fp)))
}

// requireLoopbackTarget mirrors cathost's own bind-time refusal, so a cleartext
// address that would leave this machine is rejected when the roster is built —
// at startup, with the reason — rather than on the first keystroke.
func requireLoopbackTarget(target string) error {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("addr tcp://%s: tcp is cleartext, so it may only reach a loopback address — use tls:// for another machine", target)
}

// dialerFor resolves one configured host's transport into a dialer.
//
// unix:// remains the workhorse — it is the local daemon, and, through an
// `ssh -L local.sock:remote.sock` forward, a genuinely remote one with no
// protocol work at all. tcp:// and tls:// are the native transports: the first
// for a loopback-only peer, the second for a cathost on another machine that
// nobody wants to keep an ssh session open for.
func dialerFor(h config.Host) (kind string, dial func() (net.Conn, error), err error) {
	scheme, target, err := h.Transport()
	if err != nil {
		return "", nil, err
	}
	switch scheme {
	case config.HostUnix:
		return scheme, unixDialer(target), nil
	case config.HostTCP:
		dial, err = tcpDialer(target)
		return scheme, dial, err
	case config.HostTLS:
		dial, err = tlsDialer(target, h.Fingerprint)
		return scheme, dial, err
	}
	return "", nil, fmt.Errorf("addr %q: unknown scheme %q", h.Addr, scheme)
}

// newDaemon builds the daemon for one configured host. The label is what error
// toasts and the roster show; for the synthesized local host of a single-host
// session it is never shown at all (see lostMessage), so a session that has no
// hosts: block reads exactly as it did before hosts existed.
func newDaemon(o *orch, h config.Host) (*daemon, error) {
	kind, dial, err := dialerFor(h)
	if err != nil {
		return nil, fmt.Errorf("host %s: %w", h.ID, err)
	}
	_, target, _ := h.Transport() // already parsed by dialerFor
	return &daemon{
		o:         o,
		id:        h.ID,
		label:     h.DisplayLabel(),
		kind:      kind,
		socket:    target,
		dial:      dial,
		spec:      h,
		quit:      make(chan struct{}),
		token:     h.Token,
		tokenFile: h.TokenFile,
	}, nil
}

func (d *daemon) connected() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conn != nil
}

// send writes one command to the daemon. Disconnected sends are dropped —
// reconcile replays the model when the connection comes back. Called from the
// orchestrator loop (which owns the decision to send).
func (d *daemon) send(m any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn == nil {
		return
	}
	if err := orchestration.WriteMessage(d.conn, m); err != nil {
		log.Printf("catway: daemon write: %v", err)
		_ = d.conn.Close() // the pump's read fails and triggers redial
	}
}

func (d *daemon) setConn(c net.Conn) {
	d.mu.Lock()
	d.conn = c
	if c == nil {
		d.peerVersion = 0
		// A latency reading belongs to the connection that produced it. Keeping
		// the last one across a drop would put a confident "2 ms" beside a host
		// that has been unreachable for an hour.
		d.features = nil
		d.latency = 0
		d.pingAt = time.Time{}
	} else {
		d.lastErr = "" // a completed handshake retires whatever kept us out before
	}
	d.mu.Unlock()
}

// setLastErr records why this host is unreachable (dial refused, handshake
// rejected, connection dropped). Written by the dial loop, read by the roster.
func (d *daemon) setLastErr(err error) {
	d.mu.Lock()
	if err == nil {
		d.lastErr = ""
	} else {
		d.lastErr = err.Error()
	}
	d.mu.Unlock()
}

// status is the roster's view of this host: connected, and why not when it
// isn't. One lock for the pair so a caller can never report "disconnected" with
// the error of a link that has since come back.
func (d *daemon) status() (connected bool, lastErr string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conn != nil, d.lastErr
}

// stop ends this daemon for good: the dial loop exits at its next boundary and
// the live connection (if any) is closed so a blocked read returns immediately.
// Idempotent — a second call on an already-stopped daemon is a no-op, which
// matters because a roster diff may stop a host the operator is detaching at
// the same moment.
//
// It deliberately does NOT touch the panes, the pending requests or the
// waiters: the caller (detachHost) has already re-homed or failed all of them,
// and it knows things the daemon does not — chiefly whether this is a detach or
// a redial under a changed address.
func (d *daemon) stop() {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.stopped = true
	close(d.quit)
	conn := d.conn
	d.conn = nil
	d.peerVersion = 0
	d.features = nil
	d.latency = 0
	d.mu.Unlock()
	if conn != nil {
		_ = conn.Close() // unblocks the pump's read; run() then sees stopped
	}
}

// stopping reports whether stop has been called.
func (d *daemon) stopping() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.stopped
}

// setPeerVersion records the version the connected cathost reported.
func (d *daemon) setPeerVersion(v int) {
	d.mu.Lock()
	d.peerVersion = v
	d.mu.Unlock()
}

// setFeatures records what the connected cathost advertised it can answer.
func (d *daemon) setFeatures(list []string) {
	set := make(map[string]bool, len(list))
	for _, f := range list {
		set[f] = true
	}
	d.mu.Lock()
	d.features = set
	d.mu.Unlock()
}

// supports reports whether the connected cathost advertised a capability. False
// while disconnected, which is what every caller wants: a request cannot be
// sent down a link that is not there either.
func (d *daemon) supports(feature string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conn != nil && d.features[feature]
}

// latencyMs is the roster's round-trip figure in milliseconds, 0 for "unknown"
// — never measured, not connected, or a daemon too old to answer a ping.
//
// Rounded to two decimals rather than to whole milliseconds because the local
// host is the common case and a unix socket round trip is a fraction of one:
// whole milliseconds would report every healthy local session as "0 ms", which
// reads as broken instead of as instant.
func (d *daemon) latencyMs() float64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn == nil || d.latency <= 0 {
		return 0
	}
	return math.Round(float64(d.latency.Microseconds())/10) / 100
}

const (
	// hostPingInterval paces the latency probe. The number it draws is a
	// sidebar readout, not a monitoring feed, so this is about keeping it
	// roughly current; the traffic is two ~30-byte frames per host per tick,
	// which is why it does not follow the reader the way the usage poll does.
	hostPingInterval = 20 * time.Second
	// hostPingTimeout is how long an unanswered ping is tolerated before the
	// connection is treated as dead and closed (the dial loop then reconnects).
	//
	// This is the half of the probe that earns its keep. A TCP connection to a
	// machine that slept, lost its route, or was firewalled off stays writable
	// and readable-with-nothing-on-it indefinitely: catway goes on reporting the
	// host as connected, queues keystrokes into it, and waits forever for reads
	// that will never be answered. A ping is the only traffic that is guaranteed
	// to produce a reply, so it is the only thing that can notice.
	//
	// Three intervals: long enough that a daemon busy with a burst of output is
	// not disconnected for being slow, short enough that a dead link is found
	// before a user has typed a paragraph into it.
	hostPingTimeout = 3 * hostPingInterval
)

// pingProbe measures this connection's round trip until it ends. Runs on its
// own goroutine per session (started by session, ended by its conn closing),
// and reaches the loop only to refresh the roster.
//
// It answers two questions with one message. The first is the roster's latency
// figure. The second is whether the link is alive at all — see hostPingTimeout.
func (d *daemon) pingProbe(conn net.Conn) {
	t := time.NewTicker(hostPingInterval)
	defer t.Stop()
	// The first sample is taken immediately: a host that shows no latency until
	// twenty seconds after it connected looks like a host that cannot be
	// measured, and the roster is pushed on connect precisely then.
	for {
		if !d.sendPing(conn) {
			return
		}
		select {
		case <-t.C:
		case <-d.quit:
			return
		}
	}
}

// sendPing writes one probe, first failing the connection if the previous one
// was never answered. Reports false when the probe loop should end — the
// connection has moved on, or this one has just been declared dead.
func (d *daemon) sendPing(conn net.Conn) bool {
	d.mu.Lock()
	if d.conn != conn { // reconnected (or stopped) underneath us: not our session
		d.mu.Unlock()
		return false
	}
	if !d.pingAt.IsZero() && time.Since(d.pingAt) > hostPingTimeout {
		d.mu.Unlock()
		log.Printf("catway: cathost %s answered no ping in %s — closing the connection", d.label, hostPingTimeout)
		// Close rather than mark: the pump is blocked on a read of this socket,
		// and closing is what unblocks it into the ordinary disconnect path —
		// the pending flush, the toast and the redial all happen exactly as they
		// do for a link that dropped, because as far as anything upstream is
		// concerned that is what happened.
		_ = conn.Close()
		return false
	}
	d.pingID++
	id := d.pingID
	d.pingAt = time.Now()
	d.mu.Unlock()
	if err := orchestration.WriteMessage(conn, orchestration.NewPing(id)); err != nil {
		return false // the pump's read is about to fail too; it owns the reconnect
	}
	return true
}

// notePong records the round trip for a pong, ignoring one that does not match
// the outstanding probe (a late answer to a ping already given up on). Returns
// true when the roster's displayed figure should be re-pushed.
func (d *daemon) notePong(id uint64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if id != d.pingID || d.pingAt.IsZero() {
		return false
	}
	prev := d.latency
	d.latency = time.Since(d.pingAt)
	d.pingAt = time.Time{}
	return latencyWorthPushing(prev, d.latency)
}

// latencyWorthPushing decides whether a new sample changes the roster enough to
// re-broadcast it. Every host pushes the whole roster to every browser, so
// pushing on every tick would redraw the sidebar three times a minute per host
// to move a number by a tenth of a millisecond.
//
// The first reading always counts. After that a change has to be both
// noticeable in absolute terms and a real proportion of what was there, which
// is what keeps a jittery 40 ms link from pushing on every sample while still
// reporting the moment it becomes a 400 ms one.
func latencyWorthPushing(prev, next time.Duration) bool {
	if prev <= 0 {
		return next > 0
	}
	delta := next - prev
	if delta < 0 {
		delta = -delta
	}
	return delta > 2*time.Millisecond && delta*5 > prev
}

// resolvesBranch reports whether this host resolves its own panes' git branches
// and reports them as pane_branch events. Only a connected v3+ daemon does; for
// anything else catway falls back to reading HEAD itself, which is correct
// precisely because anything else is the local machine's daemon.
func (d *daemon) resolvesBranch() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conn != nil && d.peerVersion >= 3
}

// authToken is the bearer token to present in the hello: the literal from the
// config, or the current contents of token_file. Read per handshake so a
// rotated file is picked up by the reconnect the rotation itself causes; a
// host with neither returns "", which is what every unix-socket daemon expects.
func (d *daemon) authToken() (string, error) {
	if d.tokenFile == "" {
		return d.token, nil
	}
	b, err := os.ReadFile(d.tokenFile)
	if err != nil {
		return "", fmt.Errorf("read token_file: %w", err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("token_file %s is empty", d.tokenFile)
	}
	return tok, nil
}

// run dials this host forever, with backoff.
func (d *daemon) run() {
	backoff := time.Second
	for {
		if d.stopping() {
			return
		}
		conn, err := d.dial()
		if err != nil {
			log.Printf("catway: cathost dial (%s): %v (retrying in %s)", d.label, err, backoff)
			d.setLastErr(err)
			// The roster carries connectivity, so a host that never came up must
			// still refresh it: the failure is the only news there is about a
			// machine nobody has heard from.
			d.o.post(func() { d.o.broadcastHosts() })
			// The backoff is the loop's longest wait, so it is where a detach
			// would otherwise be felt: a host that is down and being detached
			// must not keep a goroutine (and a roster row) alive for another
			// five seconds after the operator dropped it.
			select {
			case <-time.After(backoff):
			case <-d.quit:
				return
			}
			backoff = min(backoff*2, 5*time.Second)
			continue
		}
		backoff = time.Second
		err = d.session(conn)
		if err != nil {
			log.Printf("catway: cathost session (%s): %v", d.label, err)
		}
		_ = conn.Close()
		if d.stopping() {
			// A detached host is not a lost one. Everything below announces an
			// outage and fails the work that was in flight; the detach has
			// already re-homed the panes and said so, and repeating it here
			// would toast the user about a machine they just removed.
			return
		}
		d.setLastErr(err) // nil (a clean end) clears it; a real error explains the gap
		d.setConn(nil)
		// Only this host's in-flight work is failed: a request or waiter on
		// another host is still perfectly answerable, and failing it here would
		// turn one machine's reboot into a session-wide outage.
		d.o.post(func() {
			d.o.flushPendingFor(d.id, "cathost connection lost")
			d.o.flushWaitersFor(d.id, "cathost connection lost")
			d.o.broadcast(browserproto.NewError(0, d.lostMessage()))
			d.o.broadcastHosts()
		})
	}
}

// lostMessage is the disconnect toast. With one host it is the historical
// wording verbatim (nothing to disambiguate); with several, the host's label
// leads, because "which machine went away" is then the whole content.
func (d *daemon) lostMessage() string {
	if len(d.o.hosts) <= 1 {
		return "cathost connection lost — reconnecting"
	}
	return d.label + ": cathost connection lost — reconnecting"
}

// session runs one daemon connection: handshake, reconcile, event pump.
func (d *daemon) session(conn net.Conn) error {
	tok, err := d.authToken()
	if err != nil {
		return err
	}
	if err := orchestration.WriteMessage(conn, orchestration.NewHelloWithToken(tok)); err != nil {
		return err
	}
	mt, payload, err := orchestration.ReadMessage(conn)
	if err != nil {
		return err
	}
	if mt != orchestration.MsgWelcome {
		return fmt.Errorf("expected welcome, got %q", mt)
	}
	var w orchestration.Welcome
	if err := json.Unmarshal(payload, &w); err != nil {
		return err
	}
	if w.Error != "" {
		return errors.New("daemon rejected hello: " + w.Error)
	}
	// A range, not equality: a cathost on another machine is upgraded on its own
	// schedule, and refusing a peer one version behind would mean an ssh session
	// per box on every release. Everything newer than MinProtocolVersion is
	// additive, so an older daemon simply doesn't do the newer things.
	if orchestration.NegotiateVersion(w.ProtocolVersion) == 0 {
		return fmt.Errorf("daemon speaks protocol %d, this build speaks %d-%d",
			w.ProtocolVersion, orchestration.MinProtocolVersion, orchestration.ProtocolVersion)
	}

	d.setConn(conn)
	d.setPeerVersion(w.ProtocolVersion)
	d.setFeatures(w.Features)
	d.o.post(func() { d.o.broadcastHosts() }) // the roster's dot goes green
	d.reconcile(w.Panes)
	// The probe is per-session and dies with the connection: it holds the conn
	// it was started for and stops the moment d.conn is something else, so a
	// reconnect never ends up with two.
	if d.supports(orchestration.FeaturePing) {
		go d.pingProbe(conn)
	}

	for {
		mt, payload, err := orchestration.ReadMessage(conn)
		if err != nil {
			return err
		}
		d.dispatch(mt, payload)
	}
}

// reconcile syncs this host's surviving pane set to the model: mark survivors
// created, drop the created flag on the rest so syncDaemon respawns them, close
// this host's panes that are outside the model, then re-apply the model and
// resync the visible panes for any attached browser. Runs on the orchestrator
// loop.
//
// Every judgement here is host-scoped. alivePanes is what *this* cathost holds,
// so panes belonging to another host must not be measured against it: they are
// neither survivors to adopt (their own host's reconcile does that) nor
// strays to close. Pane ids are catway-allocated and globally unique, so the
// two hosts' id sets never collide — the only thing needed is the filter.
func (d *daemon) reconcile(alivePanes []uint32) {
	d.o.post(func() {
		o := d.o
		alive := make(map[uint32]bool, len(alivePanes))
		for _, id := range alivePanes {
			alive[id] = true
		}
		model := make(map[uint32]bool)
		for _, id := range o.session.AllPaneIDs() {
			pid := uint32(id)
			if o.paneHostID(pid) != d.id {
				continue // another host's pane; not ours to adopt or close
			}
			model[pid] = true
			rt := o.panes[pid]
			if rt == nil {
				continue // syncDaemon (in applyModel) creates missing runtimes
			}
			rt.created = alive[pid]
			if alive[pid] {
				// An adopted survivor keeps its live PTY, real scrollback, and
				// real cwd — the restored seeds would be stale duplicates, and
				// resuming would double-launch an agent that never died. Its
				// saved session ref goes live on the runtime instead: the agent
				// is (presumably) still running that conversation, and the
				// normal lifecycle rules (detection conflict, release, exit)
				// now own clearing it.
				delete(o.seeds, pid)
				delete(o.restoredCwds, pid)
				delete(o.resumePlans, pid)
				if s, ok := o.restoredAgents[pid]; ok && rt.agentSession == nil {
					rt.agentSession = &agentSessionRef{source: s.Source, agent: s.Agent, kind: s.Kind, value: s.Value}
					delete(o.restoredAgents, pid)
				}
			}
		}
		for _, id := range alivePanes {
			if !model[id] {
				d.send(orchestration.NewClosePane(id))
			}
		}
		o.applyModel()
		for _, id := range o.session.VisiblePaneIDs() {
			o.resyncPane(uint32(id))
		}
	})
}

// dispatch translates one daemon β event into browser messages and model
// updates, posted onto the orchestrator loop. Chrome is cached on the pane
// runtime regardless of visibility (§8), but only forwarded to browsers when
// the pane is in the current viewport; the agents rollup is always global.
func (d *daemon) dispatch(mt orchestration.MessageType, payload []byte) {
	o := d.o
	switch mt {
	case orchestration.MsgPong:
		// Timed off the pump goroutine, where the frames are read, so the figure
		// includes whatever queue the events are actually coming through. The
		// loop is reached only when the number moved enough to redraw.
		var ev orchestration.Pong
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		if d.notePong(ev.ID) {
			o.post(func() { o.broadcastHosts() })
		}
	case orchestration.MsgPaneFrame:
		var ev orchestration.PaneFrame
		if err := json.Unmarshal(payload, &ev); err != nil || ev.Frame == nil {
			return
		}
		o.post(func() {
			rt := o.panes[ev.PaneID]
			if rt == nil {
				return
			}
			rt.histDirty = true // output since the last history capture (WS3)
			if !o.visible[ev.PaneID] {
				return
			}
			for c := range o.conns {
				msg := c.translator(ev.PaneID).Translate(ev.Frame)
				if b, err := browserproto.Marshal(msg); err == nil {
					o.enqueue(c, b)
				}
			}
		})

	case orchestration.MsgPaneOutput:
		var ev orchestration.PaneOutput
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		// Raw output stream for pane.wait_for_output: matched against the pane's
		// waiters (loop goroutine). Only streamed while a waiter is active.
		o.post(func() { o.onPaneOutput(ev.PaneID, ev.Data) })

	case orchestration.MsgPaneModes:
		var ev orchestration.PaneModes
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		o.post(func() { o.applyPaneModes(ev) })

	case orchestration.MsgPaneTitle:
		var ev orchestration.PaneTitle
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		o.post(func() {
			rt := o.panes[ev.PaneID]
			if rt == nil {
				return
			}
			rt.title = ev.Title
			if o.visible[ev.PaneID] {
				o.broadcast(browserproto.NewPaneTitle(ev.PaneID, o.effectiveTitle(ev.PaneID)))
			}
			o.broadcastTitle()
			o.refreshTabNames() // an auto-named tab may be riding this title
			o.emitEvent(app.EventPaneTitle, ev.PaneID, app.PaneTitleEvent{Pane: ev.PaneID, Title: ev.Title})
		})

	case orchestration.MsgPaneCwd:
		var ev orchestration.PaneCwd
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		o.post(func() {
			rt := o.panes[ev.PaneID]
			if rt == nil {
				return
			}
			rt.cwd = ev.Cwd
			if o.visible[ev.PaneID] {
				o.broadcast(browserproto.NewPaneCwd(ev.PaneID, ev.Cwd))
			}
			// The cwd is what the branch is resolved against, so a move is the
			// one moment the header's branch is knowably wrong. The sweep would
			// catch it within seconds; this makes the pair land together.
			o.refreshPaneBranch(rt)
			o.refreshTabNames() // cwd basenames feed the agent/shell auto-name rungs
			o.emitEvent(app.EventPaneCwd, ev.PaneID, app.PaneCwdEvent{Pane: ev.PaneID, Cwd: ev.Cwd})
			o.saveSoon() // pane cwds ride the session file (restore re-spawns there)
		})

	case orchestration.MsgPaneBranch:
		var ev orchestration.PaneBranch
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		// The host resolved this against its own filesystem, which for a remote
		// pane is the only machine where the answer exists at all.
		o.post(func() { o.applyPaneBranch(ev.PaneID, ev.Branch) })

	case orchestration.MsgPaneAgent:
		var ev orchestration.PaneAgent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		o.post(func() { o.onPaneAgent(ev) })

	case orchestration.MsgPaneClipboard:
		var ev orchestration.PaneClipboard
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		o.post(func() { o.broadcast(browserproto.NewClipboard(ev.Data)) })

	case orchestration.MsgPaneExited:
		var ev orchestration.PaneExited
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		o.post(func() {
			rt := o.panes[ev.PaneID]
			if rt == nil {
				return
			}
			code := ev.ExitCode
			rt.exited = &code
			if o.visible[ev.PaneID] {
				o.broadcast(browserproto.NewPaneExited(ev.PaneID, ev.ExitCode))
			}
			o.emitEvent(app.EventPaneExited, ev.PaneID, app.PaneExitedEvent{Pane: ev.PaneID, ExitCode: ev.ExitCode})
			o.resolveWaitersOnExit(ev.PaneID) // no more output will come
			o.clearHookOnExit(rt)             // a late hook packet must not resurrect a dead agent
		})

	case orchestration.MsgPaneSelection:
		var ev orchestration.PaneSelection
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		o.post(func() {
			o.resolvePending(reqKey{ev.PaneID, reqSelection}, browserproto.ReadResult{Text: ev.Text})
		})

	case orchestration.MsgPaneText:
		var ev orchestration.PaneText
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		o.post(func() {
			o.resolvePending(reqKey{ev.PaneID, reqText}, browserproto.CaptureResult{Text: ev.Text})
		})

	case orchestration.MsgError:
		var ev orchestration.Error
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		log.Printf("catway: daemon error (pane %d): %s", ev.PaneID, ev.Message)
		o.post(func() { o.broadcast(browserproto.NewError(ev.PaneID, ev.Message)) })
	}
}

// applyPaneModes folds a β pane_modes event into the pane's runtime: the
// encoder's mode state, the browser mirror, and the focus-reporting seed.
// Loop-goroutine only.
func (o *orch) applyPaneModes(ev orchestration.PaneModes) {
	rt := o.panes[ev.PaneID]
	if rt == nil {
		return
	}
	wasReporting := rt.modes.FocusReporting
	rt.modes = inputModesFrom(ev)
	rt.enc.SetModes(rt.modes)
	// A program that just enabled focus reporting (DEC 1004) has heard nothing
	// yet and assumes it is focused — for a TUI launched while the window sat
	// in the background, that assumption is exactly the blinking caret the
	// mode exists to stop. Answer the enable with the current state (the way
	// tmux seeds it) so the program starts converged instead of waiting for
	// the next transition.
	if !wasReporting && rt.modes.FocusReporting && rt.exited == nil {
		rt.appFocused = o.paneSeen(ev.PaneID)
		if b := rt.enc.Focus(rt.appFocused); len(b) > 0 {
			o.hostOf(rt).send(orchestration.NewInput(rt.id, b))
		}
	}
	if o.visible[ev.PaneID] {
		o.broadcast(browserproto.ModesFrom(ev))
	}
}

// inputModesFrom rehydrates the β pane_modes mirror into the emulator-side
// struct the input encoder consumes.
func inputModesFrom(m orchestration.PaneModes) terminal.InputModes {
	return terminal.InputModes{
		AlternateScreen:      m.AlternateScreen,
		ApplicationCursor:    m.ApplicationCursor,
		BracketedPaste:       m.BracketedPaste,
		FocusReporting:       m.FocusReporting,
		MouseMode:            terminal.MouseMode(m.MouseMode),
		MouseEncoding:        terminal.MouseEncoding(m.MouseEncoding),
		MouseAlternateScroll: m.MouseAlternateScroll,
		SynchronizedOutput:   m.SynchronizedOutput,
		KittyKeyboardFlags:   m.KittyKeyboardFlags,
		ModifyOtherKeys:      m.ModifyOtherKeys,
	}
}
