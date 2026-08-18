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
	"github.com/rohanthewiz/cats/internal/hostmeter"
	"github.com/rohanthewiz/cats/internal/inputenc"
	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/orchestration"
	"github.com/rohanthewiz/cats/internal/persist"
	"github.com/rohanthewiz/cats/internal/push"
	"github.com/rohanthewiz/cats/internal/terminal"
	"github.com/rohanthewiz/cats/internal/workspace"
)

// chromeRows is reserved at the top of every pane rect for browser-side pane
// decoration — the per-pane header strip (pub · title · cwd · branch · agent ·
// state, the pane-scoped mode chips, and the icon buttons). It was briefly 0 when the
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
	id uint32
	// host is the id of the cathost this pane's PTY lives on — the runtime
	// mirror of the model's PaneState.HostID, resolved once in syncDaemon so
	// every send site can pick a connection without walking the session. It is
	// always a host that exists in orch.hosts (an unknown one falls back to the
	// default), so hostOf never has to guess.
	host  string
	enc   *inputenc.Encoder
	modes terminal.InputModes
	title string
	cwd   string
	// branch is the git branch checked out in cwd (gitbranch.go), "" when the
	// pane is not in a repository or its cwd is not known yet. branchAt stamps
	// the last resolution and branchBusy marks one in flight — together they
	// throttle the HEAD reads, which happen off the loop goroutine.
	branch     string
	branchAt   time.Time
	branchBusy bool
	agent      *orchestration.PaneAgent
	cols       uint16
	rows       uint16
	exited     *int
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
	// appFocused is the focus state last delivered to (or assumed by) the
	// pane's program: "this is the session's focused pane AND some client
	// window is in the foreground". syncAppFocus forwards its transitions as
	// focus reports (CSI I/O) when the program enabled DEC mode 1004; the
	// zero value (unfocused) means a fresh pane earns an explicit focus-in
	// on the first sync rather than silently assuming it is being watched.
	appFocused bool
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
	// hosts is every cathost this catway attaches to, by host id, and
	// defaultHost names the one that panes with no recorded host belong to.
	// A single-host session has exactly one entry ("local"), which is why
	// nothing above this seam had to learn about hosts at all.
	//
	// The map is owned by the loop goroutine: it is built in newOrchWith and
	// re-shaped by applyHostRoster (host.attach / host.detach / a config
	// reload), both of which run there. A dial goroutine must therefore never
	// read it directly — it reaches the orch only by posting a closure, which
	// is why every host-scoped thing a daemon needs (its id, its label) is a
	// field on the daemon rather than a lookup back through here.
	hosts map[string]*daemon
	// hostOrder is the roster in configured order — the order the sidebar and
	// host.list print, which map iteration cannot supply.
	hostOrder   []string
	defaultHost string
	// cathostSocket is the local host's socket as resolved at startup, flags
	// included. Every later roster edit re-derives the effective roster from
	// o.cfg.Hosts (the file's half) plus this (the synthesized local host's
	// address), which the config file alone cannot supply — a --socket flag may
	// have overridden it.
	cathostSocket string
	area          layout.Rect
	cellW         uint32
	cellH         uint32
	cwd           string
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
	// hostStats is each remote cathost's last reported reading of the machine
	// it runs on, keyed by host id (hoststats.go). Kept beside the poll's
	// reading rather than inside it because the two arrive on completely
	// different clocks — the poll is ours, these are pushed by other machines —
	// and usageMsg is where they are put back together.
	hostStats map[string][]hostmeter.Row
	// usageNudge asks the poller for an off-schedule reading (usage.refresh —
	// the sidebar's refresh control). Buffered by one and written to without
	// blocking: several clicks while a read is in flight collapse into the one
	// poll that follows it, which is what a caller asking for "now" wants
	// anyway. Read only by runUsage, written from the loop goroutine.
	usageNudge chan struct{}
	// usageAttention is how closely the USAGE section is being watched, as one
	// of the usageAttention* tiers. It paces the poll: the account read is a
	// request to someone else's endpoint, and its only consumer is a sidebar,
	// so the rate it happens at should follow whether that sidebar is in front
	// of anybody (see usageWait).
	//
	// The one piece of loop state read from outside the loop. runUsage is its
	// own goroutine — it has to be, the read blocks — and posting a closure to
	// ask the loop a question would mean the poller waiting on the loop while
	// the loop may be waiting on nothing at all. An int32 published on every
	// transition and read on every tick is the whole synchronisation: a tick
	// that races a transition picks up the previous tier and corrects on the
	// next one, and the nudge covers the only transition where that lag would
	// be felt.
	usageAttention atomic.Int32
	// lastUsageRead is when the stored reading was taken, kept as the floor
	// under the refocus nudge: alt-tabbing back and forth should not be able to
	// buy more readings than the poll would have taken anyway.
	lastUsageRead time.Time
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
	// lastTheme is the effective appearance as the control-API event stream last
	// reported it; broadcastTheme dedupes theme_changed against it.
	//
	// The browser push is deliberately NOT deduped — it is idempotent restyling
	// and cheap. An event is different, because a subscriber ACTS on it, and
	// broadcastTheme runs after every config.set including one that only touched
	// copy-mode keys; without this a client would rebuild its palette because
	// someone rebound a key. Seeded in newOrch from the starting config, so that
	// first no-op save is silent too.
	lastTheme app.ConfigTheme
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
	reqListDir                  // path.list on a remote host → dir_listing
)

// label names the command for user-facing errors ("<label> timed out").
func (k reqKind) label() string {
	switch k {
	case reqText:
		return "capture"
	case reqListDir:
		return "directory listing"
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
// one pane) attached to the single synthesized local host. Splits, tabs, and
// workspaces are created at runtime via commands.
func newOrch(socket, cwd string) (*orch, error) {
	return newOrchHosts(config.EffectiveHosts(socket, nil), cwd)
}

// newOrchHosts is newOrch over an explicit roster (main, from the config file).
func newOrchHosts(hosts []config.Host, cwd string) (*orch, error) {
	sess, err := app.NewSession(modelSpawner{}, cwd)
	if err != nil {
		return nil, err
	}
	return newOrchHostsWith(hosts, cwd, sess)
}

// newOrchWith builds the orchestrator around an existing session — fresh
// (newOrch) or restored from a snapshot (WS3; main falls back to fresh when
// there is nothing to restore) — on the single synthesized local host.
func newOrchWith(socket, cwd string, sess *app.Session) *orch {
	o, err := newOrchHostsWith(config.EffectiveHosts(socket, nil), cwd, sess)
	if err != nil {
		// Unreachable: the synthesized local host is a unix socket, the one
		// transport a daemon has always been able to build, and an unusable path
		// fails at dial time rather than here.
		panic("catway: local host: " + err.Error())
	}
	return o
}

// newOrchHostsWith is the real constructor: an existing session attached to an
// explicit roster. The hosts are built BEFORE the first syncDaemon, because
// that pass resolves each restored pane's host id and falls back to the default
// for any host it does not find — a roster installed afterwards would arrive
// after every pane had already been told it lives on the default machine.
func newOrchHostsWith(hosts []config.Host, cwd string, sess *app.Session) (*orch, error) {
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
	if err := o.installHosts(hosts); err != nil {
		return nil, err
	}
	o.syncDaemon()      // desired sizes; no daemon/conns yet, sends are dropped
	o.refreshViewport() // seed the visible set
	o.seedStructure()   // snapshot the initial pane set/focus (no retroactive events)
	o.seedTheme()       // ditto for the appearance: no retroactive theme_changed
	return o, nil
}

// installHosts builds the daemon per configured host and names the default.
// EffectiveHosts guarantees a non-empty roster with exactly one default, so the
// only failure here is a host whose address cannot be turned into a dialer —
// which is a startup error, not something to discover on the first split.
//
// hostOrder preserves the roster's order for display; the map is what everything
// else resolves through, and Go's map iteration would otherwise reshuffle the
// sidebar on every render.
func (o *orch) installHosts(hosts []config.Host) error {
	o.hosts = make(map[string]*daemon, len(hosts))
	o.hostOrder = make([]string, 0, len(hosts))
	for _, h := range hosts {
		d, err := newDaemon(o, h)
		if err != nil {
			return err
		}
		o.hosts[h.ID] = d
		o.hostOrder = append(o.hostOrder, h.ID)
		if h.Default {
			o.defaultHost = h.ID
		}
		if h.ID == localHostID {
			o.cathostSocket = d.socket // the effective path, flags already applied
		}
	}
	if o.defaultHost == "" && len(o.hostOrder) > 0 {
		o.defaultHost = o.hostOrder[0] // EffectiveHosts marks one, but never depend on it
	}
	return nil
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
		// Patched here rather than in BuildLayout for the same reason the tab
		// names are: resolving a host needs the roster, which is the runtime's
		// and not the workspace package's. The value is always the resolved id,
		// never the model's "" — a badge reading nothing helps nobody, and the
		// client decides whether to draw it from the roster's size.
		msg.Panes[i].Host = o.paneHostID(msg.Panes[i].Pane)
	}
	wss := o.session.Workspaces()
	for i := range msg.Workspaces {
		if i < len(wss) {
			msg.Workspaces[i].Host = o.workspaceHostID(wss[i])
		}
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

// --- host resolution ---------------------------------------------------------
//
// Every β send is addressed to a pane, and every pane belongs to exactly one
// host, so the whole multi-host seam reduces to "which daemon does this pane
// use". These four helpers are that answer; nothing else in catway may reach
// into orch.hosts directly.

// localHostID is the id of the always-present host reached over
// server.cathost_socket. It is synthesized rather than configured (by
// config.EffectiveHosts), so a session with no hosts: block still has exactly
// one host and it is this one.
const localHostID = config.LocalHostID

// nopDaemon stands in wherever a host cannot be resolved (an unknown pane, a
// vanished host). Its sends drop and it is never connected — the same behaviour
// as a real host that is down, which is what every call site already handles.
// It deliberately has no orch back-pointer: it must never be dialed or flushed.
var nopDaemon = &daemon{id: "", label: "unknown host", kind: "none"}

// hostByID resolves a host id to its daemon, falling back to the default host
// for "" and returning nopDaemon for an id no longer configured.
func (o *orch) hostByID(id string) *daemon {
	if id == "" {
		id = o.defaultHost
	}
	if d := o.hosts[id]; d != nil {
		return d
	}
	return nopDaemon
}

// hostOf resolves the daemon for a pane runtime — the form used wherever the
// caller already has the runtime in hand (the overwhelmingly common case).
func (o *orch) hostOf(rt *paneRuntime) *daemon {
	if rt == nil {
		return nopDaemon
	}
	return o.hostByID(rt.host)
}

// hostForPane resolves the daemon for a pane id. An unknown pane yields
// nopDaemon rather than the default host: a send for a pane with no runtime has
// nowhere legitimate to go, and silently routing it to the default machine is
// exactly the bug this seam exists to prevent.
func (o *orch) hostForPane(pid uint32) *daemon {
	rt := o.panes[pid]
	if rt == nil {
		return nopDaemon
	}
	return o.hostByID(rt.host)
}

// paneHostID is the resolved host id for a pane: the runtime's when there is
// one, else the model's, normalized so the answer always names a configured
// host. A pane restored onto a host that no longer exists falls back to the
// default — a wrong-machine pane is recoverable, a permanently black one is not.
func (o *orch) paneHostID(pid uint32) string {
	if rt := o.panes[pid]; rt != nil && rt.host != "" {
		return rt.host
	}
	id := o.session.PaneHost(layout.PaneID(pid))
	if id == "" {
		return o.defaultHost
	}
	if o.hosts[id] == nil {
		log.Printf("catway: pane %d names unknown host %q — using %s", pid, id, o.defaultHost)
		return o.defaultHost
	}
	return id
}

// paneIsLocal reports whether a pane's terminal runs on this catway's own
// machine. It is the gate on every feature that reads the filesystem behind a
// pane — the git branch badge, the agent-model transcript readers, the worktree
// commands, the directory picker, the restore-time cwd healing — because all of
// those open paths with this process's own syscalls, and a remote pane's cwd
// names a directory in another machine's filesystem. Answering about the wrong
// disk is worse than answering nothing: a same-named directory here would give
// a confidently wrong branch or model, and worktree create would write a
// checkout on the wrong box.
//
// Localness is the host *id*, not its transport: the first real remote host is
// reached over `ssh -L` with a unix address, so a unix socket proves nothing.
// Only the synthesized local host (server.cathost_socket) is this machine.
func (o *orch) paneIsLocal(pid uint32) bool { return o.paneHostID(pid) == localHostID }

// --- host roster (the browser's HOSTS section and §7 host.list) --------------

// hostPaneCounts is how many live panes each host currently holds, resolved the
// same way every send is (so a pane whose recorded host vanished is counted on
// the default host it actually fell back to, not on the ghost).
func (o *orch) hostPaneCounts() map[string]int {
	counts := make(map[string]int, len(o.hosts))
	if o.session == nil {
		return counts // a partial harness orch: no model, so nothing is anywhere
	}
	for _, id := range o.session.AllPaneIDs() {
		counts[o.paneHostID(uint32(id))]++
	}
	return counts
}

// Hosts is the Backend seam's roster (host.list), in configured order.
// Loop-goroutine only, like every Backend method.
func (o *orch) Hosts() []app.HostInfo {
	counts := o.hostPaneCounts()
	out := make([]app.HostInfo, 0, len(o.hostOrder))
	for _, id := range o.hostOrder {
		d := o.hosts[id]
		if d == nil {
			continue
		}
		connected, lastErr := d.status()
		out = append(out, app.HostInfo{
			ID:        d.id,
			Label:     d.label,
			Connected: connected,
			AddrKind:  d.kind,
			Default:   id == o.defaultHost,
			Local:     id == localHostID,
			Panes:     counts[id],
			Error:     lastErr,
			LatencyMs: d.latencyMs(),
			// The local machine is always listable — this process reads its own
			// disk — so it does not depend on a capability the local cathost
			// happens to advertise.
			ListsDirs: id == localHostID || d.supports(orchestration.FeatureListDir),
		})
	}
	return out
}

// hostsMsg is the same roster as a browser message. Two shapes rather than one
// because the wire structs are per-protocol by design (browserproto is the
// browser's, app's results are the command vocabulary's); the translation is
// this loop.
func (o *orch) hostsMsg() browserproto.Hosts {
	infos := o.Hosts()
	items := make([]browserproto.HostItem, 0, len(infos))
	for _, h := range infos {
		items = append(items, browserproto.HostItem{
			ID: h.ID, Label: h.Label, Connected: h.Connected,
			AddrKind: h.AddrKind, Default: h.Default, Local: h.Local,
			Panes: h.Panes, Error: h.Error, LatencyMs: h.LatencyMs,
			ListsDirs: h.ListsDirs,
		})
	}
	return browserproto.NewHosts(items)
}

// broadcastHosts pushes the roster to every client. Called on each host's
// connect and disconnect — the two moments a dot changes colour — and cheap
// enough at those rates to send unconditionally rather than diff.
func (o *orch) broadcastHosts() { o.broadcast(o.hostsMsg()) }

// workspaceHostID is a workspace's default host for new panes, resolved to a
// host that exists (the model stores "" for "the default", and may name one
// that has since left the roster).
func (o *orch) workspaceHostID(ws *workspace.Workspace) string {
	if ws == nil || ws.HostID == "" || o.hosts[ws.HostID] == nil {
		return o.defaultHost
	}
	return ws.HostID
}

// workspaceHostOwns reports whether a workspace's start directory belongs to the
// machine a pane is running on — the question paneCwd asks before handing that
// path to a cathost.
//
// It is not simply workspaceHostID(ws) == host, because that resolves an
// unknown host to the default one, and a workspace pinned to a host that has
// LEFT the roster would then match every default-host pane. That is exactly the
// state a detach produces: the workspace still says "devbox", its panes have
// been re-homed onto the local machine, and its identity cwd is a path on a
// filesystem this catway can no longer reach. A departed host owns nothing.
func (o *orch) workspaceHostOwns(ws *workspace.Workspace, hostID string) bool {
	if ws == nil {
		return false
	}
	if ws.HostID != "" && o.hosts[ws.HostID] == nil {
		return false
	}
	return o.workspaceHostID(ws) == hostID
}

// syncDaemon reconciles the daemons' PTY sets with the session: spawn panes a
// host lacks, resize panes whose grid changed, close panes dropped from the
// model, and drop their runtimes. Each pane's commands go to its own host.
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
		// Resolve the host on every sync, not just at creation: a runtime born
		// before its model state was restored would otherwise keep the default
		// host forever. paneHostID reads the runtime first, so this is a
		// one-time resolution per pane in practice.
		if rt := o.panes[pid]; rt != nil {
			rt.host = o.paneHostID(pid)
		}
	}
	for pid, rt := range o.panes {
		if _, ok := grids[pid]; !ok {
			if rt.created {
				o.hostOf(rt).send(orchestration.NewClosePane(pid))
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
			o.hostOf(rt).send(r)
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
	//
	// Which socket depends on where the pane is. A local pane gets this
	// process's own; a pane on another machine gets that cathost's relay, which
	// forwards what arrives to us. Injecting our path there would name a file in
	// a filesystem the pane cannot see — and on a remote box that happens to run
	// cats itself, would name a DIFFERENT server's socket and post a remote
	// agent's state onto someone else's panes.
	pub, _ := o.session.PublicPaneID(layout.PaneID(rt.id))
	cp.Env = paneEnvMap(o.hookSocketFor(rt), rt.id, pub)
	// Export the control socket alongside the hook env: in-pane automation
	// (cats-todo, plugin binaries launched via tab.create) resolves the socket
	// from CATS_CONTROL_SOCKET, which must hold even when catway listens on a
	// non-default path.
	//
	// A remote pane gets the variable EXPLICITLY DISABLED rather than left
	// alone. The control API is a duplex protocol rather than the hook API's
	// one-shot line, so carrying it across the seam is its own piece of work,
	// and until then there is nothing on that machine for catctl to dial.
	//
	// Silence would not be neutral. A pane inherits the cathost's environment,
	// and a cathost launched from inside another cats session carries that
	// session's CATS_CONTROL_SOCKET — so an in-pane catctl would quietly drive
	// somebody else's terminals. (Observed, not theorised.) An unset variable
	// falls back to the conventional /tmp path, which is the same hazard by a
	// more predictable route. SocketNone makes catctl say why instead.
	ctlVal := o.controlSocket
	if !o.paneIsLocal(rt.id) {
		ctlVal = ctlproto.SocketNone
	}
	if ctlVal != "" {
		if cp.Env == nil {
			cp.Env = make(map[string]string, 1)
		}
		cp.Env[ctlproto.SocketEnvVar] = ctlVal
	}
	if o.hostOf(rt).connected() {
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
	o.hostOf(rt).send(cp)
	rt.created = true
}

// paneCwd is the directory a pane's PTY spawns in: its owning workspace's
// identity cwd (set for worktree-checkout workspaces) when present, else the
// process cwd. A restored pane's saved cwd still overrides this in createPane.
// A directory only means something on the machine it lives on, so both
// fallbacks are host-scoped: the workspace's identity cwd is used when the pane
// runs on the workspace's own host (a pane placed elsewhere — "split here on
// devbox" inside a local workspace — must not be sent a path from this
// filesystem), and the process cwd only for a local pane.
func (o *orch) paneCwd(pid uint32) string {
	host := o.paneHostID(pid)
	if ws := o.session.PaneWorkspace(layout.PaneID(pid)); ws != nil && ws.IdentityCwd != "" &&
		o.workspaceHostOwns(ws, host) {
		return ws.IdentityCwd
	}
	if host != localHostID {
		// o.cwd is where *this* process was started — a directory that need not
		// exist on the other box, and if it does is almost certainly not the
		// same project. Naming nothing lets cathost spawn the pane in its own
		// default directory, which is a working shell rather than a dead pane.
		return ""
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
	o.hostForPane(pid).send(orchestration.NewRequestResync(pid))
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
	o.hostForPane(p.Pane).send(orchestration.NewRequestSelection(p.Pane,
		orchestration.SelectionPoint{Row: p.Anchor[0], Col: uint16(p.Anchor[1])},
		orchestration.SelectionPoint{Row: p.Cursor[0], Col: uint16(p.Cursor[1])},
		p.Rect))
}

// StartCapture registers an in-flight capture (app.Backend) and asks the daemon
// to extract the pane's buffer text. The pane_text reply completes r.
func (o *orch) StartCapture(r app.Responder, p app.CaptureParams) {
	o.registerPending(r, reqKey{p.Pane, reqText})
	o.hostForPane(p.Pane).send(orchestration.NewRequestText(p.Pane, p.Scope, p.Lines, p.Ansi, p.Unwrap))
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

// flushPending fails every in-flight request, whatever host it was addressed to
// (the session is going away, so no reply will arrive for any of them).
func (o *orch) flushPending(errMsg string) { o.flushPendingFor("", errMsg) }

// flushPendingFor fails the in-flight requests belonging to one host — what a
// single cathost dropping means. hostID "" flushes every host. Requests are
// keyed by pane, and a pane's host is exactly what decides whether its reply
// can still arrive.
func (o *orch) flushPendingFor(hostID, errMsg string) {
	for key, q := range o.pendingReqs {
		if hostID != "" && o.paneHostID(key.pane) != hostID {
			continue
		}
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
	o.hostForPane(pane).send(orchestration.NewSetOutputStream(pane, enabled))
}

// triggerWaiterCheck issues one capture-check for a pane's active waiters unless
// one is already in flight. The pane_text reply lands on waiterResponder, which
// matches it against each waiter; the next frame re-triggers if any remain.
func (o *orch) triggerWaiterCheck(pane uint32) {
	if len(o.waiters[pane]) == 0 || o.waiterCheck[pane] {
		return
	}
	d := o.hostForPane(pane)
	if !d.connected() {
		return // nothing to capture from; a reconnect's frames re-trigger
	}
	o.waiterCheck[pane] = true
	o.registerPending(waiterResponder{o: o, pane: pane}, reqKey{pane, reqText})
	d.send(orchestration.NewRequestText(pane, uint8(terminal.TextRecent), o.waiterScanLines(pane), false, false))
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

// flushWaiters fails every active waiter — no capture can resolve, so a wait
// can't complete. Mirrors flushPending.
func (o *orch) flushWaiters(errMsg string) { o.flushWaitersFor("", errMsg) }

// flushWaitersFor fails the waiters on one host's panes (hostID "" = all), the
// waiter half of flushPendingFor: a wait is fed by its pane's output stream and
// capture-checks, both of which die with that pane's cathost and neither of
// which is affected by another host dropping.
func (o *orch) flushWaitersFor(hostID, errMsg string) {
	for pane, q := range o.waiters {
		if hostID != "" && o.paneHostID(pane) != hostID {
			continue
		}
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
		// The pane programs' view of focus moved with it: the old pane's
		// program is no longer being watched, the new one is (window
		// permitting). Piggybacked here because this is already the one
		// choke point every model mutation flows through.
		o.syncAppFocus()
	}
}

// anyClientFocused reports whether at least one connected window is in the
// OS foreground — the "is anyone looking" half of a pane program's focus.
func (o *orch) anyClientFocused() bool {
	for c := range o.conns {
		if c.focused {
			return true
		}
	}
	return false
}

// noteUsageAttention republishes the tier the usage poller paces itself by, and
// asks for a reading now when the section has just come back into view.
//
// The nudge is what makes gating cheap enough to do at all. A backgrounded
// window's numbers are allowed to go up to usageIdleInterval stale precisely
// because coming back to the window buys a fresh one immediately — the cost of
// the slower cadence is paid by nobody, since nobody was reading it. Without it
// the gate would be a straight downgrade: the first thing you would see on
// returning is an old number and a long wait.
//
// The floor keeps that from becoming a second, faster poll under another name.
// Alt-tabbing away and back ten times in a minute is ten rising edges, and each
// one asking for a read would beat the cadence this whole change exists to
// slow; a reading younger than one normal interval is already the answer.
//
// Loop-goroutine only, like everything that touches o.conns.
func (o *orch) noteUsageAttention() {
	want := usageAttentionDark
	if len(o.conns) > 0 {
		want = usageAttentionIdle
		if o.anyClientFocused() {
			want = usageAttentionWatched
		}
	}
	prev := o.usageAttention.Swap(int32(want))
	// Remote hosts measure themselves only while somebody is looking, so the
	// tier that paces our own poll is also what re-paces theirs — including
	// down to "not at all" when the last browser goes.
	o.syncHostStats()
	if want != usageAttentionWatched || prev == int32(usageAttentionWatched) {
		return // no rising edge into view; the tier alone is the whole update
	}
	if !o.lastUsageRead.IsZero() && time.Since(o.lastUsageRead) < usageInterval {
		return // what is on screen is no older than a poll would have left it
	}
	o.RefreshUsage()
}

// paneSeen is the focus state a pane's program should believe: it is the
// session's focused pane and somebody's window is in front to see it. Both
// halves matter — a focused pane in a backgrounded app is not being watched,
// and neither is a background pane in a focused app.
func (o *orch) paneSeen(pid uint32) bool {
	return o.anyClientFocused() && pid == o.focusedPaneID()
}

// syncAppFocus reconciles every pane program's believed focus (appFocused)
// with paneSeen, forwarding each transition as a focus report. Encoding is
// per-pane mode state (inputenc.Focus returns nil unless the program enabled
// DEC mode 1004), so panes that never asked receive nothing — but appFocused
// still tracks the truth for them, ready for the seed report the moment they
// do ask (see the pane_modes handler). This is what lets a TUI stop blinking
// its caret when the user switches away from the app: the window's blur
// arrives as a Focus up-message, and the focused pane's program hears CSI O.
func (o *orch) syncAppFocus() {
	// The usage poller asks the same question of the same two facts ("is any
	// window connected, and is any of them in front"), and every caller that
	// reaches here does so precisely because one of those may have just changed
	// — a Focus report, or a connection arriving or dying. Publishing from here
	// means a new call site cannot forget to, which is the failure that would
	// show up as a poller stuck at the wrong cadence for as long as the session
	// lasts.
	o.noteUsageAttention()
	for pid, rt := range o.panes {
		want := o.paneSeen(pid)
		if want == rt.appFocused {
			continue
		}
		rt.appFocused = want
		if rt.exited != nil {
			continue
		}
		if b := rt.enc.Focus(want); len(b) > 0 {
			o.hostOf(rt).send(orchestration.NewInput(rt.id, b))
		}
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
	o.hostForPane(pane).send(orchestration.NewScrollViewport(pane, int32(delta)))
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
			o.hostOf(rt).send(orchestration.NewInput(rt.id, b))
		}
	}
	if submit {
		for _, kind := range []string{browserproto.KeyDown, browserproto.KeyUp} {
			b, err := rt.enc.Key(browserproto.Key{Code: "Enter", Key: "Enter", Kind: kind})
			if err != nil {
				return fmt.Errorf("send_input: enter encode: %w", err)
			}
			if len(b) > 0 {
				o.hostOf(rt).send(orchestration.NewInput(rt.id, b))
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

// PaneExists / DaemonConnected / PaneHostConnected gate the async round-trip
// commands. DaemonConnected reports the default host — the session-wide "is
// there a backend" answer, which is what a caller with no pane in hand (and
// every single-host session) means by it. PaneHostConnected answers for the
// machine a specific pane actually lives on, so a pane on a healthy host stays
// usable while another host is down; an unknown pane resolves to nopDaemon and
// therefore reports disconnected, which is the honest answer for it.
func (o *orch) PaneExists(pane uint32) bool { return o.panes[pane] != nil }
func (o *orch) DaemonConnected() bool       { return o.hostByID(o.defaultHost).connected() }
func (o *orch) PaneHostConnected(pane uint32) bool {
	return o.hostForPane(pane).connected()
}

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
	// Host is resolved, not the raw model id: pane.list is what an automation
	// client picks a target from, and "which machine will this run on" must be
	// answerable without the client repeating catway's fallback rules.
	meta := app.PaneMeta{Title: rt.title, Cwd: rt.cwd, Host: o.paneHostID(pane)}
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
// only the front-end half, plus the one server-side section that is no longer
// restart-only: hosts:, which is diffed against the running roster exactly as
// host.attach/host.detach diff it (a host added to the file is dialed, one
// removed is detached and its panes re-homed). A missing config path or a
// parse/validate error leaves the current page in place and reports the failure
// to the caller. Runs on the loop goroutine; the HTTP handler reads o.page
// atomically.
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
	// The roster is adopted after o.cfg, so a host that fails to build leaves the
	// reload otherwise applied and the previous roster running — the same
	// all-or-nothing guarantee applyHostRoster gives host.attach, scoped to the
	// hosts: section rather than to the whole file.
	if err := o.applyHostRoster(cfg.Hosts); err != nil {
		log.Printf("catway: server.reload_config: hosts: %v (keeping the running roster)", err)
		return err
	}
	log.Printf("catway: reloaded config from %s — theme and hosts applied live, keybindings apply to new page loads; other server settings need a restart", path)
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
		Mouse: rt.modes.MouseMode != terminal.MouseNone, AltScreen: rt.modes.AlternateScreen,
		Kitty: rt.modes.KittyKeyboardFlags})
	if t := o.effectiveTitle(pid); t != "" {
		o.broadcast(browserproto.NewPaneTitle(pid, t))
	}
	if rt.cwd != "" {
		o.broadcast(browserproto.NewPaneCwd(pid, rt.cwd))
	}
	if rt.branch != "" {
		o.broadcast(browserproto.NewPaneBranch(pid, rt.branch))
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
	dirty := o.connsDirty
	for o.connsDirty {
		o.connsDirty = false
		o.broadcast(o.clientsMsg())
	}
	// A changed connection set can change the "is anyone looking" aggregate —
	// the last focused window leaving is a blur no Focus message will ever
	// report. Deferred here with the census, and for the same reason: dropConn
	// runs mid-broadcast, where no further sends should be triggered.
	if dirty {
		o.syncAppFocus()
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
	// focused is this window's OS-level focus, per its Focus reports. Starts
	// true in registerConn: a browser that just connected is in front until it
	// says otherwise, which keeps clients that never report (older pages, ctl
	// harnesses) behaving as the pre-Focus world did. Loop-goroutine only.
	focused bool
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
	c.focused = true // in front until its first Focus report says otherwise
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
			Mouse: rt.modes.MouseMode != terminal.MouseNone, AltScreen: rt.modes.AlternateScreen,
			Kitty: rt.modes.KittyKeyboardFlags})
		// Effective title, not rt.title: a pane.rename custom name must survive
		// a page reload just like it survives a viewport switch.
		if t := o.effectiveTitle(pid); t != "" {
			o.send(c, browserproto.NewPaneTitle(pid, t))
		}
		if rt.cwd != "" {
			o.send(c, browserproto.NewPaneCwd(pid, rt.cwd))
		}
		if rt.branch != "" {
			o.send(c, browserproto.NewPaneBranch(pid, rt.branch))
		}
		if agent, state := rt.effectiveAgent(); agent != "" {
			o.send(c, browserproto.NewPaneAgent(pid, agent, state, rt.agentModel, true))
		}
		if rt.exited != nil {
			o.send(c, browserproto.NewPaneExited(pid, *rt.exited))
		}
		c.translator(pid).Reset()
		o.hostOf(rt).send(orchestration.NewRequestResync(pid))
	}
	o.send(c, o.agentsMsg())
	// The roster before the first frame settles: the host badges the layout
	// already carries are only drawn once the client knows how many hosts there
	// are, so sending it later would flash them in.
	o.send(c, o.hostsMsg())
	if m, ok := o.usageMsg(); ok {
		o.send(c, m)
	}
	if o.chat != nil {
		// The whole chat model in one message — a client joining
		// mid-conversation (or mid-permission-prompt) starts converged.
		o.send(c, o.chat.Snapshot())
	}
	o.send(c, browserproto.NewTitle(o.appTitle()))
	if !o.hostByID(o.defaultHost).connected() {
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
			o.hostOf(rt).send(orchestration.NewInput(rt.id, b))
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
			o.hostOf(rt).send(orchestration.NewInput(rt.id, b))
		case m.Kind == browserproto.MouseWheel && m.DY != 0:
			o.hostOf(rt).send(orchestration.NewScrollViewport(rt.id, int32(m.DY)))
		}

	case *browserproto.Paste:
		rt := o.inputTarget(m.Pane)
		if rt == nil {
			return
		}
		if b, err := rt.enc.Paste(m.Data); err != nil {
			log.Printf("catway: paste encode: %v", err)
		} else if len(b) > 0 {
			o.hostOf(rt).send(orchestration.NewInput(rt.id, b))
		}

	case *browserproto.Focus:
		// Viewers count: a phone in the foreground is somebody looking, even
		// if it never owns the geometry.
		if c == nil || c.focused == m.Focused {
			return
		}
		c.focused = m.Focused
		o.syncAppFocus()

	case *browserproto.Raw:
		id, ok := o.session.FocusedPane()
		if ok && len(m.Data) > 0 {
			o.hostForPane(uint32(id)).send(orchestration.NewInput(uint32(id), m.Data))
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
