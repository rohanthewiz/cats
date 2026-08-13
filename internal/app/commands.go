package app

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/startdir"
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
	PaneExists(pane uint32) bool
	DaemonConnected() bool
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

// Dispatcher runs the §7 command table against a Session and a Backend. It
// borrows the same *Session the backend holds (single-goroutine, no locking).
type Dispatcher struct {
	session *Session
	backend Backend
}

// NewDispatcher builds a dispatcher over a session and its runtime backend.
func NewDispatcher(s *Session, b Backend) *Dispatcher {
	return &Dispatcher{session: s, backend: b}
}

// Dispatch runs one §7 command. Loop-goroutine only (it shares the session with
// the backend). Model-mutating commands call a Session method then reconcile via
// ApplyModel; pure focus/rename rebroadcast the layout; scroll passes through;
// read/capture start an async daemon round-trip; server.* are lifecycle.
func (d *Dispatcher) Dispatch(name string, dec ParamDecoder, r Responder) {
	// bad reports a malformed-params failure in the historical wording.
	bad := func(err error) { r.Fail("bad params: " + err.Error()) }

	switch name {
	case CmdPaneFocus:
		var p PaneParams
		if err := dec.Decode(&p); err != nil {
			bad(err)
			return
		}
		if err := d.session.FocusPane(layout.PaneID(p.Pane)); err != nil {
			r.Fail(err.Error())
			return
		}
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
		moved, err := d.session.FocusPaneDirection(nav, d.backend.Area())
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
		if d.session.CyclePane(p.Next) {
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
		swapped, err := d.session.SwapPaneDirection(nav, d.backend.Area())
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
		swapped, err := d.session.SwapPanes(layout.PaneID(p.Pane), layout.PaneID(p.Target))
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
		if _, err := d.session.ToggleZoom(optPaneID(p.Pane)); err != nil {
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
		if err := d.session.ResizeBorder(path, p.Ratio); err != nil {
			r.Fail(err.Error())
			return
		}
		d.backend.ApplyModel() // split ratio changed → panes resize
		r.OK(nil)

	case CmdPaneLast:
		if d.session.FocusLastPane() {
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
			if ws := d.session.ActiveWorkspace(); ws != nil && ws.Locked {
				r.Fail(workspaceLockedErr(ws.ID, "run a command in"))
				return
			}
		}
		// Resolve the source pane's cwd before the split, for the same reason a new
		// tab takes its neighbor's: the new pane is another shell in the work the
		// user is already doing.
		inherited := d.inheritedSplitCwd(optPaneID(sp.Pane))
		np, err := d.session.SplitPane(optPaneID(sp.Pane), dir)
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
		if _, err := d.session.ClosePane(optPaneID(cp.Pane)); err != nil {
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
		if !d.backend.DaemonConnected() {
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
		if !d.backend.DaemonConnected() {
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
		if !d.backend.DaemonConnected() {
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
		// Gate on the daemon like read/capture: the backend's write path drops
		// silently when disconnected, and a vanished prompt is worse than an error.
		if !d.backend.DaemonConnected() {
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
		// A locked workspace takes no supplied command line — this is the path a
		// plugin action and an agent launch both come in on (the browser's
		// pluginRunAction, `catctl plugin run`). A bare tab.create is a shell the
		// user asked for by hand, so it goes through: the lock keeps automation
		// out, it does not put the workspace behind glass.
		if len(p.Command) > 0 {
			if ws := d.session.ActiveWorkspace(); ws != nil && ws.Locked {
				r.Fail(workspaceLockedErr(ws.ID, "run a command in"))
				return
			}
		}
		// Resolve the left-hand neighbor before the create, while it is still the
		// workspace's last tab.
		inherited := d.inheritedTabCwd()
		num, err := d.session.CreateTab()
		if err != nil {
			r.Fail(err.Error())
			return
		}
		// CreateTab switches to the new tab, so the globally focused pane is its
		// root pane — returned so an automation client can drive the fresh pane
		// (send_input / wait_for_output) without diffing pane.list.
		res := TabCreateResult{Num: num}
		if id, ok := d.session.FocusedPane(); ok {
			res.Pane = uint32(id)
		}
		if p.Title != "" {
			// Same session mutation as tab.rename; the tab was just created, so
			// the only failure would be a vanished tab — not worth failing the
			// whole create over.
			_ = d.session.RenameTab(num, p.Title)
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
		if err := d.session.CloseTab(p.Num); err != nil {
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
		if err := d.session.FocusTab(p.Num); err != nil {
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
		if err := d.session.RenameTab(p.Num, p.Name); err != nil {
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
		moved, err := d.session.MoveTab(p.Num, p.Index)
		if err != nil {
			r.Fail(err.Error())
			return
		}
		if moved {
			d.backend.BroadcastLayout() // order changed; pane set unchanged
		}
		r.OK(nil)

	case CmdWorkspaceCreate:
		var p WorkspaceCreateParams
		if err := decodeOptional(dec, &p); err != nil {
			bad(err)
			return
		}
		cwd, err := workspaceStartDir(p.Path, d.session.Cwd(), p.Mkdir)
		if err != nil {
			r.Fail(err.Error())
			return
		}
		id, err := d.session.CreateWorkspaceAt(cwd)
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
		d.backend.ApplyModel()
		r.OK(WorkspaceCreateResult{ID: id})

	case CmdWorkspaceClose:
		var p WorkspaceParams
		_ = dec.Decode(&p) // id optional → active workspace; ignore any decode error
		var id *string
		if p.ID != "" {
			id = &p.ID
		}
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
		if err := d.session.FocusWorkspace(p.ID); err != nil {
			r.Fail(err.Error())
			return
		}
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
		changed, err := d.session.SetWorkspaceLock(p.ID, p.Locked)
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
		if err := d.session.RevealPane(layout.PaneID(p.Pane)); err != nil {
			r.Fail(err.Error())
			return
		}
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
		r.OK(d.session.Info())

	case CmdWorkspaceList:
		r.OK(WorkspaceListResult{Workspaces: d.session.ListWorkspaces()})

	case CmdTabList:
		var p TabListParams
		if err := decodeOptional(dec, &p); err != nil {
			bad(err)
			return
		}
		// Backend meta feeds auto-naming, so tab.list reports the same derived
		// names the browser tab bar shows.
		tabs, resolved, ok := d.session.ListTabs(p.Workspace, d.backend.PaneMeta)
		if !ok {
			r.Fail(fmt.Sprintf("unknown workspace %q", p.Workspace))
			return
		}
		r.OK(TabListResult{Workspace: resolved, Tabs: tabs})

	case CmdPaneList:
		panes := d.session.ListPanes()
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
		info, ok := d.session.PaneInfoFor(optPaneID(p.Pane))
		if !ok {
			r.Fail("no such pane")
			return
		}
		info.PaneMeta = d.backend.PaneMeta(info.Pane)
		r.OK(info)

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
func (d *Dispatcher) inheritedTabCwd() string {
	pane, ok := d.session.NewTabNeighborPane()
	if !ok {
		return ""
	}
	return d.backend.PaneMeta(uint32(pane)).Cwd
}

// inheritedSplitCwd is where a pane split off target opens: the live cwd of the
// pane being split, which is the tab-level rule (inheritedTabCwd) applied to the
// one pane a split unambiguously comes from. "" — an unresolvable target, or a
// pane the backend does not know — leaves the workspace's spawn cwd in place.
func (d *Dispatcher) inheritedSplitCwd(target *layout.PaneID) string {
	src, err := d.session.ResolvePaneTarget(target)
	if err != nil {
		return ""
	}
	return d.backend.PaneMeta(uint32(src)).Cwd
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
func workspaceStartDir(path *string, sessionCwd string, mkdir bool) (string, error) {
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
