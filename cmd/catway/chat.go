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
		o.chat = acpchat.New(acpchat.Backends()[0], o.post,
			func(msg any) { o.broadcast(msg) })
	}
	return o.chat
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
