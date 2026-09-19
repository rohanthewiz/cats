  // ---- Text sink: dictation, IME and other text that never was a keypress ----
  //
  // Panes are canvases and the keyboard is read as raw keydown/keyup on the
  // window (20-keys.js), so in the ordinary state NOTHING editable holds the
  // focus — document.activeElement is <body>. That is fine for keys, and it
  // is exactly why macOS dictation was inert: Edit ▸ Start Dictation (an item
  // AppKit adds to the mac app's Edit menu by itself) opens the microphone,
  // hears the words, and then asks the first responder's text-input client to
  // insert them. WKWebView only has such a client while an editable element
  // is focused. With <body> focused the text had nowhere to land and was
  // dropped without an error — "something starts, nothing arrives".
  //
  // The same hole swallowed every other source of text that is not a
  // keypress: IME composition (CJK, and dead keys on hosts that route them
  // through the IME), the emoji & symbols palette, text replacement.
  //
  // The fix is the one every canvas terminal ends up with: an invisible
  // <textarea> that holds the focus whenever nothing else wants it, so the
  // host always has somewhere to insert. Whatever is committed into it is
  // forwarded to the focused pane and the textarea is emptied again.
  //
  //   key press   ─▶ window keydown ─▶ onKey ─▶ {t:"key"}     (unchanged;
  //                                     └─ preventDefault, so the press
  //                                        never ALSO types into the sink)
  //   dictation ┐
  //   IME       ├─▶ textarea `input` / `compositionend` ─▶ flush ─▶ {t:"paste"}
  //   emoji     ┘
  //
  // Why {t:"paste"} rather than a new message: the server's paste encoder
  // already does what inserted text needs — it wraps in bracketed paste when
  // the pane asked for it (so a dictated sentence with a newline cannot submit
  // a half-finished prompt) and scrubs control bytes. A dictated phrase is, to
  // the program in the pane, indistinguishable from a small paste.
  const textSinkEl = document.createElement("textarea");
  textSinkEl.id = "textsink";
  // Everything the host might "help" with is off: the sink is a conduit, and
  // an autocorrected or capitalised shell command is a wrong command.
  textSinkEl.setAttribute("autocomplete", "off");
  textSinkEl.setAttribute("autocorrect", "off");
  textSinkEl.setAttribute("autocapitalize", "off");
  textSinkEl.setAttribute("spellcheck", "false");
  textSinkEl.setAttribute("aria-hidden", "true");
  textSinkEl.tabIndex = -1;
  // Styled inline so the part is self-contained. It must stay RENDERED
  // (display:none / visibility:hidden elements cannot take focus), so it is
  // made invisible by being 1px, transparent and unclickable instead.
  // position:fixed lets placeTextSink move it to the terminal cursor, which is
  // where the host anchors the dictation microphone and the IME candidate box.
  textSinkEl.style.cssText =
    "position:fixed;left:0;top:0;width:1px;height:1px;padding:0;border:0;margin:0;" +
    "opacity:0;outline:none;resize:none;overflow:hidden;pointer-events:none;" +
    "caret-color:transparent;z-index:-1;";
  document.body.appendChild(textSinkEl);

  // textSinkWanted: should an idle page park its focus on the sink? Only on a
  // device with a real pointer — on a touch screen, focusing a textarea raises
  // the soft keyboard over the terminal, uninvited, on every tap.
  function textSinkWanted() {
    return !window.matchMedia || window.matchMedia("(pointer: fine)").matches;
  }

  // parkTextSink gives the sink the focus, but ONLY when nobody holds it
  // (activeElement is <body>). A dialog field, the chat composer or a picker
  // input always wins, because each of those takes focus for itself and the
  // sink never takes it back from anything but <body>.
  function parkTextSink() {
    if (!textSinkWanted()) return;
    const a = document.activeElement;
    if (a && a !== document.body && a !== document.documentElement) return;
    // preventScroll: the sink moves around (placeTextSink); focusing it must
    // never scroll the page to "reveal" a 1px element.
    textSinkEl.focus({ preventScroll: true });
  }

  // placeTextSink moves the sink to the focused pane's cursor so host UI
  // anchored to the insertion point appears where the text will land. It is
  // best-effort: p.ox/p.oy/p.gs are the grid's inset and scale inside the
  // canvas (setInset), and any pane without a cursor leaves the sink where it
  // was. A slightly-off anchor costs nothing; a throw here would cost the text.
  function placeTextSink() {
    const id = focusedPaneId();
    const p = id === null ? null : panes.get(id);
    if (!p || !p.cur) return;
    const r = p.canvas.getBoundingClientRect();
    const x = r.left + p.ox + p.cur.x * cellW * p.gs;
    const y = r.top + p.oy + (p.cur.y + 1) * cellH * p.gs;
    textSinkEl.style.left = Math.max(0, Math.min(window.innerWidth - 2, x)) + "px";
    textSinkEl.style.top = Math.max(0, Math.min(window.innerHeight - 2, y)) + "px";
  }

  // flushTextSink forwards whatever has been committed and empties the sink.
  // Emptying is what makes it safe to call from BOTH `input` and
  // `compositionend`: engines disagree about which fires last (WebKit ends the
  // composition and then fires input; Chrome does the reverse), and the second
  // call simply finds nothing left to send.
  //
  // Text is dropped, not forwarded, while an overlay or copy-mode owns the
  // keyboard — the same rule onKey applies to keys, for the same reason.
  function flushTextSink() {
    const text = textSinkEl.value;
    if (!text) return;
    textSinkEl.value = "";
    if (uiOpen() || copyModePane) return;
    clearStaleSelections();
    sendMsg({ t: "paste", data: text });
  }

  // textSinkComposing is true between compositionstart and compositionend.
  // Dictation and IMEs both revise their text while it is still "marked"
  // (dictation rewrites its hypothesis as more words arrive), and each
  // revision fires `input`. Only the committed result may reach the PTY — a
  // terminal cannot un-type — so flushes are held until the composition ends.
  let textSinkComposing = false;
  textSinkEl.addEventListener("compositionstart", () => {
    textSinkComposing = true;
    placeTextSink();
  });
  textSinkEl.addEventListener("compositionend", () => {
    textSinkComposing = false;
    flushTextSink();
  });
  textSinkEl.addEventListener("input", (e) => {
    if (textSinkComposing || e.isComposing) return;
    flushTextSink();
  });

  // Keeping the sink parked. Focus falls back to <body> whenever its holder
  // goes away — a click on a canvas or the sidebar, a dialog closing, the chat
  // composer blurring — and there is no single place those all pass through.
  // `focusout` is: it fires for every one of them. The re-park waits a frame
  // because during focusout activeElement is not settled yet (the element
  // about to RECEIVE focus has not got it), and parking early would steal it.
  document.addEventListener("focusout", () => requestAnimationFrame(parkTextSink));
  // Belt to those braces: engines differ on whether a click on something
  // unfocusable (a canvas, a sidebar row) reports a focusout at all, and
  // parking is a no-op when the sink — or anything else — already has focus.
  window.addEventListener("mouseup", () => requestAnimationFrame(parkTextSink));
  // Returning to the window: the mac app restores first-responder status to
  // the WebView, not to the element inside it that last had focus.
  window.addEventListener("focus", () => requestAnimationFrame(parkTextSink));
  parkTextSink();

