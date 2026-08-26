  // ---- Clipboard ----
  // The mac app binds catsClipWrite/catsClipRead (native pasteboard) into the
  // page, because WKWebView cripples navigator.clipboard: reads resolve empty
  // and writes demand a user activation that WebSocket-driven copies (OSC 52
  // from a pane, §7 read results) never have. Prefer the bridge when present;
  // browsers keep using navigator.clipboard.
  function clipWrite(text) {
    if (window.catsClipWrite) return window.catsClipWrite(text);
    return navigator.clipboard.writeText(text);
  }
  function clipRead() {
    if (window.catsClipRead) return window.catsClipRead();
    if (!navigator.clipboard || !navigator.clipboard.readText)
      return Promise.reject(new Error("clipboard read unavailable"));
    return navigator.clipboard.readText();
  }

  // ---- Paste ----
  function pasteText() {
    clipRead()
      .then((t) => { if (t) sendMsg({ t: "paste", data: t }); else toast("clipboard has no text"); })
      .catch((err) => toast("paste blocked: " + ((err && err.name) || err)));
  }
  document.addEventListener("paste", (e) => {
    if (chatEl.contains(e.target)) return; // a paste into the composer stays local
    const text = (e.clipboardData || window.clipboardData).getData("text");
    if (text) { e.preventDefault(); sendMsg({ t: "paste", data: text }); }
  });

