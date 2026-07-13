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

	"github.com/rohanthewiz/herdr-web/internal/browserproto"
	"github.com/rohanthewiz/herdr-web/internal/orchestration"
	"github.com/rohanthewiz/herdr-web/internal/terminal"
)

// daemon manages the gateway's single connection to the termhost daemon:
// dial + hello/welcome, reconciling the daemon's surviving panes against the
// model, then pumping events until the connection drops — and redialing.
// The daemon accepts one client at a time; gateway2 stays attached for life.
type daemon struct {
	g      *gateway
	socket string

	mu   sync.Mutex // serializes writes; guards conn
	conn net.Conn
}

func (d *daemon) connected() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conn != nil
}

// send writes one command to the daemon. Disconnected sends are dropped —
// reconcile replays the model when the connection comes back.
func (d *daemon) send(m any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.conn == nil {
		return
	}
	if err := orchestration.WriteMessage(d.conn, m); err != nil {
		log.Printf("gateway2: daemon write: %v", err)
		_ = d.conn.Close() // the pump's read fails and triggers redial
	}
}

func (d *daemon) setConn(c net.Conn) {
	d.mu.Lock()
	d.conn = c
	d.mu.Unlock()
}

// run dials the daemon forever, with backoff. Each established session
// reconciles panes and pumps events until it fails.
func (d *daemon) run() {
	backoff := time.Second
	for {
		conn, err := net.DialTimeout("unix", d.socket, 3*time.Second)
		if err != nil {
			log.Printf("gateway2: termhost dial: %v (retrying in %s)", err, backoff)
			time.Sleep(backoff)
			backoff = min(backoff*2, 5*time.Second)
			continue
		}
		backoff = time.Second
		if err := d.session(conn); err != nil {
			log.Printf("gateway2: termhost session: %v", err)
		}
		_ = conn.Close()
		d.setConn(nil)
		d.g.broadcast(browserproto.NewError(0, "termhost connection lost — reconnecting"))
	}
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
	d.reconcile(w.Panes)

	for {
		mt, payload, err := orchestration.ReadMessage(conn)
		if err != nil {
			return err
		}
		d.dispatch(mt, payload)
	}
}

// reconcile syncs the daemon's pane set to the model: surviving model panes
// get a resync (full frame + chrome replay) and a resize to the model grid,
// missing ones are created, and daemon panes outside the model are closed.
func (d *daemon) reconcile(alivePanes []uint32) {
	g := d.g
	g.mu.Lock()
	defer g.mu.Unlock()

	alive := make(map[uint32]bool, len(alivePanes))
	for _, id := range alivePanes {
		alive[id] = true
	}
	for id, p := range g.panes {
		if alive[id] {
			p.created = true
			r := orchestration.NewResize(id, p.cols, p.rows)
			r.CellWidthPx, r.CellHeightPx = g.cellW, g.cellH
			d.send(r)
			d.send(orchestration.NewRequestResync(id))
			continue
		}
		// The daemon doesn't have this pane (fresh or restarted daemon):
		// recreate it and mark it created so later resizes go through as resizes.
		p.created = true
		cp := orchestration.NewCreatePane(id, p.cols, p.rows)
		cp.Cwd = g.cwd
		cp.CellWidthPx, cp.CellHeightPx = g.cellW, g.cellH
		d.send(cp)
		p.exited = nil
	}
	for _, id := range alivePanes {
		if g.panes[id] == nil {
			d.send(orchestration.NewClosePane(id))
		}
	}
	g.broadcastLocked(g.layoutMsgLocked())
}

// dispatch translates one daemon event into browser messages (and model
// updates). It runs on the single pump goroutine, so per-connection frame
// translator state and enqueue order are safe.
func (d *daemon) dispatch(mt orchestration.MessageType, payload []byte) {
	g := d.g
	switch mt {
	case orchestration.MsgPaneFrame:
		var ev orchestration.PaneFrame
		if err := json.Unmarshal(payload, &ev); err != nil || ev.Frame == nil {
			return
		}
		g.mu.Lock()
		if g.panes[ev.PaneID] != nil {
			for c := range g.conns {
				msg := c.translator(ev.PaneID).Translate(ev.Frame)
				if b, err := browserproto.Marshal(msg); err == nil {
					c.enqueue(b)
				}
			}
		}
		g.mu.Unlock()

	case orchestration.MsgPaneModes:
		var ev orchestration.PaneModes
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		g.mu.Lock()
		if p := g.panes[ev.PaneID]; p != nil {
			p.modes = inputModesFrom(ev)
			p.enc.SetModes(p.modes)
			g.broadcastLocked(browserproto.ModesFrom(ev))
		}
		g.mu.Unlock()

	case orchestration.MsgPaneTitle:
		var ev orchestration.PaneTitle
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		g.mu.Lock()
		if p := g.panes[ev.PaneID]; p != nil {
			p.title = ev.Title
			g.broadcastLocked(browserproto.NewPaneTitle(ev.PaneID, ev.Title))
		}
		g.mu.Unlock()

	case orchestration.MsgPaneCwd:
		var ev orchestration.PaneCwd
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		g.mu.Lock()
		if p := g.panes[ev.PaneID]; p != nil {
			p.cwd = ev.Cwd
			g.broadcastLocked(browserproto.NewPaneCwd(ev.PaneID, ev.Cwd))
		}
		g.mu.Unlock()

	case orchestration.MsgPaneAgent:
		var ev orchestration.PaneAgent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		g.mu.Lock()
		if p := g.panes[ev.PaneID]; p != nil {
			p.agent = &ev
			g.broadcastLocked(browserproto.NewPaneAgent(ev.PaneID, ev.Agent, ev.State, true))
			g.broadcastLocked(g.agentsMsgLocked())
		}
		g.mu.Unlock()

	case orchestration.MsgPaneClipboard:
		var ev orchestration.PaneClipboard
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		g.broadcast(browserproto.NewClipboard(ev.Data))

	case orchestration.MsgPaneExited:
		var ev orchestration.PaneExited
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		g.mu.Lock()
		if p := g.panes[ev.PaneID]; p != nil {
			code := ev.ExitCode
			p.exited = &code
			g.broadcastLocked(browserproto.NewPaneExited(ev.PaneID, ev.ExitCode))
		}
		g.mu.Unlock()

	case orchestration.MsgError:
		var ev orchestration.Error
		if err := json.Unmarshal(payload, &ev); err != nil {
			return
		}
		log.Printf("gateway2: daemon error (pane %d): %s", ev.PaneID, ev.Message)
		g.broadcast(browserproto.NewError(ev.PaneID, ev.Message))
	}
	// pane_selection / pane_text: nothing requests them in the spike.
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

func unmarshalParams(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return errors.New("missing params")
	}
	return json.Unmarshal(raw, v)
}

// optUnmarshalParams is unmarshalParams for commands whose fields are all
// optional: empty params decode to the zero value rather than an error.
func optUnmarshalParams(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, v)
}
