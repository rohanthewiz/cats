//go:build ghostty

package main

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/rohanthewiz/rweb"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/promptopts"
	"github.com/rohanthewiz/cats/internal/push"
)

// Answering a notification from the phone.
//
// Phase 1 gave a notification buttons and one way to press them: ui.action,
// over the control socket, from something already inside the session. This file
// is the other way — a tap on a lock screen, which is the case the push bridge
// exists for in the first place. An "attention" push that says "claude needs
// attention" cannot be acted on; one that says "Yes / Yes, don't ask / No" can.
//
//	agent blocks ─► notify ─► capture the pane's screen ─► promptopts.Parse
//	                                                          │
//	    ntfy Actions header ◄── mint one token per choice ◄────┘
//	            │
//	   phone tap ─► POST /api/notify-action/<token> ─► takeNotifyAction
//
// Three decisions carry the security of that inbound path.
//
// **The token is not the catway password.** The notification server relays the
// request and therefore sees whatever credential rides it. A token that answers
// exactly one prompt, once, and expires with it is the only kind worth showing
// a third party. It authorizes nothing else — not a command, not a pane, not a
// read.
//
// **The route is POST-only.** Notification clients, link previewers and
// crawlers fetch URLs they are shown. A GET that answered a prompt would be
// answered by whatever prefetched it, which is a class of bug that only ever
// shows up in production and looks like a haunted terminal.
//
// **It is off unless the operator turns it on** (push.actions), separately from
// the topic URL, because it is the only INBOUND surface in the push config —
// everything else there is catway posting outward.

const (
	// notifyActionPath is the route prefix. It is a path segment rather than a
	// query parameter so the token never lands in a Referer header or a proxy's
	// query log — the same reason pairing tokens are path-shaped.
	notifyActionPath = "/api/notify-action/"

	// captureLinesForPrompt bounds the screen read the options are parsed out
	// of, and captureScopeRecent (CaptureParams.Scope 1) asks for the tail of
	// the buffer rather than the viewport. The prompt is at the bottom by
	// definition (see internal/promptopts), so this is a ceiling on cost rather
	// than a reach — and the tail is the right end even for a pane whose
	// viewport is scrolled up, where the visible rows are not what the agent is
	// waiting on.
	captureLinesForPrompt = 40
	captureScopeRecent    = 1
)

// actionToken is one minted button: which notification and which of its actions
// it answers. Single use is not enforced here — the notification registry drops
// itself on the first take, so a second tap finds nothing to answer regardless
// of which route it arrived by.
type actionToken struct {
	notifyID string
	actionID string
	at       time.Time
}

// pushActionsOn reports whether notifications may carry tappable buttons.
// Requires both halves of the config: the switch, and an address the phone can
// actually come back to.
func (o *orch) pushActionsOn() bool {
	return o.pushActions && o.pushActionBase != ""
}

// mintPushActions turns a notification's declared actions into the absolute
// URLs a phone posts to. Returns nil when actions are disabled — the caller
// then sends an ordinary push, which is exactly what it used to send.
func (o *orch) mintPushActions(notifyID string, actions []app.NotifyAction) []push.Action {
	if !o.pushActionsOn() || notifyID == "" || len(actions) == 0 {
		return nil
	}
	if o.actionTokens == nil {
		o.actionTokens = map[string]actionToken{}
	}
	o.pruneActionTokens()
	out := make([]push.Action, 0, len(actions))
	for _, a := range actions {
		tok := randomNotifyID() + randomNotifyID() // ~24 URL-safe chars
		o.actionTokens[tok] = actionToken{notifyID: notifyID, actionID: a.ID, at: time.Now()}
		out = append(out, push.Action{Label: a.Label, URL: o.pushActionBase + notifyActionPath + tok})
	}
	return out
}

// pruneActionTokens drops tokens whose notification can no longer be answered.
// It walks the whole map rather than keeping an ordered index: the map is
// bounded by the notification registry it mirrors (64 entries × 3 buttons), so
// the walk is cheap and one less structure can fall out of step with another.
func (o *orch) pruneActionTokens() {
	cutoff := time.Now().Add(-notifyActionTTL)
	for tok, at := range o.actionTokens {
		if at.at.Before(cutoff) || o.notifs[at.notifyID] == nil {
			delete(o.actionTokens, tok)
		}
	}
}

// handleNotifyAction serves POST /api/notify-action/<token>. It runs on an HTTP
// goroutine, so the actual work is posted onto the orchestrator loop — every
// pane, every registry entry and every daemon write is loop-owned — and this
// goroutine waits for the answer only to choose a status code.
func (o *orch) handleNotifyAction(ctx rweb.Context) error {
	tok := strings.TrimPrefix(ctx.Request().Path(), notifyActionPath)
	if tok == "" || strings.Contains(tok, "/") {
		return ctx.Status(http.StatusNotFound).WriteText("no such action")
	}
	done := make(chan string, 1)
	o.post(func() { done <- o.takeTokenAction(tok) })
	select {
	case errMsg := <-done:
		if errMsg != "" {
			logRefusedAction(errMsg)
			// 409 rather than 404 or 403: the token was shaped like one, and
			// what went wrong is that the thing it answers is gone. A phone
			// showing a stale notification is the normal way to get here, not
			// an attack, and the body is what its owner will read.
			return ctx.Status(http.StatusConflict).WriteText(errMsg)
		}
		return ctx.WriteText("ok")
	case <-time.After(reqTimeout):
		return ctx.Status(http.StatusGatewayTimeout).WriteText("timed out")
	}
}

// takeTokenAction resolves a token and takes its action. Loop goroutine.
//
// The token is deleted before the action is attempted, for the reason the
// registry entry is: a retry over a flaky mobile link must not be able to land
// a second answer because the first attempt reported an error.
func (o *orch) takeTokenAction(tok string) string {
	at, ok := o.actionTokens[tok]
	if !ok {
		return "this notification is no longer answerable"
	}
	delete(o.actionTokens, tok)
	if err := o.takeNotifyAction(at.notifyID, at.actionID, "push"); err != nil {
		return err.Error()
	}
	return ""
}

// --- deriving buttons from the agent's own screen -----------------------------

// sendPush is notifyAll's outbound half: the one place a notification becomes a
// phone push. It is a separate step from the broadcast because it is the only
// one that can WAIT — deriving an agent's menu means reading its screen, which
// is a round trip to that pane's cathost.
//
// Ordering is the same rule notifyAll already states: the browser broadcast has
// already happened, unconditionally, before anything here runs. A wedged
// capture can therefore delay the phone and nothing else.
func (o *orch) sendPush(n browserproto.Notify, agent string) {
	ev := o.pushEvent(n, agent)
	// A notification that declared its own buttons is answered as declared —
	// the caller knows what it is asking better than the screen does.
	if len(n.Actions) > 0 {
		ev.Actions = o.mintPushActions(n.ID, n.Actions)
		o.push.Send(ev)
		return
	}
	if !o.pushActionsOn() || n.Kind != push.KindAttention || n.Pane == 0 || !o.PaneHostConnected(n.Pane) {
		o.push.Send(ev)
		return
	}
	o.deriveThenPush(n, ev)
}

// deriveThenPush reads the blocked pane's visible screen, parses the agent's
// menu out of it, and pushes with buttons — or without, when nothing parsed.
//
// Buttons derived this way are deliberately phone-only: the browser already got
// its toast, and a browser is one click from the pane itself, so a second
// delayed toast carrying the same choices would be noise where the phone case
// is the one that cannot be answered any other way.
func (o *orch) deriveThenPush(n browserproto.Notify, ev push.Event) {
	pane := n.Pane
	o.StartCapture(funcResponder{fn: func(data any, errMsg string) {
		if errMsg != "" {
			// A pane that cannot be read still deserves its notification; it
			// just cannot carry choices.
			o.push.Send(ev)
			return
		}
		res, _ := data.(app.CaptureResult)
		opts := promptopts.Parse(res.Text)
		if len(opts) == 0 {
			o.push.Send(ev)
			return
		}
		// The pane may have gone (or been answered at the desk) while the
		// capture was in flight; a button that types into a dead pane is worse
		// than none.
		if !o.PaneExists(pane) {
			o.push.Send(ev)
			return
		}
		actions := make([]app.NotifyAction, 0, len(opts))
		for _, opt := range opts {
			actions = append(actions, app.NotifyAction{
				ID: opt.Key, Label: opt.Label, Send: opt.Key, Submit: true,
			})
		}
		id := o.registerNotify(pane, actions)
		ev.Actions = o.mintPushActions(id, actions)
		o.push.Send(ev)
	}}, app.CaptureParams{Pane: pane, Scope: captureScopeRecent, Lines: captureLinesForPrompt})
}

// funcResponder adapts a closure to app.Responder, for the orchestrator's own
// internal round trips. It exists so an internally-issued capture goes through
// exactly the pending/timeout/host-scoped-flush machinery a client's capture
// does, rather than a private path that would need its own timeout and its own
// disconnect handling.
//
// fn runs on the loop goroutine: pending requests are resolved there.
type funcResponder struct{ fn func(data any, errMsg string) }

func (f funcResponder) WantsReply() bool { return true }
func (f funcResponder) OK(data any)      { f.fn(data, "") }
func (f funcResponder) Fail(msg string)  { f.fn(nil, msg) }

// logRefusedAction records an inbound action that could not be honoured. Kept
// to one line without the token: a stale phone notification is the ordinary way
// to produce one, and logging the credential would put it in a file that
// outlives it.
func logRefusedAction(reason string) { log.Printf("catway: notification action refused (%s)", reason) }
