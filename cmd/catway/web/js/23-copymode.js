  // ---- Keyboard copy-mode (§7 read via hjkl/arrows, no mouse needed) ----
  //
  // A tmux-style selection driven from the keyboard: enter on a pane, move the
  // cursor, press v to begin selecting, y/Enter to yank into the clipboard, Esc
  // to leave. Motions operate on viewport cells; k/j past the top/bottom scroll
  // the pane so the selection can reach into scrollback.

  function enterCopyMode(id) {
    const p = panes.get(id);
    if (!p) return;
    sendCmd("pane.focus", { pane: id });
    // Selecting text in a dead pane is the clearest possible statement that its
    // last screen is still wanted, so entering copy mode cancels the tidy-exit
    // countdown. Without this the header would hide the countdown behind the
    // copy-mode hints and then close the pane out from under the selection.
    keepPane(id);
    exitCopyMode(); // only one pane at a time
    const start = (p.cur && p.cur.vis) ? { x: p.cur.x, y: p.cur.y } : { x: 0, y: 0 };
    p.cm = { cursor: start, anchor: null, rect: false };
    copyModePane = p;
    p.sel = null;
    renderChrome(p); // the pane's header shows the COPY chip + key hints
    scheduleDraw(p);
  }

  function exitCopyMode() {
    const p = copyModePane;
    copyModePane = null;
    if (!p) return;
    p.cm = null; p.sel = null;
    renderChrome(p);
    scheduleDraw(p);
  }

  // cmSyncSel mirrors the copy-mode range onto p.sel so drawSelection washes it
  // (only once an anchor is set with v).
  function cmSyncSel(p) {
    const cm = p.cm;
    p.sel = cm.anchor ? { anchor: cm.anchor, cursor: cm.cursor, rect: cm.rect, moved: true } : null;
  }

  // Copy-mode keybindings. catway injects window.__catsKeys.copyMode
  // (action → [keys]) from the YAML config; this built-in table is the fallback
  // when the page is served standalone or the config omits copy_mode. We index it
  // as key → action so copyModeKey is a switch on the action, not the raw key.
  const COPY_MODE_DEFAULT = {
    "move-left": ["ArrowLeft", "h"], "move-right": ["ArrowRight", "l"],
    "move-up": ["ArrowUp", "k"], "move-down": ["ArrowDown", "j"],
    "line-start": ["0", "Home"], "line-end": ["$", "End"],
    "top": ["g"], "bottom": ["G"], "select": ["v"], "rect": ["r"],
    "yank": ["y", "Enter"], "exit": ["Escape", "q"],
  };
  const COPY_KEY = (() => {
    const table = (window.__catsKeys && window.__catsKeys.copyMode) || COPY_MODE_DEFAULT;
    const map = {};
    for (const action in table) for (const k of (table[action] || [])) map[k] = action;
    return map;
  })();

  // copyModeKey handles one keydown while in copy-mode; returns without touching
  // the terminal (onKey has already swallowed the event).
  function copyModeKey(p, e) {
    const cm = p.cm;
    if (!cm) return;
    const clampX = (x) => Math.min(Math.max(0, x), Math.max(0, p.W - 1));
    const clampY = (y) => Math.min(Math.max(0, y), Math.max(0, p.H - 1));
    const move = (dx, dy) => {
      let ny = cm.cursor.y + dy;
      if (ny < 0) { sendCmd("scroll", { pane: p.id, delta: -1 }); ny = 0; }
      else if (ny > p.H - 1) { sendCmd("scroll", { pane: p.id, delta: 1 }); ny = p.H - 1; }
      cm.cursor = { x: clampX(cm.cursor.x + dx), y: clampY(ny) };
    };
    switch (COPY_KEY[e.key]) {
      case "move-left": move(-1, 0); break;
      case "move-right": move(1, 0); break;
      case "move-up": move(0, -1); break;
      case "move-down": move(0, 1); break;
      case "line-start": cm.cursor = { x: 0, y: cm.cursor.y }; break;
      case "line-end": cm.cursor = { x: Math.max(0, p.W - 1), y: cm.cursor.y }; break;
      case "top": cm.cursor = { x: cm.cursor.x, y: 0 }; break;
      case "bottom": cm.cursor = { x: cm.cursor.x, y: Math.max(0, p.H - 1) }; break;
      case "select": cm.anchor = cm.anchor ? null : { x: cm.cursor.x, y: cm.cursor.y }; break;
      case "rect": cm.rect = !cm.rect; break;
      case "yank": copyModeYank(p); return;
      case "exit": exitCopyMode(); return;
      default: return; // unbound key: stay in copy-mode, do nothing
    }
    cmSyncSel(p);
    scheduleDraw(p);
  }

  // copyModeYank reads the current copy-mode range (anchor→cursor, or just the
  // cursor cell if nothing is anchored yet) into the clipboard, then exits.
  function copyModeYank(p) {
    const cm = p.cm;
    readAndCopy(p, cm.anchor || cm.cursor, cm.cursor, cm.rect);
    exitCopyMode();
  }

  // beginScrollDrag drags the pane's scrollbar thumb: each pointer move maps to
  // a target history offset and sends the delta (§7 scroll is relative). The
  // server's next frame updates p.scroll, keeping successive deltas honest.
  function beginScrollDrag(p, ev0) {
    let queued = false;
    const move = (ev) => {
      if (!hasScrollbar(p) || queued) return;
      queued = true;
      requestAnimationFrame(() => {
        queued = false;
        if (!hasScrollbar(p)) return;
        const vh = p.H * cellH;
        const total = p.scroll.max + p.scroll.rows;
        const h = Math.max(20, vh * p.scroll.rows / total);
        const span = vh - h;
        if (span <= 0) return;
        const frac = Math.min(1, Math.max(0, (userY(p, ev.clientY) - h / 2) / span));
        const target = Math.round(p.scroll.max * (1 - frac));
        const delta = p.scroll.off - target; // positive = toward the live bottom
        if (delta) sendCmd("scroll", { pane: p.id, delta });
      });
    };
    const up = () => {
      window.removeEventListener("mousemove", move);
      window.removeEventListener("mouseup", up);
    };
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
    move(ev0);
  }

