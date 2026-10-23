//go:build darwin

package main

import (
	"encoding/json"
	"html"
	"strings"
)

// The backend overlay: what every window shows once catway's automatic restarts
// are spent (restart.go), and the cathost card shown when cathost has been
// restarted or has given up (hostrestart.go).
//
// It is injected into the live page rather than replacing it (as the connect
// form does with showHTML), for two reasons:
//   - The window keeps its URL. The restore list reads each window's workspace
//     off its URL (catsWindowsJSON), so a page swapped for literal HTML would be
//     saved as "the primary view" the moment the window moved.
//   - The page underneath is still the app. It keeps trying its WebSocket, so once
//     catway is back the only thing left to do is remove the overlay.
//
// The markup is built in Go, HTML-escaped (the detail carries an exit status and
// a path) and handed to the page as a JSON string literal, which is also a valid
// JS one with <, > and & escaped — nothing in it can close a tag or a string.

const backendOverlayID = "cats-backend-down"

// cathostNoticeID is the cathost card's element, separate from catway's so
// neither supervisor clobbers the other. catwayBack clears backendOverlayID
// when catway comes back. If the two shared an element, that clear would also
// take down a "pane processes were lost" card the user has not read yet. A
// "cathost stopped" card arriving over an unread "lost" one replaces it in
// place, which is right: the later state is the one that holds.
const cathostNoticeID = "cats-cathost-notice"

// overlayButton is the one action a card can offer.
type overlayButton int

const (
	overlayNoButton      overlayButton = iota
	overlayRestartButton               // "Restart catway": a fresh budget from the supervisor
	overlayDismissButton               // "OK": removes the card; nothing to tell the app
)

// windowBackendUI draws the supervisor's state over every window.
type windowBackendUI struct{}

func (windowBackendUI) catwayDown(detail string, canRetry bool) {
	evalInWindows(backendOverlayJS("The catway server stopped", detail, canRetry))
}

// cathostLost is modal on purpose: the restarted layout looks exactly like the
// one before it, so a toast that faded on its own could be missed. Losing a
// running build or agent is a loss the user should acknowledge.
func (windowBackendUI) cathostLost(detail string) {
	evalInWindows(overlayJS(cathostNoticeID, "cathost exited — pane processes were lost", detail, overlayDismissButton))
}

func (windowBackendUI) cathostDown(detail string) {
	evalInWindows(overlayJS(cathostNoticeID, "The cathost server stopped", detail, overlayNoButton))
}

func (windowBackendUI) catwayRestarting() {
	evalInWindows(backendOverlayJS("Restarting catway…", "", false))
}

// catwayBack clears the overlay from every page and retries every window that
// has no page at all. The second half is not covered by the first: a window
// opened or reloaded while catway was down failed its load, so there is no
// document for the clearing script (or the page's own reconnect loop) to run
// in, and it would stay blank until the user reloaded it by hand. Both run in
// the same main-thread hop, since window state is main-thread-only.
func (windowBackendUI) catwayBack() {
	js := backendClearJS()
	onMainThread(func() {
		if windows != nil {
			windows.evalAll(js)
			windows.reloadFailed()
		}
	})
}

// evalInWindows hops to the main thread (the supervisor is on its own goroutine)
// and runs js in every window.
func evalInWindows(js string) {
	onMainThread(func() {
		if windows != nil {
			windows.evalAll(js)
		}
	})
}

// backendOverlayJS returns a script that shows (or updates in place) the overlay.
// Updating in place matters: "stopped" → "restarting…" → "stopped" again must not
// stack three overlays.
func backendOverlayJS(title, detail string, canRetry bool) string {
	btn := overlayNoButton
	if canRetry {
		btn = overlayRestartButton
	}
	return overlayJS(backendOverlayID, title, detail, btn)
}

// overlayJS is backendOverlayJS for any card: the element id it shows (or
// updates in place) and the one button it offers.
func overlayJS(id, title, detail string, btn overlayButton) string {
	var card strings.Builder
	card.WriteString(`<div style="background:#202020;border:1px solid #3a2a2a;border-radius:8px;` +
		`padding:24px 26px;width:min(480px,86vw);box-shadow:0 4px 20px rgba(0,0,0,.5);color:#d4d4d4">`)
	card.WriteString(`<h1 style="font-size:15px;margin:0 0 10px;color:#ff6b6b">` + html.EscapeString(title) + `</h1>`)
	if detail != "" {
		card.WriteString(`<pre style="font-size:12px;line-height:1.45;color:#c9c9c9;background:#141414;` +
			`border:1px solid #333;border-radius:5px;padding:12px;white-space:pre-wrap;` +
			`word-break:break-word;margin:0">` + html.EscapeString(detail) + `</pre>`)
	}
	// The click handler is chosen alongside the label so the two cannot
	// disagree. Restart is disabled on click because the request is already
	// made, and the "restarting" state replaces this card as soon as the
	// supervisor picks it up. Dismiss is purely the page's: the supervisor has
	// already moved on, so there is nothing to report back.
	var label, onclick string
	switch btn {
	case overlayRestartButton:
		label = "Restart catway"
		onclick = `b.disabled=true;b.textContent="Restarting…";` +
			`if(window.catsRestartBackend){window.catsRestartBackend();}`
	case overlayDismissButton:
		label = "OK"
		onclick = `el.remove();`
	}
	if label != "" {
		card.WriteString(`<button type="button" style="margin-top:16px;padding:7px 14px;font:inherit;` +
			`font-size:13px;color:#181818;background:#d4d4d4;border:0;border-radius:5px;cursor:pointer">` +
			label + `</button>`)
	}
	card.WriteString(`</div>`)
	markup, _ := json.Marshal(card.String()) // a string always marshals

	return `(function(){` +
		`var html=` + string(markup) + `;` +
		`var el=document.getElementById("` + id + `");` +
		`if(!el){el=document.createElement("div");el.id="` + id + `";` +
		`(document.body||document.documentElement).appendChild(el);}` +
		`el.setAttribute("style",'position:fixed;inset:0;z-index:2147483647;display:flex;` +
		`align-items:center;justify-content:center;background:rgba(0,0,0,.6);` +
		`font-family:ui-monospace,"SF Mono",Menlo,Consolas,monospace');` +
		`el.innerHTML=html;` +
		`var b=el.querySelector("button");` +
		`if(b){b.onclick=function(){` + onclick + `};}` +
		`})();`
}

// backendClearJS removes the overlay; a no-op on a page that has none.
func backendClearJS() string {
	return `(function(){var el=document.getElementById("` + backendOverlayID + `");if(el){el.remove();}})();`
}
