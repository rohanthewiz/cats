//go:build darwin

package main

import (
	"encoding/json"
	"html"
	"strings"
)

// The backend overlay: what every window shows once catway's automatic restarts
// are spent (restart.go).
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

// windowBackendUI draws the supervisor's state over every window.
type windowBackendUI struct{}

func (windowBackendUI) catwayDown(detail string, canRetry bool) {
	evalInWindows(backendOverlayJS("The catway server stopped", detail, canRetry))
}

func (windowBackendUI) catwayRestarting() {
	evalInWindows(backendOverlayJS("Restarting catway…", "", false))
}

func (windowBackendUI) catwayBack() { evalInWindows(backendClearJS()) }

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
	var card strings.Builder
	card.WriteString(`<div style="background:#202020;border:1px solid #3a2a2a;border-radius:8px;` +
		`padding:24px 26px;width:min(480px,86vw);box-shadow:0 4px 20px rgba(0,0,0,.5);color:#d4d4d4">`)
	card.WriteString(`<h1 style="font-size:15px;margin:0 0 10px;color:#ff6b6b">` + html.EscapeString(title) + `</h1>`)
	if detail != "" {
		card.WriteString(`<pre style="font-size:12px;line-height:1.45;color:#c9c9c9;background:#141414;` +
			`border:1px solid #333;border-radius:5px;padding:12px;white-space:pre-wrap;` +
			`word-break:break-word;margin:0">` + html.EscapeString(detail) + `</pre>`)
	}
	if canRetry {
		card.WriteString(`<button type="button" style="margin-top:16px;padding:7px 14px;font:inherit;` +
			`font-size:13px;color:#181818;background:#d4d4d4;border:0;border-radius:5px;cursor:pointer">` +
			`Restart catway</button>`)
	}
	card.WriteString(`</div>`)
	markup, _ := json.Marshal(card.String()) // a string always marshals

	return `(function(){` +
		`var html=` + string(markup) + `;` +
		`var el=document.getElementById("` + backendOverlayID + `");` +
		`if(!el){el=document.createElement("div");el.id="` + backendOverlayID + `";` +
		`(document.body||document.documentElement).appendChild(el);}` +
		`el.setAttribute("style",'position:fixed;inset:0;z-index:2147483647;display:flex;` +
		`align-items:center;justify-content:center;background:rgba(0,0,0,.6);` +
		`font-family:ui-monospace,"SF Mono",Menlo,Consolas,monospace');` +
		`el.innerHTML=html;` +
		`var b=el.querySelector("button");` +
		// Disabled on click: the request is already made, and the "restarting"
		// state replaces this card as soon as the supervisor picks it up.
		`if(b){b.onclick=function(){b.disabled=true;b.textContent="Restarting…";` +
		`if(window.catsRestartBackend){window.catsRestartBackend();}};}` +
		`})();`
}

// backendClearJS removes the overlay; a no-op on a page that has none.
func backendClearJS() string {
	return `(function(){var el=document.getElementById("` + backendOverlayID + `");if(el){el.remove();}})();`
}
