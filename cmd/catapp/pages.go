//go:build darwin

package main

import "html"

// The launcher's three built-in pages are tiny, self-contained HTML (no
// external assets, no build step) rendered straight into a web view — the same
// raw-string approach as the catway's login page (cmd/catway/auth.go). They
// share the catway's dark palette so the window looks of a piece before the
// real UI or a remote login page loads.
//
// connectPage is the thin client's front door, errorPageHTML reports a startup
// failure, and splashPageHTML is the startup log's window.

// connectPage is the thin client's own front door: the catways it knows, and a
// form for one it does not.
//
// It is reachable at any time (Connect ▸ Connect to Another…), not only on a
// first run, because a thin client that could remember one address was the
// awkward part of mode 2 — a laptop follows its owner between a home server, a
// work VPN and a relay, and switching used to mean deleting app.json.
//
// Three bound callbacks, all on the Go side (see runRemote): catsConnect(url,
// label) saves and navigates, catsForget(url) drops a row, and catsCancel()
// returns to the current session when there is one — so reaching this page by
// accident is not a dead end.
//
// Every value interpolated here is HTML-escaped: a preset's URL and label are
// whatever the user typed, and they are placed inside attributes as well as
// text.
func connectPage(presets []remoteTarget, current string, canCancel bool) string {
	var rows string
	for _, p := range presets {
		esc := html.EscapeString(p.URL)
		mark := ""
		if p.URL == current {
			mark = `<span class="cur"> · connected</span>`
		}
		rows += `<li>` +
			`<button type="button" class="go" data-url="` + esc + `" onclick="connect(this.dataset.url)">` +
			`<span class="nm">` + html.EscapeString(p.name()) + mark + `</span>` +
			`<span class="url">` + esc + `</span>` +
			`</button>` +
			`<button type="button" class="x" title="Forget this catway" data-url="` + esc +
			`" onclick="forget(this.dataset.url)">&#10005;</button>` +
			`</li>`
	}
	if rows != "" {
		rows = `<ul class="saved">` + rows + `</ul><p class="or">or connect to another</p>`
	}
	cancel := ""
	if canCancel {
		cancel = `<button type="button" class="cancel" onclick="window.catsCancel()">Back to the current session</button>`
	}
	return `<!DOCTYPE html>
<html lang="en"><head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>Cats Mux · connect</title>
<style>
  html,body{margin:0;height:100%;background:#181818;color:#d4d4d4;
    font-family:ui-monospace,"SF Mono",Menlo,Consolas,monospace;
    display:flex;align-items:center;justify-content:center;}
  form{background:#202020;border:1px solid #333;border-radius:8px;padding:28px 26px;
    width:380px;box-shadow:0 4px 20px rgba(0,0,0,.5);}
  h1{font-size:16px;margin:0 0 4px;color:#e8e8e8;}
  p.sub{font-size:12px;color:#888;margin:0 0 18px;}
  label{display:block;font-size:12px;color:#aaa;margin:10px 0 6px;}
  input{width:100%;box-sizing:border-box;padding:9px 10px;font-size:14px;
    background:#141414;color:#e8e8e8;border:1px solid #3a3a3a;border-radius:5px;
    font-family:inherit;}
  input:focus{outline:none;border-color:#5b9dff;}
  button{font-family:inherit;cursor:pointer;}
  button[type=submit]{margin-top:16px;width:100%;padding:9px;font-size:14px;
    background:#2f68c8;color:#fff;border:none;border-radius:5px;}
  button[type=submit]:hover{background:#3a78e0;}
  /* Saved catways. The whole strip is the button rather than the name alone —
     an 11px hostname is not something to ask anyone to aim at. */
  ul.saved{list-style:none;margin:0 0 4px;padding:0;}
  ul.saved li{display:flex;align-items:stretch;gap:6px;margin-bottom:6px;}
  .go{flex:1;min-width:0;text-align:left;padding:8px 10px;border-radius:5px;
    background:#141414;color:#e8e8e8;border:1px solid #3a3a3a;display:block;}
  .go:hover{border-color:#5b9dff;}
  .nm{display:block;font-size:13px;}
  .url{display:block;font-size:11px;color:#777;overflow:hidden;
    text-overflow:ellipsis;white-space:nowrap;}
  .cur{color:#7bbf7b;font-size:11px;}
  .x{width:30px;border-radius:5px;background:#141414;color:#666;
    border:1px solid #3a3a3a;font-size:12px;}
  .x:hover{color:#ff6b6b;border-color:#5a3a3a;}
  p.or{font-size:11px;color:#666;margin:14px 0 0;text-align:center;}
  .cancel{margin-top:10px;width:100%;padding:7px;font-size:12px;background:none;
    color:#888;border:none;}
  .cancel:hover{color:#d4d4d4;}
</style></head><body>
<form onsubmit="submitConnect(event)">
  <h1>Connect to cats</h1>
  <p class="sub">A relay host, or a direct LAN/VPN address.</p>
  ` + rows + `
  <label for="url">Catway URL</label>
  <input id="url" name="url" type="url" placeholder="https://home.relay.herdr.dev"
    autofocus autocomplete="url"/>
  <label for="label">Name (optional)</label>
  <input id="label" name="label" type="text" placeholder="home"/>
  <button type="submit">Connect</button>
  ` + cancel + `
</form>
<script>
  function connect(url, label){ window.catsConnect(url, label || ""); }
  function forget(url){ window.catsForget(url); }
  function submitConnect(e){
    e.preventDefault();
    var v = document.getElementById('url').value.trim();
    if (v) connect(v, document.getElementById('label').value.trim());
  }
</script>
</body></html>`
}

// errorPageHTML renders a startup-failure page. title and detail are HTML-escaped
// because detail can be an arbitrary error string (paths, messages).
func errorPageHTML(title, detail string) string {
	return `<!DOCTYPE html>
<html lang="en"><head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>Cats Mux · error</title>
<style>
  html,body{margin:0;height:100%;background:#181818;color:#d4d4d4;
    font-family:ui-monospace,"SF Mono",Menlo,Consolas,monospace;
    display:flex;align-items:center;justify-content:center;}
  .card{background:#202020;border:1px solid #3a2a2a;border-radius:8px;
    padding:24px 26px;width:440px;box-shadow:0 4px 20px rgba(0,0,0,.5);}
  h1{font-size:15px;margin:0 0 10px;color:#ff6b6b;}
  pre{font-size:12px;color:#c9c9c9;background:#141414;border:1px solid #333;
    border-radius:5px;padding:12px;white-space:pre-wrap;word-break:break-word;
    margin:0;}
</style></head><body>
<div class="card">
  <h1>` + html.EscapeString(title) + `</h1>
  <pre>` + html.EscapeString(detail) + `</pre>
</div>
</body></html>`
}

// splashPageHTML is the startup window: the boot log, live.
//
// It renders whatever bootLog pushes at it (window.catsBootPush, called from
// splash_darwin.m as each snapshot arrives) and, between pushes, ticks the
// elapsed time of any step still running. That tick is the whole point of the
// window — a launch that has stopped moving looks exactly like a launch that is
// working, unless something on screen is counting. The last line with a number
// going up is where it hung.
//
//	┌ Starting Cats ──────────────────── 6.1s ┐
//	│ ✓ reading app settings           2ms    │
//	│ ✓ reading PATH from the login…   890ms  │
//	│ ✓ starting cathost               11ms   │
//	│ ✓ starting catway                9ms    │
//	│ … waiting for the catway         5.2s ← still going
//	│ · catway: dialing cathost socket…       │
//	└─────────────────────────────────────────┘
//
// The page takes no input and needs no bridge: the only thing a user can do to
// it is close it, which the title bar already offers (and which the shell reads
// as "stop showing me this"). Everything it displays is HTML-escaped — daemon
// output ends up in here verbatim.
func splashPageHTML() string {
	return `<!DOCTYPE html>
<html lang="en"><head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>Cats Mux · starting</title>
<style>
  html,body{margin:0;height:100%;background:#181818;color:#d4d4d4;
    font-family:ui-monospace,"SF Mono",Menlo,Consolas,monospace;font-size:12px;}
  body{display:flex;flex-direction:column;}
  header{display:flex;align-items:center;gap:8px;padding:12px 14px 10px;
    border-bottom:1px solid #2a2a2a;}
  #dot{width:8px;height:8px;border-radius:50%;background:#5b9dff;flex:none;
    animation:pulse 1.1s ease-in-out infinite;}
  #dot.ok{background:#7bbf7b;animation:none;}
  #dot.bad{background:#ff6b6b;animation:none;}
  @keyframes pulse{0%,100%{opacity:.25}50%{opacity:1}}
  h1{font-size:13px;margin:0;color:#e8e8e8;font-weight:600;flex:1;}
  h1.bad{color:#ff6b6b;}
  #total{color:#777;font-variant-numeric:tabular-nums;}
  /* The log scrolls; the header and footer do not, so the state of the launch
     is readable however long the daemon chatter gets. */
  main{flex:1;overflow-y:auto;padding:8px 14px 12px;}
  .row{display:grid;grid-template-columns:14px 1fr auto;column-gap:8px;
    align-items:baseline;padding:2px 0;}
  .g{color:#666;}
  .n{color:#d4d4d4;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;}
  .t{color:#777;font-variant-numeric:tabular-nums;font-size:11px;}
  .d{grid-column:2/4;color:#8a8a8a;font-size:11px;white-space:pre-wrap;
    word-break:break-word;margin:1px 0 2px;}
  .ok .g{color:#7bbf7b;}
  .warn .g,.warn .d{color:#d8b76a;}
  .fail .g{color:#ff6b6b;}
  .fail .n{color:#ff9c9c;}
  .fail .d{color:#ffc0c0;background:#241a1a;border:1px solid #3a2a2a;
    border-radius:4px;padding:6px 8px;}
  .running .g{color:#5b9dff;}
  .running .t{color:#5b9dff;}
  /* A note is a line somebody else wrote (a daemon's stderr, the page's own
     boot report). Dimmer than a step, and its name is a source label. */
  .note{padding:0;}
  .note .n{color:#6f6f6f;font-size:11px;}
  .note .d{color:#8a8a8a;}
  .empty{color:#666;margin:4px 0;}
  footer{border-top:1px solid #2a2a2a;padding:8px 14px;color:#6f6f6f;
    font-size:11px;white-space:pre-wrap;word-break:break-word;}
</style></head><body>
<header><span id="dot"></span><h1 id="head">Starting Cats</h1><span id="total"></span></header>
<main id="log"><p class="empty">starting&hellip;</p></main>
<footer id="foot">Close this window at any time &mdash; it goes away by itself once the workspace is up.</footer>
<script>
(function () {
  var logEl = document.getElementById("log"), headEl = document.getElementById("head"),
      totalEl = document.getElementById("total"), dotEl = document.getElementById("dot"),
      footEl = document.getElementById("foot");
  // skew converts the launcher's clock (ms since it started) into ours, so a
  // running step can keep counting between pushes instead of freezing.
  var state = null, skew = 0;
  var GLYPH = {running:"⋯", ok:"✓", warn:"!", fail:"✕", note:"·"};

  function esc(s) {
    return String(s === null || s === undefined ? "" : s).replace(/[&<>"]/g, function (c) {
      return {"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;"}[c];
    });
  }
  function fmt(ms) { return ms < 1000 ? ms + "ms" : (ms / 1000).toFixed(1) + "s"; }
  function clock() { return Date.now() - skew; }

  function render() {
    if (!state) return;
    // Stay pinned to the newest line only if the reader already was: scrolling
    // back to read an error should not be undone by the next daemon line.
    var atBottom = logEl.scrollHeight - logEl.scrollTop - logEl.clientHeight < 24;
    var out = "";
    for (var i = 0; i < state.entries.length; i++) {
      var e = state.entries[i];
      var running = e.state === "running";
      var ms = running ? clock() - e.start : e.end - e.start;
      out += '<div class="row ' + esc(e.state) + '">' +
        '<span class="g">' + (GLYPH[e.state] || "·") + '</span>' +
        '<span class="n">' + esc(e.name) + '</span>' +
        '<span class="t" data-start="' + e.start + '">' +
          (e.state === "note" ? "" : fmt(ms)) + '</span>' +
        (e.detail ? '<span class="d">' + esc(e.detail) + '</span>' : "") +
        '</div>';
    }
    logEl.innerHTML = out || '<p class="empty">starting&hellip;</p>';
    if (atBottom) logEl.scrollTop = logEl.scrollHeight;

    if (state.failed) {
      headEl.textContent = "Cats could not start";
      headEl.className = "bad"; dotEl.className = "bad";
      footEl.textContent = "The step marked ✕ is where it stopped." +
        (state.log_path ? " A copy of this log is at " + state.log_path : "");
    } else if (state.done) {
      headEl.textContent = "Ready"; headEl.className = ""; dotEl.className = "ok";
    } else {
      headEl.textContent = "Starting Cats"; headEl.className = ""; dotEl.className = "";
    }
    tick();
  }

  // tick advances the running steps' clocks without rebuilding the list.
  function tick() {
    if (!state) return;
    var t = clock();
    if (!state.done && !state.failed) totalEl.textContent = fmt(t);
    var rows = logEl.querySelectorAll(".row.running .t");
    for (var i = 0; i < rows.length; i++) {
      rows[i].textContent = fmt(t - parseInt(rows[i].getAttribute("data-start"), 10));
    }
  }

  window.catsBootPush = function (s) { state = s; skew = Date.now() - s.now; render(); };
  setInterval(tick, 200);
})();
</script>
</body></html>`
}
