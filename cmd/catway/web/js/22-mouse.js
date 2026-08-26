  // ---- Mouse (structured cell coords; server applies the pane's encoding) ----
  function cellOf(p, ev) {
    // userX/userY undo the pane's text inset first, so a click in the margin
    // clamps to the nearest edge cell (what the user visually intends).
    const x = Math.min(Math.max(0, Math.floor(userX(p, ev.clientX) / cellW)), Math.max(0, p.W - 1));
    const y = Math.min(Math.max(0, Math.floor(userY(p, ev.clientY) / cellH)), Math.max(0, p.H - 1));
    return [x, y];
  }

  function sendMouse(p, kind, btn, ev, dx, dy) {
    const [x, y] = cellOf(p, ev);
    const m = { t: "mouse", pane: p.id, x, y, btn, kind, mods: mods(ev) };
    if (dx) m.dx = dx;
    if (dy) m.dy = dy;
    sendMsg(m);
  }

  // absPoint maps a viewport cell {x,y} to the [row, col] the server's read wants:
  // absolute screen-buffer coordinates from the top of scrollback. The frame's
  // scroll gives the history above the viewport (max) and how far it is scrolled up
  // (off), so the top visible row is (max - off). No scroll info ⇒ no scrollback.
  function absPoint(p, cell) {
    const off = p.scroll ? p.scroll.off : 0;
    const max = p.scroll ? p.scroll.max : 0;
    return [max - off + cell.y, cell.x];
  }

  // readAndCopy issues a §7 read over a viewport cell range and writes the
  // extracted text to the clipboard. Endpoints are inclusive viewport cells,
  // mapped to absolute buffer coords; rect selects a block. Shared by the mouse
  // drag path and keyboard copy-mode.
  function readAndCopy(p, aCell, bCell, rect) {
    sendCmdAwait("read", {
      pane: p.id, anchor: absPoint(p, aCell), cursor: absPoint(p, bCell), rect,
    }, (res) => {
      if (!res.ok) { toast("read failed: " + (res.error || "unknown")); return; }
      const text = res.data && res.data.text;
      if (!text) { toast("selection is empty"); return; }
      clipWrite(text).then(
        () => toast("copied " + text.length + " char" + (text.length === 1 ? "" : "s")),
        () => toast("clipboard write blocked"));
    });
  }

  // finishSelection copies the pane's completed drag range, marking the wash done
  // so a later keystroke (clearStaleSelections) dismisses the now-stale highlight.
  function finishSelection(p) {
    readAndCopy(p, p.sel.anchor, p.sel.cursor, p.sel.rect);
    p.sel.done = true;
  }

  // copyScrollback captures the pane's whole buffer (§7 capture, soft-wraps
  // rejoined) and copies it — the pane menu's "copy scrollback", distinct from
  // a range read.
  function copyScrollback(id) {
    const p = panes.get(id);
    if (!p) return;
    sendCmdAwait("capture", { pane: id, scope: 1, lines: 0, unwrap: true }, (res) => {
      if (!res.ok) { toast("capture failed: " + (res.error || "unknown")); return; }
      const text = res.data && res.data.text;
      if (!text) { toast("nothing to copy"); return; }
      clipWrite(text).then(
        () => toast("copied " + text.length + " chars (scrollback)"),
        () => toast("clipboard write blocked"));
    });
  }

  // clearStaleSelections drops any completed drag wash (p.sel.done) — a fixed
  // viewport highlight goes stale once new output or the cursor moves, so the
  // next keystroke to the terminal dismisses it. Active drags are left alone.
  function clearStaleSelections() {
    for (const p of panes.values()) {
      if (p.sel && p.sel.done) { p.sel = null; scheduleDraw(p); }
    }
  }

