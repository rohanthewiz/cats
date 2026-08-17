//go:build ghostty

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
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
	// the transport ("unix" today, "tcp"/"tls" from Phase 4).
	id    string
	label string
	kind  string
	// socket is the unix path this host dials, kept for logging and for tests
	// that build a daemon directly; dial is the transport-agnostic way to open
	// a fresh connection, so adding tcp/tls means adding a dialer, not a branch
	// in run().
	socket string
	dial   func() (net.Conn, error)

	mu sync.Mutex // serializes writes; guards conn, peerVersion and lastErr
	// peerVersion is the protocol version the connected cathost reported in its
	// welcome, 0 while disconnected. Nothing branches on it yet (the handshake
	// still demands exact equality); it exists so the version-range negotiation
	// of Phase 4 has somewhere to record the answer.
	peerVersion int
	// lastErr is why this host is currently unreachable — the dial or session
	// error the retry loop last saw, cleared on a successful handshake. It is
	// what the roster shows beside a disconnected host: "not connected" alone
	// leaves the operator guessing between a stopped daemon, a wrong path and a
	// forward that died, which are three different fixes.
	lastErr string
	conn    net.Conn
}

// unixDialer builds the dial func for a unix-socket cathost — the only
// transport catway speaks today, and the one a `ssh -L` forward turns into a
// genuinely remote host with no protocol work at all.
func unixDialer(path string) func() (net.Conn, error) {
	return func() (net.Conn, error) {
		return net.DialTimeout("unix", path, 3*time.Second)
	}
}

// dialerFor resolves one configured host's transport into a dialer. Only unix
// sockets are dialable today — which is not the limitation it sounds like, since
// `ssh -L local.sock:remote.sock` turns one into a genuinely remote cathost —
// and tcp/tls are named here so a config that reaches for them fails at startup
// with the reason, rather than retrying a dial that can never work.
func dialerFor(h config.Host) (kind string, dial func() (net.Conn, error), err error) {
	scheme, target, err := h.Transport()
	if err != nil {
		return "", nil, err
	}
	switch scheme {
	case config.HostUnix:
		return scheme, unixDialer(target), nil
	case config.HostTCP, config.HostTLS:
		return "", nil, fmt.Errorf("addr %q: the %s transport is not implemented yet — use unix:// (an `ssh -L` forward reaches a remote cathost today)", h.Addr, scheme)
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
		o:      o,
		id:     h.ID,
		label:  h.DisplayLabel(),
		kind:   kind,
		socket: target,
		dial:   dial,
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

// setPeerVersion records the version the connected cathost reported.
func (d *daemon) setPeerVersion(v int) {
	d.mu.Lock()
	d.peerVersion = v
	d.mu.Unlock()
}

// run dials this host forever, with backoff.
func (d *daemon) run() {
	backoff := time.Second
	for {
		conn, err := d.dial()
		if err != nil {
			log.Printf("catway: cathost dial (%s): %v (retrying in %s)", d.label, err, backoff)
			d.setLastErr(err)
			// The roster carries connectivity, so a host that never came up must
			// still refresh it: the failure is the only news there is about a
			// machine nobody has heard from.
			d.o.post(func() { d.o.broadcastHosts() })
			time.Sleep(backoff)
			backoff = min(backoff*2, 5*time.Second)
			continue
		}
		backoff = time.Second
		err = d.session(conn)
		if err != nil {
			log.Printf("catway: cathost session (%s): %v", d.label, err)
		}
		_ = conn.Close()
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
	if err := orchestration.WriteMessage(conn, orchestration.NewHello()); err != nil {
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
	if w.ProtocolVersion != orchestration.ProtocolVersion {
		return fmt.Errorf("daemon speaks protocol %d, want %d", w.ProtocolVersion, orchestration.ProtocolVersion)
	}

	d.setConn(conn)
	d.setPeerVersion(w.ProtocolVersion)
	d.o.post(func() { d.o.broadcastHosts() }) // the roster's dot goes green
	d.reconcile(w.Panes)

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
