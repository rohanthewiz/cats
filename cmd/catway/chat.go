//go:build ghostty

package main

import (
	"github.com/rohanthewiz/cats/internal/acpchat"
	"github.com/rohanthewiz/cats/internal/app"
)

// chat.go is the thin seam between the §7 chat.* commands and the ACP chat
// engine (internal/acpchat). The manager is loop-owned state like the session
// itself: every method here already runs on the loop goroutine (Backend
// contract), so the calls go straight through — the slow subprocess work
// lives on the manager's own goroutines and comes back via o.post.

// ensureChat builds the manager on first use. Lazy on purpose: the agent
// subprocess should not exist before anyone has opened a conversation, and
// "which backend" stays a construction-time choice (the registry's first
// entry) until a picker exists.
func (o *orch) ensureChat() *acpchat.Manager {
	if o.chat == nil {
		def := acpchat.Backends()[0]
		o.chat = acpchat.New(def, o.post, func(msg any) { o.broadcast(msg) })
		o.chat.SetModelResolver(o.chatModelResolver(def))
	}
	return o.chat
}

// chatModelResolver names the model behind the chat's own answers, or nil when
// this backend's history cannot be read here.
//
// The panel would otherwise never show one: ACP's model roster is optional and
// copilot's session/new omits it (verified live against 1.0.77). But the chat's
// session is an ordinary session of that agent on disk — copilot's ACP session
// id *is* its ~/.copilot/session-state directory name, created at session/new —
// so the reader the sidebar's hover card already uses (agentmodel.go) answers
// for chat too, effort suffix and all.
//
// The cwd argument is deliberately empty. Those readers fall back to "newest
// session started in this directory" when the id does not resolve, which for
// chat would name some *pane's* agent running in the same project. An exact id
// or no answer.
func (o *orch) chatModelResolver(def acpchat.BackendDef) func(string) string {
	resolver, known := modelResolvers[def.ModelAgent]
	root := o.modelRoots[def.ModelAgent]
	if !known || root == "" {
		return nil
	}
	return func(sessionID string) string { return resolver.read(root, "", sessionID) }
}

// ChatSend implements chat.send. The cwd is resolved here, at send time, not
// in the manager: only the orch knows panes and workspaces, and the focused
// pane's live cwd is what "talk about this project" means when the agent
// starts. An established session keeps the cwd it started with.
func (o *orch) ChatSend(r app.Responder, p app.ChatSendParams) {
	o.ensureChat().Send(o.anchorPaneCwd(nil), p.Text)
	r.OK(nil)
}

// ChatCancel implements chat.cancel. No ensureChat: cancelling a chat that
// never existed should not summon one.
func (o *orch) ChatCancel(r app.Responder) {
	if o.chat != nil {
		o.chat.Cancel()
	}
	r.OK(nil)
}

// ChatPermission implements chat.permission. The one chat command that can
// fail meaningfully: a prompt another client already answered.
func (o *orch) ChatPermission(r app.Responder, p app.ChatPermissionParams) {
	if o.chat == nil {
		r.Fail("no chat session")
		return
	}
	if err := o.chat.Permission(p.ReqID, p.OptionID, p.Cancel); err != nil {
		r.Fail(err.Error())
		return
	}
	r.OK(nil)
}

// ChatClear implements chat.clear.
func (o *orch) ChatClear(r app.Responder) {
	if o.chat != nil {
		o.chat.Clear()
	}
	r.OK(nil)
}
