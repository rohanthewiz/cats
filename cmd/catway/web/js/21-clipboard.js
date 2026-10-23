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
  // onPaste routes a native paste event: to the focused pane, or left alone so
  // the browser inserts it where the user is typing.
  //
  // A native paste event is how ⌘V arrives in the mac app: Cocoa's Edit ▸ Paste
  // menu item owns the ⌘V key equivalent, so the page sees `paste`, not the
  // keydown onKey would turn into pasteText. In a browser, onKey stands aside
  // while a dialog is open (uiOpen), and the browser's own ⌘V then fires
  // `paste` too. Both routes therefore end here, and this is where a paste
  // into the New Workspace dialog used to be stolen and typed into the pane
  // underneath instead.
  //
  // Stays local when:
  //   - the chat composer holds the target (it always has);
  //   - the target is any editable field EXCEPT the text sink — a dialog
  //     input, a picker, a settings field. The sink is a textarea too, but it
  //     is the pane's stand-in (42-textsink.js): while it holds the focus the
  //     user is typing at the terminal, so its pastes must still go through;
  //   - a dialog/menu is open — the same rule onKey applies to keys. Covers a
  //     focused dialog button or checkbox, where pasting into the pane behind
  //     the overlay would be just as wrong as from an input.
  //
  //   paste event ─┬─ chat / editable field (not sink) / uiOpen ─▶ return (browser inserts)
  //                └─ otherwise ─▶ preventDefault + {t:"paste"} to the focused pane
  function onPaste(e) {
    if (chatEl.contains(e.target)) return; // a paste into the composer stays local
    if (isEditableField(e.target) && e.target !== textSinkEl) return;
    if (uiOpen()) return;
    const text = (e.clipboardData || window.clipboardData).getData("text");
    if (text) { e.preventDefault(); sendMsg({ t: "paste", data: text }); }
  }
  // isEditableField: does the browser insert pasted text into el by itself?
  // Text-like inputs, textareas and contenteditable. A checkbox or select is
  // an <input>/<select> the browser would not paste into, so those are not
  // counted — they fall to the uiOpen test instead.
  function isEditableField(el) {
    if (!el) return false;
    if (el.isContentEditable) return true;
    if (el.tagName === "TEXTAREA") return !el.readOnly && !el.disabled;
    if (el.tagName !== "INPUT") return false;
    const t = (el.type || "text").toLowerCase();
    const textLike = ["text", "search", "url", "email", "tel", "password", "number"];
    return textLike.includes(t) && !el.readOnly && !el.disabled;
  }
  document.addEventListener("paste", onPaste);

