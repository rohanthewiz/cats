//go:build ghostty

package main

import (
	"path/filepath"
	"testing"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/config"
)

// The request reaches the editor as a pane-addressed event, so an editor
// subscribed to its own pane sees its own requests and nothing else in the
// session has to filter.
func TestOpenFileInEmitsAPaneScopedEvent(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	pane := uint32(o.session.AllPaneIDs()[0])

	mine, others := &recSub{}, &recSub{}
	o.subs[&ctlSubscriber{sub: mine, filter: app.EventsSubscribeParams{Pane: &pane}}] = struct{}{}
	elsewhere := pane + 1000
	o.subs[&ctlSubscriber{sub: others, filter: app.EventsSubscribeParams{Pane: &elsewhere}}] = struct{}{}

	o.OpenFileIn(pane, app.OpenFileParams{Path: "~/projs/go/cats/main.go", Line: 42, Column: 7})

	ev, ok := findEvent(mine, app.EventPaneOpenFile).(app.PaneOpenFileEvent)
	if !ok {
		t.Fatal("the editor's own subscription saw no pane_open_file")
	}
	if ev.Pane != pane || ev.Path != "~/projs/go/cats/main.go" || ev.Line != 42 || ev.Column != 7 {
		t.Errorf("event = %+v", ev)
	}
	if findEvent(others, app.EventPaneOpenFile) != nil {
		t.Error("a subscription on another pane saw this pane's open request")
	}
}

// The policy comes from the live config, so a reload changes it — and the
// default is the editor cats ships alongside, so the feature works with no
// config at all.
func TestEditorConfigTracksTheLiveConfig(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	o.cfg = config.Default()
	if got := o.EditorConfig(); !got.IsEditorAgent("ced") || !got.Spawn {
		t.Fatalf("default editor policy = %+v", got)
	}
	// Case-insensitive: the label is a name a human typed into a hook asset.
	if !o.EditorConfig().IsEditorAgent("CEd") {
		t.Error("agent matching is case-sensitive")
	}

	o.cfg.Editor = config.Editor{Agents: []string{"vim"}, Command: []string{"vim"}}
	got := o.EditorConfig()
	if got.IsEditorAgent("ced") || !got.IsEditorAgent("vim") || got.Spawn {
		t.Errorf("edited policy = %+v", got)
	}
}
