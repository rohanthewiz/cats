//go:build ghostty

package main

import (
	"github.com/rohanthewiz/cats/internal/app"
)

// pane.open_file's backend half — which is almost nothing, and that is the
// design.
//
// "Click a path anywhere in cats and it opens in the editor" could have been
// built as an integration: cats learns an editor's CLI, runs `ced --remote` on
// the right machine, handles the case where no instance is listening. It is
// not. cats works out WHICH pane should hear the request (the dispatcher's
// openFile) and emits an event on the control stream that pane's editor is
// already subscribed to.
//
// Three things fall out of that, and each is a thing not written:
//
//   - **No editor CLI here.** ced's own remote discovery — probe every socket,
//     pick the instance whose root contains the file, longest root wins — is
//     ced's, runs on ced's machine, and is better than anything this side could
//     reconstruct from a pane id.
//   - **Cross-host is free.** An editor in a pane on another machine subscribes
//     through the control relay (slice 1, phase 7), so the event reaches it by
//     the same path a local one takes. There is no remote branch in this file
//     because there is no local one either.
//   - **Any editor works.** The contract is one event name and a pane's own
//     agent label. Nothing here knows what ced is.
//
// The one thing cats does run is a spawn, when there is no editor to ask — and
// that goes through the ordinary split path with the file in its argv, because
// an editor that has not started cannot be subscribed to an event.

// EditorConfig implements app.Backend: the editor policy, from the live config.
func (o *orch) EditorConfig() app.EditorInfo {
	e := o.cfg.Editor
	return app.EditorInfo{Agents: e.Agents, Command: e.Command, Spawn: e.Spawn}
}

// resolvePluginType settles what kind of tool a pane runs, for the two places
// that report it (the sidebar's rollup, agentsMsg, and pane.list's PaneMeta),
// so they cannot disagree. It layers tools.types over the editor policy:
//
//  1. editor.agents, or a manifest that declared "editor" → editor (unchanged:
//     wire.EditorInfo.ResolvePluginType, which the dispatcher shares).
//  2. tools.types names the agent label → that type, whatever launched it.
//  3. otherwise the launching manifest's declared type, "" for none.
//
// Step 2 beats the manifest for the same reason editor.agents does: the config
// is the user's statement about the tool, and it is the only source a tool
// typed into a shell has at all. It never makes a pane an editor
// (config.Tools.validate refuses that), so editor is decided by step 1 alone.
//
// It lives here rather than in wire because the tool map is catway's config;
// the wire type stays the editor-only policy the dispatcher needs.
func (o *orch) resolvePluginType(ed app.EditorInfo, agent, declared string) (typ string, editor bool) {
	if typ, editor = ed.ResolvePluginType(agent, declared); editor {
		return typ, true
	}
	if t := o.cfg.Tools.TypeFor(agent); t != "" {
		return t, false
	}
	return typ, false
}

// OpenFileIn implements app.Backend: hand the request to the editor in pane.
//
// The event is pane-addressed, so an editor subscribed to its own pane sees
// only its own requests and nothing else in the session has to filter. There is
// no delivery guarantee and deliberately no reply: an editor that has not
// subscribed yet simply does not receive it, which is a state, not a failure —
// and the alternative (hold the request until somebody connects) would mean a
// file opening minutes later in an editor started for something else.
func (o *orch) OpenFileIn(pane uint32, p app.OpenFileParams) {
	o.emitEvent(app.EventPaneOpenFile, pane, app.PaneOpenFileEvent{
		Pane: pane, Path: p.Path, Line: p.Line, Column: p.Column,
	})
}
