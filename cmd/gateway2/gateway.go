//go:build ghostty

package main

import (
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/rohanthewiz/rweb"

	"github.com/rohanthewiz/herdr-web/internal/browserproto"
	"github.com/rohanthewiz/herdr-web/internal/inputenc"
	"github.com/rohanthewiz/herdr-web/internal/layout"
	"github.com/rohanthewiz/herdr-web/internal/orchestration"
	"github.com/rohanthewiz/herdr-web/internal/terminal"
	"github.com/rohanthewiz/herdr-web/internal/workspace"
)

// chromeRows is reserved at the top of every pane rect for the HTML chrome
// strip (title/cwd/agent as data, 4.2); the pane's grid fills the inner rect.
const chromeRows = 1

// defaultArea is the layout area assumed until the first browser reports its
// grid via init/resize.
var defaultArea = layout.Rect{Width: 120, Height: 32}

// pane is the gateway's per-pane model: the input encoder plus the latest
// chrome state, cached so a newly connected browser gets the full picture
// without waiting for the daemon's next event.
type pane struct {
	id    uint32
	pub   string
	enc   *inputenc.Encoder
	modes terminal.InputModes
	title string
	cwd   string
	agent *orchestration.PaneAgent
	// cols/rows is the desired grid (the pane's inner rect) — what the daemon
	// pane is (or will be, on reconcile) sized to.
	cols, rows uint16
	exited     *int
	// created reports whether the daemon has spawned this pane's PTY. A
	// brand-new split pane starts false so applyLayoutLocked issues a
	// CreatePane instead of a Resize; reconcile resets it from the daemon's
	// surviving-pane set on every (re)connect.
	created bool
}

// gateway owns the hard-coded session model (one workspace, one tab, two
// panes) and fans daemon events out to connected browsers. mu guards the
// model, the pane map, and the conns set; the daemon connection has its own
// write lock (daemon.mu).
type gateway struct {
	mu     sync.Mutex
	ws     *workspace.Workspace
	area   layout.Rect
	cellW  uint32
	cellH  uint32
	cwd    string
	panes  map[uint32]*pane
	conns  map[*client]struct{}
	daemon *daemon
}

// modelSpawner satisfies workspace.PaneSpawner without touching the daemon:
// gateway2 syncs daemon panes to the model in reconcile/applyLayout instead
// (the model must be buildable before the daemon connection exists).
type modelSpawner struct{}

func (modelSpawner) Spawn(spec workspace.SpawnSpec) (workspace.TerminalID, error) {
	return workspace.TerminalID(fmt.Sprintf("term_%d", spec.PaneID)), nil
}
func (modelSpawner) Despawn(workspace.TerminalID) {}

// newGateway builds the fixed model: one workspace, one tab, a horizontal
// 50/50 split — two panes with input encoders.
func newGateway(socket, cwd string) (*gateway, error) {
	g := &gateway{
		area:  defaultArea,
		cellW: 8,
		cellH: 16,
		cwd:   cwd,
		panes: make(map[uint32]*pane),
		conns: make(map[*client]struct{}),
	}
	g.daemon = &daemon{g: g, socket: socket}

	ws, err := workspace.New(modelSpawner{}, cwd, workspace.SpawnSpec{})
	if err != nil {
		return nil, err
	}
	if _, err := ws.SplitFocused(layout.Horizontal, workspace.SpawnSpec{}); err != nil {
		return nil, err
	}
	g.ws = ws

	for _, id := range ws.ActiveTab().Layout.PaneIDs() {
		pub, _ := ws.PublicPaneID(id)
		p := &pane{id: uint32(id), pub: pub}
		if p.enc, err = inputenc.New(); err != nil {
			return nil, err
		}
		g.panes[p.id] = p
	}
	g.applyLayoutLocked() // set desired pane sizes; no daemon, no conns yet
	return g, nil
}

// layoutMsgLocked builds the layout message for the current model, reserving
// the chrome strip: each pane's inner rect loses chromeRows at the top.
// Callers hold g.mu.
func (g *gateway) layoutMsgLocked() browserproto.Layout {
	msg := browserproto.BuildLayout([]*workspace.Workspace{g.ws}, 0, g.area)
	for i := range msg.Panes {
		r := msg.Panes[i].Rect
		if r[3] > chromeRows {
			msg.Panes[i].Inner = browserproto.Rect{r[0], r[1] + chromeRows, r[2], r[3] - chromeRows}
		}
	}
	return msg
}

// applyLayoutLocked recomputes desired pane grids from the layout, updates
// the encoders' mouse bounds, and pushes resizes to the daemon for panes
// whose grid changed. Returns the layout message so callers can broadcast it.
// Callers hold g.mu.
func (g *gateway) applyLayoutLocked() browserproto.Layout {
	msg := g.layoutMsgLocked()
	for _, pr := range msg.Panes {
		p := g.panes[pr.Pane]
		if p == nil {
			continue
		}
		cols, rows := pr.Inner[2], pr.Inner[3]
		if cols == 0 || rows == 0 {
			continue
		}
		changed := cols != p.cols || rows != p.rows
		if changed {
			p.cols, p.rows = cols, rows
			p.enc.SetGrid(cols, rows)
		}
		switch {
		case !p.created:
			// The daemon has never seen this pane (fresh gateway or a new
			// split): spawn it at the desired grid rather than resize.
			g.createPaneLocked(p)
		case changed:
			r := orchestration.NewResize(p.id, cols, rows)
			r.CellWidthPx, r.CellHeightPx = g.cellW, g.cellH
			g.daemon.send(r)
		}
	}
	return msg
}

// createPaneLocked asks the daemon to spawn a pane's PTY at its desired grid
// and marks it created. Callers hold g.mu.
func (g *gateway) createPaneLocked(p *pane) {
	cp := orchestration.NewCreatePane(p.id, p.cols, p.rows)
	cp.Cwd = g.cwd
	cp.CellWidthPx, cp.CellHeightPx = g.cellW, g.cellH
	g.daemon.send(cp)
	p.created = true
}

// focusedPaneLocked resolves the model's focused pane. Callers hold g.mu.
func (g *gateway) focusedPaneLocked() *pane {
	id, ok := g.ws.FocusedPaneID()
	if !ok {
		return nil
	}
	return g.panes[uint32(id)]
}

// broadcast marshals one down message and enqueues it on every connection.
func (g *gateway) broadcast(m any) {
	b, err := browserproto.Marshal(m)
	if err != nil {
		log.Printf("gateway2: marshal broadcast: %v", err)
		return
	}
	g.mu.Lock()
	for c := range g.conns {
		c.enqueue(b)
	}
	g.mu.Unlock()
}

// broadcastLocked is broadcast for callers already holding g.mu.
func (g *gateway) broadcastLocked(m any) {
	b, err := browserproto.Marshal(m)
	if err != nil {
		log.Printf("gateway2: marshal broadcast: %v", err)
		return
	}
	for c := range g.conns {
		c.enqueue(b)
	}
}

// agentsMsgLocked builds the sidebar rollup from cached pane agent state.
// Callers hold g.mu.
func (g *gateway) agentsMsgLocked() browserproto.Agents {
	items := []browserproto.AgentItem{}
	for _, id := range g.ws.ActiveTab().Layout.PaneIDs() {
		p := g.panes[uint32(id)]
		if p == nil || p.agent == nil || p.agent.Agent == "" {
			continue
		}
		items = append(items, browserproto.AgentItem{
			Pane:      p.id,
			Pub:       p.pub,
			Workspace: g.ws.ID,
			Agent:     p.agent.Agent,
			State:     p.agent.State,
			Seen:      true, // Seen tracking is WS2's job; the spike renders live state
		})
	}
	return browserproto.NewAgents(items)
}

// --- Browser connections -----------------------------------------------------

// client is one connected browser. Writes are serialized through out — the
// writer goroutine is the only WSConn writer. Frame translators are per pane
// per connection (browserproto.FrameTranslator contract).
type client struct {
	g     *gateway
	ws    *rweb.WSConn
	out   chan []byte
	trans map[uint32]*browserproto.FrameTranslator
	once  sync.Once
}

func (c *client) enqueue(b []byte) {
	select {
	case c.out <- b:
	default:
		// A connection too slow to drain 512 queued messages is beyond
		// spike-level flow control: drop it.
		log.Printf("gateway2: dropping slow browser connection")
		c.shutdown()
	}
}

func (c *client) send(m any) {
	b, err := browserproto.Marshal(m)
	if err != nil {
		log.Printf("gateway2: marshal: %v", err)
		return
	}
	c.enqueue(b)
}

func (c *client) shutdown() {
	c.once.Do(func() {
		close(c.out)
	})
}

// translator returns the connection's frame translator for a pane.
// Called only from the daemon pump (single goroutine) under g.mu.
func (c *client) translator(paneID uint32) *browserproto.FrameTranslator {
	t := c.trans[paneID]
	if t == nil {
		t = browserproto.NewFrameTranslator(paneID)
		c.trans[paneID] = t
	}
	return t
}

// serve runs one browser WebSocket session: init/welcome handshake, initial
// state push, then the up-message loop.
func (g *gateway) serve(ws *rweb.WSConn) error {
	defer ws.Close(1000, "bye")

	// First message must be init (§2).
	first, err := ws.ReadMessage()
	if err != nil {
		return nil
	}
	up, err := browserproto.DecodeUp(first.Data)
	init, ok := up.(*browserproto.Init)
	if err != nil || !ok {
		b, _ := browserproto.Marshal(browserproto.NewWelcome("first message must be init"))
		_ = ws.WriteMessage(rweb.TextMessage, b)
		return nil
	}
	if init.V != browserproto.ProtocolVersion {
		b, _ := browserproto.Marshal(browserproto.NewWelcome(
			fmt.Sprintf("protocol version %d unsupported (server speaks %d)", init.V, browserproto.ProtocolVersion)))
		_ = ws.WriteMessage(rweb.TextMessage, b)
		return nil
	}

	c := &client{
		g:     g,
		ws:    ws,
		out:   make(chan []byte, 512),
		trans: make(map[uint32]*browserproto.FrameTranslator),
	}
	go c.writer()

	// Register, apply the browser's grid, and push initial state (§2): welcome,
	// layout, cached chrome, agents rollup — then ask the daemon to replay a
	// full frame per pane (arrives via the pump after everything enqueued here).
	g.mu.Lock()
	g.conns[c] = struct{}{}
	if init.Cols > 0 && init.Rows > 0 {
		g.area = layout.Rect{Width: init.Cols, Height: init.Rows}
	}
	if init.CellWPx > 0 && init.CellHPx > 0 {
		g.cellW, g.cellH = init.CellWPx, init.CellHPx
	}
	c.send(browserproto.NewWelcome(""))
	c.send(g.applyLayoutLocked())
	for _, p := range g.panes {
		c.send(browserproto.PaneModes{
			T: browserproto.MsgPaneModes, Pane: p.id,
			Mouse: p.modes.MouseMode != terminal.MouseNone, AltScreen: p.modes.AlternateScreen,
		})
		if p.title != "" {
			c.send(browserproto.NewPaneTitle(p.id, p.title))
		}
		if p.cwd != "" {
			c.send(browserproto.NewPaneCwd(p.id, p.cwd))
		}
		if p.agent != nil {
			c.send(browserproto.NewPaneAgent(p.id, p.agent.Agent, p.agent.State, true))
		}
		if p.exited != nil {
			c.send(browserproto.NewPaneExited(p.id, *p.exited))
		}
		g.daemon.send(orchestration.NewRequestResync(p.id))
	}
	c.send(g.agentsMsgLocked())
	if !g.daemon.connected() {
		c.send(browserproto.NewError(0, "termhost daemon not connected — retrying"))
	}
	g.mu.Unlock()

	// Up-message loop.
	for {
		m, err := ws.ReadMessage()
		if err != nil {
			break
		}
		if m.Type != rweb.TextMessage {
			continue
		}
		up, err := browserproto.DecodeUp(m.Data)
		if err != nil {
			if !errors.Is(err, browserproto.ErrUnknownType) {
				log.Printf("gateway2: bad up message: %v", err)
			}
			continue // spec §1: unknown types are dropped
		}
		g.handleUp(c, up)
	}

	g.mu.Lock()
	delete(g.conns, c)
	g.mu.Unlock()
	c.shutdown()
	return nil
}

// writer drains the outbound queue; it is the connection's only writer.
func (c *client) writer() {
	for b := range c.out {
		if err := c.ws.WriteMessage(rweb.TextMessage, b); err != nil {
			return
		}
	}
	_ = c.ws.Close(1000, "bye")
}

// handleUp dispatches one browser → server message.
func (g *gateway) handleUp(c *client, up any) {
	g.mu.Lock()
	defer g.mu.Unlock()

	switch m := up.(type) {
	case *browserproto.Key:
		p := g.focusedPaneLocked()
		if p == nil || p.exited != nil {
			return
		}
		b, err := p.enc.Key(*m)
		if err != nil {
			log.Printf("gateway2: key encode: %v", err)
			return
		}
		if len(b) > 0 {
			g.daemon.send(orchestration.NewInput(p.id, b))
		}

	case *browserproto.Mouse:
		p := g.panes[m.Pane]
		if p == nil || p.exited != nil {
			return
		}
		b, err := p.enc.Mouse(*m)
		if err != nil {
			log.Printf("gateway2: mouse encode: %v", err)
			return
		}
		switch {
		case len(b) > 0:
			g.daemon.send(orchestration.NewInput(p.id, b))
		case m.Kind == browserproto.MouseWheel && m.DY != 0:
			// Uncaptured, non-alternate-scroll wheel belongs to the viewport
			// (inputenc contract: nil bytes means the PTY doesn't want it).
			g.daemon.send(orchestration.NewScrollViewport(p.id, int32(m.DY)))
		}

	case *browserproto.Paste:
		p := g.focusedPaneLocked()
		if p == nil || p.exited != nil {
			return
		}
		b, err := p.enc.Paste(m.Data)
		if err != nil {
			log.Printf("gateway2: paste encode: %v", err)
			return
		}
		if len(b) > 0 {
			g.daemon.send(orchestration.NewInput(p.id, b))
		}

	case *browserproto.Raw:
		if p := g.focusedPaneLocked(); p != nil && len(m.Data) > 0 {
			g.daemon.send(orchestration.NewInput(p.id, m.Data))
		}

	case *browserproto.Resize:
		if m.Cols == 0 || m.Rows == 0 {
			return
		}
		g.area = layout.Rect{Width: m.Cols, Height: m.Rows}
		g.broadcastLocked(g.applyLayoutLocked())

	case *browserproto.Image:
		c.send(browserproto.NewError(0, "image paste is not supported by the gateway2 spike"))

	case *browserproto.Cmd:
		g.handleCmdLocked(c, m)
	}
}

// handleCmdLocked implements the spike's command subset: pane.focus and
// scroll (what the acceptance run needs). Everything else answers
// unsupported so the browser learns the truth instead of timing out.
// Callers hold g.mu.
func (g *gateway) handleCmdLocked(c *client, m *browserproto.Cmd) {
	reply := func(ok bool, errMsg string) {
		if m.ID == "" {
			return
		}
		r, err := browserproto.NewCmdResult(m.ID, ok, errMsg, nil)
		if err != nil {
			return
		}
		c.send(r)
	}

	switch m.Name {
	case browserproto.CmdPaneFocus:
		var p browserproto.PaneParams
		if err := unmarshalParams(m.Params, &p); err != nil {
			reply(false, "bad params: "+err.Error())
			return
		}
		if g.panes[p.Pane] == nil {
			reply(false, fmt.Sprintf("unknown pane %d", p.Pane))
			return
		}
		g.ws.ActiveTab().Layout.FocusPane(layout.PaneID(p.Pane))
		g.broadcastLocked(g.layoutMsgLocked())
		reply(true, "")

	case browserproto.CmdPaneSplit:
		g.handleSplitLocked(m, reply)

	case browserproto.CmdPaneClose:
		g.handleCloseLocked(m, reply)

	case browserproto.CmdScroll:
		var p browserproto.ScrollParams
		if err := unmarshalParams(m.Params, &p); err != nil {
			reply(false, "bad params: "+err.Error())
			return
		}
		if g.panes[p.Pane] == nil {
			reply(false, fmt.Sprintf("unknown pane %d", p.Pane))
			return
		}
		g.daemon.send(orchestration.NewScrollViewport(p.Pane, int32(p.Delta)))
		reply(true, "")

	default:
		reply(false, fmt.Sprintf("command %q not supported by the gateway2 spike", m.Name))
	}
}

// paneTargetLocked resolves an optional pane param to a target: the given pane
// if present and known, else the focused pane. Shared by split and close.
// Callers hold g.mu.
func (g *gateway) paneTargetLocked(p *uint32) (layout.PaneID, bool) {
	if p != nil {
		if g.panes[*p] == nil {
			return 0, false
		}
		return layout.PaneID(*p), true
	}
	fp := g.focusedPaneLocked()
	if fp == nil {
		return 0, false
	}
	return layout.PaneID(fp.id), true
}

// handleSplitLocked splits a pane (WS8), wiring the new pane's model entry and
// input encoder; applyLayoutLocked then spawns its PTY on the daemon (the pane
// starts uncreated) and resizes the shrunken sibling(s). Callers hold g.mu.
func (g *gateway) handleSplitLocked(m *browserproto.Cmd, reply func(bool, string)) {
	var sp browserproto.SplitParams
	if err := unmarshalParams(m.Params, &sp); err != nil {
		reply(false, "bad params: "+err.Error())
		return
	}
	dir, ok := browserproto.SplitDirection(sp.Direction)
	if !ok {
		reply(false, fmt.Sprintf("bad split direction %q", sp.Direction))
		return
	}
	target, ok := g.paneTargetLocked(sp.Pane)
	if !ok {
		reply(false, "unknown pane")
		return
	}

	_, np, err := g.ws.SplitPane(target, dir, true, workspace.SpawnSpec{})
	if err != nil {
		reply(false, "split: "+err.Error())
		return
	}
	enc, err := inputenc.New()
	if err != nil {
		reply(false, "encoder: "+err.Error())
		return
	}
	pub, _ := g.ws.PublicPaneID(np.PaneID)
	g.panes[uint32(np.PaneID)] = &pane{id: uint32(np.PaneID), pub: pub, enc: enc}
	g.broadcastLocked(g.applyLayoutLocked())
	reply(true, "")
}

// handleCloseLocked closes a pane (WS8): drops it from the model, tells the
// daemon to close its PTY, and re-lays-out the survivors. The gateway model
// keeps at least one pane (never collapses its sole tab). Callers hold g.mu.
func (g *gateway) handleCloseLocked(m *browserproto.Cmd, reply func(bool, string)) {
	var cp browserproto.OptPaneParams
	if err := optUnmarshalParams(m.Params, &cp); err != nil {
		reply(false, "bad params: "+err.Error())
		return
	}
	target, ok := g.paneTargetLocked(cp.Pane)
	if !ok {
		reply(false, "unknown pane")
		return
	}
	if len(g.panes) <= 1 {
		reply(false, "cannot close the last pane")
		return
	}
	// The guard above means this close never empties the sole tab, so
	// ClosePane must not report a workspace close; treat one as an error.
	if g.ws.ClosePane(target) {
		reply(false, "cannot close the last pane")
		return
	}
	id := uint32(target)
	delete(g.panes, id)
	g.daemon.send(orchestration.NewClosePane(id))
	g.broadcastLocked(g.applyLayoutLocked())
	g.broadcastLocked(g.agentsMsgLocked())
	reply(true, "")
}
