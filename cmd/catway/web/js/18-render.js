  // ---- Rendering ----
  //
  // Two kinds of repaint, one rAF per pane per frame:
  //
  //   scheduleDraw(p)   FULL — every cell, the inset margin, every overlay.
  //                     What every caller outside the diff path asks for: a
  //                     new frame, a resize, a theme, a selection or copy-mode
  //                     change, focus. These are rare, so they stay simple.
  //   markRow(p, y) +   ROWS — only the bands of rows a pane_diff touched,
  //   requestPaint(p)   plus the rows the cursor left and entered.
  //
  // The diff path is the hot one — an agent streaming output, a spinner, a
  // clock in a status line — and it typically changes a handful of cells on a
  // row or two. Repainting the whole grid for that was the dominant cost of a
  // busy pane: a 200×60 pane is 12,000 cells, two passes over each, every
  // animation frame, per streaming pane. Only the changed rows are repainted
  // now, and a diff that touched more than half the rows falls back to a full
  // draw (scrolling output shifts every row anyway, and a full draw needs no
  // clip bookkeeping).
  function scheduleDraw(p) { p.full = true; requestPaint(p); }

  // markRow records one row as needing a repaint on the next frame. The set is
  // created lazily so panes built before this existed (and test doubles) work.
  function markRow(p, y) { (p.rows || (p.rows = new Set())).add(y); }

  function requestPaint(p) {
    if (p.dirty) return;
    p.dirty = true;
    requestAnimationFrame(() => { p.dirty = false; paint(p); });
  }

  // paint resolves a frame's worth of requests into one full draw or a set of
  // row bands.
  function paint(p) {
    const rows = p.rows || (p.rows = new Set());
    if (p.full || !p.H) { p.full = false; rows.clear(); draw(p); return; }
    // The cursor is an overlay, not a cell, so a diff that only moves it
    // touches no cell: both the row it was painted on and the row it now
    // belongs on have to be repainted, or the old block would be left behind.
    // Compared on the column as well as the row: a cursor stepping along its
    // own row (typing, arrow keys) changes no row number at all.
    const cy = cursorRow(p), cx = cy >= 0 ? p.cur.x : -1;
    if (cy !== p.curRow || cx !== p.curCol) {
      if (p.curRow >= 0) rows.add(p.curRow);
      if (cy >= 0) rows.add(cy);
    }
    if (!rows.size) return;
    if (rows.size * 2 > p.H) { rows.clear(); draw(p); return; }
    const ys = Array.from(rows).sort((a, b) => a - b);
    rows.clear();
    const ctx = p.ctx;
    beginFrame(p, ctx);
    // Nearby dirty rows are merged into one band so a multi-line update costs
    // one clip and one neighbour margin, not one per row. "Nearby" is within
    // three: each band clears a neighbour on either side, so rows three apart
    // would clear abutting ranges anyway, and one band paints fewer context
    // rows than two.
    for (let i = 0; i < ys.length;) {
      let j = i;
      while (j + 1 < ys.length && ys[j + 1] - ys[j] <= 3) j++;
      if (ys[i] < p.H) drawBand(p, ctx, ys[i], Math.min(ys[j], p.H - 1));
      i = j + 1;
    }
    p.curRow = cy; p.curCol = cx;
  }

  // cursorRow is the row the text cursor is painted on, or -1 when none is
  // painted — the same condition drawOverlays tests.
  function cursorRow(p) {
    return (p.cur && p.cur.vis && p.info && p.info.focused) ? p.cur.y : -1;
  }

  // beginFrame installs the padded grid transform (a canvas resize clears it,
  // so it is re-installed on every paint — see setInset) and the text state
  // every glyph shares.
  function beginFrame(p, ctx) {
    const s = p.gs || 1;
    ctx.setTransform(dpr * s, 0, 0, dpr * s, (p.ox || 0) * dpr, (p.oy || 0) * dpr);
    ctx.textBaseline = "top";
  }

  function draw(p) {
    const ctx = p.ctx;
    const defFg = p.defFg || THEME_FG, defBg = p.defBg || THEME_BG;
    // Two passes over the transform: paint the whole canvas (padding included)
    // in the terminal's own background so the inset reads as terminal margin
    // rather than a hole, then switch to the inset grid space everything else
    // draws in. Re-installed every frame because a canvas resize clears it.
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.fillStyle = css(defBg);
    ctx.fillRect(0, 0, p.canvas.width / dpr, p.canvas.height / dpr);
    beginFrame(p, ctx);
    paintCells(p, ctx, 0, p.H - 1, defFg, defBg);
    drawOverlays(p, ctx, defFg);
    p.curRow = cursorRow(p);
    p.curCol = p.curRow >= 0 ? p.cur.x : -1;
  }

  // drawBand repaints dirty rows y0..y1 (inclusive) in place, leaving the rest
  // of the canvas untouched.
  //
  // It has to reproduce, inside what it repaints, exactly the pixels a full
  // draw would have left there — and a full draw does not keep each row inside
  // its own box: background rects overshoot by half a pixel (to close
  // antialiasing seams between same-colored cells) into the row below, and a
  // glyph can overhang its cell (box-drawing strokes reach the full line
  // height, emoji run tall). That cuts both ways:
  //
  //   - a changed row's OLD overhang sits in its neighbours, so the neighbours
  //     have to be cleared and repainted too, or the stale ink stays; and
  //   - the neighbours' neighbours overhang into the neighbours, so they are
  //     painted as well — underneath a clip that keeps their own pixels intact.
  //
  //   y0-2  ░░░░░░░░░░░░   painted, clipped away: restores its overhang into y0-1
  //   y0-1  ▒▒▒▒▒▒▒▒▒▒▒▒ ┐
  //   y0    ████████████ │ clip: cleared, then repainted (dirty rows plus one
  //   …     ████████████ │ neighbour each side, which may carry the dirty rows'
  //   y1    ████████████ │ old overhang)
  //   y1+1  ▒▒▒▒▒▒▒▒▒▒▒▒ ┘
  //   y1+2  ░░░░░░░░░░░░   painted, clipped away
  //
  // Painting is in the same bg-then-glyph order draw() uses, so the layering
  // matches. A one-row change therefore paints five rows — still a small slice
  // of a 60-row pane, and the price of never leaving a sliver of stale ink.
  //
  // Horizontally the clip spans the whole canvas, inset margin included, so
  // the last column's overshoot into the right margin is repainted too; a band
  // on the first or last row reaches the top or bottom margin for the same
  // reason. Overlays (cursor, selection, copy cursor, scrollbar) are drawn
  // whole and fall to the same clip, so each lands in the band exactly once —
  // which matters for the scrollbar's translucent track, which would darken
  // with every repaint if it were layered over pixels that were not cleared.
  function drawBand(p, ctx, y0, y1) {
    const defFg = p.defFg || THEME_FG, defBg = p.defBg || THEME_BG;
    const s = p.gs || 1, ox = p.ox || 0, oy = p.oy || 0;
    const c0 = Math.max(0, y0 - 1), c1 = Math.min(p.H - 1, y1 + 1); // the cleared rows
    const left = -ox / s, right = (p.canvas.width / dpr - ox) / s;
    const top = c0 === 0 ? -oy / s : c0 * cellH;
    const bottom = c1 >= p.H - 1 ? (p.canvas.height / dpr - oy) / s : (c1 + 1) * cellH;
    ctx.save();
    ctx.beginPath();
    ctx.rect(left, top, right - left, bottom - top);
    ctx.clip();
    ctx.fillStyle = css(defBg);
    ctx.fillRect(left, top, right - left, bottom - top);
    paintCells(p, ctx, Math.max(0, c0 - 1), Math.min(p.H - 1, c1 + 1), defFg, defBg);
    drawOverlays(p, ctx, defFg);
    ctx.restore();
  }

  // paintCells draws rows y0..y1 (inclusive) of the grid, backgrounds first,
  // then glyphs.
  //
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
  //
  // Canvas state is written only when it changes. Assigning ctx.font makes
  // the browser parse a CSS font shorthand, and assigning fillStyle a CSS
  // color, EVEN WHEN THE VALUE IS THE SAME — so the old per-glyph
  // `ctx.font = …` / `fillStyle = css(…)` paid two string parses (and two
  // string allocations) for every character on screen. Colors and fonts come
  // from caches that hand back the same string for the same input, so a plain
  // === against the last value written is enough to skip the write; a line of
  // one-colored text now costs one fillStyle and one font for the whole run.
  function paintCells(p, ctx, y0, y1, defFg, defBg) {
    const W = p.W, cells = p.cells;
    let fill = null; // the fillStyle string this call last wrote
    for (let y = y0; y <= y1; y++) {
      for (let x = 0; x < W; x++) {
        const c = cells[y * W + x];
        if (!c) continue;
        const m = c.m || 0;
        let fg = c.f || defFg, bg = c.b || defBg;
        if (m & M_REVERSED) { const t = fg; fg = bg; bg = t; }
        if (bg !== defBg) {
          const st = css(bg);
          if (st !== fill) ctx.fillStyle = fill = st;
          ctx.fillRect(x * cellW, y * cellH, cellW + 0.5, cellH + 0.5);
        }
      }
    }
    const fonts = cellFonts();
    let font = -1; // the bold/italic bits the font was last set for
    for (let y = y0; y <= y1; y++) {
      for (let x = 0; x < W; x++) {
        const c = cells[y * W + x];
        if (!c) continue;
        const ch = c.s;
        const m = c.m || 0;
        if (!ch || ch === " " || (m & M_HIDDEN)) continue;
        let fg = c.f || defFg, bg = c.b || defBg;
        if (m & M_REVERSED) { const t = fg; fg = bg; bg = t; }
        const px = x * cellW, py = y * cellH;
        const fk = m & (M_BOLD | M_ITALIC);
        if (fk !== font) { ctx.font = fonts[fk]; font = fk; }
        const st = (m & M_DIM) ? dimCss(fg, bg) : css(fg);
        if (st !== fill) ctx.fillStyle = fill = st;
        ctx.fillText(ch, px, py + 1);
        if ((m & M_UNDERLINED) || c.h) ctx.fillRect(px, py + cellH - 2, cellW, 1);
      }
    }
  }

  // cellFonts returns the four font shorthands a cell can need, indexed by its
  // M_BOLD|M_ITALIC bits (bold is 0x1, italic 0x4, so the table is sparse at
  // 2 and 3). Rebuilt only when the font size changes.
  let cellFontsPx = 0, cellFontsTab = null;
  function cellFonts() {
    if (cellFontsPx !== FONT_PX || !cellFontsTab) {
      const f = (pre) => `${pre}${FONT_PX}px ui-monospace, Menlo, monospace`;
      cellFontsTab = [];
      cellFontsTab[0] = f("");
      cellFontsTab[M_BOLD] = f("bold ");
      cellFontsTab[M_ITALIC] = f("italic ");
      cellFontsTab[M_BOLD | M_ITALIC] = f("bold italic ");
      cellFontsPx = FONT_PX;
    }
    return cellFontsTab;
  }

  // drawOverlays paints everything that sits on top of the cells.
  function drawOverlays(p, ctx, defFg) {
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

