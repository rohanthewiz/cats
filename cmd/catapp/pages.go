//go:build darwin

package main

import "html"

// The launcher's two built-in pages are tiny, self-contained HTML (no external
// assets, no build step) rendered via webview.SetHtml — the same raw-string
// approach as the catway's login page (cmd/catway/auth.go). They share the
// catway's dark palette so the window looks of a piece before the real UI or a
// remote login page loads.

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
