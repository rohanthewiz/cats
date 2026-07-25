package main

import (
	"os"
	"path/filepath"
	"strings"
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
			t.Errorf("ctx = %+v, want only the directory fields set outside cats", ctx)
		}
	})

	t.Run("resolves the project root above the cwd", func(t *testing.T) {
		// This test file lives in the cats repo, so the walk from the test's cwd
		// (cmd/cats-todo) must land on an ancestor holding the repo's markers —
		// never the cwd itself. That is precisely the subdirectory case that used
		// to scope the manager to the wrong backlog.
		t.Setenv(integration.CatsPaneIDEnvVar, "")
		ctx := gatherRunContext(nil)
		if ctx.ProjectRoot == "" {
			t.Fatal("ProjectRoot should resolve whenever WorkDir does")
		}
		if ctx.ProjectRoot == ctx.WorkDir {
			t.Errorf("ProjectRoot = WorkDir = %q, want an ancestor (the repo root)", ctx.WorkDir)
		}
		if !strings.HasPrefix(ctx.WorkDir, ctx.ProjectRoot) {
			t.Errorf("ProjectRoot %q is not an ancestor of WorkDir %q", ctx.ProjectRoot, ctx.WorkDir)
		}
	})
}

// TestFindProjectRoot covers the walk both entry points share: an existing
// backlog wins over an enclosing repo root, a repo root is the fallback, and a
// directory with neither marker roots itself.
func TestFindProjectRoot(t *testing.T) {
	t.Run("an existing backlog wins", func(t *testing.T) {
		root := t.TempDir()
		// A repo root above, a backlog below it: the backlog is the answer even
		// though the .git marker is closer to the top of the walk.
		mkdir(t, filepath.Join(root, ".git"))
		mkdir(t, filepath.Join(root, "sub", projectConfigDirName))
		deep := filepath.Join(root, "sub", "a", "b")
		mkdir(t, deep)

		if got := findProjectRoot(deep); got != filepath.Join(root, "sub") {
			t.Errorf("findProjectRoot = %q, want the nearest existing backlog %q", got, filepath.Join(root, "sub"))
		}
	})

	t.Run("falls back to the repo root", func(t *testing.T) {
		root := t.TempDir()
		mkdir(t, filepath.Join(root, ".git"))
		deep := filepath.Join(root, "cmd", "tool")
		mkdir(t, deep)

		if got := findProjectRoot(deep); got != root {
			t.Errorf("findProjectRoot = %q, want the repo root %q", got, root)
		}
	})

	t.Run("no markers roots the directory itself", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "loose")
		mkdir(t, dir)

		if got := findProjectRoot(dir); got != dir {
			t.Errorf("findProjectRoot = %q, want the directory itself %q", got, dir)
		}
	})
}

// TestRunContextProjectDir pins the fallback order for the directory that scopes
// project todos: the resolved root, else the raw working directory.
func TestRunContextProjectDir(t *testing.T) {
	tests := []struct {
		name string
		ctx  RunContext
		want string
	}{
		{"root wins when resolved", RunContext{WorkDir: "/repo/sub", ProjectRoot: "/repo"}, "/repo"},
		{"falls back to the workdir", RunContext{WorkDir: "/repo/sub"}, "/repo/sub"},
		{"empty when neither is known", RunContext{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ctx.projectDir(); got != tt.want {
				t.Errorf("projectDir() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRunContextPaneTitle pins the terminal title the manager sets: the project
// basename, tracking projectDir's own fallback order, and the bare app name when
// no directory resolved.
func TestRunContextPaneTitle(t *testing.T) {
	tests := []struct {
		name string
		ctx  RunContext
		want string
	}{
		{"names the project root", RunContext{WorkDir: "/repo/sub", ProjectRoot: "/repo"}, "todo: repo"},
		{"falls back to the workdir", RunContext{WorkDir: "/repo/sub"}, "todo: sub"},
		{"bare name with no directory", RunContext{}, "todo"},
		{"bare name at the filesystem root", RunContext{WorkDir: "/"}, "todo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ctx.paneTitle(); got != tt.want {
				t.Errorf("paneTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

// mkdir creates dir and its parents, failing the test if it cannot.
func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
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
