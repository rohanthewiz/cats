//go:build ghostty

package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/config"
	"github.com/rohanthewiz/cats/internal/flags"
	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/orchestration"
	"github.com/rohanthewiz/cats/internal/plugin"
)

// The AGENTS rollup carries plugin panes in their own list, grouped and ordered
// by plugin id — the two properties the sidebar's blocks are cut on. A pane that
// is BOTH (a plugin that launched an agent) belongs to the agent list only: one
// pane, one row, and the row that reports a live state is the useful one.
func TestAgentsRollupGroupsPluginPanes(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	first := layout.PaneID(o.session.AllPaneIDs()[0])
	second, err := o.session.SplitPane(nil, layout.Horizontal)
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	third, err := o.session.SplitPane(nil, layout.Vertical)
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	o.syncDaemon() // gives every pane a runtime

	// Two plugins, deliberately staged in the reverse of their sorted order, so
	// a rollup that merely preserved the walk would fail this.
	o.session.SetPanePlugin(first, "zz.notes", "notes_mgr")
	o.panes[uint32(first)].title = "notes"
	o.session.SetPanePlugin(second, "aa.todo", "todos_mgr")
	o.panes[uint32(second)].title = "todo: cats (3)"
	// The third pane is a plugin pane running an agent.
	o.session.SetPanePlugin(third, "aa.todo", "todos_mgr")
	o.onPaneAgent(orchestration.PaneAgent{PaneID: uint32(third), Agent: "claude", State: "working"})

	msg := o.agentsMsg()
	if len(msg.Items) != 1 || msg.Items[0].Pane != uint32(third) {
		t.Fatalf("agent items: %+v", msg.Items)
	}
	if len(msg.Plugins) != 2 {
		t.Fatalf("plugin panes: got %d want 2 (%+v)", len(msg.Plugins), msg.Plugins)
	}
	if msg.Plugins[0].Plugin != "aa.todo" || msg.Plugins[0].Pane != uint32(second) {
		t.Fatalf("plugin panes are not ordered by plugin id: %+v", msg.Plugins)
	}
	if msg.Plugins[0].Title != "todo: cats (3)" {
		t.Fatalf("plugin pane title: %+v", msg.Plugins[0])
	}
	// The declared type rides each row: it is what the sidebar splits AGENTS
	// from PLUGINS on.
	if msg.Plugins[0].Type != "todos_mgr" || msg.Plugins[1].Type != "notes_mgr" {
		t.Fatalf("plugin pane types: %+v", msg.Plugins)
	}
	pub, _ := o.session.PublicPaneID(second)
	if msg.Plugins[0].Pub != pub || msg.Plugins[0].Workspace == "" {
		t.Fatalf("plugin pane handle: %+v", msg.Plugins[0])
	}
	if msg.Plugins[1].Plugin != "zz.notes" || msg.Plugins[1].Pane != uint32(first) {
		t.Fatalf("second group: %+v", msg.Plugins[1])
	}

	// A flag set on a plugin pane rides the rollup, as it does on an agent row —
	// AGENTS is where flags get set, and the rollup is the only message that
	// spans every workspace.
	f, err := flags.New("followup", "check the backlog", time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("flags.New: %v", err)
	}
	if _, err := o.session.SetPaneFlag(second, f); err != nil {
		t.Fatalf("SetPaneFlag: %v", err)
	}
	if got := o.agentsMsg().Plugins[0].Flag; got != "followup" {
		t.Fatalf("flag on a plugin row: %q", got)
	}

	// A corpse leaves the section: nothing is running in it to come back to.
	o.panes[uint32(first)].exited = new(int)
	if got := len(o.agentsMsg().Plugins); got != 1 {
		t.Fatalf("an exited plugin pane should leave the rollup, got %d rows", got)
	}
}

// An editor reports over the hook API, but it is a tool, not an agent: the
// rollup files its pane with the plugins — under its launch id when the plugin
// host started it, under its agent label when it was typed into a shell — while
// PaneMeta keeps the agent label, because that label is how pane.open_file
// finds the editor. A real coding agent in the same session stays an agent row.
func TestAgentsRollupListsEditorsAsTools(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	o.cfg = config.Default() // editor.agents = ["ced"]
	launched := layout.PaneID(o.session.AllPaneIDs()[0])
	typed, err := o.session.SplitPane(nil, layout.Horizontal)
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	agentPane, err := o.session.SplitPane(nil, layout.Vertical)
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	o.syncDaemon()

	// Hook authority, as ced's reporter establishes it — including a blocked
	// state, which must not be what drags an editor back into the agent list.
	// No declared type: ced's manifest predates the key, and editor.agents
	// alone has to be enough to call it an editor.
	o.session.SetPanePlugin(launched, "rohanthewiz.ced", "")
	o.panes[uint32(launched)].title = "main.go — ced"
	o.panes[uint32(launched)].hook = &hookAuthority{source: "ced", agent: "ced", state: "blocked", reportedAt: time.Now()}
	o.panes[uint32(typed)].hook = &hookAuthority{source: "ced", agent: "ced", state: "idle", reportedAt: time.Now()}
	o.onPaneAgent(orchestration.PaneAgent{PaneID: uint32(agentPane), Agent: "claude", State: "working"})

	msg := o.agentsMsg()
	if len(msg.Items) != 1 || msg.Items[0].Pane != uint32(agentPane) {
		t.Fatalf("only the coding agent should be an agent row: %+v", msg.Items)
	}
	if len(msg.Plugins) != 2 {
		t.Fatalf("plugin panes: got %d want 2 (%+v)", len(msg.Plugins), msg.Plugins)
	}
	// Sorted by id: "ced" < "rohanthewiz.ced".
	if msg.Plugins[0].Plugin != "ced" || msg.Plugins[0].Pane != uint32(typed) {
		t.Fatalf("a shell-launched editor should carry its agent label: %+v", msg.Plugins[0])
	}
	if msg.Plugins[1].Plugin != "rohanthewiz.ced" || msg.Plugins[1].Title != "main.go — ced" {
		t.Fatalf("a plugin-launched editor should carry its launch id and title: %+v", msg.Plugins[1])
	}
	// Both are typed "editor" by the config, however they were started — that
	// is what lands them in PLUGINS rather than back in AGENTS.
	for _, p := range msg.Plugins {
		if p.Type != "editor" {
			t.Fatalf("an editor pane should be typed editor: %+v", p)
		}
	}

	// The label open_file matches on survives the reclassification.
	if meta := o.PaneMeta(uint32(launched)); meta.Agent != "ced" || meta.AgentState != "blocked" {
		t.Fatalf("PaneMeta lost the editor's agent pair: %+v", meta)
	}
	// pane.list says what the sidebar says: an editor, launched by ced's
	// plugin — so a client picking a drop target skips it without keeping
	// its own copy of editor.agents. The shell-typed one has no launch id,
	// but is an editor all the same.
	if meta := o.PaneMeta(uint32(launched)); meta.Plugin != "rohanthewiz.ced" || meta.PluginType != "editor" || meta.IsDropAgent() {
		t.Fatalf("PaneMeta should mark a launched editor as a non-drop plugin: %+v", meta)
	}
	if meta := o.PaneMeta(uint32(typed)); meta.Plugin != "" || meta.PluginType != "editor" || meta.IsDropAgent() {
		t.Fatalf("PaneMeta should mark a shell-typed editor as a non-drop plugin: %+v", meta)
	}
	if meta := o.PaneMeta(uint32(agentPane)); meta.PluginType != "" || !meta.IsDropAgent() {
		t.Fatalf("a coding agent should stay a drop target: %+v", meta)
	}

	// With no editors configured the old rule holds: a hook-reported pane is an
	// agent, whatever launched it.
	o.cfg.Editor.Agents = nil
	if got := len(o.agentsMsg().Items); got != 3 {
		t.Fatalf("without editor.agents every reporting pane is an agent, got %d", got)
	}

	// ...unless the plugin's own manifest says it is an editor. The declared
	// type is enough to keep it out of AGENTS when the user never listed it.
	o.session.SetPanePlugin(launched, "rohanthewiz.ced", "editor")
	msg = o.agentsMsg()
	if len(msg.Items) != 2 {
		t.Fatalf("a declared editor should leave the agent roster, got %d agent rows", len(msg.Items))
	}
	if len(msg.Plugins) != 1 || msg.Plugins[0].Plugin != "rohanthewiz.ced" || msg.Plugins[0].Type != "editor" {
		t.Fatalf("a declared editor should be a plugin row: %+v", msg.Plugins)
	}
}

// A tool that reports over the hook API under its own name is typed by
// tools.types however it was started (N-035): dbc typed into a shell is a
// db_client, not an LLM agent, so a drop picker reading pane.list skips it —
// exactly as it already skipped a dbc that `plugin run` launched. The map beats
// a launching manifest's type, never reaches claude, and an opt-out ("") falls
// back to the old reading.
func TestToolTypesTypeShellLaunchedTools(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	o.cfg = config.Default() // tools.types: dbc → db_client, gonotes → notes_mgr
	dbc := layout.PaneID(o.session.AllPaneIDs()[0])
	notes, err := o.session.SplitPane(nil, layout.Horizontal)
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	agentPane, err := o.session.SplitPane(nil, layout.Vertical)
	if err != nil {
		t.Fatalf("SplitPane: %v", err)
	}
	o.syncDaemon()

	// Shell-typed: no SetPanePlugin, so nothing but the hook label to go on.
	// The label's case is the tool's own business; the match ignores it.
	o.panes[uint32(dbc)].hook = &hookAuthority{source: "dbc", agent: "dbc", state: "idle", reportedAt: time.Now()}
	o.panes[uint32(notes)].hook = &hookAuthority{source: "gonotes", agent: "GoNotes", state: "idle", reportedAt: time.Now()}
	o.onPaneAgent(orchestration.PaneAgent{PaneID: uint32(agentPane), Agent: "claude", State: "working"})

	if meta := o.PaneMeta(uint32(dbc)); meta.PluginType != "db_client" || meta.IsDropAgent() {
		t.Fatalf("a shell-typed dbc should be a db_client, not a drop target: %+v", meta)
	}
	if meta := o.PaneMeta(uint32(notes)); meta.PluginType != "notes_mgr" || meta.IsDropAgent() {
		t.Fatalf("a shell-typed gonotes should be a notes_mgr, not a drop target: %+v", meta)
	}
	if meta := o.PaneMeta(uint32(agentPane)); meta.PluginType != "" || !meta.IsDropAgent() {
		t.Fatalf("a coding agent should stay a drop target: %+v", meta)
	}

	// The config beats whatever the launching manifest declared.
	o.session.SetPanePlugin(dbc, "rohanthewiz.dbc", "git")
	if meta := o.PaneMeta(uint32(dbc)); meta.PluginType != "db_client" {
		t.Fatalf("tools.types should beat the manifest's type: %+v", meta)
	}

	// Opting a label out restores the manifest's word, or none at all.
	o.cfg.Tools.Types["dbc"] = ""
	if meta := o.PaneMeta(uint32(dbc)); meta.PluginType != "git" {
		t.Fatalf("an opted-out label should fall back to the manifest: %+v", meta)
	}
	o.session.SetPanePlugin(dbc, "", "")
	if meta := o.PaneMeta(uint32(dbc)); meta.PluginType != "" || !meta.IsDropAgent() {
		t.Fatalf("an opted-out, shell-typed label reads as an agent again: %+v", meta)
	}
}

// createPane is the one place that knows what a pane's child actually is, so it
// is where the pane's plugin identity is written — and, just as importantly,
// cleared. A plugin pane whose host restarted comes back through here as a plain
// shell, and a stale id would leave the sidebar naming a program that is no
// longer running.
func TestCreatePaneRecordsAndClearsThePluginIdentity(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	pd := newPipeDaemon(t, o)
	pid := layout.PaneID(o.session.AllPaneIDs()[0])
	rt := o.panes[uint32(pid)]

	// A plugin launch: tab.create stages the manifest's env, and createPane
	// carries it into the spawn.
	rt.created = false
	o.StageSpawn(uint32(pid), app.SpawnOverride{
		Command: []string{"cats-todo"},
		Env:     map[string]string{plugin.IDEnvVar: "rohanthewiz.cats-todo", plugin.TypeEnvVar: "todos_mgr"},
	})
	synced := make(chan struct{})
	go func() { o.createPane(rt); close(synced) }() // pipe writes block until the pump reads
	var cp orchestration.CreatePane
	if err := json.Unmarshal(pd.expect(t, orchestration.MsgCreatePane), &cp); err != nil {
		t.Fatalf("unmarshal create_pane: %v", err)
	}
	<-synced
	if cp.Env[plugin.IDEnvVar] != "rohanthewiz.cats-todo" {
		t.Fatalf("spawn env: %+v", cp.Env)
	}
	if id, typ := o.session.PanePlugin(pid); id != "rohanthewiz.cats-todo" || typ != "todos_mgr" {
		t.Fatalf("pane plugin after launch: %q %q", id, typ)
	}

	// The same pane respawned with no plan at all — what a cathost restart does
	// to a pane nothing resumes.
	rt.created = false
	synced = make(chan struct{})
	go func() { o.createPane(rt); close(synced) }()
	pd.expect(t, orchestration.MsgCreatePane)
	<-synced
	if id, typ := o.session.PanePlugin(pid); id != "" || typ != "" {
		t.Fatalf("a pane respawned as a shell still claims plugin %q (%q)", id, typ)
	}
}
