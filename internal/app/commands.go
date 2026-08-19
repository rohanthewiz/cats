package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/startdir"
	"github.com/rohanthewiz/cats/internal/workspace"
)

// This file is the protocol-neutral §7 command dispatcher. It mutates the
// app.Session domain model and drives runtime effects through the Backend seam,
// replying to the caller through a Responder. catway's orch implements Backend
// (browser WebSocket effects) and a Responder over one connection; a future
// CLI/control-API implements the same two interfaces differently. The dispatcher
// itself is libghostty-free and unit-testable with fakes.

// Backend is the runtime-effect seam the dispatcher drives. Every method runs on
// the caller's single actor-loop goroutine (the same one that owns the Session),
// so implementations need no locking.
type Backend interface {
	// Area is the current viewport grid; directional nav resolves against it.
	Area() layout.Rect

	// SetViewWorkspace moves the *issuing* view to a workspace — what
	// workspace.focus means once a window is a lens rather than the viewport.
	// It is a Backend effect and not a Session mutation because only the
	// backend knows which window asked: a browser command carries its
	// connection, while catctl, a hook action and a runbook step carry none and
	// resolve to the session default (the primary view). The dispatcher has
	// already checked that the workspace exists.
	//
	// An empty id CLEARS the issuing view's pin, putting it back to following
	// the primary view. That is the state a viewer starts in, so the round trip
	// "watch that window / go back to the desk" is one command in both
	// directions. A caller with no view of its own has no pin to clear and the
	// backend ignores it.
	SetViewWorkspace(wsID string)

	// ApplyModel reconciles pane PTYs with the session and rebroadcasts the
	// viewport (layout + agents + newly-visible chrome/frames). Called after a
	// command that changed the pane set or sizes.
	ApplyModel()
	// BroadcastLayout rebroadcasts just the viewport layout — for commands that
	// moved focus or renamed without changing the pane set.
	BroadcastLayout()
	// BroadcastPaneTitle pushes a pane's effective title to observers if the pane
	// is currently on screen (else it rides the chrome resend when next visible).
	BroadcastPaneTitle(pane uint32)

	// ScrollPane passes a scrollback delta straight to the pane's PTY; it errors
	// if the pane is unknown.
	ScrollPane(pane uint32, delta int) error
	// SendInput injects text (and, with submit, an Enter keypress) into a pane's
	// PTY, encoded against the pane's live mode state — the pane.send_input
	// command. Synchronous like ScrollPane: the daemon write is fire-and-forget,
	// so success means "encoded and sent", not "the app consumed it"; it errors
	// if the pane is unknown/exited or the encode fails.
	SendInput(pane uint32, text string, submit bool) error

	// StageSpawn registers a one-shot spawn override for a pane the next
	// ApplyModel will realize: an argv to exec instead of the default shell, a
	// cwd override, and/or extra environment (tab.create's optional params).
	// It must be called before that ApplyModel — the dispatcher knows the new
	// pane's id from the session before the backend creates its PTY, which is
	// the only window where the override can influence the spawn.
	StageSpawn(pane uint32, ov SpawnOverride)

	// PaneExists / DaemonConnected gate the async round-trip commands.
	// DaemonConnected answers for the default cathost — the session-wide
	// "is there a backend at all" question; PaneHostConnected answers it for
	// the one host a pane-addressed command actually needs, which is the same
	// answer in a single-host session and the only correct one once panes can
	// live on different machines.
	PaneExists(pane uint32) bool
	DaemonConnected() bool
	PaneHostConnected(pane uint32) bool
	// Hosts reports the cathost roster (host.list): which machines this session
	// is attached to, whether each is connected, and how many panes it holds.
	// It is on the Backend seam rather than the Session because a host is a
	// runtime connection, not domain state — the model only ever records which
	// host id a pane named.
	Hosts() []HostInfo
	// HostAttach / HostDetach edit the roster live (host.attach / host.detach):
	// dial a newly named cathost, or stop talking to one and re-home whatever it
	// held. Both persist the change to the config's hosts: block and resolve r
	// synchronously — the work is a config write plus a goroutine, and the dial
	// itself is the daemon's own retry loop, not something to wait on here.
	HostAttach(r Responder, p HostAttachParams)
	HostDetach(r Responder, p HostDetachParams)
	// PaneMeta reports the runtime-side metadata for a pane — detected agent,
	// live title, cwd — which the session's domain model cannot know. The
	// dispatcher merges it into pane.list / pane.get results; an unknown pane
	// yields the zero value (all-empty is a valid answer, so no ok flag).
	PaneMeta(pane uint32) PaneMeta
	// StartRead / StartCapture begin a daemon round-trip and resolve r when the
	// reply (or a timeout / disconnect) arrives — the dispatch returns first.
	StartRead(r Responder, p ReadParams)
	StartCapture(r Responder, p CaptureParams)
	// StartWaitForOutput registers a waiter that resolves r when the pane's output
	// matches p (WaitForOutputResult{Matched:true}), or on the wait's own timeout /
	// pane exit (Matched:false). The dispatch returns first; the waiter matches
	// against the pane's live output stream (plus a one-shot seed of the current
	// screen).
	StartWaitForOutput(r Responder, p WaitForOutputParams)

	// StartWorktreeList / StartWorktreeCreate / StartWorktreeOpen /
	// StartWorktreeRemove run the git-worktree commands (WS8 dialogs). The git
	// subprocess work happens off the loop goroutine and r resolves later;
	// worktree.open needs no git and may resolve synchronously. The backend owns
	// pane-cwd resolution, the worktree root, and the workspace effects
	// (create/focus/close + reconcile).
	StartWorktreeList(r Responder, p WorktreeListParams)
	StartWorktreeCreate(r Responder, p WorktreeCreateParams)
	StartWorktreeOpen(r Responder, p WorktreeOpenParams)
	StartWorktreeRemove(r Responder, p WorktreeRemoveParams)

	// ConfigGet resolves the live configuration snapshot (config.get); ConfigSet
	// validates, persists, and applies a change (config.set). Both resolve r
	// synchronously — the config file is small and local.
	ConfigGet(r Responder)
	ConfigSet(r Responder, p ConfigSetParams)

	// ThemeList / ThemeSave / ThemeDelete manage the theme library (theme.*):
	// enumerate every available theme, write a user theme file, remove one.
	// Synchronous like the config pair — same small local files, and they sit
	// on the Backend seam because only the backend holds the theme registry
	// and the live config the save/delete may re-point.
	ThemeList(r Responder)
	ThemeSave(r Responder, p ThemeSaveParams)
	ThemeDelete(r Responder, p ThemeDeleteParams)

	// StartPluginList / StartPluginUninstall run the plugin-host commands (the
	// plugins dialog). Both are filesystem work under the plugins root, kept on
	// the Backend seam (not called into internal/plugin here) for the same
	// reason as the worktree commands: the disk work runs off the loop
	// goroutine and r resolves later, and the dispatcher stays free of the
	// host dependency for fakes.
	StartPluginList(r Responder)
	StartPluginUninstall(r Responder, p PluginUninstallParams)

	// StartPathList answers the start-path picker's directory listing. Off-loop
	// like the commands above — a listing can land on a cold network mount — and
	// on the Backend seam because only the backend knows the anchor a relative
	// path resolves against (the addressed pane's live cwd).
	StartPathList(r Responder, p PathListParams)

	// UINotify raises a notification from an arbitrary caller (ui.notify) and
	// UIAction takes one of its buttons (ui.action). Both resolve r
	// synchronously: the notification fan-out is a broadcast plus a
	// non-blocking push, and an action is one encoded write into a PTY —
	// neither has anything to wait for.
	//
	// They are on the Backend seam rather than answered here because the
	// dispatcher holds no notification registry and no PTYs, and because the
	// registry is the thing that makes an action single-use: putting it
	// anywhere else would let a second entry point answer a prompt the first
	// one already answered.
	UINotify(r Responder, p UINotifyParams)
	UIAction(r Responder, p UIActionParams)

	// EditorConfig reports the editor policy (pane.open_file): which agent
	// labels mark a pane as an editor, the argv that starts one, and whether
	// starting one is allowed. It is a Backend question because it is
	// configuration, which the dispatcher deliberately holds none of.
	//
	// OpenFileIn delivers the request to the chosen pane. The dispatcher decided
	// WHICH pane; the backend owns the event stream that pane's editor is
	// subscribed to. It cannot fail: an event with no subscriber is not an
	// error, it is an editor that has not connected yet.
	EditorConfig() EditorInfo
	OpenFileIn(pane uint32, p OpenFileParams)

	// StartFileStat / StartFileGet / StartFilePut are the file-transfer commands
	// (file.stat / file.get / file.put). All three resolve r later, because all
	// three may be a round trip to another machine's disk — and are off-loop
	// even for this machine's, since a read can land on a cold network mount.
	//
	// They are on the Backend seam for the reason path.list is: the dispatcher
	// holds no host roster and no panes, and the two things a file command needs
	// before it can run — which machine, and what a relative path resolves
	// against — are both answers only the backend has.
	StartFileStat(r Responder, p FileStatParams)
	StartFileGet(r Responder, p FileGetParams)
	StartFilePut(r Responder, p FilePutParams)

	// LedgerList answers a command-history query (ledger.list). On the Backend
	// seam because the store is the backend's, and synchronous: the whole
	// dataset is an in-memory B-tree, so the scan is a filtered walk rather than
	// anything worth waiting on.
	LedgerList(r Responder, p LedgerListParams)
	// LedgerOutput reads a recorded command's output out of its pane's live
	// scrollback (ledger.output), and LedgerJump scrolls that pane's viewport to
	// it (ledger.jump). Both are round trips to the pane's cathost — the marks
	// that bound a block are the terminal's, and only it can say where they are
	// now — so both resolve r later.
	LedgerOutput(r Responder, p LedgerBlockParams)
	LedgerJump(r Responder, p LedgerBlockParams)

	// RunbookList enumerates the runbooks on disk (runbook.list) and RunbookRun
	// executes one (runbook.run).
	//
	// They are on the Backend seam for the reason the dispatcher holds no
	// filesystem and no clock: a runbook is a file, and running one is a chain
	// of dispatches that outlives the single call the dispatcher models. The
	// backend re-enters this very Dispatcher once per step, which is what keeps
	// a runbook step and a client command the same thing — there is no second
	// implementation of any command for runbooks to use.
	//
	// RunbookRun resolves r only when the last step has finished, so a caller
	// that waits for the reply knows the whole sequence is done.
	RunbookList(r Responder)
	RunbookRun(r Responder, p RunbookRunParams)
	// RunbookRecord arms, disarms or reports the macro recorder
	// (runbook.record). It is the backend's for the same two reasons the pair
	// above are: the recording is live daemon state, and stopping one writes a
	// file. Synchronous — arming is a flag, and emitting a runbook is a YAML
	// marshal and a write of a few kilobytes.
	//
	// A backend that implements it also implements the unexported recorder
	// seam (see record.go), which is what puts commands into the recording;
	// one without a recorder can answer every action with "not recording".
	RunbookRecord(r Responder, p RunbookRecordParams)

	// RefreshUsage asks the backend's rate-limit poller to take a reading now
	// (usage.refresh). It returns immediately: the read is one network round
	// trip on the poller's own goroutine, and its result reaches clients as a
	// pushed `usage` message, so there is nothing for the command to wait for
	// and nothing to hand back.
	RefreshUsage()

	// Chat commands (the ACP side panel). All resolve r synchronously: the
	// chat manager is loop-owned state, and the slow work (the agent
	// subprocess) runs on the manager's own goroutines, reaching clients as
	// pushed chat_* messages — same contract as RefreshUsage. ChatPermission
	// alone can fail meaningfully (an already-answered prompt), which is why
	// it takes the Responder rather than acking unconditionally.
	ChatSend(r Responder, p ChatSendParams)
	ChatCancel(r Responder)
	ChatPermission(r Responder, p ChatPermissionParams)
	ChatClear(r Responder)

	// ReloadConfig acknowledges a config reload (a no-op today).
	ReloadConfig() error
	// Shutdown notifies observers the server is going away and triggers the quit.
	Shutdown()
}

// Responder delivers a command's terminal result to its caller. For the browser
// it marshals a cmd_result on that connection; a CLI/API caller implements its
// own. It is storable in a pending round-trip for the async commands.
type Responder interface {
	// WantsReply reports whether the caller can receive a result. read/capture
	// short-circuit when false, so they never register an unresolvable pending.
	WantsReply() bool
	// OK completes the command successfully; data is command-specific
	// (ReadResult/CaptureResult) or nil.
	OK(data any)
	// Fail completes the command with an error message.
	Fail(errMsg string)
}

// ParamDecoder decodes a command's params into the typed struct v. The browser
// backend wraps the Cmd's json params; a CLI could bind parsed flags. Decode
// returns ErrNoParams when the caller supplied none, so the dispatcher decides
// per command (required ⇒ error, optional ⇒ zero value).
type ParamDecoder interface {
	Decode(v any) error
}

// ErrNoParams signals that the caller supplied no params for a command.
var ErrNoParams = errors.New("missing params")

// JSONParamDecoder is the ParamDecoder for any JSON-params protocol — the
// browser cmd envelope and the control-API request both carry their params as a
// json.RawMessage. Empty params report ErrNoParams so the dispatcher can treat
// them as the zero value for optional commands, or an error for required ones.
type JSONParamDecoder struct{ Raw json.RawMessage }

func (d JSONParamDecoder) Decode(v any) error {
	if len(d.Raw) == 0 {
		return ErrNoParams
	}
	return json.Unmarshal(d.Raw, v)
}

// RawParams hands back the bytes Decode read, for the macro recorder — which
// wants the shape the CALLER sent rather than the decoded struct, since a zero
// value that was sent explicitly is invisible once it has been through an
// omitempty field. See recordDecoder.
func (d JSONParamDecoder) RawParams() []byte { return d.Raw }

// Dispatcher runs the §7 command table against a Session and a Backend. It
// borrows the same *Session the backend holds (single-goroutine, no locking).
type Dispatcher struct {
	session *Session
	backend Backend
	// view is the window this command was issued from (view.go). Every command
	// that used to mean "the active workspace" means "this view's workspace"
	// instead; the zero View is the view-less caller and resolves back to
	// s.active, which is why catctl, hooks and runbook steps are unchanged.
	view View
	// rec is the backend's macro recorder, nil unless the backend has one. It
	// is read from the backend at construction rather than set on the
	// dispatcher, because dispatchers are built per call in places (the runbook
	// executor builds one per step) and a recorder that had to be installed on
	// each of them would miss whichever site forgot.
	rec Recorder
}

// NewDispatcher builds a dispatcher over a session and its runtime backend for
// a caller with no window — catctl, a hook action, a runbook step. Its commands
// resolve against the session's default view (the primary window's workspace).
func NewDispatcher(s *Session, b Backend) *Dispatcher {
	return NewDispatcherFor(s, b, View{})
}

// NewDispatcherFor builds a dispatcher for a caller that *has* a window. The
// browser path uses it, handing over the view its connection is showing, so two
// windows on two workspaces no longer drive each other's tabs, focus or splits.
func NewDispatcherFor(s *Session, b Backend, v View) *Dispatcher {
	d := &Dispatcher{session: s, backend: b, view: v}
	if rb, ok := b.(recorderBackend); ok {
		d.rec = rb.Recorder()
	}
	return d
}

// ws is the workspace id this dispatcher's commands default to: the issuing
// window's, or "" for a view-less caller (which every …In form reads as "the
// session's active workspace"). Every use of it in the table below marks a
// command that used to read s.active implicitly.
func (d *Dispatcher) ws() string { return d.view.WorkspaceID }

// viewWorkspace is the live workspace this dispatcher acts in — the target of
// the workspace-lock checks and the default for commands that take an optional
// workspace id. Never nil for a session with at least one workspace.
func (d *Dispatcher) viewWorkspace() *workspace.Workspace {
	return d.session.viewWorkspace(d.ws())
}

// viewWorkspaceID is viewWorkspace's id, resolved: a view naming a workspace
// that has since been closed reports the one it actually falls back to, so a
// command scoped by it cannot land in a workspace that no longer exists.
func (d *Dispatcher) viewWorkspaceID() string { return d.viewWorkspace().ID }

// Dispatch runs one §7 command. Loop-goroutine only (it shares the session with
// the backend). Model-mutating commands call a Session method then reconcile via
// ApplyModel; pure focus/rename rebroadcast the layout; scroll passes through;
// read/capture start an async daemon round-trip; server.* are lifecycle.
//
// When a recorder is armed and the command is one it captures, the decoder and
// the responder are wrapped before the switch sees them — see record.go. The
// wrapping is the whole of the recorder's reach into this file: no case knows
// it is being recorded, which is what keeps "what a macro replays" the same
// question as "what the command does".
func (d *Dispatcher) Dispatch(name string, dec ParamDecoder, r Responder) {
	if d.rec != nil && RecordedCommand(name) {
		if seq := d.rec.Begin(name); seq != 0 {
			rd := &recordDecoder{inner: dec}
			dec = rd
			r = &recordResponder{inner: r, rec: d.rec, dec: rd, seq: seq}
		}
	}
	d.dispatch(name, dec, r)
}

// dispatch is the command table itself.
func (d *Dispatcher) dispatch(name string, dec ParamDecoder, r Responder) {
	// bad reports a malformed-params failure in the historical wording.
	bad := func(err error) { r.Fail("bad params: " + err.Error()) }

	switch name {
	case CmdPaneFocus:
		var p PaneParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		// FocusPaneView, not FocusPane: the pane's tab records the new focus
		// (session state, shared by every window showing it), while *which*
		// window follows it into that workspace is the view's business. A
		// click on a pane in another workspace still reveals it — in the
		// window that clicked, and only there.
		wsID, err := d.session.FocusPaneView(layout.PaneID(p.Pane))
		if err != nil {
			r.Fail(err.Error())
			return
		}
		d.backend.SetViewWorkspace(wsID)
		d.backend.BroadcastLayout() // focus flag moved; pane set unchanged
		r.OK(nil)

	case CmdPaneFocusDirection:
		var p DirParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		nav, ok := NavDirection(p.Dir)
		if !ok {
			r.Fail(fmt.Sprintf("bad direction %q", p.Dir))
			return
		}
		moved, err := d.session.FocusPaneDirectionIn(d.ws(), nav, d.backend.Area())
		if err != nil {
			r.Fail(err.Error())
			return
		}
		if moved {
			d.backend.BroadcastLayout()
		}
		r.OK(nil)

	case CmdPaneCycle:
		var p CycleParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if d.session.CyclePaneIn(d.ws(), p.Next) {
			d.backend.BroadcastLayout()
		}
		r.OK(nil)

	case CmdPaneSwap:
		var p DirParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		nav, ok := NavDirection(p.Dir)
		if !ok {
			r.Fail(fmt.Sprintf("bad direction %q", p.Dir))
			return
		}
		swapped, err := d.session.SwapPaneDirectionIn(d.ws(), nav, d.backend.Area())
		if err != nil {
			r.Fail(err.Error())
			return
		}
		if swapped {
			d.backend.ApplyModel() // panes changed slots/sizes
		}
		r.OK(nil)

	case CmdPaneSwapWith:
		var p SwapWithParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		swapped, err := d.session.SwapPanesIn(d.ws(), layout.PaneID(p.Pane), layout.PaneID(p.Target))
		if err != nil {
			r.Fail(err.Error())
			return
		}
		if !swapped {
			r.Fail("panes must be two distinct panes of the active tab")
			return
		}
		d.backend.ApplyModel() // panes changed slots/sizes
		r.OK(nil)

	case CmdPaneZoom:
		var p OptPaneParams
		if err := decodeOptional(dec, &p); err != nil {
			bad(err)
			return
		}
		if _, err := d.session.ToggleZoomIn(d.ws(), optPaneID(p.Pane)); err != nil {
			r.Fail(err.Error())
			return
		}
		d.backend.ApplyModel() // viewport pane set + zoomed pane size changed
		r.OK(nil)

	case CmdPaneResizeBorder:
		var p ResizeBorderParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		path, ok := BorderPath(p.Border)
		if !ok {
			r.Fail(fmt.Sprintf("bad border id %q", p.Border))
			return
		}
		if err := d.session.ResizeBorderIn(d.ws(), path, p.Ratio); err != nil {
			r.Fail(err.Error())
			return
		}
		d.backend.ApplyModel() // split ratio changed → panes resize
		r.OK(nil)

	case CmdPaneLast:
		if d.session.FocusLastPaneIn(d.ws()) {
			d.backend.BroadcastLayout()
		}
		r.OK(nil)

	case CmdPaneRename:
		var p RenamePaneParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if err := d.session.RenamePane(layout.PaneID(p.Pane), p.Name); err != nil {
			r.Fail(err.Error())
			return
		}
		// Push the new effective title if the pane is on screen; otherwise it
		// rides the chrome resend when the pane next becomes visible.
		d.backend.BroadcastPaneTitle(p.Pane)
		r.OK(nil)

	case CmdPaneSplit:
		var sp SplitParams
		if err := dec.Decode(&sp); err != nil {
			bad(err)
			return
		}
		dir, ok := SplitDirection(sp.Direction)
		if !ok {
			r.Fail(fmt.Sprintf("bad split direction %q", sp.Direction))
			return
		}
		if err := sp.Validate(); err != nil {
			bad(err)
			return
		}
		// Same lock rule as tab.create, and it has to be here rather than only
		// there: a split carrying a command is the second way to start a process
		// in a workspace, so leaving it open would make the lock a matter of which
		// verb the automation happened to pick. A bare split is a shell the user
		// asked for by hand and goes through.
		if len(sp.Command) > 0 {
			if ws := d.viewWorkspace(); ws != nil && ws.Locked {
				r.Fail(workspaceLockedErr(ws.ID, "run a command in"))
				return
			}
		}
		// A host that is not in the roster has to fail here: the alternative is a
		// pane silently created on the default machine, which looks like a
		// success and puts the user's shell somewhere they did not ask for.
		if err := d.checkHost(sp.Host); err != nil {
			r.Fail(err.Error())
			return
		}
		// Resolve the source pane's cwd before the split, for the same reason a new
		// tab takes its neighbor's: the new pane is another shell in the work the
		// user is already doing. Scoped to the host the new pane will run on —
		// with no host param that is the split pane's own machine, which is what
		// SplitPane fills in below.
		inherited := d.inheritedSplitCwd(optPaneID(sp.Pane), sp.Host)
		np, err := d.session.SplitPaneOnIn(d.ws(), optPaneID(sp.Pane), dir, sp.Host)
		if err != nil {
			r.Fail(err.Error())
			return
		}
		// Stage before ApplyModel, which is what creates the new pane's PTY. An
		// explicit cwd always wins over the inherited one — same precedence
		// tab.create gives its params over the neighbor tab's directory.
		ov, stage := sp.spawnOverride()
		if ov.Cwd == "" && inherited != "" {
			ov.Cwd, stage = inherited, true
		}
		if stage {
			d.backend.StageSpawn(uint32(np), ov)
		}
		d.backend.ApplyModel()
		r.OK(SplitResult{Pane: uint32(np)})

	case CmdPaneClose:
		var cp OptPaneParams
		if err := decodeOptional(dec, &cp); err != nil {
			bad(err)
			return
		}
		if _, err := d.session.ClosePaneIn(d.ws(), optPaneID(cp.Pane)); err != nil {
			r.Fail(err.Error())
			return
		}
		d.backend.ApplyModel()
		r.OK(nil)

	case CmdScroll:
		var p ScrollParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if err := d.backend.ScrollPane(p.Pane, p.Delta); err != nil {
			r.Fail(err.Error())
			return
		}
		r.OK(nil)

	case CmdRead:
		var p ReadParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if !r.WantsReply() {
			return // read yields only a result; with no reply channel there's nowhere to send it
		}
		if !d.backend.PaneExists(p.Pane) {
			r.Fail(fmt.Sprintf("unknown pane %d", p.Pane))
			return
		}
		if !d.backend.PaneHostConnected(p.Pane) {
			r.Fail("cathost daemon not connected")
			return
		}
		d.backend.StartRead(r, p) // async: the daemon reply resolves r later

	case CmdCapture:
		var p CaptureParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if !r.WantsReply() {
			return // capture yields only a result; with no reply channel there's nowhere to send it
		}
		if !d.backend.PaneExists(p.Pane) {
			r.Fail(fmt.Sprintf("unknown pane %d", p.Pane))
			return
		}
		if !d.backend.PaneHostConnected(p.Pane) {
			r.Fail("cathost daemon not connected")
			return
		}
		d.backend.StartCapture(r, p) // async: the daemon reply resolves r later

	case CmdWaitForOutput:
		var p WaitForOutputParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if !r.WantsReply() {
			return // wait yields only a result; with no reply channel there's nothing to await
		}
		if _, err := p.Matcher(); err != nil {
			r.Fail(err.Error()) // empty pattern / bad regex
			return
		}
		if !d.backend.PaneExists(p.Pane) {
			r.Fail(fmt.Sprintf("unknown pane %d", p.Pane))
			return
		}
		if !d.backend.PaneHostConnected(p.Pane) {
			r.Fail("cathost daemon not connected")
			return
		}
		d.backend.StartWaitForOutput(r, p) // async: a match / timeout / exit resolves r later

	case CmdPaneSendInput:
		var p SendInputParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if err := p.Validate(); err != nil {
			bad(err)
			return
		}
		if !d.backend.PaneExists(p.Pane) {
			r.Fail(fmt.Sprintf("unknown pane %d", p.Pane))
			return
		}
		// Gate on the pane's own host like read/capture: the backend's write
		// path drops silently when disconnected, and a vanished prompt is worse
		// than an error.
		if !d.backend.PaneHostConnected(p.Pane) {
			r.Fail("cathost daemon not connected")
			return
		}
		// Typing into a pane is the other half of what a workspace lock keeps
		// out (tab.create's command being the first): it is how an automation
		// client drives an agent that already lives there.
		if ws := d.session.PaneWorkspace(layout.PaneID(p.Pane)); ws != nil && ws.Locked {
			r.Fail(workspaceLockedErr(ws.ID, "send input to a pane in"))
			return
		}
		if err := d.backend.SendInput(p.Pane, p.Text, p.Submit); err != nil {
			r.Fail(err.Error())
			return
		}
		r.OK(nil)

	case CmdTabCreate:
		var p TabCreateParams
		if err := decodeOptional(dec, &p); err != nil {
			bad(err)
			return
		}
		if err := p.Validate(); err != nil {
			bad(err)
			return
		}
		// Resolve the target up front: everything below is scoped to it, and an
		// unknown id has to fail before the create rather than silently landing
		// the tab in the viewport instead of where the caller aimed it.
		// An omitted workspace means "the one this window is showing", which
		// with several windows open is no longer the same as "the session's
		// active workspace". Resolved once, here, and used for the lock check,
		// the cwd inheritance, the create and the rename below — passing ""
		// down would let each of them re-resolve against a different default.
		targetWS := p.Workspace
		if targetWS == "" {
			targetWS = d.viewWorkspaceID()
		}
		ws := d.session.WorkspaceByID(targetWS)
		if ws == nil {
			r.Fail(fmt.Sprintf("unknown workspace %s", p.Workspace))
			return
		}
		// A locked workspace takes no supplied command line — this is the path a
		// plugin action and an agent launch both come in on (the browser's
		// pluginRunAction, `catctl plugin run`). A bare tab.create is a shell the
		// user asked for by hand, so it goes through: the lock keeps automation
		// out, it does not put the workspace behind glass.
		//
		// The lock consulted is the *target's*, not the viewport's. With an
		// explicit workspace those differ, and it is the workspace the process
		// lands in that was set aside for hand work — a fan-out across every
		// workspace has to be refused by exactly the locked ones.
		if len(p.Command) > 0 && ws.Locked {
			r.Fail(workspaceLockedErr(ws.ID, "run a command in"))
			return
		}
		if err := d.checkHost(p.Host); err != nil {
			r.Fail(err.Error())
			return
		}
		// Resolve the left-hand neighbor before the create, while it is still the
		// workspace's last tab. The host the tab will actually run on decides
		// whether that neighbor's directory means anything: the param when one
		// was given, else the workspace's own default.
		tabHost := p.Host
		if tabHost == "" {
			tabHost = ws.HostID
		}
		inherited := d.inheritedTabCwd(targetWS, tabHost)
		num, root, err := d.session.CreateTabInOn(targetWS, p.Host)
		if err != nil {
			r.Fail(err.Error())
			return
		}
		// The new tab's root pane — returned so an automation client can drive it
		// (send_input / wait_for_output) without diffing pane.list. It comes back
		// from CreateTabIn rather than from FocusedPane because the latter reads
		// the viewport, which is not the target when a workspace was named.
		res := TabCreateResult{Num: num, Pane: uint32(root)}
		if p.Title != "" {
			// Same session mutation as tab.rename; the tab was just created, so
			// the only failure would be a vanished tab — not worth failing the
			// whole create over. Scoped to the same workspace: tab numbers are
			// per workspace, so an unscoped rename would find a different tab.
			_ = d.session.RenameTabIn(targetWS, num, p.Title)
		}
		// Stage the spawn override before ApplyModel: that call reconciles the
		// daemon's PTY set and is what actually creates the pane's process. An
		// explicit cwd always wins over the inherited one.
		ov, stage := p.spawnOverride()
		if ov.Cwd == "" && inherited != "" {
			ov.Cwd, stage = inherited, true
		}
		if stage {
			d.backend.StageSpawn(res.Pane, ov)
		}
		d.backend.ApplyModel()
		r.OK(res)

	case CmdTabClose:
		var p OptTabParams
		if err := decodeOptional(dec, &p); err != nil {
			bad(err)
			return
		}
		if err := d.session.CloseTabIn(d.ws(), p.Num); err != nil {
			r.Fail(err.Error())
			return
		}
		d.backend.ApplyModel()
		r.OK(nil)

	case CmdTabFocus:
		var p TabParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if err := d.session.FocusTabIn(d.ws(), p.Num); err != nil {
			r.Fail(err.Error())
			return
		}
		d.backend.ApplyModel()
		r.OK(nil)

	case CmdTabRename:
		var p RenameTabParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if err := d.session.RenameTabIn(d.ws(), p.Num, p.Name); err != nil {
			r.Fail(err.Error())
			return
		}
		d.backend.BroadcastLayout()
		r.OK(nil)

	case CmdTabMove:
		var p MoveTabParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		moved, err := d.session.MoveTabIn(d.ws(), p.Num, p.Index)
		if err != nil {
			r.Fail(err.Error())
			return
		}
		if moved {
			d.backend.BroadcastLayout() // order changed; pane set unchanged
		}
		r.OK(nil)

	case CmdTabMoveToWorkspace:
		var p MoveTabToWorkspaceParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if p.Workspace == "" {
			r.Fail("workspace is required")
			return
		}
		// From defaults to the issuing window's workspace: dragging a tab out of
		// a window's own strip is the case this exists for, and that window is
		// the only one that knows which strip it was. Resolved here rather than
		// passed as "" — MoveTabTo would read an empty source as the SESSION's
		// active workspace, which is a different window's.
		from := p.From
		if from == "" {
			from = d.viewWorkspaceID()
		}
		num, err := d.session.MoveTabTo(from, p.Num, p.Workspace)
		if err != nil {
			r.Fail(err.Error())
			return
		}
		// ApplyModel, not BroadcastLayout: the tab left one workspace's viewport
		// and joined another's, so both windows' visible pane sets changed and
		// every pane is re-sized against its new workspace's grid.
		d.backend.ApplyModel()
		r.OK(MoveTabToWorkspaceResult{Workspace: p.Workspace, Num: num})

	case CmdWorkspaceCreate:
		var p WorkspaceCreateParams
		if err := decodeOptional(dec, &p); err != nil {
			bad(err)
			return
		}
		host, ok := d.hostInfo(p.Host)
		if !ok {
			r.Fail(unknownHostErr(p.Host))
			return
		}
		// Path resolution is this machine's filesystem, so it only applies to a
		// workspace that will live on this machine (see workspaceStartDir).
		cwd, err := workspaceStartDir(p.Path, d.session.Cwd(), p.Mkdir, host.Local)
		if err != nil {
			r.Fail(err.Error())
			return
		}
		id, err := d.session.CreateWorkspaceAtOn(cwd, p.Host)
		if err != nil {
			r.Fail(err.Error())
			return
		}
		if p.Name != "" {
			// Same session mutation as workspace.rename, applied before
			// ApplyModel so the workspace reaches every observer already
			// named — no create-then-rename flicker in the sidebar. The only
			// way this fails is a vanished workspace, which cannot happen
			// between these two lines, so the create still succeeds.
			_ = d.session.RenameWorkspace(id, p.Name)
		}
		// The window that asked for a workspace is the one that wanted to be in
		// it. Other windows stay where they are — a new workspace appearing in
		// the sidebar is not a reason to move somebody else's screen.
		d.backend.SetViewWorkspace(id)
		d.backend.ApplyModel()
		r.OK(WorkspaceCreateResult{ID: id})

	case CmdWorkspaceClose:
		var p WorkspaceParams
		_ = dec.Decode(&p) // id optional → active workspace; ignore any decode error
		// An omitted id closes the workspace this window is showing, not the
		// session default's — "close this workspace" from window B must not
		// close window A's.
		target := p.ID
		if target == "" {
			target = d.viewWorkspaceID()
		}
		id := &target
		if err := d.session.CloseWorkspace(id); err != nil {
			r.Fail(err.Error())
			return
		}
		d.backend.ApplyModel()
		r.OK(nil)

	case CmdWorkspaceFocus:
		var p WorkspaceParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		// The command that used to switch every client at once. It now sets
		// *this* view's workspace: the issuing window gets a new layout and the
		// frames for its panes, and no other window hears about it (except
		// through the Clients census). From catctl, which has no window, the
		// backend applies it to the primary view — what it has always done.
		//
		// An empty id is the inverse: it clears the pin, so the connection goes
		// back to following the primary view. Init.workspace already says
		// "start unpinned" by being absent; without this there is no way to get
		// back there on a live socket, which is precisely what a phone needs
		// after it picks a desktop window to watch — and what a window opened
		// on "?ws=w2" needs to rejoin whatever the user is actually doing.
		//
		// Deliberately not capability-gated. A capability exists for a field a
		// client cannot tell was dropped (see browserproto's caps doc); this is
		// a command, and a server too old to know it answers ok:false with
		// "unknown workspace", which is a perfectly detectable "no".
		if p.ID != "" && d.session.WorkspaceByID(p.ID) == nil {
			r.Fail(fmt.Sprintf("unknown workspace %s", p.ID))
			return
		}
		d.backend.SetViewWorkspace(p.ID)
		d.backend.ApplyModel()
		r.OK(nil)

	case CmdWorkspaceRename:
		var p RenameWorkspaceParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if err := d.session.RenameWorkspace(p.ID, p.Name); err != nil {
			r.Fail(err.Error())
			return
		}
		d.backend.BroadcastLayout()
		r.OK(nil)

	case CmdWorkspaceMove:
		var p MoveWorkspaceParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		moved, err := d.session.MoveWorkspace(p.ID, p.Index)
		if err != nil {
			r.Fail(err.Error())
			return
		}
		if moved {
			d.backend.BroadcastLayout() // order changed; pane set unchanged
		}
		r.OK(nil)

	case CmdWorkspaceLock:
		var p LockWorkspaceParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		lockTarget := p.ID
		if lockTarget == "" {
			lockTarget = d.viewWorkspaceID()
		}
		changed, err := d.session.SetWorkspaceLock(lockTarget, p.Locked)
		if err != nil {
			r.Fail(err.Error())
			return
		}
		if changed {
			// Durable state the sidebar draws; the pane set is untouched, so
			// this is the rename/move path (which also arms the save).
			d.backend.BroadcastLayout()
		}
		r.OK(nil)

	case CmdAgentFocus:
		var p PaneParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		// Unlike pane.focus, the agents sidebar is global (§8): the target pane
		// may live in another workspace/tab, so reveal it into the viewport.
		wsID, err := d.session.RevealPaneView(layout.PaneID(p.Pane))
		if err != nil {
			r.Fail(err.Error())
			return
		}
		d.backend.SetViewWorkspace(wsID)
		d.backend.ApplyModel() // viewport may have changed (different workspace/tab)
		r.OK(nil)

	case CmdWorktreeList:
		var p WorktreeListParams
		if err := decodeOptional(dec, &p); err != nil {
			bad(err)
			return
		}
		if !r.WantsReply() {
			return // list yields only a result; with no reply channel there's nowhere to send it
		}
		d.backend.StartWorktreeList(r, p) // async: git list resolves r later

	case CmdWorktreeCreate:
		var p WorktreeCreateParams
		if err := decodeOptional(dec, &p); err != nil {
			bad(err)
			return
		}
		d.backend.StartWorktreeCreate(r, p) // async: git add + workspace create resolve r later

	case CmdWorktreeOpen:
		var p WorktreeOpenParams
		if err := decodeOptional(dec, &p); err != nil {
			bad(err)
			return
		}
		if p.Path == "" {
			r.Fail("worktree.open: path is required")
			return
		}
		d.backend.StartWorktreeOpen(r, p)

	case CmdWorktreeRemove:
		var p WorktreeRemoveParams
		if err := decodeOptional(dec, &p); err != nil {
			bad(err)
			return
		}
		if p.Workspace == "" {
			r.Fail("worktree.remove: workspace is required")
			return
		}
		d.backend.StartWorktreeRemove(r, p) // async: git remove + workspace close resolve r later

	case CmdPluginList:
		if !r.WantsReply() {
			return // list yields only a result; with no reply channel there's nowhere to send it
		}
		d.backend.StartPluginList(r) // async: the plugins-root scan resolves r later

	case CmdPluginUninstall:
		var p PluginUninstallParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if p.ID == "" {
			r.Fail("plugin.uninstall: id is required")
			return
		}
		d.backend.StartPluginUninstall(r, p) // async: the directory removal resolves r later

	case CmdPathList:
		var p PathListParams
		if err := decodeOptional(dec, &p); err != nil {
			bad(err)
			return
		}
		if !r.WantsReply() {
			return // a listing yields only a result; with no reply channel there's nowhere to send it
		}
		d.backend.StartPathList(r, p) // async: the directory read resolves r later

	case CmdConfigGet:
		if !r.WantsReply() {
			return // config.get yields only a result
		}
		d.backend.ConfigGet(r)

	case CmdConfigSet:
		var p ConfigSetParams
		if err := decodeOptional(dec, &p); err != nil {
			bad(err)
			return
		}
		d.backend.ConfigSet(r, p)

	case CmdThemeList:
		if !r.WantsReply() {
			return // a listing yields only a result
		}
		d.backend.ThemeList(r)

	case CmdThemeSave:
		var p ThemeSaveParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if p.Name == "" {
			r.Fail("theme.save: name is required")
			return
		}
		d.backend.ThemeSave(r, p)

	case CmdThemeDelete:
		var p ThemeDeleteParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if p.Name == "" {
			r.Fail("theme.delete: name is required")
			return
		}
		d.backend.ThemeDelete(r, p)

	case CmdUINotify:
		var p UINotifyParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if strings.TrimSpace(p.Title) == "" {
			r.Fail("title is required")
			return
		}
		if p.Kind == "" {
			p.Kind = NotifyKindInfo
		}
		if !NotifyKindOK(p.Kind) {
			r.Fail("kind must be one of attention, finished, info")
			return
		}
		// Shape-only checks here; whether a pane exists and whether an action's
		// text can be encoded for it are the backend's, which holds the panes.
		for i, a := range p.Actions {
			if strings.TrimSpace(a.Label) == "" {
				r.Fail("actions[" + strconv.Itoa(i) + "]: label is required")
				return
			}
		}
		if err := uniqueActionIDs(p.Actions); err != nil {
			r.Fail(err.Error())
			return
		}
		d.backend.UINotify(r, p)

	case CmdUIAction:
		var p UIActionParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if p.ID == "" {
			r.Fail("id is required")
			return
		}
		d.backend.UIAction(r, p)

	case CmdPaneOpenFile:
		var p OpenFileParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		d.openFile(r, p)

	case CmdFileStat:
		// The reply gate comes FIRST, before any validation, the way
		// ledger.output's does: a reply-required command sent with nowhere to
		// answer is a no-op, not a refusal nobody can read.
		if !r.WantsReply() {
			return
		}
		var p FileStatParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if p.Path == "" {
			r.Fail("path is required")
			return
		}
		d.backend.StartFileStat(r, p) // async: the disk answers later

	case CmdFileGet:
		// Reply gate first, as above — and here it is more than a rule: the
		// bytes ARE the result, so a get with nowhere to send them must not
		// touch a disk on another machine at all.
		if !r.WantsReply() {
			return
		}
		var p FileGetParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if p.Path == "" {
			r.Fail("path is required")
			return
		}
		if p.Offset < 0 || p.Length < 0 {
			// Checked here rather than left to the filesystem because a negative
			// offset is a caller bug in arithmetic, and it should be named as one
			// on the machine that made it rather than travelling to another box
			// to come back as an unhelpful read error.
			r.Fail("offset and length cannot be negative")
			return
		}
		d.backend.StartFileGet(r, p)

	case CmdFilePut:
		var p FilePutParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if p.Path == "" {
			r.Fail("path is required")
			return
		}
		if p.Offset < 0 {
			r.Fail("offset cannot be negative")
			return
		}
		d.backend.StartFilePut(r, p)

	case CmdLedgerList:
		var p LedgerListParams
		if err := decodeOptional(dec, &p); err != nil {
			bad(err)
			return
		}
		if !r.WantsReply() {
			return // a listing yields only a result
		}
		d.backend.LedgerList(r, p)

	case CmdRunbookList:
		if !r.WantsReply() {
			return // a listing yields only a result
		}
		d.backend.RunbookList(r)

	case CmdRunbookRun:
		var p RunbookRunParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		// Shape-only: whether a runbook by this name exists, and whether its
		// steps still address live panes, are the backend's — it holds the
		// directory and the session.
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" {
			r.Fail("name is required")
			return
		}
		d.backend.RunbookRun(r, p)

	case CmdRunbookRecord:
		var p RunbookRecordParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		// Shape-only, like runbook.run above: the action has to be one of four
		// words and stop has to name the file it is writing. Whether anything is
		// being recorded, and whether what was recorded can be emitted at all,
		// are the backend's — it holds the recorder and the directory.
		p.Action = strings.TrimSpace(p.Action)
		switch p.Action {
		case RecordStart, RecordStop, RecordCancel, RecordStatus:
		case "":
			r.Fail("action is required: " + strings.Join([]string{RecordStart, RecordStop, RecordCancel, RecordStatus}, " | "))
			return
		default:
			r.Fail(fmt.Sprintf("unknown action %q: %s", p.Action,
				strings.Join([]string{RecordStart, RecordStop, RecordCancel, RecordStatus}, " | ")))
			return
		}
		p.Name = strings.TrimSpace(p.Name)
		if p.Action == RecordStop && p.Name == "" {
			r.Fail("name is required to stop a recording: it is the name the runbook is saved and run under")
			return
		}
		d.backend.RunbookRecord(r, p)

	case CmdLedgerOutput:
		// The reply gate comes FIRST, before any validation: a reply-required
		// command sent with nowhere to answer is a no-op, not a refusal nobody
		// can read.
		if !r.WantsReply() {
			return
		}
		p, ok := d.decodeBlockParams(dec, r)
		if !ok {
			return
		}
		d.backend.LedgerOutput(r, p)

	case CmdLedgerJump:
		p, ok := d.decodeBlockParams(dec, r)
		if !ok {
			return
		}
		d.backend.LedgerJump(r, p)

	case CmdUsageRefresh:
		// Ack the ask, not the answer: the reading lands later, on every client
		// at once, as a `usage` push.
		d.backend.RefreshUsage()
		r.OK(nil)

	case CmdChatSend:
		var p ChatSendParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if err := p.Validate(); err != nil {
			bad(err)
			return
		}
		d.backend.ChatSend(r, p)

	case CmdChatCancel:
		d.backend.ChatCancel(r)

	case CmdChatPermission:
		var p ChatPermissionParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if err := p.Validate(); err != nil {
			bad(err)
			return
		}
		d.backend.ChatPermission(r, p)

	case CmdChatClear:
		d.backend.ChatClear(r)

	case CmdServerReloadConfig:
		if err := d.backend.ReloadConfig(); err != nil {
			r.Fail(err.Error())
			return
		}
		r.OK(nil)

	case CmdServerStop:
		// Reply first so the caller gets its result, then go away.
		r.OK(nil)
		d.backend.Shutdown()

	case CmdSessionGet:
		// Read-only queries below answer straight from the session — no Backend
		// effect, no async round-trip.
		r.OK(d.session.InfoIn(d.ws()))

	case CmdWorkspaceList:
		r.OK(WorkspaceListResult{Workspaces: d.session.ListWorkspacesIn(d.ws())})

	case CmdTabList:
		var p TabListParams
		if err := decodeOptional(dec, &p); err != nil {
			bad(err)
			return
		}
		// Backend meta feeds auto-naming, so tab.list reports the same derived
		// names the browser tab bar shows.
		tabs, resolved, ok := d.session.ListTabsIn(d.ws(), p.Workspace, d.backend.PaneMeta)
		if !ok {
			r.Fail(fmt.Sprintf("unknown workspace %q", p.Workspace))
			return
		}
		r.OK(TabListResult{Workspace: resolved, Tabs: tabs})

	case CmdPaneList:
		panes := d.session.ListPanesIn(d.ws())
		// Merge in the runtime-side metadata (agent/title/cwd) the session can't
		// know; the backend answers from its per-pane runtime cache, so this is
		// loop-local and cheap even for many panes.
		for i := range panes {
			panes[i].PaneMeta = d.backend.PaneMeta(panes[i].Pane)
		}
		r.OK(PaneListResult{Panes: panes})

	case CmdPaneGet:
		var p OptPaneParams
		if err := decodeOptional(dec, &p); err != nil {
			bad(err)
			return
		}
		info, ok := d.session.PaneInfoForIn(d.ws(), optPaneID(p.Pane))
		if !ok {
			r.Fail("no such pane")
			return
		}
		info.PaneMeta = d.backend.PaneMeta(info.Pane)
		r.OK(info)

	case CmdHostList:
		r.OK(HostListResult{Hosts: d.backend.Hosts()})

	case CmdHostAttach:
		var p HostAttachParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		// Shape checks only. Whether the id is free, the address parses, and the
		// scheme is one catway can dial are all answered against the live roster
		// and config by the backend — the half that holds both.
		if p.ID == "" {
			r.Fail("host.attach: id is required")
			return
		}
		if p.Addr == "" {
			r.Fail("host.attach: addr is required (unix://path, tcp://host:port or tls://host:port)")
			return
		}
		d.backend.HostAttach(r, p)

	case CmdHostDetach:
		var p HostDetachParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if p.ID == "" {
			r.Fail("host.detach: id is required")
			return
		}
		// Unlike attach, the id must already be one: detaching a host nobody
		// named is a caller mistake worth reporting, and checkHost's message
		// already lists what does exist.
		if err := d.checkHost(p.ID); err != nil {
			r.Fail(err.Error())
			return
		}
		d.backend.HostDetach(r, p)

	default:
		r.Fail(fmt.Sprintf("command %q not supported yet (WS2 in progress)", name))
	}
}

// inheritedTabCwd is where a new tab opens when its caller named no cwd: the
// live working directory of the tab it lands beside — the workspace's last tab,
// since tab.create appends to the right end of the bar. Opening a tab next to
// one you are working in means "another shell here", and the neighbor's cwd is
// what the user sees as "here"; the workspace identity cwd it otherwise falls
// back to is only the directory the workspace *started* in.
//
// The pane's cwd comes from the Backend, which seeds it with the pane's spawn
// directory and refreshes it from OSC 7 — so a shell that never reports still
// yields the directory it was launched in, and the chain of inheritance holds.
// "" (no neighbor, or a pane the backend does not know) leaves Session.CreateTab's
// own default in place.
//
// wsID scopes the neighbor to the workspace the tab is actually going into ("" =
// the active one). Inheriting from the viewport's last tab when the tab lands
// elsewhere is the one way this can go quietly wrong: the pane would open in a
// directory belonging to a different project, which is precisely the mistake a
// per-workspace plugin launch exists to avoid.
// host is the cathost the new tab will run on ("" = the backend's default). A
// neighbor on another machine hands back nothing: its cwd names a directory in
// a filesystem the new pane cannot see, and spawning there would either fail or
// — worse — land on a same-named directory that is not the one the user is
// looking at.
func (d *Dispatcher) inheritedTabCwd(wsID, host string) string {
	pane, ok := d.session.NewTabNeighborPaneIn(wsID)
	if !ok {
		return ""
	}
	meta := d.backend.PaneMeta(uint32(pane))
	if !d.sameHost(meta.Host, host) {
		return ""
	}
	return meta.Cwd
}

// inheritedSplitCwd is where a pane split off target opens: the live cwd of the
// pane being split, which is the tab-level rule (inheritedTabCwd) applied to the
// one pane a split unambiguously comes from. "" — an unresolvable target, or a
// pane the backend does not know — leaves the workspace's spawn cwd in place.
// host is pane.split's host param. The cwd is inherited only when the pane being
// split is on that same machine, for the reason inheritedTabCwd gives.
//
// An empty host is the common case and always inherits: a split that names no
// host lands on the split pane's own machine (Workspace.splitHost), so the
// directory and the new terminal are on the same filesystem by construction.
// Resolving "" through the roster instead would compare the source pane against
// the *default* host and drop the cwd for every split of a guest pane.
func (d *Dispatcher) inheritedSplitCwd(target *layout.PaneID, host string) string {
	src, err := d.session.ResolvePaneTargetIn(d.ws(), target)
	if err != nil {
		return ""
	}
	meta := d.backend.PaneMeta(uint32(src))
	if host == "" {
		return meta.Cwd
	}
	if !d.sameHost(meta.Host, host) {
		return ""
	}
	return meta.Cwd
}

// decodeBlockParams decodes and checks the params ledger.output and ledger.jump
// share. Both refusals are about the PANE rather than the block, because a
// block is live terminal state: a closed pane has no blocks at all, and a
// disconnected host cannot be asked where one is.
func (d *Dispatcher) decodeBlockParams(dec ParamDecoder, r Responder) (LedgerBlockParams, bool) {
	var p LedgerBlockParams
	if err := dec.Decode(&p); err != nil {
		r.Fail("bad params: " + err.Error())
		return p, false
	}
	if p.Pane == 0 || p.Block == 0 {
		r.Fail("pane and block are both required")
		return p, false
	}
	if !d.backend.PaneExists(p.Pane) {
		r.Fail(fmt.Sprintf("pane %d not found: its scrollback is gone", p.Pane))
		return p, false
	}
	if !d.backend.PaneHostConnected(p.Pane) {
		r.Fail("cathost daemon not connected")
		return p, false
	}
	return p, true
}

// openFile implements pane.open_file: find the editor that should open Path,
// starting one if there is none, and hand it the request.
//
// The whole of this lives in the dispatcher rather than the backend because
// every question it asks is a MODEL question — which tab is this pane in, which
// panes exist, split this one — and the two runtime facts it needs (a pane's
// agent and host, the editor policy) already come over the Backend seam for
// other commands.
func (d *Dispatcher) openFile(r Responder, p OpenFileParams) {
	if strings.TrimSpace(p.Path) == "" {
		r.Fail("path is required")
		return
	}
	anchor, err := d.session.ResolvePaneTargetIn(d.ws(), optPaneID(p.Pane))
	if err != nil {
		r.Fail(err.Error())
		return
	}
	if err := d.checkHost(p.Host); err != nil {
		r.Fail(err.Error())
		return
	}
	// The target machine is where the FILE is, and that is the anchor's machine
	// unless the caller says otherwise. Everything below is scoped to it: an
	// editor elsewhere would resolve the same path string against a different
	// disk, which is the "a path is only half an identity" rule this codebase
	// learned the hard way in the worktree slice.
	host := p.Host
	if host == "" {
		host = d.backend.PaneMeta(uint32(anchor)).Host
	}
	ed := d.backend.EditorConfig()

	if p.Editor != nil {
		target := *p.Editor
		if !d.backend.PaneExists(target) {
			r.Fail(fmt.Sprintf("pane %d not found", target))
			return
		}
		if meta := d.backend.PaneMeta(target); !d.sameHost(meta.Host, host) {
			r.Fail(fmt.Sprintf("pane %d is on host %q, but the file is on %q",
				target, meta.Host, host))
			return
		}
		d.backend.OpenFileIn(target, p)
		r.OK(OpenFileResult{Pane: target, Host: host})
		return
	}

	if target, ok := d.findEditorPane(anchor, host, ed); ok {
		d.backend.OpenFileIn(target, p)
		r.OK(OpenFileResult{Pane: target, Host: host})
		return
	}

	spawn := ed.Spawn
	if p.Spawn != nil {
		spawn = *p.Spawn
	}
	if !spawn {
		r.Fail(noEditorErr(host))
		return
	}
	if len(ed.Command) == 0 {
		r.Fail("no editor is running and editor.command is not configured")
		return
	}
	// Starting an editor is starting a process in the workspace, so it answers
	// to the same lock as tab.create and a split carrying a command.
	if ws := d.viewWorkspace(); ws != nil && ws.Locked {
		r.Fail(workspaceLockedErr(ws.ID, "start an editor in"))
		return
	}
	np, err := d.session.SplitPaneOnIn(d.ws(), &anchor, layout.Horizontal, p.Host)
	if err != nil {
		r.Fail(err.Error())
		return
	}
	// The path rides the ARGV rather than a follow-up event: a just-spawned
	// editor is not subscribed yet, and an event emitted into that gap is
	// simply lost. The cost is the line number, which no editor CLI here
	// accepts — a cold open lands at the top of the file, which is why the
	// result reports that it spawned.
	d.backend.StageSpawn(uint32(np), SpawnOverride{Command: append(append([]string{}, ed.Command...), p.Path)})
	d.backend.ApplyModel()
	r.OK(OpenFileResult{Pane: uint32(np), Host: host, Spawned: true})
}

// findEditorPane picks the editor that should receive a request anchored at
// anchor, on host.
//
// Nearest wins: the anchor's own tab, then its workspace, then anywhere in the
// session. That is the order a person means by "the editor" — the one beside
// what they are looking at — and it degrades to "the only one there is" in the
// common case of a single editor.
//
// Ties inside a rung are broken by the lowest pane id rather than by focus
// recency. Deliberately: a stable answer means clicking two paths in a row
// opens both in the same editor, where "most recently focused" would send the
// second one wherever the first click happened to leave the focus.
func (d *Dispatcher) findEditorPane(anchor layout.PaneID, host string, ed EditorInfo) (uint32, bool) {
	if len(ed.Agents) == 0 {
		return 0, false
	}
	var inTab, inWorkspace, anywhere []uint32
	tabPanes, wsPanes := d.session.PaneNeighbourhood(anchor)
	for _, id := range d.session.AllPaneIDs() {
		meta := d.backend.PaneMeta(uint32(id))
		if !ed.IsEditorAgent(meta.Agent) || !d.sameHost(meta.Host, host) {
			continue
		}
		switch {
		case tabPanes[id]:
			inTab = append(inTab, uint32(id))
		case wsPanes[id]:
			inWorkspace = append(inWorkspace, uint32(id))
		default:
			anywhere = append(anywhere, uint32(id))
		}
	}
	for _, rung := range [][]uint32{inTab, inWorkspace, anywhere} {
		if len(rung) > 0 {
			return slices.Min(rung), true
		}
	}
	return 0, false
}

// noEditorErr phrases the refusal so the caller learns both facts it needs: no
// editor was found, and starting one was not allowed here.
func noEditorErr(host string) string {
	where := ""
	if host != "" {
		where = " on host " + strconv.Quote(host)
	}
	return "no editor pane" + where + " and spawning one is disabled"
}

// --- host resolution (the roster is the Backend's; the rules are here) -------
//
// Every host question a command asks — does this id exist, is it this machine,
// is it the same machine as that pane's — is answered from Backend.Hosts(), the
// roster host.list already reports. That is deliberately the only seam: a
// separate HostExists/DefaultHost pair would be two more methods every fake has
// to implement and two more chances for "exists" and "listed" to disagree.

// hostInfo resolves a host id against the live roster, following the model's
// own "" = the default host rule. ok is false only for a non-empty id the
// roster does not list — an empty roster (a backend with no hosts at all, which
// only a fake produces) resolves "" to the zero HostInfo and reports ok, since
// "no host named, none configured" is not a caller error.
func (d *Dispatcher) hostInfo(id string) (HostInfo, bool) {
	hosts := d.backend.Hosts()
	for _, h := range hosts {
		if h.ID == id {
			return h, true
		}
	}
	if id != "" {
		return HostInfo{}, false
	}
	for _, h := range hosts {
		if h.Default {
			return h, true
		}
	}
	if len(hosts) > 0 {
		return hosts[0], true // no host marked default: first configured wins
	}
	return HostInfo{}, true
}

// checkHost turns an unknown host id into the command's error. A command that
// names no host can never fail here — the default host always exists.
func (d *Dispatcher) checkHost(id string) error {
	if _, ok := d.hostInfo(id); !ok {
		return errors.New(unknownHostErr(id))
	}
	return nil
}

// sameHost reports whether two host ids name the same machine once both are
// resolved: "" is the default host on either side, and an id the roster has
// dropped compares equal only to itself. Used by the cwd-inheritance rules,
// where the wrong answer means a spawn directory from the wrong filesystem.
func (d *Dispatcher) sameHost(a, b string) bool {
	if a == b {
		return true
	}
	ha, aok := d.hostInfo(a)
	hb, bok := d.hostInfo(b)
	return aok && bok && ha.ID == hb.ID
}

// unknownHostErr phrases the refusal so a scripted caller can see which id was
// rejected and where the valid ones come from.
func unknownHostErr(id string) string {
	return fmt.Sprintf("unknown host %q (see host.list)", id)
}

// workspaceLockedErr phrases a refusal from a workspace lock. verb names what
// was being attempted ("run a command in"), so the message says which door is
// shut and how to open it rather than a bare "locked" — the caller may well be
// a script that never saw the sidebar's lock mark.
func workspaceLockedErr(id, verb string) string {
	return fmt.Sprintf("workspace %s is locked: cannot %s it (unlock it first)", id, verb)
}

// workspaceStartDir resolves workspace.create's optional Path against the
// session default, following WorkspaceCreateParams' three states: nil inherits
// sessionCwd, an explicitly empty string means the user's home ("start me
// somewhere neutral", the cleared field in the new-workspace dialog), and a
// typed path is expanded and checked. A path that does not resolve is returned
// as an error so the caller sees "no such directory" instead of a workspace
// quietly rooted somewhere else — unless mkdir says the user already confirmed
// they want that directory brought into existence, in which case it is created
// parents-and-all. The two defaulted states ignore mkdir: there is nothing to
// create when the answer is the session cwd or home.
//
// local says whether the workspace will live on this machine. When it does not,
// none of that resolution applies: "~" is the remote user's home, the stat
// would answer about the wrong filesystem, and mkdir would create the directory
// on the wrong machine. A typed path is passed through verbatim for cathost to
// interpret, and both defaulted states become "" — no directory named, so the
// remote pane starts wherever its cathost starts panes. That is also why a bad
// remote path cannot be reported here: the cwd fallback that keeps it from
// becoming a dead pane is host-side work (Phase 4).
func workspaceStartDir(path *string, sessionCwd string, mkdir, local bool) (string, error) {
	if !local {
		if path == nil {
			return "", nil
		}
		return *path, nil
	}
	if path == nil {
		return sessionCwd, nil
	}
	resolve := startdir.Resolve
	if mkdir {
		resolve = startdir.ResolveOrCreate
	}
	dir, err := resolve(*path, sessionCwd)
	if err != nil {
		return "", err
	}
	if dir == "" { // the field was cleared
		return startdir.Usable(), nil
	}
	return dir, nil
}

// decodeOptional decodes params whose fields are all optional: no params decodes
// to the zero value rather than an error (mirrors the old optUnmarshalParams).
func decodeOptional(dec ParamDecoder, v any) error {
	if err := dec.Decode(v); err != nil && !errors.Is(err, ErrNoParams) {
		return err
	}
	return nil
}
