//go:build ghostty

package main

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rohanthewiz/rweb"

	"github.com/rohanthewiz/cats/internal/acpchat"
	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/config"
	"github.com/rohanthewiz/cats/internal/ctlproto"
	"github.com/rohanthewiz/cats/internal/inputenc"
	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/orchestration"
	"github.com/rohanthewiz/cats/internal/persist"
	"github.com/rohanthewiz/cats/internal/push"
	"github.com/rohanthewiz/cats/internal/terminal"
	"github.com/rohanthewiz/cats/internal/workspace"
)

// chromeRows is reserved at the top of every pane rect for browser-side pane
// decoration — the per-pane header strip (pub · title · cwd · agent, the
// pane-scoped mode chips, and the icon buttons). It was briefly 0 when the
// strip was dropped in the green-theme facelift; the strip is back, so the
// terminal grid gives the row up again.
const chromeRows = 1

// defaultArea is the layout area assumed until the first browser reports its
// grid via init/resize.
var defaultArea = layout.Rect{Width: 120, Height: 32}

// paneRuntime is the orchestrator's per-pane runtime state — everything about a
// pane that is NOT the domain model (which lives in app.Session): the input
// encoder, cached chrome for late-joining browsers, the desired grid, and
// whether the daemon has spawned its PTY. Keyed by pane id in orch.panes.
type paneRuntime struct {
	id     uint32
	enc    *inputenc.Encoder
	modes  terminal.InputModes
	title  string
	cwd    string
	agent  *orchestration.PaneAgent
	cols   uint16
	rows   uint16
	exited *int
	// --- hook-report ingestion (hooks.go), all loop-goroutine only ---
	// agentAt stamps when the daemon's detection last reported (hook-vs-detection
	// recency in effectiveAgent). hook is the live hook authority; agentSession
	// the resumable session identity; hookSeqs/hookSuppressed the per-source
	// idempotency and release-suppression state. pubAgent/pubState are the last
	// *published* arbitrated pair — the transition baseline for notifications.
	agentAt        time.Time
	hook           *hookAuthority
	agentSession   *agentSessionRef
	hookSeqs       map[string]uint64
	hookSuppressed map[string]hookSuppression
	pubAgent       string
	pubState       string
	// stateAt stamps when pubState last actually changed — the sidebar shows the
	// age of a settled state ("10s ago · idle"), so it must survive the repeated
	// publishes that report the same state.
	stateAt time.Time
	// agentModel is the LLM the pane's agent is currently running under, read
	// from the agent's transcript (agentmodel.go); "" when unknown or when no
	// agent whose model is resolvable is running. modelAt stamps the last
	// resolution and modelBusy marks one in flight — together they throttle the
	// reads, which happen off the loop goroutine.
	agentModel string
	modelAt    time.Time
	modelBusy  bool
	// unseen marks an agent completion that landed while the pane was off the
	// viewport (cats's pane.seen, inverted so the zero value means seen).
	// Set by publishAgent on a finished transition, cleared when the pane
	// re-enters the viewport or the agent leaves idle; feeds the rollup's
	// attention markers ("done" tier).
	unseen bool
	// created reports whether the daemon holds this pane's PTY. reconcile
	// resets it from the daemon's surviving-pane set on every (re)connect.
	created bool
	// histDirty marks output since the last history capture (WS3): the periodic
	// sweep only captures panes whose content actually changed. Set on every
	// pane_frame, cleared when a capture is issued.
	histDirty bool
}

// orch is the WS2 orchestrator: a single event-loop actor (run) that owns all
// mutable state — the app.Session domain model, per-pane runtimes, connected
// browsers, the layout area, and the daemon link. Producers (per-connection
// readers, the daemon pump) never touch that state directly; they post closures
// onto mailbox, which run serially in the loop goroutine. So there is no lock:
// state is touched by exactly one goroutine.
type orch struct {
	session *app.Session
	panes   map[uint32]*paneRuntime
	conns   map[*client]struct{}
	// connsDirty says the Clients census is stale — a connection arrived or
	// left, or the grid it reports changed. Set anywhere; cleared only by
	// flushClients between mailbox closures (see dropConn for why).
	connsDirty bool
	daemon     *daemon
	area       layout.Rect
	cellW      uint32
	cellH      uint32
	cwd        string
	// visible is the current viewport's pane set (active workspace's active
	// tab) — the panes whose frames stream to browsers (§8). Recomputed by
	// refreshViewport whenever the viewport changes.
	visible map[uint32]bool
	// pendingReqs holds in-flight daemon round-trip commands (read, capture)
	// awaiting their reply, FIFO per (pane, kind). Both replies carry no command
	// id, so the kind picks the queue and per-pane order does the correlation.
	pendingReqs map[reqKey][]*pending
	// waiters holds active pane.wait_for_output waiters per pane; each matches the
	// pane's live output stream (plus a one-shot seed of the current screen) and
	// resolves on a match, its own timeout, or the pane exiting. waiterCheck marks
	// the seed capture-check in flight. outAccum is the per-pane rolling cleaned-text
	// buffer fed by the β pane_output stream (enabled via set_output_stream while any
	// waiter is active); it exists only for panes with a live waiter.
	waiters     map[uint32][]*waiter
	waiterCheck map[uint32]bool
	outAccum    map[uint32]*outputScanner
	// subs holds control-API event subscribers (events.subscribe); emitEvent fans
	// a pane event out to the matching ones and drops any that can't keep up.
	subs map[*ctlSubscriber]struct{}
	// structPanes (pane id → public handle) and structFocus snapshot the model's
	// pane set and globally-focused pane at the last emit; emitStructuralEvents
	// diffs against them after each mutation to derive pane_added / pane_removed /
	// focus_changed. Seeded in newOrch so pre-existing panes never emit retroactively.
	structPanes map[uint32]string
	structFocus uint32
	// claudeProjects is the root claude keeps its transcripts under. It is what
	// the usage estimator sums over when no account credential is readable
	// (usage.go); "" disables that fallback.
	claudeProjects string
	// modelRoots is where each readable agent keeps its history, keyed by agent
	// label — the root half of modelResolvers, resolved once at construction
	// (agentmodel.go). A pane's current model is read from under it. An agent
	// missing here has no readable home on this machine and shows no model, which
	// is also how tests point one entry at a fixture tree.
	modelRoots map[string]string
	// chat is the ACP side panel's engine (chat.go), nil until the first
	// chat.send — the agent subprocess should not exist before anyone has
	// spoken to it.
	chat *acpchat.Manager
	// usage is the account's last-read rate-limit standing (usage.go), nil
	// until the first poll lands. Held so a browser connecting between polls is
	// sent the current numbers rather than an empty section for up to two
	// minutes.
	usage *browserproto.Usage
	// usageNudge asks the poller for an off-schedule reading (usage.refresh —
	// the sidebar's refresh control). Buffered by one and written to without
	// blocking: several clicks while a read is in flight collapse into the one
	// poll that follows it, which is what a caller asking for "now" wants
	// anyway. Read only by runUsage, written from the loop goroutine.
	usageNudge chan struct{}
	// push is the outbound notification bridge (internal/push), nil when no
	// webhook is configured — which is the default, and why every use goes
	// through the nil-safe Send. It reaches a phone whose screen is off, so it
	// deliberately does not care whether any client is connected. Wired by main
	// after construction; nil in tests unless a recorder is installed.
	push pushSink
	// lastTitle is the app-level browser-tab title last broadcast (WS8):
	// broadcastTitle dedupes against it so focus/title churn doesn't spam every
	// connection with identical title messages.
	lastTitle string
	// lastTabNames is the active workspace's derived tab names as last put on
	// the wire (viewportLayout records it). Tab auto-naming depends on pane
	// meta, so the meta ingest paths call refreshTabNames, which diffs against
	// this to rebroadcast the layout only when a visible name actually changed.
	lastTabNames string
	mailbox      chan func()
	// stop is the process-shutdown hook wired by main (server.stop). It flushes
	// pending browser writes, then exits — the persistent cathost daemon is a
	// separate process and survives. nil in tests, where stop is a no-op.
	stop func()
	// hookSocket is where the hook-report API listens (hooks.go); createPane
	// injects it into every pane's environment so installed agent hooks can
	// dial back. Wired by main before the loop starts; "" disables injection.
	hookSocket string
	// controlSocket is where the §7 control API listens (control.go); createPane
	// exports it to every pane as CATS_CONTROL_SOCKET so in-pane automation
	// (cats-todo, plugin binaries) finds the socket without relying on the
	// compiled-in default path. Wired by main before the loop starts; "" skips
	// the export.
	controlSocket string
	// baseHTML is the un-injected served page; cfgPath is the config file to
	// re-read on server.reload_config; page holds the config-injected page the
	// HTTP handler serves. The handler (rweb goroutine) and ReloadConfig (loop
	// goroutine) both touch page, so it is an atomic pointer. All three are wired
	// by main after construction; nil/"" in tests.
	baseHTML []byte
	cfgPath  string
	page     atomic.Pointer[[]byte]
	// pairing is the device-pairing context (pair.go): the authenticator that
	// mints grants plus the URL and certificate pin a new device needs. Wired by
	// main after the auth guard and TLS are resolved, which is *after* the
	// control socket is already accepting — hence the atomic, like page above.
	// nil means pairing is unavailable (auth disabled, or startup incomplete).
	pairing atomic.Pointer[pairing]
	// cfg is the loaded config-file state (defaults + file, not flag overrides —
	// config.set marshals it back to disk, so flag values must never leak in).
	// worktreeDir is the tilde-expanded worktrees root new checkouts land under.
	// Both wired by main; loop-goroutine only after the loop starts.
	cfg         config.Config
	worktreeDir string
	// --- session persistence (WS3), wired by main; zero values disable it ---
	// sessionPath/historyPath are the state files ("" ⇒ persistence off). seeds
	// and restoredCwds are loaded at startup and consumed by createPane for
	// panes the daemon no longer holds (cold start): the seed replays saved
	// scrollback via create_pane.initial_history, the cwd re-spawns the shell
	// where it was. capturedHist accumulates the latest ANSI capture per pane —
	// initialized from the loaded seeds so a partial capture sweep never wipes
	// another pane's seed from disk. saveArmed/histArmed debounce the writes.
	// histLines bounds each capture (0 = whole buffer). All loop-goroutine only.
	// restoredAgents and resumePlans carry the persisted agent-session refs
	// across a restart (resume.go): restoredAgents holds each pane's loaded
	// ref until the pane goes live (adopted by reconcile or re-spawned by
	// createPane) and is merged under live refs at save time; resumePlans is
	// the argv to exec instead of a shell for a cold-started pane, consumed
	// exactly once like seeds/restoredCwds.
	sessionPath    string
	historyPath    string
	seeds          map[uint32]string
	restoredCwds   map[uint32]string
	restoredAgents map[uint32]persist.AgentSession
	resumePlans    map[uint32][]string
	// spawnPlans carries tab.create's optional spawn override (argv/cwd/env)
	// from the dispatcher's StageSpawn to the pane's createPane — the live-
	// command sibling of resumePlans, consumed exactly once the same way.
	// Loop-goroutine only.
	spawnPlans   map[uint32]app.SpawnOverride
	capturedHist map[uint32]string
	histLines    uint32
	saveArmed    bool
	histArmed    bool
	// finalCap tracks the clean-shutdown capture sweep (nil when not shutting
	// down): Shutdown captures every live pane's scrollback, bounded by a short
	// deadline, before firing the stop hook.
	finalCap *finalCapture
}

// reqKind distinguishes the two §7 commands that need a daemon round-trip: read
// (RequestSelection → pane_selection) and capture (RequestText → pane_text). A
// pane may have both in flight at once, and the daemon's replies carry no command
// id, so the reply's message type — not just per-pane order — picks the queue.
type reqKind uint8

const (
	reqSelection reqKind = iota // read → pane_selection
	reqText                     // capture → pane_text
)

// label names the command for user-facing errors ("<label> timed out").
func (k reqKind) label() string {
	if k == reqText {
		return "capture"
	}
	return "read"
}

// reqKey identifies a per-pane FIFO queue of in-flight requests of one kind.
type reqKey struct {
	pane uint32
	kind reqKind
}

// pending is one in-flight daemon round-trip (read or capture). The dispatch
// sends the β request and returns without replying; the matching daemon reply
// (resolvePending), a timeout, or a daemon disconnect (flushPending) resolves the
// caller's Responder. The daemon emits one reply per request over its single
// ordered connection, so per-(pane, kind) FIFO correlation is exact (the reply
// carries no command id).
type pending struct {
	resp  app.Responder
	timer *time.Timer
}

// reqTimeout bounds how long a read/capture waits for the daemon's reply, so a
// browser's cmd never hangs if the daemon errors or the reply is lost without the
// connection itself dropping.
const reqTimeout = 5 * time.Second

// modelSpawner satisfies workspace.PaneSpawner without touching the daemon: the
// orchestrator syncs the daemon's PTYs to the model separately (syncDaemon), so
// the model must be buildable before any daemon connection exists.
type modelSpawner struct{}

func (modelSpawner) Spawn(spec workspace.SpawnSpec) (workspace.TerminalID, error) {
	return workspace.TerminalID(fmt.Sprintf("term_%d", spec.PaneID)), nil
}
func (modelSpawner) Despawn(workspace.TerminalID) {}

// newOrch builds the orchestrator with a fresh session (one workspace, one tab,
// one pane). Splits, tabs, and workspaces are created at runtime via commands.
func newOrch(socket, cwd string) (*orch, error) {
	sess, err := app.NewSession(modelSpawner{}, cwd)
	if err != nil {
		return nil, err
	}
	return newOrchWith(socket, cwd, sess), nil
}

// newOrchWith builds the orchestrator around an existing session — fresh
// (newOrch) or restored from a snapshot (WS3; main falls back to fresh when
// there is nothing to restore).
func newOrchWith(socket, cwd string, sess *app.Session) *orch {
	o := &orch{
		session: sess,
		panes:   make(map[uint32]*paneRuntime),
		conns:   make(map[*client]struct{}),
		// A typed nil, not a nil interface: (*push.Bridge).Send is nil-safe, so
		// this makes notifyAll's unconditional Send correct for every orch —
		// including the ones tests build and main's, before the bridge is wired.
		// Leaving the field zero would make that same call panic.
		push:           (*push.Bridge)(nil),
		area:           defaultArea,
		cellW:          8,
		cellH:          16,
		cwd:            cwd,
		visible:        make(map[uint32]bool),
		pendingReqs:    make(map[reqKey][]*pending),
		waiters:        make(map[uint32][]*waiter),
		waiterCheck:    make(map[uint32]bool),
		outAccum:       make(map[uint32]*outputScanner),
		subs:           make(map[*ctlSubscriber]struct{}),
		seeds:          make(map[uint32]string),
		restoredCwds:   make(map[uint32]string),
		restoredAgents: make(map[uint32]persist.AgentSession),
		resumePlans:    make(map[uint32][]string),
		spawnPlans:     make(map[uint32]app.SpawnOverride),
		capturedHist:   make(map[uint32]string),
		claudeProjects: claudeProjectsDir(),
		modelRoots:     modelRootsFor(),
		usageNudge:     make(chan struct{}, 1),
		mailbox:        make(chan func(), 256),
	}
	o.daemon = &daemon{o: o, socket: socket}
	o.syncDaemon()      // desired sizes; no daemon/conns yet, sends are dropped
	o.refreshViewport() // seed the visible set
	o.seedStructure()   // snapshot the initial pane set/focus (no retroactive events)
	return o
}

// run is the event loop: the sole owner of orch state. Every state mutation
// happens inside a closure delivered here.
func (o *orch) run() {
	for fn := range o.mailbox {
		fn()
		// The one place the connection set is provably settled: no broadcast is
		// in progress and no map is being ranged. Coalescing here also means a
		// closure that drops three connections sends one census, not three.
		o.flushClients()
	}
}

// post enqueues work onto the loop. It blocks if the mailbox is momentarily
// full (backpressure); the loop is always draining, so it never deadlocks.
func (o *orch) post(fn func()) { o.mailbox <- fn }

// --- Layout / daemon reconciliation ------------------------------------------

// viewportLayout builds the browser layout message for the current viewport
// (active workspace's active tab), reserving chromeRows in each pane's inner
// rect and swapping each auto-named tab's number for its derived name
// (Session.TabDisplayName over the runtime pane meta — the same patch-after-
// BuildLayout pattern the layout's agent summary uses). The derived names are
// recorded on lastTabNames so refreshTabNames can tell when a meta change
// actually renamed something.
func (o *orch) viewportLayout() browserproto.Layout {
	msg := browserproto.BuildLayout(o.session.Workspaces(), o.session.ActiveIndex(), o.area)
	for i := range msg.Panes {
		cols, rows := innerGrid(msg.Panes[i].Rect)
		msg.Panes[i].Inner = browserproto.Rect{msg.Panes[i].Rect[0], msg.Panes[i].Rect[1] + chromeRows, cols, rows}
	}
	if ws := o.activeWorkspace(); ws != nil && len(ws.Tabs) == len(msg.Tabs) {
		for i, tab := range ws.Tabs {
			msg.Tabs[i].Name = o.session.TabDisplayName(tab, o.PaneMeta)
		}
	}
	o.lastTabNames = tabNamesOf(msg)
	return msg
}

// activeWorkspace is the session's active workspace, nil when the index is
// somehow out of range (BuildLayout guards the same way).
func (o *orch) activeWorkspace() *workspace.Workspace {
	wss := o.session.Workspaces()
	if i := o.session.ActiveIndex(); i >= 0 && i < len(wss) {
		return wss[i]
	}
	return nil
}

// tabNamesOf flattens a layout message's tab names for change detection.
func tabNamesOf(msg browserproto.Layout) string {
	var b strings.Builder
	for _, t := range msg.Tabs {
		b.WriteString(t.Name)
		b.WriteByte(0)
	}
	return b.String()
}

// refreshTabNames rebroadcasts the viewport layout iff some visible tab's
// derived name changed. The meta ingest paths (OSC title, OSC 7 cwd, agent
// arbitration, pane.rename) call this because auto-names are computed from
// that meta, but none of those paths otherwise touches the layout; structural
// changes need no call — every layout rebroadcast re-derives the names anyway.
// Diffing against lastTabNames keeps meta churn (a busy agent retitling its
// pane every spinner tick) from spamming layouts when the derived name is
// unaffected.
func (o *orch) refreshTabNames() {
	ws := o.activeWorkspace()
	if ws == nil {
		return
	}
	var b strings.Builder
	for _, tab := range ws.Tabs {
		b.WriteString(o.session.TabDisplayName(tab, o.PaneMeta))
		b.WriteByte(0)
	}
	if b.String() == o.lastTabNames {
		return
	}
	o.broadcast(o.viewportLayout()) // viewportLayout re-records lastTabNames
}

// innerGrid is a pane rect's terminal grid after reserving the chrome row.
func innerGrid(r browserproto.Rect) (cols, rows uint16) {
	cols, rows = r[2], r[3]
	if rows > chromeRows {
		rows -= chromeRows
	}
	return cols, rows
}

// desiredGrids computes the target grid for every pane in every tab/workspace —
// all are live PTYs on the daemon (§8), sized from their own tab's layout over
// the shared window area.
func (o *orch) desiredGrids() map[uint32][2]uint16 {
	grids := make(map[uint32][2]uint16)
	gridRows := func(h uint16) uint16 {
		if h > chromeRows {
			return h - chromeRows
		}
		return h
	}
	for _, ws := range o.session.Workspaces() {
		for _, tab := range ws.Tabs {
			for _, info := range tab.Layout.Panes(o.area) {
				grids[uint32(info.ID)] = [2]uint16{info.Rect.Width, gridRows(info.Rect.Height)}
			}
			// A zoomed tab renders its focused pane at the full area (§8, the
			// browser only sees that one), so it must be sized to fill it. The
			// hidden siblings keep their split-rect sizes above — they stay live
			// PTYs so syncDaemon won't close them, and don't stream while hidden.
			if tab.Zoomed {
				grids[uint32(tab.Layout.Focused())] = [2]uint16{o.area.Width, gridRows(o.area.Height)}
			}
		}
	}
	return grids
}

// syncDaemon reconciles the daemon's PTY set with the session: spawn panes the
// daemon lacks, resize panes whose grid changed, close panes dropped from the
// model, and drop their runtimes.
func (o *orch) syncDaemon() {
	grids := o.desiredGrids()

	for pid := range grids {
		if o.panes[pid] == nil {
			enc, err := inputenc.New()
			if err != nil {
				log.Printf("catway: encoder: %v", err)
				continue
			}
			o.panes[pid] = &paneRuntime{id: pid, enc: enc}
		}
	}
	for pid, rt := range o.panes {
		if _, ok := grids[pid]; !ok {
			if rt.created {
				o.daemon.send(orchestration.NewClosePane(pid))
			}
			delete(o.panes, pid)
			// A never-realized spawn override dies with its pane (a plan is
			// staged live per tab.create, so unlike the restored-state maps it
			// can be cleaned eagerly).
			delete(o.spawnPlans, pid)
		}
	}
	for pid, g := range grids {
		rt := o.panes[pid]
		if rt == nil {
			continue
		}
		cols, rows := g[0], g[1]
		if cols == 0 || rows == 0 {
			continue
		}
		changed := cols != rt.cols || rows != rt.rows
		if changed {
			rt.cols, rt.rows = cols, rows
			rt.enc.SetGrid(cols, rows)
		}
		switch {
		case !rt.created:
			o.createPane(rt)
		case changed:
			r := orchestration.NewResize(pid, cols, rows)
			r.CellWidthPx, r.CellHeightPx = o.cellW, o.cellH
			o.daemon.send(r)
		}
	}
}

// createPane spawns a pane's PTY at its desired grid and marks it created. A
// restored pane the daemon no longer holds (cold start) is re-spawned in its
// saved cwd with its saved scrollback replayed via initial_history (WS3) — or,
// when a resume plan exists (resume.go), with the agent's native resume argv
// as the pane command instead of a shell. Everything restored is consumed only
// on a connected send — a pre-connection create is dropped by daemon.send and
// retried by reconcile, which must still find it.
func (o *orch) createPane(rt *paneRuntime) {
	cp := orchestration.NewCreatePane(rt.id, rt.cols, rt.rows)
	cp.Cwd = o.paneCwd(rt.id)
	cp.CellWidthPx, cp.CellHeightPx = o.cellW, o.cellH
	// Arm the integration hooks: every pane learns the hook-report socket and
	// its own public handle (WS7's catctl installers plant hooks that read
	// exactly these variables).
	pub, _ := o.session.PublicPaneID(layout.PaneID(rt.id))
	cp.Env = paneEnvMap(o.hookSocket, rt.id, pub)
	// Export the control socket alongside the hook env: in-pane automation
	// (cats-todo, plugin binaries launched via tab.create) resolves the socket
	// from CATS_CONTROL_SOCKET, which must hold even when catway listens on a
	// non-default path.
	if o.controlSocket != "" {
		if cp.Env == nil {
			cp.Env = make(map[string]string, 1)
		}
		cp.Env[ctlproto.SocketEnvVar] = o.controlSocket
	}
	if o.daemon.connected() {
		if cwd, ok := o.restoredCwds[rt.id]; ok {
			cp.Cwd = cwd
			delete(o.restoredCwds, rt.id)
		}
		if argv, ok := o.resumePlans[rt.id]; ok {
			cp.Command, cp.Args = argv[0], argv[1:]
			delete(o.resumePlans, rt.id)
			// The resuming pane keeps its ref live so the next snapshot still
			// carries it (cats's set_persisted_agent_session on restore).
			if s, ok := o.restoredAgents[rt.id]; ok {
				rt.agentSession = &agentSessionRef{source: s.Source, agent: s.Agent, kind: s.Kind, value: s.Value}
				delete(o.restoredAgents, rt.id)
			}
		}
		// A live spawn override (tab.create's optional command/cwd/env) wins
		// over the defaults; like the restored state above it is consumed only
		// on a connected send, so a pre-connection create keeps the plan for
		// reconcile's retry.
		if plan, ok := o.spawnPlans[rt.id]; ok {
			if plan.Cwd != "" {
				cp.Cwd = plan.Cwd
			}
			if len(plan.Command) > 0 {
				cp.Command, cp.Args = plan.Command[0], plan.Command[1:]
			}
			if len(plan.Env) > 0 && cp.Env == nil {
				cp.Env = make(map[string]string, len(plan.Env))
			}
			for k, v := range plan.Env {
				cp.Env[k] = v
			}
			delete(o.spawnPlans, rt.id)
		}
		if h, ok := o.seeds[rt.id]; ok {
			cp.InitialHistory = h
			delete(o.seeds, rt.id)
		}
	}
	o.daemon.send(cp)
	rt.created = true
}

// paneCwd is the directory a pane's PTY spawns in: its owning workspace's
// identity cwd (set for worktree-checkout workspaces) when present, else the
// process cwd. A restored pane's saved cwd still overrides this in createPane.
func (o *orch) paneCwd(pid uint32) string {
	if ws := o.session.PaneWorkspace(layout.PaneID(pid)); ws != nil && ws.IdentityCwd != "" {
		return ws.IdentityCwd
	}
	return o.cwd
}

// refreshViewport recomputes the visible-pane set and returns the panes that
// just became visible (a viewport change), so the loop can resend their chrome
// and full frames.
func (o *orch) refreshViewport() (added []uint32) {
	next := make(map[uint32]bool)
	for _, id := range o.session.VisiblePaneIDs() {
		pid := uint32(id)
		next[pid] = true
		if !o.visible[pid] {
			added = append(added, pid)
		}
	}
	o.visible = next
	return added
}

// applyModel is the standard follow-up after a model-mutating command: sync the
// daemon, recompute the viewport, broadcast the new layout + agents, refresh any
// newly-visible panes (chrome + full frame), emit any structural events, and
// arm the debounced session save.
func (o *orch) applyModel() {
	o.syncDaemon()
	added := o.refreshViewport()
	// Entering the viewport marks a pane's completions seen (cats: switching
	// to a tab sets pane.seen on everything it shows).
	for _, pid := range added {
		if rt := o.panes[pid]; rt != nil {
			rt.unseen = false
		}
	}
	o.broadcast(o.viewportLayout())
	o.broadcast(o.agentsMsg())
	for _, pid := range added {
		o.broadcastPaneChrome(pid)
		o.resyncPane(pid)
	}
	o.emitStructuralEvents()
	o.broadcastTitle()
	o.saveSoon()
}

// resyncPane forces every connection's translator for the pane to emit a full
// frame and asks the daemon to replay one.
func (o *orch) resyncPane(pid uint32) {
	for c := range o.conns {
		if t := c.trans[pid]; t != nil {
			t.Reset()
		}
	}
	o.daemon.send(orchestration.NewRequestResync(pid))
}

// agentsMsg builds the global sidebar rollup from every pane's cached agent
// state (agent chrome is not viewport-filtered, §8).
func (o *orch) agentsMsg() browserproto.Agents {
	items := []browserproto.AgentItem{}
	for _, ws := range o.session.Workspaces() {
		for _, tab := range ws.Tabs {
			for _, id := range tab.Layout.PaneIDs() {
				rt := o.panes[uint32(id)]
				if rt == nil {
					continue
				}
				agent, state := rt.effectiveAgent()
				if agent == "" {
					continue
				}
				pub, _ := o.session.PublicPaneID(id)
				since := int64(-1)
				if !rt.stateAt.IsZero() {
					since = time.Since(rt.stateAt).Milliseconds()
				}
				items = append(items, browserproto.AgentItem{
					Pane: rt.id, Pub: pub, Workspace: ws.ID, Tab: tab.Number,
					Agent: agent, State: state, Model: rt.agentModel,
					Seen: !rt.unseen, SinceMs: since,
				})
			}
		}
	}
	return browserproto.NewAgents(items)
}

// --- daemon round-trips: read + capture (loop goroutine only) ----------------
//
// read and capture are the only §7 commands that need a daemon round-trip: the
// dispatch sends a β request and returns without replying, then the daemon's
// reply (or a timeout / disconnect) resolves the browser's cmd_result later.
// registerPending / resolvePending / timeoutPending / flushPending are shared;
// only the request shape (startRead vs startCapture) and the reply data type
// differ per command.

// StartRead registers an in-flight read (app.Backend) and asks the daemon to
// extract the selection. The pane_selection reply completes r in resolvePending.
func (o *orch) StartRead(r app.Responder, p app.ReadParams) {
	o.registerPending(r, reqKey{p.Pane, reqSelection})
	o.daemon.send(orchestration.NewRequestSelection(p.Pane,
		orchestration.SelectionPoint{Row: p.Anchor[0], Col: uint16(p.Anchor[1])},
		orchestration.SelectionPoint{Row: p.Cursor[0], Col: uint16(p.Cursor[1])},
		p.Rect))
}

// StartCapture registers an in-flight capture (app.Backend) and asks the daemon
// to extract the pane's buffer text. The pane_text reply completes r.
func (o *orch) StartCapture(r app.Responder, p app.CaptureParams) {
	o.registerPending(r, reqKey{p.Pane, reqText})
	o.daemon.send(orchestration.NewRequestText(p.Pane, p.Scope, p.Lines, p.Ansi, p.Unwrap))
}

// registerPending enqueues an in-flight request under key and arms its timeout.
// The caller sends the β request separately (the request shape is per-command).
func (o *orch) registerPending(resp app.Responder, key reqKey) {
	pr := &pending{resp: resp}
	o.pendingReqs[key] = append(o.pendingReqs[key], pr)
	pr.timer = time.AfterFunc(reqTimeout, func() {
		o.post(func() { o.timeoutPending(key, pr) })
	})
}

// resolvePending completes the oldest in-flight request for key with the daemon's
// reply data. Per-(pane, kind) FIFO: the daemon replies to requests in order over
// its single connection, and the reply carries no command id.
func (o *orch) resolvePending(key reqKey, data any) {
	q := o.pendingReqs[key]
	if len(q) == 0 {
		return
	}
	pr := q[0]
	o.dropPending(key, 0)
	if pr.timer != nil {
		pr.timer.Stop()
	}
	o.replyPending(pr, data, "")
}

// timeoutPending fails a still-pending request after reqTimeout. It removes the
// request by identity, not position, since a late reply may have shifted the queue.
func (o *orch) timeoutPending(key reqKey, pr *pending) {
	for i, e := range o.pendingReqs[key] {
		if e == pr {
			o.dropPending(key, i)
			o.replyPending(pr, nil, key.kind.label()+" timed out")
			return
		}
	}
}

// flushPending fails every in-flight request (the daemon connection dropped, so
// no reply will arrive).
func (o *orch) flushPending(errMsg string) {
	for key, q := range o.pendingReqs {
		for _, pr := range q {
			if pr.timer != nil {
				pr.timer.Stop()
			}
			o.replyPending(pr, nil, errMsg)
		}
		delete(o.pendingReqs, key)
	}
}

// dropPending removes the request at index i from a (pane, kind) FIFO queue.
func (o *orch) dropPending(key reqKey, i int) {
	q := append(o.pendingReqs[key][:i], o.pendingReqs[key][i+1:]...)
	if len(q) == 0 {
		delete(o.pendingReqs, key)
	} else {
		o.pendingReqs[key] = q
	}
}

// replyPending completes a pending request through its Responder — the reply
// data on success, or an error. The Responder skips a caller with no reply
// channel (e.g. a browser cmd with no id).
func (o *orch) replyPending(pr *pending, data any, errMsg string) {
	if errMsg != "" {
		pr.resp.Fail(errMsg)
		return
	}
	pr.resp.OK(data)
}

// --- pane.wait_for_output waiters (loop goroutine only) ----------------------
//
// wait_for_output rides the unary envelope but resolves only when the pane's
// output matches. Registering the first waiter for a pane turns on the daemon's
// raw-output stream (set_output_stream); each β pane_output chunk is stripped to
// plain text into a per-pane rolling buffer (outAccum) and matched against the
// pane's waiters (onPaneOutput). Because it is the *byte* stream, it catches
// fast-scrolling transient output the diffed frames coalesce away and the child's
// final pre-exit output. A one-shot capture-check at registration seeds the match
// with output already on screen (onWaiterText). A match resolves the caller
// Matched:true; the wait's own timer or the pane exiting resolves Matched:false; a
// daemon drop fails it. Removing the last waiter turns the stream back off.

// waiter is one in-flight pane.wait_for_output. match runs over the pane's cleaned
// output text, returning the matched line (for the result's context) and whether
// the pattern is present. done guards a single resolution.
type waiter struct {
	resp  app.Responder
	match func(text string) (line string, ok bool)
	lines uint32
	timer *time.Timer
	done  bool
}

// StartWaitForOutput registers a waiter (app.Backend). Registering the first
// waiter for a pane turns on the daemon's raw-output stream and starts a fresh
// accumulator, so all subsequent output is matched byte-for-byte; a one-shot
// capture-check then seeds the match with output already on screen. The dispatcher
// has validated the pattern and gated pane/daemon, so Matcher can't fail here
// (re-derived defensively).
func (o *orch) StartWaitForOutput(r app.Responder, p app.WaitForOutputParams) {
	match, err := p.Matcher()
	if err != nil {
		r.Fail(err.Error())
		return
	}
	first := len(o.waiters[p.Pane]) == 0
	w := &waiter{resp: r, match: match, lines: p.Lines}
	o.waiters[p.Pane] = append(o.waiters[p.Pane], w)
	w.timer = time.AfterFunc(app.WaitTimeout(p.TimeoutMs), func() {
		o.post(func() { o.finishWaiter(p.Pane, w, false, "") })
	})
	if first {
		o.outAccum[p.Pane] = &outputScanner{}
		o.sendStreamSub(p.Pane, true) // enable the raw stream *before* the seed
	}
	o.triggerWaiterCheck(p.Pane) // seed with output already on screen
}

// sendStreamSub toggles the daemon's raw pane_output stream for a pane. A nil or
// disconnected daemon drops the send (there is no stream to toggle); an enable is
// only issued while connected — the dispatcher gates on DaemonConnected before a
// waiter registers — and a disable on the last waiter is a best-effort cleanup.
func (o *orch) sendStreamSub(pane uint32, enabled bool) {
	if o.daemon == nil {
		return
	}
	o.daemon.send(orchestration.NewSetOutputStream(pane, enabled))
}

// triggerWaiterCheck issues one capture-check for a pane's active waiters unless
// one is already in flight. The pane_text reply lands on waiterResponder, which
// matches it against each waiter; the next frame re-triggers if any remain.
func (o *orch) triggerWaiterCheck(pane uint32) {
	if len(o.waiters[pane]) == 0 || o.waiterCheck[pane] {
		return
	}
	if o.daemon == nil || !o.daemon.connected() {
		return // nothing to capture from; a reconnect's frames re-trigger
	}
	o.waiterCheck[pane] = true
	o.registerPending(waiterResponder{o: o, pane: pane}, reqKey{pane, reqText})
	o.daemon.send(orchestration.NewRequestText(pane, uint8(terminal.TextRecent), o.waiterScanLines(pane), false, false))
}

// waiterScanLines is how many recent rows a capture-check reads: 0 (the whole
// buffer) if any waiter wants it, else the largest requested bound.
func (o *orch) waiterScanLines(pane uint32) uint32 {
	var max uint32
	for _, w := range o.waiters[pane] {
		if w.lines == 0 {
			return 0
		}
		if w.lines > max {
			max = w.lines
		}
	}
	return max
}

// waiterResponder is the app.Responder for a waiter capture-check: the pane_text
// reply (resolvePending) lands on OK and is matched against the pane's waiters; a
// failed capture (timeout / no such pane) just clears the in-flight flag so the
// next frame retries. It delivers no result to a client itself.
type waiterResponder struct {
	o    *orch
	pane uint32
}

func (waiterResponder) WantsReply() bool { return true }
func (r waiterResponder) OK(data any)    { r.o.onWaiterText(r.pane, data) }
func (r waiterResponder) Fail(string)    { r.o.waiterCheck[r.pane] = false }

// onWaiterText matches the one-shot seed capture-check (output already on screen)
// against the pane's waiters and clears the in-flight flag.
func (o *orch) onWaiterText(pane uint32, data any) {
	o.waiterCheck[pane] = false
	if cr, ok := data.(browserproto.CaptureResult); ok {
		o.matchWaiters(pane, cr.Text)
	}
}

// onPaneOutput feeds a raw β pane_output chunk into the pane's accumulator and
// matches the resulting cleaned text against its waiters. A chunk that arrives
// after the last waiter resolved (outAccum already dropped) is ignored — the
// daemon's set_output_stream(false) races the tail of the stream.
func (o *orch) onPaneOutput(pane uint32, data []byte) {
	sc := o.outAccum[pane]
	if sc == nil {
		return
	}
	o.matchWaiters(pane, sc.feed(data))
}

// matchWaiters resolves every still-pending waiter for a pane whose pattern now
// appears in text (Matched:true, with the matched line). Iterates a copy because
// finishWaiter mutates the pane's waiter slice.
func (o *orch) matchWaiters(pane uint32, text string) {
	for _, w := range append([]*waiter(nil), o.waiters[pane]...) {
		if w.done {
			continue
		}
		if line, ok := w.match(text); ok {
			o.finishWaiter(pane, w, true, line)
		}
	}
}

// finishWaiter resolves a waiter once — match (Matched:true), or timeout / pane
// exit (Matched:false) — and removes it from the pane's list. Idempotent via
// w.done, so a match racing the timeout resolves exactly once.
func (o *orch) finishWaiter(pane uint32, w *waiter, matched bool, line string) {
	if w.done {
		return
	}
	w.done = true
	if w.timer != nil {
		w.timer.Stop()
	}
	o.removeWaiter(pane, w)
	w.resp.OK(app.WaitForOutputResult{Matched: matched, Text: line})
}

// removeWaiter drops w from the pane's waiter list. When the last one goes it
// tears down the pane's waiter state and turns the raw-output stream back off, so
// a pane with no waiter stops paying the stream cost.
func (o *orch) removeWaiter(pane uint32, w *waiter) {
	q := o.waiters[pane]
	for i, e := range q {
		if e == w {
			q = append(q[:i], q[i+1:]...)
			break
		}
	}
	if len(q) == 0 {
		delete(o.waiters, pane)
		delete(o.waiterCheck, pane)
		delete(o.outAccum, pane)
		o.sendStreamSub(pane, false)
	} else {
		o.waiters[pane] = q
	}
}

// resolveWaitersOnExit fails a pane's remaining waiters when it exits: no more
// output will come, so an unmatched pattern won't appear. Output that arrived only
// in the final frame (which the post-exit capture can't reach) is the accepted edge.
func (o *orch) resolveWaitersOnExit(pane uint32) {
	for _, w := range append([]*waiter(nil), o.waiters[pane]...) {
		o.finishWaiter(pane, w, false, "")
	}
}

// flushWaiters fails every active waiter when the daemon connection drops — no
// capture can resolve, so a wait can't complete. Mirrors flushPending.
func (o *orch) flushWaiters(errMsg string) {
	for pane, q := range o.waiters {
		for _, w := range q {
			if w.done {
				continue
			}
			w.done = true
			if w.timer != nil {
				w.timer.Stop()
			}
			w.resp.Fail(errMsg)
		}
		delete(o.waiters, pane)
		delete(o.waiterCheck, pane)
		delete(o.outAccum, pane)
	}
}

// --- control-API event subscribers (loop goroutine only) ---------------------

// emitEvent fans a pane event out to every control-API subscriber whose filter
// accepts it, dropping any sink that can't keep up (a slow/dead reader).
func (o *orch) emitEvent(name string, pane uint32, data any) {
	for s := range o.subs {
		if !s.filter.Match(name, pane) {
			continue
		}
		if !s.sub.Send(name, data) {
			delete(o.subs, s)
		}
	}
}

// seedStructure records the current pane set + focused pane without emitting, so
// the first emitStructuralEvents diff reports only real changes — a subscriber
// that connects later never gets a retroactive pane_added for a pane that already
// existed. Called once at construction.
func (o *orch) seedStructure() {
	o.structPanes = make(map[uint32]string)
	for _, id := range o.session.AllPaneIDs() {
		h, _ := o.session.PublicPaneID(id)
		o.structPanes[uint32(id)] = h
	}
	o.structFocus = o.focusedPaneID()
}

// focusedPaneID is the globally-focused pane's internal id (0 if none).
func (o *orch) focusedPaneID() uint32 {
	if id, ok := o.session.FocusedPane(); ok {
		return uint32(id)
	}
	return 0
}

// emitStructuralEvents diffs the model's pane set + focused pane against the last
// snapshot and emits pane_added / pane_removed / focus_changed for any change,
// then updates the snapshot. Called at the end of every model mutation (applyModel,
// BroadcastLayout); a no-op when nothing structural changed (a browser resize, a
// rename, a re-focus of the same pane). Loop-goroutine only. The snapshot is kept
// current even with no subscribers, so a later subscriber diffs from a live base.
func (o *orch) emitStructuralEvents() {
	cur := make(map[uint32]string, len(o.structPanes))
	for _, id := range o.session.AllPaneIDs() {
		pid := uint32(id)
		h, _ := o.session.PublicPaneID(id)
		cur[pid] = h
		if _, existed := o.structPanes[pid]; !existed {
			o.emitEvent(app.EventPaneAdded, pid, app.PaneRefEvent{Pane: pid, Handle: h})
		}
	}
	for pid, h := range o.structPanes {
		if _, still := cur[pid]; !still {
			o.emitEvent(app.EventPaneRemoved, pid, app.PaneRefEvent{Pane: pid, Handle: h})
		}
	}
	o.structPanes = cur

	if focus := o.focusedPaneID(); focus != o.structFocus {
		o.structFocus = focus
		h, _ := o.session.PublicPaneID(layout.PaneID(focus))
		o.emitEvent(app.EventFocusChanged, focus, app.PaneRefEvent{Pane: focus, Handle: h})
	}
}

// --- app.Backend adapters (the runtime-effect seam) --------------------------
//
// orch implements app.Backend so the protocol-neutral app.Dispatcher can drive
// it. Most are one-liners over existing orch/daemon methods; the async round-trip
// pair (StartRead/StartCapture) is above with the pending machinery. All run on
// the loop goroutine.

// Area is the current viewport grid.
func (o *orch) Area() layout.Rect { return o.area }

// ApplyModel reconciles the daemon and rebroadcasts the viewport after a
// model-mutating command.
func (o *orch) ApplyModel() { o.applyModel() }

// BroadcastLayout rebroadcasts just the viewport layout (focus/rename moved) and
// emits any structural event — a focus_changed for the focus commands that route
// here without touching the pane set (rename is a no-op diff). Focus and names
// are durable state, so the debounced save is armed too.
func (o *orch) BroadcastLayout() {
	o.broadcast(o.viewportLayout())
	o.emitStructuralEvents()
	o.broadcastTitle()
	o.saveSoon()
}

// BroadcastPaneTitle pushes a pane's effective title if it is on screen; else it
// rides the chrome resend when the pane next becomes visible. The custom name it
// reflects (pane.rename) is durable, so the save is armed.
func (o *orch) BroadcastPaneTitle(pane uint32) {
	if o.visible[pane] {
		o.broadcast(browserproto.NewPaneTitle(pane, o.effectiveTitle(pane)))
	}
	o.broadcastTitle()
	o.refreshTabNames() // a pane custom name is the top auto-name rung
	o.saveSoon()
}

// ScrollPane passes a scrollback delta to the pane's PTY.
func (o *orch) ScrollPane(pane uint32, delta int) error {
	if o.panes[pane] == nil {
		return fmt.Errorf("unknown pane %d", pane)
	}
	o.daemon.send(orchestration.NewScrollViewport(pane, int32(delta)))
	return nil
}

// SendInput injects text into a pane's PTY (pane.send_input), addressed by pane
// id rather than riding focus like the browser Key/Paste path — an automation
// client targets panes the user isn't looking at. Text goes through the pane's
// own paste encoder, so bracketed-paste wrapping (and ghostty's control-byte
// sanitizing) tracks the foreground app's live modes; submit then synthesizes a
// real Enter press+release through the key encoder, which adapts to kitty/
// modifyOtherKeys/DECCKM state the same way a browser keystroke would. The
// release usually encodes to nothing and is skipped — it exists for apps that
// asked for release events (kitty report-event-types).
func (o *orch) SendInput(pane uint32, text string, submit bool) error {
	rt := o.panes[pane]
	if rt == nil {
		return fmt.Errorf("unknown pane %d", pane)
	}
	if rt.exited != nil {
		return fmt.Errorf("pane %d has exited", pane)
	}
	if text != "" {
		b, err := rt.enc.Paste(text)
		if err != nil {
			return fmt.Errorf("send_input: paste encode: %w", err)
		}
		if len(b) > 0 {
			o.daemon.send(orchestration.NewInput(rt.id, b))
		}
	}
	if submit {
		for _, kind := range []string{browserproto.KeyDown, browserproto.KeyUp} {
			b, err := rt.enc.Key(browserproto.Key{Code: "Enter", Key: "Enter", Kind: kind})
			if err != nil {
				return fmt.Errorf("send_input: enter encode: %w", err)
			}
			if len(b) > 0 {
				o.daemon.send(orchestration.NewInput(rt.id, b))
			}
		}
	}
	return nil
}

// StageSpawn registers a one-shot spawn override for a pane the next
// applyModel will create (tab.create's optional command/cwd/env). Loop
// goroutine, and always dispatched before that applyModel — createPane is the
// sole consumer, so the plan can never be applied to an already-live pane.
func (o *orch) StageSpawn(pane uint32, ov app.SpawnOverride) {
	o.spawnPlans[pane] = ov
}

// PaneExists / DaemonConnected gate the async round-trip commands.
func (o *orch) PaneExists(pane uint32) bool { return o.panes[pane] != nil }
func (o *orch) DaemonConnected() bool       { return o.daemon.connected() }

// PaneMeta answers pane.list/pane.get's runtime-metadata merge from the pane's
// cached runtime state — no daemon round trip. The agent pair comes from
// effectiveAgent (the same hook-vs-detection arbitration the sidebar shows), so
// a control-API client sees exactly what the browser chrome does. An unknown
// pane returns the zero value; the dispatcher treats all-empty as a valid
// answer.
func (o *orch) PaneMeta(pane uint32) app.PaneMeta {
	rt := o.panes[pane]
	if rt == nil {
		return app.PaneMeta{}
	}
	meta := app.PaneMeta{Title: rt.title, Cwd: rt.cwd}
	if agent, state := rt.effectiveAgent(); agent != "" {
		// The model rides the agent: it is resolved from that agent's transcript
		// (agentmodel.go), so reporting it for a pane with no agent would be
		// reporting a leftover.
		meta.Agent, meta.AgentState, meta.AgentModel = agent, state, rt.agentModel
	}
	return meta
}

// ReloadConfig re-reads the config file and re-renders the served page so its
// theme and copy-mode keybindings take effect on the next page load / browser
// connection. Server settings (addr, sockets, auth, tls) are fixed for the
// process's lifetime — they need a restart — so this deliberately re-applies
// only the front-end half. A missing config path or a parse/validate error
// leaves the current page in place and reports the failure to the caller. Runs
// on the loop goroutine; the HTTP handler reads o.page atomically.
func (o *orch) ReloadConfig() error {
	if o.cfgPath == "" || o.baseHTML == nil {
		log.Printf("catway: server.reload_config — no config file in use; nothing to reload")
		return nil
	}
	cfg, path, err := config.Load(o.cfgPath)
	if err != nil {
		log.Printf("catway: server.reload_config failed: %v", err)
		return err
	}
	o.cfg = cfg // keep config.get / config.set working from the reloaded state
	page := renderPage(o.baseHTML, cfg)
	o.page.Store(&page)
	o.broadcastTheme() // the theme lands live everywhere; keybindings still need a reload
	log.Printf("catway: reloaded config from %s — theme applied live, keybindings apply to new page loads; server settings need a restart", path)
	return nil
}

// Shutdown tells every browser we are going away, saves the session state, and
// — after a short, bounded final scrollback capture (WS3) — fires the
// process-exit hook (set by main). The persistent cathost daemon is a separate
// process and deliberately survives.
func (o *orch) Shutdown() {
	o.broadcast(browserproto.NewShutdown())
	if o.chat != nil {
		o.chat.Shutdown() // the ACP agent is ours alone; it must not outlive us
	}
	o.saveNow()
	o.beginFinalCapture(func() {
		if o.stop != nil {
			o.stop()
		}
	})
}

// --- Broadcasting ------------------------------------------------------------

func (o *orch) broadcast(m any) {
	b, err := browserproto.Marshal(m)
	if err != nil {
		log.Printf("catway: marshal broadcast: %v", err)
		return
	}
	for c := range o.conns {
		o.enqueue(c, b)
	}
}

func (o *orch) send(c *client, m any) {
	b, err := browserproto.Marshal(m)
	if err != nil {
		log.Printf("catway: marshal: %v", err)
		return
	}
	o.enqueue(c, b)
}

// effectiveTitle is what the browser should show for a pane: the user's custom
// name (pane.rename) when set, otherwise the terminal-reported title cached on
// the runtime.
func (o *orch) effectiveTitle(pid uint32) string {
	if name, ok := o.session.PaneCustomName(layout.PaneID(pid)); ok && name != "" {
		return name
	}
	if rt := o.panes[pid]; rt != nil {
		return rt.title
	}
	return ""
}

// appTitle is the app-level browser-tab title (WS8): the focused pane's
// effective title when it has one, otherwise the active workspace's name.
func (o *orch) appTitle() string {
	if id, ok := o.session.FocusedPane(); ok {
		if t := o.effectiveTitle(uint32(id)); t != "" {
			return t
		}
	}
	wss := o.session.Workspaces()
	if i := o.session.ActiveIndex(); i >= 0 && i < len(wss) {
		return wss[i].DisplayName()
	}
	return ""
}

// broadcastTitle pushes the app title to every browser when it changed —
// called after anything that can move focus or retitle the focused pane.
func (o *orch) broadcastTitle() {
	if t := o.appTitle(); t != o.lastTitle {
		o.lastTitle = t
		o.broadcast(browserproto.NewTitle(t))
	}
}

// broadcastPaneChrome resends a pane's cached chrome to all connections (used
// when a pane becomes visible after a viewport switch).
func (o *orch) broadcastPaneChrome(pid uint32) {
	rt := o.panes[pid]
	if rt == nil {
		return
	}
	o.broadcast(browserproto.PaneModes{T: browserproto.MsgPaneModes, Pane: pid,
		Mouse: rt.modes.MouseMode != terminal.MouseNone, AltScreen: rt.modes.AlternateScreen})
	if t := o.effectiveTitle(pid); t != "" {
		o.broadcast(browserproto.NewPaneTitle(pid, t))
	}
	if rt.cwd != "" {
		o.broadcast(browserproto.NewPaneCwd(pid, rt.cwd))
	}
	if agent, state := rt.effectiveAgent(); agent != "" {
		o.broadcast(browserproto.NewPaneAgent(pid, agent, state, rt.agentModel, true))
	}
	if rt.exited != nil {
		o.broadcast(browserproto.NewPaneExited(pid, *rt.exited))
	}
}

// enqueue delivers bytes to a connection's writer, dropping the connection if
// it can't keep up. Loop-goroutine only.
func (o *orch) enqueue(c *client, b []byte) {
	if _, ok := o.conns[c]; !ok {
		return
	}
	select {
	case c.out <- b:
	default:
		log.Printf("catway: dropping slow browser connection")
		o.dropConn(c)
	}
}

// dropConn removes a connection and closes its writer queue. Idempotent and
// loop-goroutine only, so the queue is closed exactly once.
func (o *orch) dropConn(c *client) {
	if _, ok := o.conns[c]; !ok {
		return
	}
	delete(o.conns, c)
	close(c.out)
	// Only flag it. Broadcasting the census from here would re-enter: dropConn's
	// hottest caller is enqueue, which is itself running inside a broadcast loop
	// over o.conns — so the census would iterate a map the outer range is still
	// walking, and could drop a second connection mid-flight. flushClients runs
	// it once the stack has unwound.
	o.connsDirty = true
}

// clientsMsg is the current census. Sizers counts the connections that declared
// a grid (Init without viewer): those are the ones o.area actually reflects, so
// "2 clients, 1 sizer" reads as one desktop and one phone along for the ride.
func (o *orch) clientsMsg() browserproto.Clients {
	sizers := 0
	for c := range o.conns {
		if !c.viewer {
			sizers++
		}
	}
	return browserproto.NewClients(len(o.conns), sizers, o.area.Width, o.area.Height)
}

// flushClients broadcasts the census if the connection set (or the grid it
// reports) changed during the mailbox closure that just ran. Called from the
// loop between closures, which is the only place guaranteed not to be inside a
// broadcast.
//
// The loop is deliberate rather than a single send: the broadcast can itself
// drop a wedged connection (enqueue → dropConn), re-flagging mid-flush and
// leaving a census on the wire that is already wrong. Each extra pass follows a
// strict decrease in len(o.conns), so it terminates — at worst when the last
// connection is gone.
func (o *orch) flushClients() {
	for o.connsDirty {
		o.connsDirty = false
		o.broadcast(o.clientsMsg())
	}
}

// --- Browser connections -----------------------------------------------------

// WebSocket keepalive. Nothing below the application protocol notices a peer
// that stops existing without closing its socket — a phone that walks out of
// signal, a laptop that suspends, a NAT that forgets the mapping — so without
// this a dead connection stays in o.conns forever, holding its writer goroutine
// and its per-pane translators.
//
// The server pings on an interval and requires the peer to produce *something*
// within wsReadTimeout: a pong, or any up-message. Both refresh the deadline.
//
//	t= 0s  ─ping──►  ◄──pong──   read deadline pushed to t=90s
//	t=30s  ─ping──►  ◄──pong──   read deadline pushed to t=120s
//	t=60s  ─ping──►      ✗       (peer gone; nothing refreshes the deadline)
//	t=90s  ─ping──►      ✗
//	t=120s               ✗       ReadMessage fails ─► serve returns ─► dropConn
//
// Three unanswered pings before the reap, so a brief cellular stall or a tab
// that resumes quickly is not punished. wsWriteTimeout is the other half: a
// half-open socket accepts writes into the kernel buffer until it fills, then
// blocks forever, which would park the writer goroutine past any read deadline.
const (
	wsPingInterval = 30 * time.Second
	wsReadTimeout  = 90 * time.Second
	wsWriteTimeout = 10 * time.Second
)

// client is one connected browser. The writer goroutine is the only WSConn
// writer; trans (per-pane frame translators) is touched only in the loop.
//
// viewer mirrors Init.Viewer: this connection watches the session without
// owning its geometry. Written once in serve before the client is published to
// the loop, then read-only — so no synchronisation, and the writer goroutine
// never sees a half-built client.
type client struct {
	o      *orch
	ws     *rweb.WSConn
	out    chan []byte
	pong   chan []byte // ping payloads to echo back; see serve's ping handler
	viewer bool
	trans  map[uint32]*browserproto.FrameTranslator
}

func (c *client) translator(pid uint32) *browserproto.FrameTranslator {
	t := c.trans[pid]
	if t == nil {
		t = browserproto.NewFrameTranslator(pid)
		c.trans[pid] = t
	}
	return t
}

// installKeepalive wires the read half of the keepalive onto the connection.
// Both handlers run on whichever goroutine is inside ReadMessage, so they must
// never touch the write side directly — hence the hop through c.pong.
//
// Call before starting the writer, and only from the reading goroutine.
func (c *client) installKeepalive() {
	// A pong never surfaces from ReadMessage — rweb dispatches control frames
	// inside its own frame loop — so an otherwise silent peer's only proof of
	// life has to refresh the deadline from here.
	c.ws.SetPongHandler(func([]byte) error {
		return c.ws.SetReadDeadline(time.Now().Add(wsReadTimeout))
	})
	// Answer the peer's pings from the writer goroutine rather than rweb's
	// default handler, which would reply inline and make the reader a second
	// writer of the socket. Browsers never ping, but a native client keeping
	// its own liveness clock will.
	c.ws.SetPingHandler(func(data []byte) error {
		// RFC 6455 §5.5.3: the pong echoes the ping's payload. Copy it — the
		// buffer belongs to rweb's reader and is reused past this call.
		select {
		case c.pong <- append([]byte(nil), data...):
		default: // pinging faster than we can answer needs no help from us
		}
		return nil
	})
}

// writer is the sole writer of this connection's socket: down-messages,
// keepalive pings, and the pongs owed for the peer's pings all funnel through
// here. Keeping them on one goroutine is what makes the per-write deadline
// safe to set — rweb applies ws.writeDeadline inside the write path without
// holding its write mutex, so setting it from a second goroutine would race.
func (c *client) writer() { c.writeLoop(wsPingInterval) }

// writeLoop is writer with the ping cadence injected, so tests need not wait
// out the production interval.
func (c *client) writeLoop(pingEvery time.Duration) {
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()

	for {
		select {
		case b, ok := <-c.out:
			if !ok { // dropConn closed the queue: a clean, deliberate goodbye
				_ = c.ws.Close(1000, "bye")
				return
			}
			if !c.write(rweb.TextMessage, b) {
				return
			}
		case data := <-c.pong:
			if !c.write(rweb.PongMessage, data) {
				return
			}
		case <-ping.C:
			// A nil payload: RFC 6455 §5.5.2 allows it, and we correlate
			// nothing — any pong at all is the liveness signal.
			if !c.write(rweb.PingMessage, nil) {
				return
			}
		}
	}
}

// write sends one frame under a fresh deadline, asking the loop to drop this
// connection if it fails. Reports whether the writer should keep running.
//
// The connection is *not* closed here. A write failure means the read side is
// doomed too, and it will notice on its own — either immediately (a real socket
// error) or at wsReadTimeout — whereupon serve's deferred Close runs on the
// goroutine that owns the read half.
func (c *client) write(mt rweb.MessageType, b []byte) bool {
	_ = c.ws.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	if err := c.ws.WriteMessage(mt, b); err != nil {
		c.o.post(func() { c.o.dropConn(c) })
		return false
	}
	return true
}

// serve runs one browser session: the init handshake (synchronous), then the
// up-message read loop that posts each message onto the orchestrator loop.
func (o *orch) serve(ws *rweb.WSConn) error {
	defer ws.Close(1000, "bye")

	// The handshake gets a deadline of its own: a peer that completes the HTTP
	// upgrade and then says nothing owns this goroutine until the process dies.
	// Reusing wsReadTimeout rather than a tighter bound leaves room for a first
	// message crossing a slow link.
	_ = ws.SetReadDeadline(time.Now().Add(wsReadTimeout))

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

	c := &client{o: o, ws: ws, out: make(chan []byte, 512),
		pong:   make(chan []byte, 4),
		viewer: init.Viewer,
		trans:  make(map[uint32]*browserproto.FrameTranslator)}

	c.installKeepalive()
	go c.writer()
	o.post(func() { o.registerConn(c, init) })

	for {
		// Refreshed per read, not just per pong, so a peer that talks but whose
		// pongs are lost stays connected.
		_ = ws.SetReadDeadline(time.Now().Add(wsReadTimeout))
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
				log.Printf("catway: bad up message: %v", err)
			}
			continue // spec §1: unknown types are dropped
		}
		o.post(func() { o.handleUp(c, up) })
	}
	o.post(func() { o.dropConn(c) })
	return nil
}

// registerConn adds a connection, applies its reported grid, and pushes the
// initial viewport state (welcome, layout, cached chrome, agents) plus a full
// frame per visible pane. Loop-goroutine only.
func (o *orch) registerConn(c *client, init *browserproto.Init) {
	o.conns[c] = struct{}{}
	o.connsDirty = true
	// A viewer declares no geometry: neither the cell grid nor the cell pixel
	// metrics. Skipping the metrics matters as much as skipping the grid — they
	// ride β create_pane/resize (see syncDaemon and the split path), so a phone's
	// cell size would reach every pane's TERM even though its grid did not.
	if !c.viewer {
		if init.Cols > 0 && init.Rows > 0 {
			o.area = layout.Rect{Width: init.Cols, Height: init.Rows}
		}
		if init.CellWPx > 0 && init.CellHPx > 0 {
			o.cellW, o.cellH = init.CellWPx, init.CellHPx
		}
	}
	o.syncDaemon() // the new grid may resize panes
	o.refreshViewport()

	o.send(c, browserproto.NewWelcome(""))
	o.send(c, o.viewportLayout())
	for _, id := range o.session.VisiblePaneIDs() {
		pid := uint32(id)
		rt := o.panes[pid]
		if rt == nil {
			continue
		}
		o.send(c, browserproto.PaneModes{T: browserproto.MsgPaneModes, Pane: pid,
			Mouse: rt.modes.MouseMode != terminal.MouseNone, AltScreen: rt.modes.AlternateScreen})
		// Effective title, not rt.title: a pane.rename custom name must survive
		// a page reload just like it survives a viewport switch.
		if t := o.effectiveTitle(pid); t != "" {
			o.send(c, browserproto.NewPaneTitle(pid, t))
		}
		if rt.cwd != "" {
			o.send(c, browserproto.NewPaneCwd(pid, rt.cwd))
		}
		if agent, state := rt.effectiveAgent(); agent != "" {
			o.send(c, browserproto.NewPaneAgent(pid, agent, state, rt.agentModel, true))
		}
		if rt.exited != nil {
			o.send(c, browserproto.NewPaneExited(pid, *rt.exited))
		}
		c.translator(pid).Reset()
		o.daemon.send(orchestration.NewRequestResync(pid))
	}
	o.send(c, o.agentsMsg())
	if o.usage != nil {
		o.send(c, *o.usage)
	}
	if o.chat != nil {
		// The whole chat model in one message — a client joining
		// mid-conversation (or mid-permission-prompt) starts converged.
		o.send(c, o.chat.Snapshot())
	}
	o.send(c, browserproto.NewTitle(o.appTitle()))
	if !o.daemon.connected() {
		o.send(c, browserproto.NewError(0, "cathost daemon not connected — retrying"))
	}
}

// --- Up-message handling (loop goroutine) ------------------------------------

// inputTarget resolves the pane a key or paste should reach, or nil to drop it.
//
// pane == 0 is the historical path, byte for byte: route to the session's
// focused pane. That is what every browser sends, so the desktop is untouched
// by this field existing.
//
// A non-zero pane addresses one directly — the pattern Mouse has always used —
// and clears two gates that focus-routed input has no need of:
//
//   - Visible, the same rule Mouse takes. Frames stream only for visible panes,
//     so visibility is the client's freshness proof: you may type where the
//     server recently told you it was streaming, not into a pane you last saw
//     three workspace switches ago.
//   - workspace.lock, the same rule pane.send_input takes (internal/app/
//     commands.go). Without it, a client wanting past a stated guardrail would
//     simply send key{pane:N} instead of pane.send_input — a real bypass of a
//     real safety feature, and one we would have introduced ourselves.
//
// A refusal is silent, as Mouse's is; the alternative is an error toast per
// keystroke. Nothing is left to guess at: layout.workspaces[].locked already
// names every locked workspace, and the viewport's pane set is what visibility
// means, so a client can render the refusal before it types.
func (o *orch) inputTarget(pane uint32) *paneRuntime {
	if pane == 0 {
		id, ok := o.session.FocusedPane()
		if !ok {
			return nil
		}
		pane = uint32(id)
	} else {
		if !o.visible[pane] {
			return nil
		}
		if ws := o.session.PaneWorkspace(layout.PaneID(pane)); ws != nil && ws.Locked {
			return nil
		}
	}
	rt := o.panes[pane]
	if rt == nil || rt.exited != nil {
		return nil
	}
	return rt
}

func (o *orch) handleUp(c *client, up any) {
	switch m := up.(type) {
	case *browserproto.Key:
		rt := o.inputTarget(m.Pane)
		if rt == nil {
			return
		}
		if b, err := rt.enc.Key(*m); err != nil {
			log.Printf("catway: key encode: %v", err)
		} else if len(b) > 0 {
			o.daemon.send(orchestration.NewInput(rt.id, b))
		}

	case *browserproto.Mouse:
		rt := o.panes[m.Pane]
		if rt == nil || rt.exited != nil || !o.visible[m.Pane] {
			return
		}
		b, err := rt.enc.Mouse(*m)
		if err != nil {
			log.Printf("catway: mouse encode: %v", err)
			return
		}
		switch {
		case len(b) > 0:
			o.daemon.send(orchestration.NewInput(rt.id, b))
		case m.Kind == browserproto.MouseWheel && m.DY != 0:
			o.daemon.send(orchestration.NewScrollViewport(rt.id, int32(m.DY)))
		}

	case *browserproto.Paste:
		rt := o.inputTarget(m.Pane)
		if rt == nil {
			return
		}
		if b, err := rt.enc.Paste(m.Data); err != nil {
			log.Printf("catway: paste encode: %v", err)
		} else if len(b) > 0 {
			o.daemon.send(orchestration.NewInput(rt.id, b))
		}

	case *browserproto.Raw:
		id, ok := o.session.FocusedPane()
		if ok && len(m.Data) > 0 {
			o.daemon.send(orchestration.NewInput(uint32(id), m.Data))
		}

	case *browserproto.Resize:
		// A viewer that rotates its phone must not reflow the desktop's panes.
		// Gating here as well as at init is the point: the declaration is made
		// once, but resize is the message that would arrive forever after.
		if c.viewer || m.Cols == 0 || m.Rows == 0 {
			return
		}
		o.area = layout.Rect{Width: m.Cols, Height: m.Rows}
		o.connsDirty = true // Clients carries the grid; it just changed
		o.applyModel()

	case *browserproto.Image:
		o.send(c, browserproto.NewError(0, "image paste is not supported by the catway spike"))

	case *browserproto.Cmd:
		o.handleCmd(c, m)
	}
}
