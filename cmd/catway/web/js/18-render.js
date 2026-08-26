  // ---- Rendering ----
  function scheduleDraw(p) {
    if (p.dirty) return;
    p.dirty = true;
    requestAnimationFrame(() => { p.dirty = false; draw(p); });
  }

  function draw(p) {
    const ctx = p.ctx;
    const defFg = p.defFg || THEME_FG, defBg = p.defBg || THEME_BG;
    // Two passes over the transform: paint the whole canvas (padding included)
    // in the terminal's own background so the inset reads as terminal margin
    // rather than a hole, then switch to the inset grid space everything else
    // draws in. Re-installed every frame because a canvas resize clears it.
    const s = p.gs || 1;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.fillStyle = css(defBg);
    ctx.fillRect(0, 0, p.canvas.width / dpr, p.canvas.height / dpr);
    ctx.setTransform(dpr * s, 0, 0, dpr * s, (p.ox || 0) * dpr, (p.oy || 0) * dpr);
    ctx.textBaseline = "top";
    // The grid is drawn in two passes — every background first, then every
    // glyph — because a glyph is not confined to the cell it is anchored in.
    //
    // A wide grapheme (an emoji, a CJK character) occupies two columns: the VT
    // grid stores it in the first and leaves the second a blank *spacer*, and
    // the canvas draws it at its natural advance, spilling across both. Painted
    // cell by cell, that spacer's background rect lands after the glyph and
    // erases its right half — which is what turned a 🍏 in a highlighted row
    // into a green sliver, and only in a highlighted row, since a spacer at the
    // default background paints nothing at all:
    //
    //   one pass:   [bg][🍏 →][bg over the right half]   ✗
    //   two passes: [bg][bg][🍏 → drawn over both]       ✓
    //
    // Separating the passes fixes it without the grid having to know which
    // cells are spacers, and it covers everything else that overflows a cell
    // for the same reason — italics leaning past their box, a font's ligatures,
    // box-drawing that overshoots by a hairline.
    for (let y = 0; y < p.H; y++) {
      for (let x = 0; x < p.W; x++) {
        const c = p.cells[y * p.W + x];
        if (!c) continue;
        const m = c.m || 0;
        let fg = c.f || defFg, bg = c.b || defBg;
        if (m & M_REVERSED) { const t = fg; fg = bg; bg = t; }
        if (bg !== defBg) { ctx.fillStyle = css(bg); ctx.fillRect(x * cellW, y * cellH, cellW + 0.5, cellH + 0.5); }
      }
    }
    for (let y = 0; y < p.H; y++) {
      for (let x = 0; x < p.W; x++) {
        const c = p.cells[y * p.W + x];
        if (!c) continue;
        const m = c.m || 0;
        let fg = c.f || defFg, bg = c.b || defBg;
        if (m & M_REVERSED) { const t = fg; fg = bg; bg = t; }
        const px = x * cellW, py = y * cellH;
        const ch = c.s;
        if (ch && ch !== " " && !(m & M_HIDDEN)) {
          let font = "";
          if (m & M_BOLD) font += "bold ";
          if (m & M_ITALIC) font += "italic ";
          ctx.font = `${font}${FONT_PX}px ui-monospace, Menlo, monospace`;
          ctx.fillStyle = (m & M_DIM) ? blend(fg, bg, 0.5) : css(fg);
          ctx.fillText(ch, px, py + 1);
          if ((m & M_UNDERLINED) || c.h) ctx.fillRect(px, py + cellH - 2, cellW, 1);
        }
      }
    }
    if (p.cur && p.cur.vis && p.info && p.info.focused) {
      // The filled block belongs to the window that owns the keyboard. When
      // the app itself is in the background it hollows to an outline — the
      // classic terminal idiom for "this is where typing would land, but
      // nothing lands here right now".
      ctx.globalAlpha = 0.7;
      const px = p.cur.x * cellW, py = p.cur.y * cellH;
      if (winFocused) {
        ctx.fillStyle = css(defFg);
        ctx.fillRect(px, py, cellW, cellH);
      } else {
        ctx.strokeStyle = css(defFg); ctx.lineWidth = 1;
        ctx.strokeRect(px + 0.5, py + 0.5, cellW - 1, cellH - 1);
      }
      ctx.globalAlpha = 1;
    }
    if (p.sel) drawSelection(p, ctx);
    if (p.cm) drawCopyCursor(p, ctx);
    if (hasScrollbar(p)) drawScrollbar(p, ctx);
  }

  // ---- Scrollback scrollbar (WS8): drawn on the pane's right edge whenever
  // history exists (alt-screen panes scroll in-app, not in the buffer). The
  // thumb brightens while scrolled up; dragging it is wired in attachMouse.
  const SB_W = 6; // px
  function hasScrollbar(p) {
    return p.scroll && p.scroll.max > 0 && !(p.modes && p.modes.alt);
  }
  function drawScrollbar(p, ctx) {
    const vw = p.W * cellW, vh = p.H * cellH;
    const total = p.scroll.max + p.scroll.rows;
    const h = Math.max(20, vh * p.scroll.rows / total);
    const span = vh - h; // travel available to the thumb
    const top = total > p.scroll.rows ? span * (p.scroll.max - p.scroll.off) / p.scroll.max : 0;
    ctx.fillStyle = "rgba(255,255,255,0.07)";
    ctx.fillRect(vw - SB_W, 0, SB_W, vh);
    ctx.fillStyle = p.scroll.off ? SCROLL_THUMB : SCROLL_THUMB_IDLE;
    ctx.fillRect(vw - SB_W, top, SB_W, h);
  }

  // drawCopyCursor outlines the keyboard copy-mode cursor cell (the wash, if a
  // selection is anchored, is drawn by drawSelection via p.sel).
  function drawCopyCursor(p, ctx) {
    const { x, y } = p.cm.cursor;
    ctx.save();
    ctx.strokeStyle = CM_CURSOR; ctx.lineWidth = 2;
    ctx.strokeRect(x * cellW + 1, y * cellH + 1, cellW - 2, cellH - 2);
    ctx.restore();
  }

  // drawSelection washes the pane-local drag range (viewport cells). Linear mode
  // fills reading-order spans (first row from anchor, full middle rows, last row
  // to cursor); rect mode fills the bounding box. Endpoints are inclusive, matching
  // the server's read extraction.
  function drawSelection(p, ctx) {
    let a = p.sel.anchor, b = p.sel.cursor;
    if (b.y < a.y || (b.y === a.y && b.x < a.x)) { const t = a; a = b; b = t; }
    ctx.fillStyle = SEL_FILL;
    if (p.sel.rect) {
      const x0 = Math.min(a.x, b.x), x1 = Math.max(a.x, b.x);
      ctx.fillRect(x0 * cellW, a.y * cellH, (x1 - x0 + 1) * cellW, (b.y - a.y + 1) * cellH);
      return;
    }
    for (let y = a.y; y <= b.y; y++) {
      const sx = (y === a.y) ? a.x : 0;
      const ex = (y === b.y) ? b.x : p.W - 1;
      ctx.fillRect(sx * cellW, y * cellH, (ex - sx + 1) * cellW, cellH);
    }
  }

