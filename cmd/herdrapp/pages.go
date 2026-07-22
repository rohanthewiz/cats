//go:build darwin

package main

import "html"

// The launcher's two built-in pages are tiny, self-contained HTML (no external
// assets, no build step) rendered via webview.SetHtml — the same raw-string
// approach as the gateway's login page (cmd/gateway/auth.go). They share the
// gateway's dark palette so the window looks of a piece before the real UI or a
// remote login page loads.

// connectPageHTML is the first-run form for the thin client. Submitting calls
// the Go-bound herdrConnect(url) (see runRemote), which persists the URL and
// navigates the same window to the remote gateway.
const connectPageHTML = `<!DOCTYPE html>
<html lang="en"><head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>herdr · connect</title>
<style>
  html,body{margin:0;height:100%;background:#181818;color:#d4d4d4;
    font-family:ui-monospace,"SF Mono",Menlo,Consolas,monospace;
    display:flex;align-items:center;justify-content:center;}
  form{background:#202020;border:1px solid #333;border-radius:8px;padding:28px 26px;
    width:360px;box-shadow:0 4px 20px rgba(0,0,0,.5);}
  h1{font-size:16px;margin:0 0 4px;color:#e8e8e8;}
  p.sub{font-size:12px;color:#888;margin:0 0 18px;}
  label{display:block;font-size:12px;color:#aaa;margin:0 0 6px;}
  input{width:100%;box-sizing:border-box;padding:9px 10px;font-size:14px;
    background:#141414;color:#e8e8e8;border:1px solid #3a3a3a;border-radius:5px;
    font-family:inherit;}
  input:focus{outline:none;border-color:#5b9dff;}
  button{margin-top:16px;width:100%;padding:9px;font-size:14px;cursor:pointer;
    background:#2f68c8;color:#fff;border:none;border-radius:5px;font-family:inherit;}
  button:hover{background:#3a78e0;}
</style></head><body>
<form onsubmit="submitConnect(event)">
  <h1>Connect to herdr</h1>
  <p class="sub">Enter your gateway URL — a relay host or a direct LAN/VPN address.</p>
  <label for="url">Gateway URL</label>
  <input id="url" name="url" type="url" placeholder="https://home.relay.herdr.dev"
    autofocus autocomplete="url"/>
  <button type="submit">Connect</button>
</form>
<script>
  function submitConnect(e){
    e.preventDefault();
    var v = document.getElementById('url').value.trim();
    if (v) window.herdrConnect(v);
  }
</script>
</body></html>`

// errorPageHTML renders a startup-failure page. title and detail are HTML-escaped
// because detail can be an arbitrary error string (paths, messages).
func errorPageHTML(title, detail string) string {
	return `<!DOCTYPE html>
<html lang="en"><head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>herdr · error</title>
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
