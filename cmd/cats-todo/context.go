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
	"strconv"
	"strings"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/integration"
)

// RunContext describes where cats-todo is running: the pane's working directory
// (which scopes project todos and roots any new session's tab), the pane's own
// handle (so the drop-target picker can exclude the manager's pane), and the
// workspace it lives in (so the picker can mark same-project sessions).
type RunContext struct {
	WorkDir        string
	OwnPane        string // CATS_PANE_ID handle: "w1:p3", or the "p_<id>" fallback
	WorkspaceID    string // public workspace id ("w1")
	WorkspaceLabel string
}

// gatherRunContext builds the launch context. The working directory always
// resolves (it is this process's cwd); the pane handle comes from the
// environment; the workspace resolves via the client — from the pane handle's
// "w1" prefix when present, else the active workspace — and is left empty when
// the control socket is unavailable (client nil). A partial context still runs:
// project todos scope to the cwd regardless.
func gatherRunContext(client *catsClient) RunContext {
	ctx := RunContext{OwnPane: os.Getenv(integration.CatsPaneIDEnvVar)}
	if wd, err := os.Getwd(); err == nil {
		ctx.WorkDir = wd
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
