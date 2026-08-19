package app

import "slices"

// This file is the control-API streaming-event vocabulary (events.subscribe): the
// event names and their payload shapes the control API pushes to a subscribed
// client, plus the subscription filter. It lives in internal/app alongside the §7
// command vocabulary so the event contract has one home, but unlike the unary
// commands these are not routed through Dispatcher — the streaming transport
// (internal/ctlproto) carries them and the orchestrator (cmd/catway) emits them.
// They mirror the pane lifecycle/chrome the orchestrator already observes from the
// terminal backend, flattened into an automation-friendly JSON stream (catctl
// prints one per line); the browser's own richer down-messages live in browserproto.
//
// The Pane field on every event is the internal pane id — the same id every other
// §7 command addresses a pane by (get it from pane.list), so a subscriber can act
// on the pane an event names.

// Event names (events.subscribe). A subscription with no Events filter receives
// all of them. They come from four places: the terminal backend (a pane's child
// — exit, agent, title, cwd), the orchestrator's own doings (notify, action,
// open_file), a diff of the session model after each mutation (added, removed,
// focus — split/close/focus/tab/workspace), and the session at large (theme, the
// host link, a finished runbook), which name no pane and so carry pane 0.
const (
	EventPaneExited = "pane_exited" // the pane's child process exited
	EventPaneAgent  = "pane_agent"  // detected agent identity/state changed
	EventPaneTitle  = "pane_title"  // the program set the pane's title (OSC 0/2)
	EventPaneCwd    = "pane_cwd"    // the pane's working directory changed (OSC 7)
	EventPaneNotify = "pane_notify" // an agent state change warrants attention (blocked / background finish)
	EventUIAction   = "ui_action"   // somebody took an action on a notification (ui.action, or a push action button)
	// EventPaneOpenFile is addressed AT a pane rather than reported about one:
	// it is cats asking the editor running in that pane to open a path
	// (pane.open_file). Every other event here is a fact; this one is a
	// request, which is why an editor subscribes filtered to its own pane and
	// nothing else in the session needs to care.
	EventPaneOpenFile = "pane_open_file"

	EventPaneAdded    = "pane_added"    // a pane entered the session (split / new tab / new workspace)
	EventPaneRemoved  = "pane_removed"  // a pane left the session (close pane / tab / workspace)
	EventFocusChanged = "focus_changed" // the globally-focused pane changed

	// EventThemeChanged is the first SESSION-scoped event: it names no pane, so
	// it is emitted with pane 0 and a pane-scoped subscription
	// (EventsSubscribeParams.Pane) will not see it. That follows from the filter's
	// contract rather than working around it — a client that asked about one pane
	// asked about one pane — so a subscriber that wants both takes two streams, or
	// one unfiltered stream and does its own pane matching.
	EventThemeChanged = "theme_changed" // the effective theme changed (config.set / theme.save / theme.delete)

	// EventHostConnected / EventHostDisconnected are the roster's transitions,
	// and they are named for the LINK rather than for host.attach / host.detach
	// on purpose. Those two commands edit the roster and answer immediately —
	// host.attach returns before a single packet has been sent, because the dial
	// has its own retry loop — so an event called "host_attached" would fire at
	// a moment that has nothing to do with the command of nearly the same name,
	// and would also fire on every reconnect, when nothing was attached at all.
	//
	// What a subscriber (and a runbook trigger) actually wants to know is when
	// the machine became usable and when it stopped being usable, which is the
	// handshake completing and the pump returning. Both are session-scoped: they
	// name a host, not a pane, so they carry pane 0 and a pane-filtered
	// subscription will not see them, exactly as theme_changed does.
	EventHostConnected    = "host_connected"    // a cathost completed its handshake and is serving
	EventHostDisconnected = "host_disconnected" // a cathost's link dropped (or was detached)

	// EventRunbookFinished reports the outcome of one runbook run. It exists for
	// the runs nobody is waiting on: a run started by an `on:` trigger has no
	// caller to hand a RunbookRunResult back to, so without this a triggered
	// runbook that failed halfway would leave no trace outside the log. Emitted
	// for MANUAL runs too — a client that watches the stream should not have to
	// know which runs it will be told about — and session-scoped for the same
	// reason as the two above.
	EventRunbookFinished = "runbook_finished"
)

// eventSpec is one entry in the event vocabulary: the name a subscriber filters
// on, and a zero value of the payload it carries.
//
// The payload is here so the vocabulary is machine-readable rather than only
// documented. A runbook trigger checks its `where:` keys and its
// `{{ event.field }}` references against these structs at LOAD time, which is
// the only way `where: {exit_cod: 0}` can be a refusal instead of a filter that
// silently never matches — the same failure encoding/json's dropped-key
// behaviour causes for command params, one layer out.
type eventSpec struct {
	Name    string
	Payload any
}

// eventSpecs is the whole streaming vocabulary. Order is the reading order of
// the const block above: pane facts, the one pane request, structure, session.
var eventSpecs = []eventSpec{
	{EventPaneExited, PaneExitedEvent{}},
	{EventPaneAgent, PaneAgentEvent{}},
	{EventPaneTitle, PaneTitleEvent{}},
	{EventPaneCwd, PaneCwdEvent{}},
	{EventPaneNotify, PaneNotifyEvent{}},
	{EventUIAction, UIActionEvent{}},
	{EventPaneOpenFile, PaneOpenFileEvent{}},

	{EventPaneAdded, PaneRefEvent{}},
	{EventPaneRemoved, PaneRefEvent{}},
	{EventFocusChanged, PaneRefEvent{}},

	{EventThemeChanged, ThemeChangedEvent{}},
	{EventHostConnected, HostLinkEvent{}},
	{EventHostDisconnected, HostLinkEvent{}},
	{EventRunbookFinished, RunbookFinishedEvent{}},
}

// EventNames returns every event name events.subscribe can emit, in a stable
// order — the vocabulary a client validates an Events filter against.
func EventNames() []string {
	out := make([]string, 0, len(eventSpecs))
	for _, s := range eventSpecs {
		out = append(out, s.Name)
	}
	return out
}

// EventPayload returns a zero value of the payload the named event carries, and
// whether the name is in the vocabulary at all. A caller that only wants to
// know an event exists ignores the first return.
func EventPayload(name string) (any, bool) {
	for _, s := range eventSpecs {
		if s.Name == name {
			return s.Payload, true
		}
	}
	return nil, false
}

// PaneExitedEvent is the payload for EventPaneExited.
type PaneExitedEvent struct {
	Pane     uint32 `json:"pane"`
	ExitCode int    `json:"exit_code"`
}

// PaneAgentEvent is the payload for EventPaneAgent. Agent is "" for a plain shell;
// State is one of idle|working|blocked|unknown.
type PaneAgentEvent struct {
	Pane  uint32 `json:"pane"`
	Agent string `json:"agent"`
	State string `json:"state"`
}

// PaneTitleEvent is the payload for EventPaneTitle. Title is "" on a title-clear.
type PaneTitleEvent struct {
	Pane  uint32 `json:"pane"`
	Title string `json:"title"`
}

// PaneCwdEvent is the payload for EventPaneCwd.
type PaneCwdEvent struct {
	Pane uint32 `json:"pane"`
	Cwd  string `json:"cwd"`
}

// PaneNotifyEvent is the payload for EventPaneNotify: a notification-worthy
// agent state transition — the agent hit a blocker (kind "attention") or a
// background run completed (kind "finished"). Mirrors the browser's notify
// down-message so an automation client can react to the same moments.
type PaneNotifyEvent struct {
	Pane    uint32 `json:"pane"`
	Agent   string `json:"agent"`
	Kind    string `json:"kind"` // attention | finished | info
	Message string `json:"message"`
	// ID and Actions are set for a notification raised through ui.notify that
	// declared buttons. A subscriber that wants to answer one — a phone bridge,
	// a chat relay, a status bar — needs the id to send ui.action and the
	// labels to draw, and learning them from the event costs it no extra call.
	ID      string         `json:"id,omitempty"`
	Actions []NotifyAction `json:"actions,omitempty"`
}

// UIActionEvent is the payload for EventUIAction: somebody took action Action
// on notification ID. It is emitted AFTER the action's own Send has been
// injected, so a subscriber reading it knows the effect has already happened
// rather than that it is about to.
//
// Source says where the tap came from — "control" for a ui.action command,
// "push" for the notification-action endpoint a phone reaches. A runbook that
// treats "the human answered from their desk" and "the human answered from a
// lock screen" identically is free to ignore it; one that logs who did what is
// not.
type UIActionEvent struct {
	Pane   uint32 `json:"pane"` // the notification's pane, 0 for a session-level one
	ID     string `json:"id"`
	Action string `json:"action"`
	Source string `json:"source"`
}

// PaneOpenFileEvent is the payload for EventPaneOpenFile: open Path in the
// editor running in Pane.
//
// Path is verbatim as the caller gave it — see OpenFileParams — because the
// editor is the thing that can resolve it: it is on that machine, and it knows
// its own root. Line and Column are 1-based and 0 means "wherever the file
// opens", which is also what a freshly spawned editor gets, since it learns the
// path from its argv and never sees this event.
type PaneOpenFileEvent struct {
	Pane   uint32 `json:"pane"`
	Path   string `json:"path"`
	Line   int    `json:"line,omitempty"`
	Column int    `json:"column,omitempty"`
}

// PaneRefEvent is the payload for the three model-structure events
// (EventPaneAdded / EventPaneRemoved / EventFocusChanged): they all just name a
// pane. Pane is the internal id (as pane.list reports); Handle is its public label
// ("w1:p3") at the moment of the event — for a removed pane, the handle it last
// had. For focus_changed, Pane is the newly-focused pane (0 if none).
type PaneRefEvent struct {
	Pane   uint32 `json:"pane"`
	Handle string `json:"handle,omitempty"`
}

// ThemeChangedEvent is the payload for EventThemeChanged: the EFFECTIVE
// appearance after the change — exactly the ConfigTheme a config.get would
// return, fully resolved (named theme + the user's per-key overrides already
// applied), so a subscriber restyles from the event alone.
//
// It is an alias rather than a struct of its own because the two must never
// drift: an automation client that already parses config.get's theme section
// parses this with the same code, and a new color key added to the resolved
// palette reaches the stream without a second edit. The name exists to give the
// event vocabulary its own word for the payload.
//
// This is the event that retires the poll-on-focus_changed workaround: without
// it a client watching the theme had to re-issue config.get whenever anything
// happened and diff the answer.
type ThemeChangedEvent = ConfigTheme

// HostLinkEvent is the payload for EventHostConnected / EventHostDisconnected:
// which cathost changed state, and — on a disconnect — why.
//
// Pane is 0 and always will be: a host is not a pane. It is carried anyway
// because every event on this stream has the field, and a subscriber's filter
// (EventsSubscribeParams.Match) reads it unconditionally.
//
// Error is the disconnect's cause when there was one ("connection refused", a
// handshake rejection), and "" both for a connect and for a link that was closed
// deliberately — host.detach, or catway shutting down. A subscriber that wants
// to distinguish "the box went away" from "I let it go" reads it; one that just
// wants to know the host is unusable does not have to.
type HostLinkEvent struct {
	Pane  uint32 `json:"pane"`
	Host  string `json:"host"`            // the roster id, as host.list reports it
	Label string `json:"label,omitempty"` // its display name, "" when it is just the id
	Addr  string `json:"addr,omitempty"`  // scheme://target, as configured
	Error string `json:"error,omitempty"`
}

// RunbookFinishedEvent is the payload for EventRunbookFinished: one run of one
// runbook is over, and this is how it went.
//
// It deliberately carries a SUMMARY rather than the RunbookRunResult a caller
// gets back. A run's per-step list is the answer to "what happened", and the
// caller who asked for the run already has it; this event answers "did the thing
// I set up actually work", which is a question asked by whoever is watching the
// stream — usually about a run nobody called at all.
//
// Trigger is the event name that started it, "" for a manual run. With Source it
// makes a triggered run traceable back to its cause without correlating
// timestamps across two streams.
type RunbookFinishedEvent struct {
	Pane    uint32 `json:"pane"` // always 0; a runbook belongs to the session, not a pane
	Name    string `json:"name"`
	Source  string `json:"source"` // "control" for runbook.run, "trigger" for an on: firing
	Trigger string `json:"trigger,omitempty"`
	Steps   int    `json:"steps"` // steps that ran (skipped ones are not counted)
	Failed  bool   `json:"failed,omitempty"`
	// FailedStep and Error name the FIRST step that failed, 0/"" when none did.
	// One is enough: a runbook stops at its first untolerated failure, and a run
	// that continued past one has already said so in its own log line.
	FailedStep int    `json:"failed_step,omitempty"`
	Error      string `json:"error,omitempty"`
}

// EventsSubscribeParams is the params object for events.subscribe. Both fields are
// optional: an absent Pane matches every pane, an empty Events matches every event
// name. The orchestrator applies the filter server-side so a narrow subscription
// only carries the frames it wants.
type EventsSubscribeParams struct {
	Pane   *uint32  `json:"pane,omitempty"`
	Events []string `json:"events,omitempty"`
}

// Match reports whether an event of the given name for the given pane passes this
// filter.
func (f EventsSubscribeParams) Match(event string, pane uint32) bool {
	if f.Pane != nil && *f.Pane != pane {
		return false
	}
	if len(f.Events) > 0 && !slices.Contains(f.Events, event) {
		return false
	}
	return true
}
