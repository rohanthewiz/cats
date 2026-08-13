//go:build ghostty

package main

import (
	"errors"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/clipboard"
	"github.com/rohanthewiz/cats/internal/ctlproto"
)

// Host clipboard reads (`catctl clipboard`, and the editor integration behind
// it). The method is answered here rather than through app.Dispatcher, and that
// placement is the security boundary — the same one pair.go draws, for the same
// reason:
//
//	browser  ──ws cmd──▶ app.Dispatcher ──▶ §7 command table   ← no clipboard here
//	catctl   ──unix───▶ controlDispatch ──┬─▶ app.Dispatcher
//	                                      └─▶ handleClipboardRead  ← owner-only socket
//
// A §7 command is reachable from both front ends by construction, and the browser
// front end is the network-facing one. The clipboard is the user's rather than
// the session's — it holds whatever they last copied in ANY application, which on
// a work machine is as likely to be a password as a paragraph — so it must not be
// answerable to a client on the other end of a token. See
// ctlproto.MethodClipboardRead for why there is no config flag on top of that.

// handleClipboardRead reads the host clipboard and answers the control request.
//
// Like handlePair it runs on the ctlproto connection goroutine and deliberately
// does NOT post onto the orchestrator loop: the read touches no session state,
// and it shells out to pbpaste/wl-paste — putting a subprocess on the loop would
// stall every pane's frame delivery behind it, for a call that has nothing to do
// with the session.
func (o *orch) handleClipboardRead(r app.Responder) {
	text, truncated, err := clipboard.Read()
	if err != nil {
		if errors.Is(err, clipboard.ErrUnsupported) {
			// Named separately because the caller can act on it: this host will
			// never answer, so an editor should retire the feature for the session
			// rather than retry and re-report a transient-looking failure.
			r.Fail("clipboard unavailable: no reader on this host (install wl-paste, xclip or xsel)")
			return
		}
		r.Fail(err.Error())
		return
	}
	r.OK(ctlproto.ClipboardData{Text: text, Truncated: truncated})
}
