  // ---- Keyboard (D4: structured, never pre-encoded) ----
  function mods(e) {
    return (e.shiftKey ? 1 : 0) | (e.altKey ? 2 : 0) | (e.ctrlKey ? 4 : 0) | (e.metaKey ? 8 : 0);
  }

  // CMD_TO_PANE is the curated set of ⌘ chords cats hands DOWN to a pane
  // instead of leaving them to the browser — the accelerators a terminal
  // app wants and a browser can spare, matched on e.code so a non-QWERTY
  // layout still gets the physical key (same rule as ⌘K / ⌘B above).
  //
  // Chosen by what the browser loses. ⌘W ⌘T ⌘L ⌘R ⌘N ⌘Q are the window's
  // own and are deliberately absent: swallowing "close the tab" to give a
  // pane a shortcut is a bad trade, and a user who cannot close a tab
  // blames cats, correctly. The set below costs a page that draws its
  // panes into a CANVAS almost nothing — ⌘F's find bar cannot see canvas
  // text, ⌘P would print a screenshot of a terminal, ⌘S would save this
  // page's HTML, ⌘D bookmarks a URL that is already the only one — while
  // on the other side of the wire they are save, find, print-free
  // navigation and duplicate-line in an editor. Note ⌘P also takes ⌘⇧P
  // with it (e.code ignores Shift), which is Firefox's private-window
  // chord; that is the one real cost, and it is paid only while a
  // kitty-protocol pane has focus.
  //
  // Shift is NOT part of the key: the pane receives the modifiers and
  // decides, so ⌘F and ⌘⇧F are one entry here and two chords there.
  //
  // ⌘E was added after the fact, on the same test: Safari and Chrome spend
  // it on "use selection for find", which asks a CANVAS for a text
  // selection it cannot have, so the browser gives up nothing at all —
  // while on the other side it is the editor's recent-files switcher.
  //
  // Confirmed by hand 2026-08-14, and it corrects the test above: in
  // CHROME ⌘E never reaches this page. The chord is a native menu item,
  // resolved before the keydown is dispatched, so there is no event to
  // preventDefault and this set has no say in the matter. The entry stays
  // because the MAC APP — which has no ⌘E menu item, so the chord falls
  // through the responder chain to the WebView — does deliver it, and the
  // mac app is where this is actually used.
  //
  // ⌘E is the ONLY one, and the rest of the set was checked the same day
  // to establish that: ⌘S ⌘P ⌘F ⌘D ⌘G all reach an editor pane in Chrome
  // AND in the mac app. Each of those is a Chrome menu item too (Save
  // Page As, Print, Find, Bookmark, Find Next), so a menu binding is not
  // by itself disqualifying — Chrome dispatches the keydown first and
  // honours preventDefault, which is the same courtesy that lets any web
  // editor claim ⌘S. ⌘E is the exception to that rule, not an example of
  // it. ⌘/ has no menu binding anywhere and was never in doubt.
  //
  // The lesson is narrow, then, but keep it in mind when adding a chord:
  // this set was curated by what the browser LOSES, which is a proxy for
  // the question that actually decides delivery — will the host HAND IT
  // OVER. The proxy holds for every chord here but one. Test a new entry
  // in a real browser rather than reasoning it in; the failure is benign
  // (a chord the host keeps is inert here, never a chord that does the
  // wrong thing) but silent, and silent is how ⌘E sat wrong for a day.
  const CMD_TO_PANE = new Set(["KeyS", "KeyP", "KeyE", "KeyF", "KeyD", "KeyG", "Slash"]);

  // cmdGoesToPane answers "does the focused pane want this ⌘ chord?" —
  // and the answer is no unless that pane ASKED for the kitty keyboard
  // protocol. That gate is what keeps this from being a regression for
  // shells: a legacy pane cannot receive a super chord at all (the input
  // encoder emits no bytes for one, same as Ghostty), so forwarding it
  // there would eat the user's browser shortcut and send nothing in its
  // place. An app that turned the protocol on is an app with its own
  // keymap, which is exactly who this is for.
  function cmdGoesToPane(e) {
    if (!CMD_TO_PANE.has(e.code)) return false;
    const id = focusedPaneId();
    if (id === null) return false;
    const p = panes.get(id);
    return !!(p && p.modes && p.modes.kitty);
  }

  function onKey(e) {
    // The chat panel owns the keyboard while focus is inside it: composed
    // text must never reach a PTY, and terminal shortcuts (⌘V paste-to-pane,
    // font sizing) must not fire mid-composition.
    if (chatEl.contains(document.activeElement)) return;
    // An open dialog/menu/palette owns the keyboard: its own listeners handle
    // Enter/arrows, Escape falls through to here, and nothing reaches the PTY.
    if (uiOpen()) {
      if (e.type === "keydown" && e.key === "Escape") { e.preventDefault(); closeCtx(); closeModal(); }
      return;
    }
    // Copy-mode captures the keyboard entirely — motions/yank, nothing to the PTY.
    if (copyModePane) {
      e.preventDefault();
      if (e.type === "keydown") copyModeKey(copyModePane, e);
      return;
    }
    // ⌘K (mac) / Ctrl+Alt+K opens the command palette / navigator.
    if (e.type === "keydown" && e.code === "KeyK" &&
        ((e.metaKey && !e.ctrlKey) || (e.ctrlKey && e.altKey))) {
      e.preventDefault(); openPalette(); return;
    }
    // ⌘B (mac) / Ctrl+Alt+B folds the left column away and back. Same modifier
    // pair as the palette above, so the window-chrome toggles are learned once;
    // matched on e.code so a non-QWERTY layout still gets the physical key.
    if (e.type === "keydown" && e.code === "KeyB" &&
        ((e.metaKey && !e.ctrlKey) || (e.ctrlKey && e.altKey))) {
      e.preventDefault(); setSidebarHidden(!sidebarHidden()); return;
    }
    // ⌘[ / ⌘] (mac) — Ctrl+Alt+[ / ] elsewhere: cats-level back/forward
    // through this window's focus-location history (nav.back / nav.forward),
    // reaching across panes, tabs and workspaces. The server owns the stack;
    // the page only names the direction. Shift is excluded so ⌘⇧[ / ⌘⇧]
    // (browser tab prev/next) stay with the browser.
    if (e.type === "keydown" && !e.shiftKey &&
        (e.code === "BracketLeft" || e.code === "BracketRight") &&
        ((e.metaKey && !e.ctrlKey) || (e.ctrlKey && e.altKey))) {
      e.preventDefault();
      sendCmd(e.code === "BracketLeft" ? "nav.back" : "nav.forward", {});
      return;
    }
    const isV = e.key === "v" || e.key === "V";
    if (e.type === "keydown" && e.metaKey && !e.ctrlKey && isV) { e.preventDefault(); pasteText(); return; }
    // ⌘+ / ⌘- / ⌘0 size the terminal type instead of zooming the browser (page
    // zoom would blur the canvas and leave the grid math on the old cell size).
    // Matched on e.code so ⌘= and ⌘⇧+ are the same key, numpad included.
    if (e.type === "keydown" && e.metaKey && !e.ctrlKey) {
      switch (e.code) {
        case "Equal": case "NumpadAdd":
          e.preventDefault(); setFontSize(FONT_PX + 1); return;
        case "Minus": case "NumpadSubtract":
          e.preventDefault(); setFontSize(FONT_PX - 1); return;
        case "Digit0": case "Numpad0":
          e.preventDefault(); setFontSize(FONT_DEFAULT); return;
      }
    }
    // ⌘← / ⌘→ — the macOS line-start / line-end convention — delivered to
    // the pane as Home / End. The chord itself cannot be forwarded: on a
    // legacy pane the encoder emits no bytes for a super chord (see
    // CMD_TO_PANE below), so the pane would receive nothing — while
    // Home/End is something every pane can act on: readline and zsh bind
    // them to beginning/end-of-line out of the box, and full-screen apps
    // read them as the motions they already are. The preventDefault
    // matters as much as the translation: without it the browser spends
    // ⌘←/⌘→ on history navigation, which tears down the WebSocket.
    // Shift is excluded so ⌘⇧←/→ stay whatever the host means by them,
    // rather than a phantom ⇧Home selection the pane never asked for.
    if (!e.shiftKey && e.metaKey && !e.ctrlKey && !e.altKey &&
        (e.code === "ArrowLeft" || e.code === "ArrowRight")) {
      e.preventDefault();
      if (e.type === "keydown") clearStaleSelections();
      const code = e.code === "ArrowLeft" ? "Home" : "End";
      // mods 0, not mods(e): the pane must see a bare Home/End, not
      // super+Home (which a kitty-protocol app would treat as a different,
      // unbound chord).
      sendMsg({ t: "key", code, key: code, mods: 0,
                kind: e.type === "keyup" ? "u" : (e.repeat ? "r" : "d") });
      return;
    }
    // ⌘C and ⌘Z/⌘⇧Z fall through to the pane below: apps speaking the kitty
    // keyboard protocol (editors like rd) receive super+c / super+z and run
    // their own copy/undo; for legacy panes the server's encoder emits
    // nothing, same as Ghostty. cats' own copy paths (drag, copy-mode)
    // already copied by the time ⌘C could be pressed, so nothing here
    // competes with them.
    //
    // Everything else with Meta is decided by CMD_TO_PANE + the focused
    // pane's keyboard mode (see the table above): an editor that asked for
    // the kitty protocol gets its accelerator, a shell does not and keeps
    // the browser's.
    if (e.metaKey && !e.ctrlKey && e.code !== "KeyC" && e.code !== "KeyZ" && !cmdGoesToPane(e)) return;
    if (e.code === "F12") return;         // devtools
    e.preventDefault();
    // Typing to the terminal dismisses a lingering (stale) copy wash.
    if (e.type === "keydown") clearStaleSelections();
    const kind = e.type === "keyup" ? "u" : (e.repeat ? "r" : "d");
    sendMsg({ t: "key", code: e.code, key: e.key, mods: mods(e), kind });
  }
  window.addEventListener("keydown", onKey);
  window.addEventListener("keyup", onKey);

  // The mouse's back/forward buttons (4/5, ev.button 3/4) mirror ⌘[ / ⌘]:
  // cats-level navigation, wherever on the page they are pressed. Capture
  // phase + preventDefault on mousedown is what stops the browser from
  // driving its own history instead (which would tear down the WebSocket);
  // the auxclick suppressor covers browsers that navigate on that event.
  // The pane mouse path never sees these buttons (attachMouse drops >2).
  window.addEventListener("mousedown", (ev) => {
    if (ev.button !== 3 && ev.button !== 4) return;
    ev.preventDefault(); ev.stopPropagation();
    sendCmd(ev.button === 3 ? "nav.back" : "nav.forward", {});
  }, true);
  window.addEventListener("auxclick", (ev) => {
    if (ev.button === 3 || ev.button === 4) ev.preventDefault();
  }, true);

