// context.go — where cats-todo is running from. Unlike the herdr original —
// which relayed a plugin-action context from a server-side process into a
// plugin pane via env blobs — cats-todo runs directly in a shell pane, so the
// context is gathered in-process at startup: the working directory from the
// pane itself, the pane identity from the CATS_PANE_ID env cats injects into
// every pane, and the workspace from a control-socket query.
//
// Adapted from herdr-plus (https://github.com/cloudmanic/herdr-plus),
// Copyright (c) 2026 Cloudmanic Labs, LLC, MIT License. See NOTICE.

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/integration"
)

// RunContext describes where cats-todo is running: the pane's working directory
// and the project root above it (which scopes project todos and roots any new
// session's tab), the pane's own handle (so the drop-target picker can exclude
// the manager's pane), and the workspace it lives in (so the picker can mark
// same-project sessions).
type RunContext struct {
	WorkDir string
	// ProjectRoot is the directory that owns the project backlog: WorkDir or an
	// ancestor of it (see findProjectRoot). Kept separate from WorkDir so the
	// pane's actual location stays available, and so a context built without a
	// root — the tests, and any caller that only knows a directory — still
	// scopes sensibly via projectDir.
	ProjectRoot    string
	OwnPane        string // CATS_PANE_ID handle: "w1:p3", or the "p_<id>" fallback
	WorkspaceID    string // public workspace id ("w1")
	WorkspaceLabel string
}

// projectDir is the directory that scopes project todos and roots a new
// session's tab: the resolved project root, falling back to the raw working
// directory when no root was resolved. Empty when neither is known — launched
// somewhere we could not even name, where only the global backlog applies.
func (c RunContext) projectDir() string {
	return firstNonEmpty(c.ProjectRoot, c.WorkDir)
}

// paneTitle is the terminal title the manager advertises while it runs, named
// after the project whose backlog it is showing. Cats scans OSC 0/2 off the PTY
// into its pane chrome, so this is what labels the pane in the browser — a
// shell pane otherwise keeps whatever title it last had, which says nothing
// about the backlog now on screen.
//
// The project basename, not the full path: pane chrome already prints the cwd,
// and a title has to stay short to survive tab-bar truncation. Degrades to the
// bare app name when no directory resolved (global-only launch).
func (c RunContext) paneTitle() string {
	if name := baseName(c.projectDir()); name != "" {
		return "todo: " + name
	}
	return "todo"
}

// findProjectRoot walks up from dir to the directory that owns the project
// backlog: the nearest ancestor with an existing .cats-todo backlog wins, then
// the nearest with a .git directory (the repo root). With neither, dir itself is
// the root — a directory that is not in a repo and has no backlog yet gets its
// own on first save.
//
// The existing-backlog pass runs to completion before the .git pass so an
// established backlog always beats a repo root that merely encloses it — a repo
// with a backlog in a subdirectory keeps using that subdirectory rather than
// silently starting a second one at the root.
func findProjectRoot(dir string) string {
	for d := dir; ; {
		if fi, err := os.Stat(filepath.Join(d, projectConfigDirName)); err == nil && fi.IsDir() {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return dir
}

// gatherRunContext builds the launch context. The working directory always
// resolves (it is this process's cwd) and the project root is walked up from it,
// so opening the manager from a subdirectory reaches the same backlog `cats-todo
// add` writes to from that subdirectory; the pane handle comes from the
// environment; the workspace resolves via the client — from the pane handle's
// "w1" prefix when present, else the active workspace — and is left empty when
// the control socket is unavailable (client nil). A partial context still runs:
// project todos scope to the resolved directory regardless.
func gatherRunContext(client *catsClient) RunContext {
	ctx := RunContext{OwnPane: os.Getenv(integration.CatsPaneIDEnvVar)}
	if wd, err := os.Getwd(); err == nil {
		ctx.WorkDir = wd
		ctx.ProjectRoot = findProjectRoot(wd)
	}

	if ws, _, ok := strings.Cut(ctx.OwnPane, ":"); ok {
		ctx.WorkspaceID = ws
	}
	if client == nil {
		return ctx
	}
	// No handle-derived workspace (launched outside a cats pane, or the p_<id>
	// fallback form) — fall back to the active workspace: the user is driving
	// this TUI, so their workspace is almost certainly the active one.
	if ctx.WorkspaceID == "" {
		if info, err := client.sessionInfo(); err == nil {
			ctx.WorkspaceID = info.ActiveWorkspace
		}
	}
	if labels, err := client.workspaceLabels(); err == nil {
		ctx.WorkspaceLabel = labels[ctx.WorkspaceID]
	}
	return ctx
}

// isOwnPane reports whether p is the pane this manager runs in, matching both
// CATS_PANE_ID forms: the public "w1:p3" handle, and the "p_<raw>" fallback
// that embeds the internal pane id directly.
func isOwnPane(ctx RunContext, p app.PaneInfo) bool {
	if ctx.OwnPane == "" {
		return false
	}
	if p.Handle != "" && p.Handle == ctx.OwnPane {
		return true
	}
	if raw, ok := strings.CutPrefix(ctx.OwnPane, "p_"); ok {
		if n, err := strconv.ParseUint(raw, 10, 32); err == nil {
			return uint32(n) == p.Pane
		}
	}
	return false
}

// paneWorkspaceID extracts the public workspace id from a pane's "w1:p3"
// handle, or "" when the handle is absent/unparseable.
func paneWorkspaceID(p app.PaneInfo) string {
	if ws, _, ok := strings.Cut(p.Handle, ":"); ok {
		return ws
	}
	return ""
}
