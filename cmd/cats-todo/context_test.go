package main

import (
	"testing"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/integration"
)

// TestGatherRunContextWithoutClient pins the degraded launch: with no control
// socket, the context still resolves the working directory (this process's cwd)
// and the workspace id from the pane handle's "w1" prefix — enough to scope
// project todos and mark same-workspace targets.
func TestGatherRunContextWithoutClient(t *testing.T) {
	t.Run("handle-form pane id yields the workspace", func(t *testing.T) {
		t.Setenv(integration.CatsPaneIDEnvVar, "w2:p5")
		ctx := gatherRunContext(nil)
		if ctx.OwnPane != "w2:p5" {
			t.Errorf("OwnPane = %q, want w2:p5", ctx.OwnPane)
		}
		if ctx.WorkspaceID != "w2" {
			t.Errorf("WorkspaceID = %q, want w2 (handle prefix)", ctx.WorkspaceID)
		}
		if ctx.WorkDir == "" {
			t.Error("WorkDir should resolve to the process cwd")
		}
	})

	t.Run("fallback-form pane id yields no workspace", func(t *testing.T) {
		t.Setenv(integration.CatsPaneIDEnvVar, "p_7")
		ctx := gatherRunContext(nil)
		if ctx.OwnPane != "p_7" || ctx.WorkspaceID != "" {
			t.Errorf("ctx = %+v, want OwnPane p_7 with no workspace (needs the client)", ctx)
		}
	})

	t.Run("outside cats yields a bare cwd context", func(t *testing.T) {
		t.Setenv(integration.CatsPaneIDEnvVar, "")
		ctx := gatherRunContext(nil)
		if ctx.OwnPane != "" || ctx.WorkspaceID != "" || ctx.WorkspaceLabel != "" {
			t.Errorf("ctx = %+v, want only WorkDir set outside cats", ctx)
		}
	})
}

// TestIsOwnPane covers both CATS_PANE_ID forms the manager must recognize to
// keep its own pane out of the drop-target picker.
func TestIsOwnPane(t *testing.T) {
	pane := app.PaneInfo{Pane: 7, Handle: "w1:p3"}

	tests := []struct {
		name    string
		ownPane string
		want    bool
	}{
		{"matches the public handle", "w1:p3", true},
		{"matches the p_<id> fallback", "p_7", true},
		{"different handle", "w1:p4", false},
		{"different fallback id", "p_8", false},
		{"empty own pane never matches", "", false},
		{"malformed fallback never matches", "p_x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isOwnPane(RunContext{OwnPane: tt.ownPane}, pane); got != tt.want {
				t.Errorf("isOwnPane(%q, %+v) = %v, want %v", tt.ownPane, pane, got, tt.want)
			}
		})
	}
}

// TestPaneWorkspaceID pins the handle-prefix extraction the picker uses to
// group panes by workspace.
func TestPaneWorkspaceID(t *testing.T) {
	if got := paneWorkspaceID(app.PaneInfo{Handle: "w3:p9"}); got != "w3" {
		t.Errorf("paneWorkspaceID(w3:p9) = %q, want w3", got)
	}
	if got := paneWorkspaceID(app.PaneInfo{Handle: ""}); got != "" {
		t.Errorf("paneWorkspaceID(no handle) = %q, want empty", got)
	}
}
