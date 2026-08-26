//go:build ghostty

package main

import (
	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/layout"
)

// handleCmd runs one §7 command from a browser. The command table itself lives in
// internal/app (app.Dispatcher) so the same vocabulary can serve a CLI/control-API
// too (see cmd/catway/control.go); here we just adapt the browser wire to the
// neutral seam: an app.JSONParamDecoder over the cmd's params and a browserResponder
// that marshals the cmd_result back on this connection. orch itself implements
// app.Backend (the runtime effects). Loop-goroutine only.
func (o *orch) handleCmd(c *client, m *browserproto.Cmd) {
	// The command is dispatched FOR the window that sent it: its workspace is
	// the default every "the active workspace" command resolves against, and
	// viewBackend answers Area()/SetViewWorkspace() for that window rather than
	// for the session. A catctl or runbook caller has no window and goes
	// through plain NewDispatcher, which resolves to the primary view.
	d := app.NewDispatcherFor(o.session, viewBackend{orch: o, c: c}, o.viewOf(c))
	d.Dispatch(m.Name, app.JSONParamDecoder{Raw: m.Params}, browserResponder{o: o, c: c, id: m.ID})
	// Whatever the command did to focus, record where this window is now — the
	// generic half of cats-level navigation (see noteNav for why post-dispatch
	// snapshot rather than instrumenting the focus commands one by one).
	o.noteNav(c, m.Name)
}

// viewBackend is orch as seen from inside one window. It is the whole of the
// per-view seam on the effects side: everything an app.Backend does is
// session-wide except the two things that are inherently "which window asked" —
// the grid a command resolves geometry against, and where a workspace switch
// lands.
//
// Embedding *orch rather than copying it keeps every other Backend method (and
// the optional Recorder seam) exactly as it was, so a command that gains a
// Backend call tomorrow does not have to be taught about views.
type viewBackend struct {
	*orch
	c *client
}

// Area is the issuing window's grid, so directional pane navigation resolves
// against what that user is actually looking at. Two windows of different sizes
// on the same workspace each navigate their own geometry.
func (b viewBackend) Area() layout.Rect { return b.orch.viewArea(b.c) }

// SetViewWorkspace moves the issuing window — not the session, and not any
// other window. This is what turns workspace.focus from "everyone switches" into
// "this window switches".
func (b viewBackend) SetViewWorkspace(wsID string) { b.orch.setViewWorkspace(b.c, wsID) }

// The worktree pair opens (or re-focuses) a workspace on a checkout, which is a
// workspace switch by another name — so they carry the issuing window too. The
// rest of the Backend is session-wide and rides the embedded *orch unchanged.
func (b viewBackend) StartWorktreeCreate(r app.Responder, p app.WorktreeCreateParams) {
	b.orch.startWorktreeCreate(b.c, r, p)
}

func (b viewBackend) StartWorktreeOpen(r app.Responder, p app.WorktreeOpenParams) {
	b.orch.startWorktreeOpen(b.c, r, p)
}

// browserResponder delivers a command's cmd_result to the browser that issued it.
// A command with no id yields no result (WantsReply is false), so async commands
// skip the round-trip and any reply is a no-op.
type browserResponder struct {
	o  *orch
	c  *client
	id string
}

func (r browserResponder) WantsReply() bool   { return r.id != "" }
func (r browserResponder) OK(data any)        { r.reply(true, "", data) }
func (r browserResponder) Fail(errMsg string) { r.reply(false, errMsg, nil) }

func (r browserResponder) reply(ok bool, errMsg string, data any) {
	if r.id == "" {
		return
	}
	if res, err := browserproto.NewCmdResult(r.id, ok, errMsg, data); err == nil {
		r.o.send(r.c, res)
	}
}
