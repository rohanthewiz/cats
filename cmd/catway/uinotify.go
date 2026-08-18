//go:build ghostty

package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/layout"
)

// ui.notify / ui.action — notifications anything holding the control socket can
// raise, and the buttons that answer them.
//
// Until now catway had exactly one notification source: an agent state
// transition (notify.go's publishAgent). That is the only moment catway itself
// can recognise, which left everything else — a build that finished in a pane,
// a plugin that wants the user back, an editor that hit a merge conflict —
// with nothing better than printing a line into a pane nobody is watching.
// ui.notify hands that moment to the caller and routes it through notifyAll,
// so a new source reaches the browser toast, the pane_notify event stream and
// the phone by construction rather than by remembering to.
//
// The registry below is the only state this adds, and its whole job is that an
// action is taken ONCE:
//
//	ui.notify ──► registry (id, actions, TTL) ──► toast / event / push
//	                   │
//	  ui.action ───────┤ take: perform Send, announce, DROP the entry
//	  (phase 2: POST) ─┘
//
// Two clients can be showing the same buttons — a browser toast and a lock
// screen — and only one of them can answer. Dropping the entry on the first
// successful take is what makes the second tap a refusal by name instead of a
// second "yes" landing in a shell that has already moved on.

const (
	// notifyActionTTL bounds how long a notification's buttons stay answerable.
	// It is long because the case this exists for is a phone in a pocket, and
	// short because the pane's prompt is a live thing: half an hour later the
	// agent has usually timed out, the shell has scrolled, and an answer would
	// land somewhere it was never meant for.
	notifyActionTTL = 30 * time.Minute

	// maxLiveNotifies bounds the registry. A caller in a loop must not be able
	// to grow catway's memory by notifying; the oldest entry is evicted, which
	// is also the one least likely to still be answerable.
	maxLiveNotifies = 64
)

// liveNotify is one answerable notification. Pane is the notification's own
// pane (0 for a session-level one) and is the default target of an action's
// Send — an action may name another, which is how one notification offers
// "answer it" alongside "look at the log over there".
type liveNotify struct {
	id      string
	pane    uint32
	actions []app.NotifyAction
	at      time.Time
}

// UINotify implements app.Backend: raise a notification (ui.notify).
//
// The dispatcher has already checked the shape (title present, kind known,
// labels present, ids unique and filled in). What is left is what only the
// backend can answer: does the pane exist, and does anything need a registry
// entry.
func (o *orch) UINotify(r app.Responder, p app.UINotifyParams) {
	var pane uint32
	if p.Pane != nil {
		pane = *p.Pane
		if !o.PaneExists(pane) {
			r.Fail(fmt.Sprintf("pane %d not found", pane))
			return
		}
	}
	// An action with no pane of its own answers the notification's pane, so a
	// notification with neither has a button that cannot land anywhere. That is
	// a caller bug worth naming rather than a button that silently does half of
	// what it says.
	for _, a := range p.Actions {
		target := pane
		if a.Pane != nil {
			target = *a.Pane
		}
		if a.Send == "" {
			continue // announcement-only: no pane needed
		}
		if target == 0 {
			r.Fail(fmt.Sprintf("actions[%s]: send needs a pane — set one on the action or on the notification", a.ID))
			return
		}
		if !o.PaneExists(target) {
			r.Fail(fmt.Sprintf("actions[%s]: pane %d not found", a.ID, target))
			return
		}
	}

	n := browserproto.NewNotify(p.Kind, p.Title, p.Body)
	n.Pane = pane
	if pane != 0 {
		n.Pub, _ = o.session.PublicPaneID(layout.PaneID(pane))
	}
	if len(p.Actions) > 0 {
		n.ID = o.registerNotify(pane, p.Actions)
		n.Actions = p.Actions
	}
	// Agent is "" — this notification is the caller's, not an agent's. The push
	// bridge uses it only as a filter tag, and inventing one here would put a
	// plugin's name where a phone's mute-this-agent rule expects an agent.
	o.notifyAll(n, "", p.Title)
	r.OK(app.UINotifyResult{ID: n.ID})
}

// registerNotify stores an answerable notification and returns its id. Ids are
// random rather than sequential: they travel to clients, and a guessable id
// would let one browser answer a notification meant for a pane it cannot see
// simply by counting. Cheap insurance — 12 bytes and no bookkeeping.
func (o *orch) registerNotify(pane uint32, actions []app.NotifyAction) string {
	if o.notifs == nil {
		o.notifs = map[string]*liveNotify{}
	}
	o.pruneNotifies()
	id := randomNotifyID()
	o.notifs[id] = &liveNotify{id: id, pane: pane, actions: actions, at: time.Now()}
	o.notifOrder = append(o.notifOrder, id)
	return id
}

// pruneNotifies drops expired entries, then the oldest survivors until the
// registry fits. Both passes walk notifOrder, which is insertion-ordered, so
// "oldest" is a slice index rather than a scan.
func (o *orch) pruneNotifies() {
	cutoff := time.Now().Add(-notifyActionTTL)
	kept := o.notifOrder[:0]
	for _, id := range o.notifOrder {
		ln := o.notifs[id]
		if ln == nil {
			continue // already taken
		}
		if ln.at.Before(cutoff) {
			delete(o.notifs, id)
			continue
		}
		kept = append(kept, id)
	}
	o.notifOrder = kept
	for len(o.notifOrder) >= maxLiveNotifies {
		delete(o.notifs, o.notifOrder[0])
		o.notifOrder = o.notifOrder[1:]
	}
}

// UIAction implements app.Backend: take one of a notification's buttons
// (ui.action), from a caller holding the control socket or a browser toast.
func (o *orch) UIAction(r app.Responder, p app.UIActionParams) {
	if err := o.takeNotifyAction(p.ID, p.Action, "control"); err != nil {
		r.Fail(err.Error())
		return
	}
	r.OK(nil)
}

// takeNotifyAction is the one implementation both entry points reach — the
// ui.action command here, and (phase 2) the HTTP endpoint a phone's action
// button posts to. Having one is the point: "answered once" is a property of
// the registry, and a second implementation would be a second registry check
// that agrees with the first until it doesn't.
//
// Order is load-bearing. The entry is removed FIRST, before the input is sent:
// SendInput can fail (the pane exited between the notification and the tap),
// and an action that failed must still be spent — otherwise a phone with a
// flaky connection retries a "yes" that may have landed the first time.
func (o *orch) takeNotifyAction(id, actionID, source string) error {
	ln := o.notifs[id]
	if ln == nil {
		// One message for expired, taken and never-existed. The distinction is
		// not one the caller can act on differently, and spelling it out would
		// tell a caller holding a stale id whether a given notification was
		// ever real.
		return fmt.Errorf("notification %q is no longer answerable", id)
	}
	var act *app.NotifyAction
	for i := range ln.actions {
		if ln.actions[i].ID == actionID {
			act = &ln.actions[i]
			break
		}
	}
	if act == nil {
		return fmt.Errorf("notification %q has no action %q", id, actionID)
	}

	delete(o.notifs, id)
	// notifOrder keeps the id until the next prune; pruneNotifies skips ids
	// whose entry is gone, so there is nothing to compact here.

	if act.Send != "" {
		target := ln.pane
		if act.Pane != nil {
			target = *act.Pane
		}
		if err := o.SendInput(target, act.Send, act.Submit); err != nil {
			return fmt.Errorf("notification %q: %w", id, err)
		}
	}
	o.emitEvent(app.EventUIAction, ln.pane, app.UIActionEvent{
		Pane: ln.pane, ID: id, Action: actionID, Source: source,
	})
	// The toast that offered these buttons is stale everywhere else now. A
	// dismissal message would be a new down-message and a new client rule for
	// something the client can infer: a failed ui.action tells it the same
	// thing, and the notification is transient chrome either way.
	return nil
}

// randomNotifyID mints an unguessable, URL-safe notification id. Short enough
// to sit in a log line, long enough that guessing one is not a strategy.
func randomNotifyID() string {
	b := make([]byte, 9)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is not a condition catway can continue past
		// meaningfully, but a notification is not worth a panic either: fall
		// back to a time-derived id, which is unique but guessable, and the
		// registry's own expiry still bounds it.
		return "t" + strings.TrimSpace(fmt.Sprintf("%d", time.Now().UnixNano()))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
