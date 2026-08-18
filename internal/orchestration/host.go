//go:build ghostty

package orchestration

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
	"github.com/rohanthewiz/cats/internal/detect"
	"github.com/rohanthewiz/cats/internal/gitbranch"
	"github.com/rohanthewiz/cats/internal/pathpick"
	"github.com/rohanthewiz/cats/internal/terminal"
	"github.com/rohanthewiz/cats/internal/worktree"
)

// DefaultFlushInterval coalesces dirty panes into frames at ~60 Hz, mirroring
// the Phase A requestAnimationFrame coalescing.
const DefaultFlushInterval = 16 * time.Millisecond

// Branch resolution pacing (pane_branch). The daemon resolves branches because
// the pane's cwd is a path on *this* filesystem — see gitbranch.
const (
	// branchSweepInterval paces the background refresh, which is what catches a
	// `git checkout` in a pane whose directory never changes: there is no OSC
	// event for a branch change, so nothing else would notice it.
	branchSweepInterval = 10 * time.Second
	// branchRefreshInterval floors the gap between two reads for the *same*
	// directory, so a burst of sweeps costs one read. A pane that has moved is
	// exempt: a cd is precisely when the label is knowably wrong, and the user
	// expects the pair to land together.
	branchRefreshInterval = 3 * time.Second
)

// pane is one terminal: a PTY + go-libghostty emulator + child process.
type pane struct {
	id   uint32
	emu  terminal.Emulator
	ptmx *os.File
	cmd  *exec.Cmd

	dirty atomic.Bool

	// detectSeq counts non-empty PTY reads; detectPump uses it to skip a redundant
	// screen scan when an idle agent has produced no new output (Stage C content-skip).
	detectSeq atomic.Uint64

	// emuMu serializes all emulator access (the emulator is not concurrency
	// safe) and guards prev/closed.
	emuMu  sync.Mutex
	prev   *terminal.Snapshot // last snapshot sent, for diffing
	closed bool

	// OSC passthrough scanners, owned exclusively by this pane's readPump goroutine
	// (libghostty-vt does not surface OSC 7 cwd, so we scan the raw byte stream).
	osc      oscScanner
	osc133   osc133Scanner    // OSC 133/633 shell-integration marks, for the command ledger
	cmds     cmdTracker       // pairs those marks into commands; readPump-owned like the scanners
	osc52    osc52Scanner     // OSC 52 clipboard writes (also not surfaced by go-libghostty)
	osc9     osc9Scanner      // OSC 9 progress, owned by readPump; latest published to progress
	oscTitle oscTitleScanner  // OSC 0/2 window title, for the pane_title chrome event
	xtmod    xtmodkeysScanner // XTMODKEYS modifyOtherKeys (also not surfaced)

	// modifyOtherKeys is the scanner's current state, published by readPump and
	// read by the flusher/resync when reporting pane_modes.
	modifyOtherKeys atomic.Bool

	// streamOutput, when set (via set_output_stream), makes readPump emit a
	// pane_output event carrying each raw PTY chunk it reads — the byte stream the
	// orchestrator matches pane.wait_for_output against. Off by default so a pane
	// with no waiter never streams raw bytes. Written by the dispatch goroutine,
	// read by readPump.
	streamOutput atomic.Bool

	// oscCwd records that this pane's shell has reported a cwd over OSC 7 at least
	// once, which retires detectPump's process probe for it (readPump writes,
	// detectPump reads).
	oscCwd atomic.Bool

	// metaMu guards the last-emitted "chrome" — cwd/title/agent — so a reconnecting
	// client can be resynced with the pane's current state by another goroutine.
	// readPump writes title (and cwd from OSC 7); detectPump writes the agent fields
	// (and cwd from the process probe); resyncPane reads all of them.
	metaMu         sync.Mutex
	lastPwd        string // last cwd emitted, for change detection + resync
	lastTitle      string // last OSC 0/2 title emitted, for change detection + resync
	lastAgent      string // last pane_agent identity ("" = plain shell)
	lastAgentState string // last pane_agent state (idle|working|blocked|unknown)
	lastVisBlocker bool
	lastVisWorking bool
	hasAgent       bool // a pane_agent has been emitted at least once
	// Branch state, written by the branch pump and read by resync. branchCwd is
	// the directory lastBranch was resolved for, which is what separates "we
	// already answered this" from "the pane moved" — the throttle only applies
	// to the former.
	lastBranch string
	branchCwd  string
	branchAt   time.Time
	hasBranch  bool // a pane_branch has been emitted at least once

	// lastModes is the input-mode state last reported via pane_modes; the flusher
	// re-queries after a dirty frame and emits only on change. hasModes guards the
	// first report. Owned by the single flusher goroutine (no lock needed).
	lastModes terminal.InputModes
	hasModes  bool

	// progress holds the latest OSC 9 progress payload (readPump writes, detectPump
	// reads). nil = none retained; detectPump clears it on agent change so a new
	// agent does not inherit the previous process's progress.
	progress atomic.Pointer[string]

	// ptyMu serializes writes to the PTY master (user input + the emulator's
	// query-response callback can both write).
	ptyMu sync.Mutex
}

func (p *pane) writePTY(b []byte) error {
	p.ptyMu.Lock()
	defer p.ptyMu.Unlock()
	_, err := p.ptmx.Write(b)
	return err
}

// childPid is the pane's own process — the shell whose cwd the working-directory
// probe reads. 0 when the child never started (nothing to inspect).
func (p *pane) childPid() int {
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// setCwdMeta records a new cwd and reports whether it changed (so the caller only
// emits pane_cwd on a real change). Held under metaMu so resyncPane can read it.
func (p *pane) setCwdMeta(cwd string) (changed bool) {
	p.metaMu.Lock()
	defer p.metaMu.Unlock()
	if cwd == p.lastPwd {
		return false
	}
	p.lastPwd = cwd
	return true
}

// cwdMeta reads the pane's last reported working directory. Held under metaMu
// because readPump writes it and the ledger scanner (also readPump) and
// resyncPane (dispatch goroutine) both read it.
func (p *pane) cwdMeta() string {
	p.metaMu.Lock()
	defer p.metaMu.Unlock()
	return p.lastPwd
}

// setTitleMeta records a new title and reports whether it changed.
func (p *pane) setTitleMeta(title string) (changed bool) {
	p.metaMu.Lock()
	defer p.metaMu.Unlock()
	if title == p.lastTitle {
		return false
	}
	p.lastTitle = title
	return true
}

// setAgentMeta records the last-emitted agent identity/state for resync. Called
// by detectPump alongside every pane_agent emission.
func (p *pane) setAgentMeta(agent, state string, visBlocker, visWorking bool) {
	p.metaMu.Lock()
	p.lastAgent, p.lastAgentState = agent, state
	p.lastVisBlocker, p.lastVisWorking = visBlocker, visWorking
	p.hasAgent = true
	p.metaMu.Unlock()
}

// branchDue reports the directory whose branch is worth resolving now, if any:
// nothing when the pane has no known cwd, and nothing when the same directory
// was resolved within branchRefreshInterval. A pane that has moved since its
// last resolution is always due.
func (p *pane) branchDue(now time.Time) (cwd string, ok bool) {
	p.metaMu.Lock()
	defer p.metaMu.Unlock()
	if p.lastPwd == "" {
		return "", false
	}
	if p.hasBranch && p.branchCwd == p.lastPwd && now.Sub(p.branchAt) < branchRefreshInterval {
		return "", false
	}
	return p.lastPwd, true
}

// setBranchMeta records a resolution and reports whether it changed what the
// client has been told. The first resolution always counts as a change — even
// to "" — because that empty answer is how the orchestrator learns this daemon
// owns the pane's branch and should stop resolving it itself.
func (p *pane) setBranchMeta(cwd, branch string, now time.Time) (changed bool) {
	p.metaMu.Lock()
	defer p.metaMu.Unlock()
	p.branchCwd, p.branchAt = cwd, now
	if p.hasBranch && p.lastBranch == branch {
		return false
	}
	p.lastBranch, p.hasBranch = branch, true
	return true
}

// Host is the Go terminal backend: it owns panes and serves the orchestration
// protocol. In managed mode (Serve) panes are torn down when the single
// connection ends. In persistent mode the panes — PTYs, emulators, detection —
// outlive any one connection: a client can Attach, drop, and a later client can
// reconnect and resync, so live shells survive a cats restart or binary handoff.
// One client attaches at a time (single-writer); Attach is called serially.
type Host struct {
	FlushInterval time.Duration

	// Persistent keeps panes alive across connection drops and arms the idle
	// timeout. Managed mode (Serve) leaves it false.
	Persistent bool
	// IdleTimeout exits a persistent daemon if no client is attached for this long
	// (a crashed cats that never reconnects). Zero disables it. Only consulted in
	// persistent mode.
	IdleTimeout time.Duration

	// RequireToken, when set, is the bearer token a client's hello must carry.
	// Empty means no authentication, which is right for a unix socket (the
	// socket's file permissions are the gate) and wrong for anything reachable
	// over the network — cathost refuses to open a tcp/tls listener without one.
	RequireToken string

	// branchWake nudges the branch pump out of its sweep interval when a pane's
	// cwd changes. Buffered depth 1 and sent non-blocking: the pump re-reads
	// every pane anyway, so a nudge that arrives while one is already pending is
	// already accounted for.
	branchWake chan struct{}

	mu    sync.Mutex
	panes map[uint32]*pane

	// connMu guards the currently-attached client's outbound sink. out is nil when
	// no client is attached (emit drops); sessDone is closed when the current
	// attachment ends, so an in-flight emit on the old channel unblocks.
	connMu   sync.Mutex
	out      chan any
	sessDone chan struct{}

	closed     chan struct{} // closed by Stop; pumps/emit bail on it
	closedOnce sync.Once
	startOnce  sync.Once

	exit     chan struct{} // closed on shutdown command / idle timeout; main waits on it
	exitOnce sync.Once

	idleMu    sync.Mutex
	idleTimer *time.Timer

	// The host-stats subscription (hoststats.go): nil until a client asks, and
	// dropped again when it stops asking or the session ends.
	statsFields
	// The hook relay (hookrelay.go): a socket on this machine that carries
	// agent hook reports back to the orchestrator.
	relayFields
	// The control relay (controlrelay.go): a socket on this machine that
	// carries the orchestrator's own control API, when the orchestrator has
	// been told to trust this host with it.
	controlFields
	// The command-ledger subscription (ledger.go): off until a client asks for
	// shell-integration marks, and off again when it stops.
	ledgerFields
}

// NewHost creates an empty Host.
func NewHost() *Host {
	return &Host{
		FlushInterval: DefaultFlushInterval,
		panes:         make(map[uint32]*pane),
		closed:        make(chan struct{}),
		exit:          make(chan struct{}),
		branchWake:    make(chan struct{}, 1),
	}
}

// Start launches the daemon-lifetime flusher (panes coalesce into frames whether
// or not a client is attached) and arms the idle timeout. Call once before the
// first Attach. ctx bounds the flusher; Stop tears everything down.
func (h *Host) Start(ctx context.Context) {
	h.startOnce.Do(func() {
		// Before the first welcome can be written, since the welcome advertises
		// their paths and a pane created off that welcome must find them there.
		h.startHookRelay()
		h.startControlRelay()
		h.armIdle() // exit if a persistent daemon is spawned but no client ever attaches
		go h.branchPump(ctx)
		go func() {
			ticker := time.NewTicker(h.FlushInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-h.closed:
					return
				case <-ticker.C:
					h.flushDirty()
				}
			}
		}()
	})
}

// Attach binds conn as the active client and runs the read/write loop until the
// connection closes or ctx is cancelled. It does NOT tear down panes on return —
// in persistent mode they keep running for the next client to reconnect and
// resync. Returns the read error (nil on a clean EOF).
func (h *Host) Attach(ctx context.Context, conn io.ReadWriteCloser) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	out := make(chan any, 256)
	sessDone := make(chan struct{})
	h.connMu.Lock()
	h.out = out
	h.sessDone = sessDone
	h.connMu.Unlock()
	h.disarmIdle()

	// Close the connection on ctx cancellation so a blocked read unblocks and the
	// session ends on daemon shutdown, not just on a client EOF.
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-sessDone:
		}
	}()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // writer: drain outbound events to the connection
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-sessDone:
				return
			case ev := <-out:
				if _, end := ev.(endSession); end {
					// Everything queued ahead of the sentinel is on the wire by
					// now, so the rejection welcome has been delivered; cancel
					// closes the connection, which unblocks the reader.
					cancel()
					return
				}
				if err := WriteMessage(conn, ev); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	var readErr, fatalErr error
	for {
		typ, payload, err := ReadMessage(conn)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				readErr = err
			}
			break
		}
		// A rejected handshake is the only fatal dispatch. We do not break here:
		// the rejection welcome is still queued behind the writer, and leaving
		// the loop would close sessDone and drop it — leaving the client with a
		// bare disconnect, unable to tell a bad token from a dead daemon. The
		// writer ends the session once it reaches the sentinel, and the read
		// below fails on the closed connection.
		if err := h.dispatch(typ, payload); err != nil {
			fatalErr = err
		}
	}

	// Detach: stop routing events to this connection (new emits drop), then unblock
	// any in-flight emit/writer on the old channel. Panes are left running.
	h.connMu.Lock()
	h.out = nil
	h.sessDone = nil
	h.connMu.Unlock()
	// The subscription belongs to the connection that asked for it. Left
	// running, a persistent daemon would go on sampling (and, on darwin, keep an
	// iostat alive) for a client that has gone.
	h.stopHostStats()
	h.stopCommandMarks()
	close(sessDone)
	cancel()
	wg.Wait()
	h.armIdle()
	if fatalErr != nil {
		return fatalErr // why we hung up beats "connection closed" as the log line
	}
	return readErr
}

// endSession is a writer sentinel: everything queued before it reaches the wire,
// then the session ends. It exists so a rejected handshake can *explain itself*
// before the connection goes — the welcome carrying the reason is queued like
// any other event, and a plain close would race it.
type endSession struct{}

// Stop tears down all panes and signals the flusher/pumps to exit. Idempotent.
func (h *Host) Stop() {
	h.closedOnce.Do(func() { close(h.closed) })
	h.stopHostStats()
	h.stopHookRelay()
	h.stopControlRelay()
	h.shutdownAll()
}

// Exit is closed when the daemon should stop accepting and exit — a clean-quit
// shutdown command or the idle timeout firing. The accept loop selects on it.
func (h *Host) Exit() <-chan struct{} { return h.exit }

// requestExit signals the accept loop to exit (shutdown command / idle timeout).
func (h *Host) requestExit() { h.exitOnce.Do(func() { close(h.exit) }) }

// armIdle (persistent mode only) schedules an exit if no client reconnects within
// IdleTimeout. Called when no client is attached.
func (h *Host) armIdle() {
	if !h.Persistent || h.IdleTimeout <= 0 {
		return
	}
	h.idleMu.Lock()
	if h.idleTimer != nil {
		h.idleTimer.Stop()
	}
	h.idleTimer = time.AfterFunc(h.IdleTimeout, h.requestExit)
	h.idleMu.Unlock()
}

// disarmIdle cancels a pending idle exit (a client just attached).
func (h *Host) disarmIdle() {
	h.idleMu.Lock()
	if h.idleTimer != nil {
		h.idleTimer.Stop()
		h.idleTimer = nil
	}
	h.idleMu.Unlock()
}

// Serve runs a single connection to completion and tears down all panes — the
// managed-mode entry (the orchestrator owns our lifecycle and we exit when it
// disconnects). Persistent reconnects use Start/Attach/Stop directly.
func (h *Host) Serve(ctx context.Context, conn io.ReadWriteCloser) error {
	h.Start(ctx)
	err := h.Attach(ctx, conn)
	h.Stop()
	return err
}

// dispatch handles one client message. It returns a non-nil error only when the
// session must end — today, a hello this daemon refuses (unsupported protocol
// version, bad token). Everything else reports failures to the client as error
// events and keeps the connection.
func (h *Host) dispatch(typ MessageType, payload []byte) error {
	switch typ {
	case MsgHello:
		return h.handleHello(payload)
	case MsgShutdown:
		h.requestExit()
	case MsgRequestResync:
		var c RequestResync
		if err := json.Unmarshal(payload, &c); err != nil {
			h.emit(NewError(0, "bad request_resync: "+err.Error()))
			return nil
		}
		if p := h.getPane(c.PaneID); p != nil {
			h.resyncPane(p)
		}
	case MsgSetOutputStream:
		var c SetOutputStream
		if err := json.Unmarshal(payload, &c); err != nil {
			h.emit(NewError(0, "bad set_output_stream: "+err.Error()))
			return nil
		}
		if p := h.getPane(c.PaneID); p != nil {
			p.streamOutput.Store(c.Enabled) // readPump picks it up on its next read
		}
	case MsgCreatePane:
		var c CreatePane
		if err := json.Unmarshal(payload, &c); err != nil {
			h.emit(NewError(0, "bad create_pane: "+err.Error()))
			return nil
		}
		if err := h.createPane(c); err != nil {
			h.emit(NewError(c.PaneID, err.Error()))
		}
	case MsgInput:
		var c Input
		if err := json.Unmarshal(payload, &c); err != nil {
			h.emit(NewError(0, "bad input: "+err.Error()))
			return nil
		}
		if p := h.getPane(c.PaneID); p != nil {
			if err := p.writePTY(c.Data); err != nil {
				h.emit(NewError(c.PaneID, err.Error()))
			}
		} else {
			h.emit(NewError(c.PaneID, "no such pane"))
		}
	case MsgResize:
		var c Resize
		if err := json.Unmarshal(payload, &c); err != nil {
			h.emit(NewError(0, "bad resize: "+err.Error()))
			return nil
		}
		if err := h.resizePane(c); err != nil {
			h.emit(NewError(c.PaneID, err.Error()))
		}
	case MsgClosePane:
		var c ClosePane
		if err := json.Unmarshal(payload, &c); err != nil {
			h.emit(NewError(0, "bad close_pane: "+err.Error()))
			return nil
		}
		if p := h.removePane(c.PaneID); p != nil {
			h.closePane(p) // read pump observes EOF and emits pane_exited
		}
	case MsgScrollViewport:
		var c ScrollViewport
		if err := json.Unmarshal(payload, &c); err != nil {
			h.emit(NewError(0, "bad scroll_viewport: "+err.Error()))
			return nil
		}
		if err := h.scrollPane(c); err != nil {
			h.emit(NewError(c.PaneID, err.Error()))
		}
	case MsgRequestSelection:
		var c RequestSelection
		if err := json.Unmarshal(payload, &c); err != nil {
			h.emit(NewError(0, "bad request_selection: "+err.Error()))
			return nil
		}
		if err := h.requestSelection(c); err != nil {
			h.emit(NewError(c.PaneID, err.Error()))
		}
	case MsgRequestText:
		var c RequestText
		if err := json.Unmarshal(payload, &c); err != nil {
			h.emit(NewError(0, "bad request_text: "+err.Error()))
			return nil
		}
		if err := h.requestText(c); err != nil {
			h.emit(NewError(c.PaneID, err.Error()))
		}
	case MsgControlReply:
		var c ControlReply
		if err := json.Unmarshal(payload, &c); err != nil {
			h.emit(NewError(0, "bad control_reply: "+err.Error()))
			return nil
		}
		h.deliverControlReply(c)
	case MsgControlClose:
		var c ControlClose
		if err := json.Unmarshal(payload, &c); err != nil {
			h.emit(NewError(0, "bad control_close: "+err.Error()))
			return nil
		}
		h.closeControlConn(c)
	case MsgHookReply:
		var c HookReply
		if err := json.Unmarshal(payload, &c); err != nil {
			h.emit(NewError(0, "bad hook_reply: "+err.Error()))
			return nil
		}
		h.deliverHookReply(c)
	case MsgRequestListDir:
		var c RequestListDir
		if err := json.Unmarshal(payload, &c); err != nil {
			h.emit(NewError(0, "bad request_list_dir: "+err.Error()))
			return nil
		}
		// Off the dispatch goroutine: a listing of a cold network mount takes as
		// long as the mount does, and the connection's reader is what every
		// keystroke in every pane arrives through. The reply is emitted from
		// there, which is fine — emit is the queue, and a client matches replies
		// to requests per pane in order, so two listings for one pane cannot
		// overtake each other because a pane's requests arrive one at a time.
		go func() {
			h.emit(NewDirListing(c.PaneID, pathpick.List(c.Dir, c.Base, c.Recents, c.Live)))
		}()
	case MsgRequestWorktree:
		var c RequestWorktree
		if err := json.Unmarshal(payload, &c); err != nil {
			h.emit(NewError(0, "bad request_worktree: "+err.Error()))
			return nil
		}
		// Off the dispatch goroutine, and for a stronger reason than the listing
		// above: `git worktree add` checks out a whole tree, which on a large
		// repository takes seconds to minutes. The connection's reader is what
		// every keystroke in every pane arrives through, so running git here
		// would freeze the machine's terminals for the duration.
		//
		// Which is also why the reply is id-matched rather than ordered: two
		// operations started in one order finish in whichever order git
		// finishes them.
		go func() {
			h.emit(NewWorktreeResult(c.ID, worktree.Do(c.Req)))
		}()
	case MsgRequestCommandMarks:
		var c RequestCommandMarks
		if err := json.Unmarshal(payload, &c); err != nil {
			h.emit(NewError(0, "bad request_command_marks: "+err.Error()))
			return nil
		}
		h.requestCommandMarks(c)
	case MsgRequestHostStats:
		var c RequestHostStats
		if err := json.Unmarshal(payload, &c); err != nil {
			h.emit(NewError(0, "bad request_host_stats: "+err.Error()))
			return nil
		}
		h.requestHostStats(c)
	case MsgPing:
		var c Ping
		if err := json.Unmarshal(payload, &c); err != nil {
			h.emit(NewError(0, "bad ping: "+err.Error()))
			return nil
		}
		// Queued like any other event rather than written straight back. That
		// is the measurement the client is asking for: a pong that overtook the
		// pane frames ahead of it would report a healthy link on a daemon whose
		// output the user is watching arrive in slow motion.
		h.emit(NewPong(c.ID))
	default:
		h.emit(NewError(0, "unknown message type: "+string(typ)))
	}
	return nil
}

// handleHello authenticates the client, then answers with a welcome listing the
// live pane IDs and replays each pane's current state (full frame + modes + cwd
// + title + agent + branch). On a fresh daemon the list is empty and nothing is
// replayed; on a reconnect the client reconciles its restored session against
// these surviving panes and adopts them instead of re-creating them.
//
// The payload was ignored entirely before v3 — which was defensible while both
// ends only ever met across a unix socket on one machine, and is not once a
// daemon listens on a port. Two things are checked now: that the peer's protocol
// version is one this build can serve, and that it presents the token when one
// is required. A refusal returns an error so the caller ends the session, after
// the welcome carrying the reason has been queued.
func (h *Host) handleHello(payload []byte) error {
	var c Hello
	if err := json.Unmarshal(payload, &c); err != nil {
		return h.rejectHello("bad hello: " + err.Error())
	}
	version := NegotiateVersion(c.ProtocolVersion)
	if version == 0 {
		return h.rejectHello(fmt.Sprintf("protocol version %d unsupported: this daemon speaks %d..%d",
			c.ProtocolVersion, MinProtocolVersion, ProtocolVersion))
	}
	if h.RequireToken != "" {
		// Constant time, because the comparison is against a secret and the
		// client controls one side of it. Unequal lengths compare false here
		// without an early return.
		if subtle.ConstantTimeCompare([]byte(c.Token), []byte(h.RequireToken)) != 1 {
			return h.rejectHello("authentication failed: bad or missing token")
		}
	}

	h.mu.Lock()
	ids := make([]uint32, 0, len(h.panes))
	ps := make([]*pane, 0, len(h.panes))
	for id, p := range h.panes {
		ids = append(ids, id)
		ps = append(ps, p)
	}
	h.mu.Unlock()

	// The welcome reports the *negotiated* version, not ours: an older client
	// demands equality with what it sent, so answering with a newer number is
	// how a rolled-out daemon would break every catway not yet upgraded.
	w := NewWelcomeAt(version, "", ids)
	// The path the client injects into every pane it creates here. Filled in on
	// the way out rather than by NewWelcomeAt, which knows nothing about this
	// daemon's sockets.
	w.HookSocket = h.hookSocketPath()
	w.ControlSocket = h.controlSocketPath()
	h.emit(w)
	for _, p := range ps {
		h.resyncPane(p)
	}
	return nil
}

// rejectHello queues the refusal welcome, then the sentinel that ends the
// session once it has been written, and returns the reason for the daemon's own
// log. The client is told why in the same breath: a silent close would look
// identical to a dead socket, and "check the token" is not a guess an operator
// should have to make.
func (h *Host) rejectHello(reason string) error {
	h.emit(NewWelcomeAt(ProtocolVersion, reason, nil))
	h.emit(endSession{})
	return errors.New("rejected client: " + reason)
}

// resyncPane replays a pane's current state to the freshly-attached client: a
// full frame (re-baselining the diff against what the client now holds), the
// current input modes, and the last-known cwd/title/agent. Used on reconnect so
// an adopted pane is immediately consistent without waiting for new output.
func (h *Host) resyncPane(p *pane) {
	p.emuMu.Lock()
	if p.closed {
		p.emuMu.Unlock()
		return
	}
	snap, err := p.emu.Snapshot()
	var modes terminal.InputModes
	var modesErr error
	if err == nil {
		p.prev = snap // re-baseline: subsequent diffs are relative to this full frame
		modes, modesErr = p.emu.InputModes()
	}
	p.emuMu.Unlock()
	if err != nil {
		return
	}

	h.emit(NewPaneFrame(p.id, FrameFromSnapshot(snap, nil))) // full frame
	modes.ModifyOtherKeys = p.modifyOtherKeys.Load()
	if modesErr == nil {
		// Emit current modes directly; don't touch the flusher-owned lastModes/hasModes.
		// Re-sending modes the client already has is an idempotent mirror update.
		h.emit(NewPaneModes(p.id, modes))
	}

	p.metaMu.Lock()
	cwd, title := p.lastPwd, p.lastTitle
	agent, state := p.lastAgent, p.lastAgentState
	vb, vw, hasAgent := p.lastVisBlocker, p.lastVisWorking, p.hasAgent
	branch, hasBranch := p.lastBranch, p.hasBranch
	p.metaMu.Unlock()
	if cwd != "" {
		h.emit(NewPaneCwd(p.id, cwd))
	}
	if title != "" {
		h.emit(NewPaneTitle(p.id, title))
	}
	if hasAgent {
		h.emit(NewPaneAgent(p.id, agent, state, vb, vw))
	}
	// Replayed even when empty: a reconnecting client has to learn that this
	// pane's branch is the daemon's to report, and "" is a real answer (the
	// pane is not in a repository), not a missing one.
	if hasBranch {
		h.emit(NewPaneBranch(p.id, branch))
	}
}

func (h *Host) createPane(c CreatePane) error {
	// The requested cwd was chosen on the orchestrator's machine — inherited
	// from a neighbouring pane, restored from a session file, or typed into a
	// dialog — and this daemon may be on a different one, where that path
	// simply does not exist. exec would fail with chdir ENOENT and the pane
	// would be born dead, which is the worst possible answer for a directory
	// that was only ever a suggestion. Fall back to the home directory and say
	// so instead.
	cwd, cwdNote := h.resolveSpawnCwd(c.Cwd)

	name := c.Command
	if name == "" {
		name = defaultShell()
	}
	cmd := exec.Command(name, c.Args...)
	cmd.Env = buildEnv(c.Env)
	if cwd != "" {
		cmd.Dir = cwd
	}

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: c.Cols, Rows: c.Rows})
	if err != nil && c.Command != "" {
		// A pane with an explicit command (an agent-session resume) degrades to
		// the default shell rather than a dead pane — cats types the resume
		// command into a shell, so a missing agent binary leaves a usable shell
		// there; match that outcome.
		h.emit(NewError(c.PaneID, fmt.Sprintf("command %q: %v — falling back to shell", name, err)))
		cmd = exec.Command(defaultShell())
		cmd.Env = buildEnv(c.Env)
		if cwd != "" {
			cmd.Dir = cwd
		}
		ptmx, err = pty.StartWithSize(cmd, &pty.Winsize{Cols: c.Cols, Rows: c.Rows})
	}
	if err != nil {
		return fmt.Errorf("start pty: %w", err)
	}

	p := &pane{id: c.PaneID, ptmx: ptmx, cmd: cmd}
	// Seed the pane's cwd with the directory it spawned in, so consumers (the
	// browser tooltip, worktree anchoring, session persistence) have a pwd even
	// when the shell never reports OSC 7. A real OSC 7 later overwrites this via
	// setCwdMeta's change detection, so shells that do report stay authoritative.
	spawnDir := cwd
	if spawnDir == "" {
		// No explicit dir means the PTY inherited the daemon's process cwd.
		spawnDir, _ = os.Getwd()
	}
	p.lastPwd = spawnDir
	emu, err := terminal.New(c.Cols, c.Rows, terminal.WithWritePTY(func(d []byte) {
		_ = p.writePTY(d)
	}))
	if err != nil {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		return fmt.Errorf("new emulator: %w", err)
	}
	p.emu = emu

	// Seed restored scrollback before the child's output starts rendering, so it
	// appears as history above the live shell. Safe to write directly: the read
	// pump isn't running yet, so nothing else touches the emulator.
	if c.InitialHistory != "" {
		_, _ = emu.Write([]byte(c.InitialHistory))
	}

	h.mu.Lock()
	h.panes[p.id] = p
	h.mu.Unlock()

	// Announce the seeded spawn cwd immediately — without this, panes whose
	// shell never emits OSC 7 would show no directory at all downstream.
	if spawnDir != "" {
		h.emit(NewPaneCwd(p.id, spawnDir))
		h.wakeBranch()
	}
	// Reported after the pane exists so the toast is attributable to it: the
	// pane is usable, it just isn't where the caller asked for.
	if cwdNote != "" {
		h.emit(NewError(p.id, cwdNote))
	}

	go h.readPump(p)
	go h.detectPump(p)
	return nil
}

// readPump copies PTY output into the emulator until the child exits, then emits
// a final frame and pane_exited.
func (h *Host) readPump(p *pane) {
	buf := make([]byte, 32*1024)
	for {
		n, err := p.ptmx.Read(buf)
		if n > 0 {
			h.feed(p, buf[:n])
			p.dirty.Store(true)
			p.detectSeq.Add(1) // mark new content for the detector's content-skip
			// Stream the raw chunk to a subscribed orchestrator (pane.wait_for_output).
			// Emitted here in the read loop — before the EOF break below emits
			// pane_exited — so a pattern in the child's final output is delivered for
			// matching ahead of the exit. Copy: buf is reused and emit is async.
			if p.streamOutput.Load() {
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				h.emit(NewPaneOutput(p.id, chunk))
			}
			// Scan the raw stream for OSC passthrough the emulator doesn't surface.
			if cwd, ok := p.osc.scan(buf[:n]); ok {
				// A shell that reports its own cwd is authoritative from here on —
				// it can name a directory the local process probe cannot see at all
				// (an ssh session's remote path), so the probe stands down.
				p.oscCwd.Store(true)
				if p.setCwdMeta(cwd) {
					h.emit(NewPaneCwd(p.id, cwd))
					h.wakeBranch() // the branch is resolved against this cwd
				}
			}
			for _, clip := range p.osc52.scan(buf[:n]) {
				h.emit(NewPaneClipboard(p.id, clip))
			}
			if prog, ok := p.osc9.scan(buf[:n]); ok {
				p.progress.Store(&prog)
			}
			if title, ok := p.oscTitle.scan(buf[:n]); ok && p.setTitleMeta(title) {
				h.emit(NewPaneTitle(p.id, title))
			}
			if v, changed := p.xtmod.scan(buf[:n]); changed {
				p.modifyOtherKeys.Store(v)
			}
			// Last, and only on a subscription: the ledger scan is the one pass
			// here that most sessions never ask for.
			if h.commandMarksOn() {
				h.scanCommandMarks(p, buf[:n])
			}
		}
		if err != nil { // EOF / EIO when the child exits or the PTY closes
			break
		}
	}

	h.removePane(p.id) // stop the flusher from touching it
	if f, err := h.takeFrame(p); err == nil && f != nil {
		h.emit(NewPaneFrame(p.id, f))
	}
	h.closePane(p)
	h.emit(NewPaneExited(p.id, exitCode(p.cmd.Wait())))
}

// detectPump probes the pane's foreground process group for agent identity and
// runs the agent's detection manifest over the screen to classify state, emitting
// a pane_agent event whenever the debounced result changes. Identity is
// process-based; state (idle/working/blocked) comes from the manifest rules on the
// screen + OSC title.
//
// Stage C — driver parity: the raw per-tick classification is smoothed through the
// detectstate.go state machine (ported from cats) so transient flicker doesn't
// reach the wire. Concretely: a newly-acquired agent is pinned to Idle for a
// startup grace window; Working→plain-Idle drops are debounced over several fast
// rechecks; an idle agent with no new output skips the screen scan entirely; and a
// steady visible blocker is periodically re-emitted. Identity itself is throttled
// (detectthrottle.go): the expensive foreground enumeration runs only when the
// process group changed or a recheck interval elapsed, and survives transient
// misses, so an idle pane costs ~one tcgetpgrp per tick.
func (h *Host) detectPump(p *pane) {
	state := detect.StateUnknown
	var lastVIdle, lastVBlocker, lastVWorking bool
	var lastRefresh time.Time
	var hasRefresh bool

	var graceUntil time.Time
	var graceActive bool

	var lastScanSeq uint64
	var hasLastScanSeq bool

	// lastCwdSeq is the output count at the last working-directory probe; 0 makes
	// the pane's first output trigger one.
	var lastCwdSeq uint64

	var pending pendingIdle

	// Process-probe throttle state.
	var presence agentPresence
	var lastProcessCheck time.Time
	lastForegroundPgid := noPGID
	var hasProcessProbe bool
	var acquisitionStartedAt time.Time
	var hasAcquisition bool

	for {
		sleep := detectInterval
		if pending.active() {
			sleep = detectPendingIdleRecheck
		}
		timer := time.NewTimer(sleep)
		select {
		case <-h.closed:
			timer.Stop()
			return
		case <-timer.C:
		}
		if h.getPane(p.id) == nil {
			return // pane closed/removed
		}
		now := time.Now()

		// Working directory: the shell's own cwd, for the shells that never emit
		// OSC 7 (the default zsh/bash setup on both platforms). Skipped entirely
		// once a pane has reported one itself, and gated on new output since the
		// last probe — a `cd` always draws a fresh prompt, and an idle pane costs
		// nothing.
		if !p.oscCwd.Load() {
			if seq := p.detectSeq.Load(); seq != lastCwdSeq {
				lastCwdSeq = seq
				if cwd := detect.ProcessCwd(p.childPid()); cwd != "" && p.setCwdMeta(cwd) {
					h.emit(NewPaneCwd(p.id, cwd))
					h.wakeBranch()
				}
			}
		}

		// Identity: a cheap tcgetpgrp every tick gates the expensive enumeration.
		foregroundPgid := detect.ForegroundPGID(p.ptmx.Fd())
		groupChanged := foregroundGroupChanged(foregroundPgid, lastForegroundPgid)

		var acquisitionAge time.Duration
		if hasAcquisition {
			acquisitionAge = now.Sub(acquisitionStartedAt)
			if acquisitionAge > processAcquisitionWindow {
				hasAcquisition = false // window elapsed; stop fast-probing
			}
		}

		agentChanged := false
		if shouldProbeForegroundJob(processProbeInput{
			currentAgentPresent: presence.currentAgent() != "",
			foregroundPgid:      foregroundPgid,
			lastForegroundPgid:  lastForegroundPgid,
			hasProcessProbe:     hasProcessProbe,
			hasAcquisition:      hasAcquisition,
			acquisitionAge:      acquisitionAge,
			elapsedSinceCheck:   now.Sub(lastProcessCheck),
		}) {
			lastProcessCheck = now
			hadProcessProbe := hasProcessProbe
			hasProcessProbe = true
			prevAgent := presence.currentAgent()
			changed := presence.observeProcessProbe(detect.ForegroundAgent(p.ptmx.Fd()))
			lastForegroundPgid = foregroundPgid
			if presence.currentAgent() != "" {
				hasAcquisition = false // identified — no need to keep acquiring
			} else if hadProcessProbe && groupChanged {
				// Unidentified group change: open an acquisition window so a
				// still-starting agent is caught quickly.
				acquisitionStartedAt = now
				hasAcquisition = true
			}
			if changed {
				agentChanged = prevAgent != presence.currentAgent()
			}
		}

		agent := presence.currentAgent()
		if agentChanged {
			pending.clear()
			hasLastScanSeq = false
			hasRefresh = false
			lastVIdle, lastVBlocker, lastVWorking = false, false, false
			p.progress.Store(nil) // don't let a new agent inherit the previous progress
			if agent != "" {
				// New agent acquired: publish Idle and hold for the startup grace
				// window so startup paint doesn't register as Working.
				graceUntil = now.Add(detectStartupGrace)
				graceActive = true
				state = detect.StateIdle
				lastVIdle = true
				p.setAgentMeta(agent, string(detect.StateIdle), false, false)
				h.emit(NewPaneAgent(p.id, agent, string(detect.StateIdle), false, false))
			} else {
				// Agent gone: report the pane back to a plain shell.
				graceActive = false
				state = detect.StateUnknown
				p.setAgentMeta("", string(detect.StateUnknown), false, false)
				h.emit(NewPaneAgent(p.id, "", string(detect.StateUnknown), false, false))
			}
			continue
		}

		if agent == "" {
			continue // plain shell: nothing to classify
		}

		// Startup grace: keep the held Idle until the window elapses.
		if graceActive {
			if now.Before(graceUntil) {
				pending.clear()
				continue
			}
			graceActive = false
			hasLastScanSeq = false
			pending.clear()
			continue
		}

		// Content-skip: while idle with no new PTY bytes, skip the screen scan.
		currentSeq := p.detectSeq.Load()
		if shouldSkipIdleScreenScan(state, true, pending.active(), false, false, currentSeq, lastScanSeq, hasLastScanSeq) {
			continue
		}

		screen, title := h.paneScreenAndTitle(p)
		lastScanSeq = currentSeq
		hasLastScanSeq = true

		progress := ""
		if pp := p.progress.Load(); pp != nil {
			progress = *pp
		}
		d := detect.Detect(agent, detect.Input{Screen: screen, OscTitle: title, OscProgress: progress})
		if d.SkipStateUpdate {
			pending.clear()
			continue // e.g. transcript viewer / model picker — keep last reported state
		}

		prev := publishState{state: state, visibleIdle: lastVIdle, visibleBlocker: lastVBlocker, visibleWorking: lastVWorking}
		next := publishState{state: d.State, visibleIdle: d.VisibleIdle, visibleBlocker: d.VisibleBlocker, visibleWorking: d.VisibleWorking}

		refreshDue := stableVisibleSignalRefreshDue(prev, next, lastRefresh, hasRefresh, now)
		if !decideDetectionTransition(prev, next, false, false, refreshDue, now, &pending) {
			continue
		}

		state = next.state
		lastVIdle, lastVBlocker, lastVWorking = next.visibleIdle, next.visibleBlocker, next.visibleWorking
		if next.visibleBlocker || next.visibleWorking {
			lastRefresh = now
			hasRefresh = true
		} else {
			hasRefresh = false
		}
		p.setAgentMeta(agent, string(next.state), next.visibleBlocker, next.visibleWorking)
		h.emit(NewPaneAgent(p.id, agent, string(next.state), next.visibleBlocker, next.visibleWorking))
	}
}

// paneScreenAndTitle snapshots the pane's screen (rows joined by '\n', trailing
// blanks trimmed) and OSC title for detection — all under emuMu.
func (h *Host) paneScreenAndTitle(p *pane) (screen, title string) {
	p.emuMu.Lock()
	defer p.emuMu.Unlock()
	if p.closed {
		return "", ""
	}
	if t, err := p.emu.Title(); err == nil {
		title = t
	}
	snap, err := p.emu.Snapshot()
	if err != nil {
		return "", title
	}
	rows := make([]string, len(snap.Cells))
	for i, row := range snap.Cells {
		var b strings.Builder
		for _, cell := range row {
			if cell.Rune == "" {
				b.WriteByte(' ')
			} else {
				b.WriteString(cell.Rune)
			}
		}
		rows[i] = strings.TrimRight(b.String(), " ")
	}
	return strings.Join(rows, "\n"), title
}

func (h *Host) feed(p *pane, b []byte) {
	p.emuMu.Lock()
	defer p.emuMu.Unlock()
	if p.closed {
		return
	}
	_, _ = p.emu.Write(b)
}

// takeFrame snapshots the pane, diffs against the last sent snapshot, and
// records the new snapshot — all under emuMu. Returns (nil, nil) if closed.
func (h *Host) takeFrame(p *pane) (*Frame, error) {
	p.emuMu.Lock()
	defer p.emuMu.Unlock()
	if p.closed {
		return nil, nil
	}
	snap, err := p.emu.Snapshot()
	if err != nil {
		return nil, err
	}
	f := FrameFromSnapshot(snap, p.prev)
	p.prev = snap
	return f, nil
}

func (h *Host) resizePane(c Resize) error {
	p := h.getPane(c.PaneID)
	if p == nil {
		return errors.New("no such pane")
	}
	p.ptyMu.Lock()
	err := pty.Setsize(p.ptmx, &pty.Winsize{Cols: c.Cols, Rows: c.Rows})
	p.ptyMu.Unlock()
	if err != nil {
		return fmt.Errorf("pty resize: %w", err)
	}

	p.emuMu.Lock()
	if !p.closed {
		err = p.emu.Resize(c.Cols, c.Rows)
	}
	p.emuMu.Unlock()
	if err != nil {
		return fmt.Errorf("emulator resize: %w", err)
	}
	p.dirty.Store(true) // dimensions changed ⇒ next frame is full
	return nil
}

func (h *Host) scrollPane(c ScrollViewport) error {
	p := h.getPane(c.PaneID)
	if p == nil {
		return errors.New("no such pane")
	}
	p.emuMu.Lock()
	defer p.emuMu.Unlock()
	if p.closed {
		return nil
	}
	if err := p.emu.Scroll(int(c.Delta)); err != nil {
		return fmt.Errorf("scroll: %w", err)
	}
	p.dirty.Store(true) // viewport moved ⇒ emit a frame at the new position
	return nil
}

// requestSelection extracts the text of the selection bounded by the request's
// endpoints and replies with a pane_selection event (always, so the caller gets a
// definite response). The emulator resolves the screen-buffer coordinates to text
// under emuMu.
func (h *Host) requestSelection(c RequestSelection) error {
	p := h.getPane(c.PaneID)
	if p == nil {
		return errors.New("no such pane")
	}
	anchor := terminal.SelectionEndpoint{Row: c.Anchor.Row, Col: c.Anchor.Col}
	cursor := terminal.SelectionEndpoint{Row: c.Cursor.Row, Col: c.Cursor.Col}

	p.emuMu.Lock()
	var (
		text string
		err  error
	)
	if !p.closed {
		text, err = p.emu.FormatSelection(anchor, cursor, c.Rectangle)
	}
	p.emuMu.Unlock()
	if err != nil {
		return fmt.Errorf("format selection: %w", err)
	}
	h.emit(NewPaneSelection(c.PaneID, text))
	return nil
}

// requestText extracts buffer text for a pane and replies with a pane_text event
// (always, so the caller gets a definite response). Reads under emuMu.
func (h *Host) requestText(c RequestText) error {
	p := h.getPane(c.PaneID)
	if p == nil {
		return errors.New("no such pane")
	}
	p.emuMu.Lock()
	var (
		text string
		err  error
	)
	if !p.closed {
		text, err = p.emu.ExtractText(terminal.TextScope(c.Scope), int(c.Lines), c.Ansi, c.Unwrap)
	}
	p.emuMu.Unlock()
	if err != nil {
		return fmt.Errorf("extract text: %w", err)
	}
	h.emit(NewPaneText(c.PaneID, text))
	return nil
}

func (h *Host) flushDirty() {
	h.mu.Lock()
	ps := make([]*pane, 0, len(h.panes))
	for _, p := range h.panes {
		ps = append(ps, p)
	}
	h.mu.Unlock()

	for _, p := range ps {
		if !p.dirty.Swap(false) {
			continue
		}
		f, err := h.takeFrame(p)
		if err != nil {
			h.emit(NewError(p.id, err.Error()))
			continue
		}
		if f != nil {
			h.emit(NewPaneFrame(p.id, f))
		}
		// Input modes can only change as a result of program output, so a pane that
		// just produced a frame is exactly when to re-check them.
		h.emitModeChanges(p)
	}
}

// emitModeChanges re-reads the pane's input modes and emits pane_modes if they
// changed since the last report (or on the first observation).
func (h *Host) emitModeChanges(p *pane) {
	p.emuMu.Lock()
	if p.closed {
		p.emuMu.Unlock()
		return
	}
	modes, err := p.emu.InputModes()
	p.emuMu.Unlock()
	if err != nil {
		return
	}
	modes.ModifyOtherKeys = p.modifyOtherKeys.Load()
	if p.hasModes && modes == p.lastModes {
		return
	}
	p.lastModes = modes
	p.hasModes = true
	h.emit(NewPaneModes(p.id, modes))
}

func (h *Host) closePane(p *pane) {
	p.emuMu.Lock()
	if p.closed {
		p.emuMu.Unlock()
		return
	}
	p.closed = true
	p.emu.Close()
	p.emuMu.Unlock()

	_ = p.ptmx.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

func (h *Host) shutdownAll() {
	h.mu.Lock()
	ps := make([]*pane, 0, len(h.panes))
	for _, p := range h.panes {
		ps = append(ps, p)
	}
	h.panes = make(map[uint32]*pane)
	h.mu.Unlock()
	for _, p := range ps {
		h.closePane(p)
	}
}

func (h *Host) getPane(id uint32) *pane {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.panes[id]
}

func (h *Host) removePane(id uint32) *pane {
	h.mu.Lock()
	defer h.mu.Unlock()
	p := h.panes[id]
	delete(h.panes, id)
	return p
}

// emit routes an event to the currently-attached client. When no client is
// attached (out == nil) the event is dropped: panes keep running and the next
// client gets a full resync, so a dropped frame/cwd/title costs nothing. sessDone
// unblocks an emit that races a detach on the old channel.
func (h *Host) emit(ev any) {
	h.connMu.Lock()
	out, sessDone := h.out, h.sessDone
	h.connMu.Unlock()
	if out == nil {
		return
	}
	select {
	case out <- ev:
	case <-sessDone:
	case <-h.closed:
	}
}

// --- working directory + branch -------------------------------------------

// resolveSpawnCwd decides where a new pane actually starts, and what to say
// when that is not what was asked for.
//
// An empty request means "wherever the daemon lives" and is left alone. A
// request that names a directory this machine does not have (or that is not a
// directory at all) degrades to $HOME rather than killing the pane: the caller
// is frequently on another machine, and a path that exists only there is a
// routine outcome of splitting a pane or restoring a session across hosts, not
// an error worth a dead terminal over. The note is the whole reason the
// fallback is safe — a pane that silently started somewhere else would leave
// the next command running in the wrong tree.
func (h *Host) resolveSpawnCwd(want string) (cwd, note string) {
	if want == "" {
		return "", ""
	}
	if fi, err := os.Stat(want); err == nil && fi.IsDir() {
		return want, ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// No home either: fall back to the daemon's own directory (cmd.Dir
		// unset), which is always somewhere real.
		return "", fmt.Sprintf("%s is not a directory on this host — started in the daemon's working directory instead", want)
	}
	return home, fmt.Sprintf("%s is not a directory on this host — started in %s instead", want, home)
}

// wakeBranch nudges the branch pump to resolve now rather than at its next
// sweep. Non-blocking by design: the pump re-reads every pane when it runs, so
// a nudge arriving while one is already queued is redundant, and dropping it
// must never stall the read pump that sent it.
func (h *Host) wakeBranch() {
	select {
	case h.branchWake <- struct{}{}:
	default:
	}
}

// branchPump keeps every pane's branch label current: on a nudge (a pane just
// moved) and on a timer (someone ran `git checkout` in a pane that never moved,
// which emits nothing at all).
//
// It runs daemon-side because the pane's cwd is a path on *this* filesystem.
// For a pane on another machine the orchestrator cannot resolve it — at best it
// finds nothing, at worst a same-named checkout of its own sitting on a
// different branch — so the answer has to be computed where the directory is
// and shipped as an event.
func (h *Host) branchPump(ctx context.Context) {
	t := time.NewTicker(branchSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.closed:
			return
		case <-h.branchWake:
		case <-t.C:
		}
		h.sweepBranches()
	}
}

// sweepBranches resolves and emits the branch of every pane that is due. The
// reads (two syscalls per pane, no process) happen on this goroutine rather
// than a pane's read pump, so a slow filesystem — a network mount is the case
// that matters — delays a label instead of the terminal output behind it.
func (h *Host) sweepBranches() {
	h.mu.Lock()
	ps := make([]*pane, 0, len(h.panes))
	for _, p := range h.panes {
		ps = append(ps, p)
	}
	h.mu.Unlock()

	for _, p := range ps {
		now := time.Now()
		cwd, ok := p.branchDue(now)
		if !ok {
			continue
		}
		branch := gitbranch.Resolve(cwd)
		if p.setBranchMeta(cwd, branch, now) {
			h.emit(NewPaneBranch(p.id, branch))
		}
	}
}

func defaultShell() string {
	if s := os.Getenv("SHELL"); s != "" {
		return s
	}
	return "/bin/sh"
}

func buildEnv(extra map[string]string) []string {
	env := append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
