  // ---- Overlay + modal infrastructure (WS8) ----
  //
  // One overlay at a time (dialog, palette, or help) plus an independent
  // context menu. While any is open the keyboard belongs to the UI: onKey
  // returns early (no preventDefault, so modal inputs type normally) and a
  // capture-phase Escape closes whatever is up.

  let modalEl = null; // current #overlay root, or null
  let ctxEl = null;   // current #ctxmenu, or null
  let modalCleanup = null; // runs on any close path (settings preview rollback)
  function uiOpen() { return !!(modalEl || ctxEl); }

  function closeModal() {
    if (modalEl) { modalEl.remove(); modalEl = null; }
    if (modalCleanup) { const fn = modalCleanup; modalCleanup = null; fn(); }
  }
  // Menus form a chain (a menu's open submenu hangs off _sub), so closing means
  // walking it: a submenu is a sibling in the DOM, not a descendant, and would
  // otherwise be orphaned on screen when its parent goes.
  function closeCtx() { if (ctxEl) { closeCtxChain(ctxEl); ctxEl = null; } }
  function closeCtxChain(m) { if (m._sub) closeCtxChain(m._sub); m.remove(); }
  function ctxChainHas(m, node) {
    for (; m; m = m._sub) if (m.contains(node)) return true;
    return false;
  }

  // openOverlay mounts a fresh overlay; build receives the overlay element and
  // appends its .modal. A mousedown on the backdrop dismisses.
  function openOverlay(build) {
    closeModal(); closeCtx();
    const ov = document.createElement("div");
    ov.id = "overlay";
    ov.addEventListener("mousedown", (e) => { if (e.target === ov) closeModal(); });
    build(ov);
    document.body.appendChild(ov);
    modalEl = ov;
    return ov;
  }

  // focusField hands a freshly-mounted dialog its caret — and then checks that
  // it kept it.
  //
  // One requestAnimationFrame is enough whenever the dialog is the first thing
  // to open: by the time the frame runs, the overlay is mounted and laid out,
  // so focus() lands. It is NOT enough when a dialog REPLACES another overlay
  // in the same tick — openOverlay's closeModal() removes the old #overlay
  // subtree, and the engine's "the focused element just left the document"
  // reset is not guaranteed to have run by the time our frame does. Chrome
  // clears it synchronously while the node is being removed and so never
  // showed this; the mac app's WKWebView (window_darwin.m) can clear it out of
  // band, landing AFTER our focus() and handing the document back to <body>.
  // That hand-back is invisible: the pane underneath is a canvas, never a focus
  // target, and it keeps drawing its own blinking cursor — so the dialog looks
  // and behaves as though the terminal still owns the keyboard. "add plugin"
  // (the plugins dialog's add… button) is the one dialog in the UI opened from
  // inside another overlay, which is why it was the one that showed it.
  //
  // So: take the focus, then re-assert it one frame later if something took it
  // away. Re-asserting is a no-op in the common case (we still hold it), and
  // two frames is ~33ms — far too short for the user to have deliberately
  // clicked elsewhere, so this can never fight a real intent.
  //
  // select re-selects the prefill on the first take only; a re-assert must not
  // wipe a selection the user has since narrowed by hand.
  function focusField(el, select) {
    const take = () => {
      if (document.activeElement === el) return;
      el.focus();
      // A <select> field (dialogFields' choice fields) has no select() method.
      if (select && el.select) el.select();
    };
    requestAnimationFrame(() => {
      take();
      requestAnimationFrame(() => take());
    });
  }

  function mkModalBtn(label, cls, fn) {
    const b = document.createElement("button");
    b.textContent = label; if (cls) b.className = cls;
    b.addEventListener("click", fn);
    return b;
  }

