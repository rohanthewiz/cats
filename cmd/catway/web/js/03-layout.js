  // ---- Layout (D3): position pane DOM from computed rects + render chrome ----
  // placePane writes one pane's pixel geometry from a rect/inner pair in cells.
  // Split out of applyLayout because refitLayout below drives the same writes
  // from rects the server has not sent yet, and the two must not drift.
  function placePane(p, rect, inner) {
    const [rx, ry, rw, rh] = rect, [ix, iy, iw, ih] = inner;
    p.el.style.left = (rx * cellW) + "px";
    p.el.style.top = (ry * cellH) + "px";
    p.el.style.width = (rw * cellW) + "px";
    p.el.style.height = (rh * cellH) + "px";
    p.chrome.style.height = ((iy - ry) * cellH) + "px";
    p.canvas.style.left = ((ix - rx) * cellW) + "px";
    p.canvas.style.top = ((iy - ry) * cellH) + "px";
    p.canvas.style.width = (iw * cellW) + "px";
    p.canvas.style.height = (ih * cellH) + "px";
    p.canvas.width = Math.floor(iw * cellW * dpr);
    p.canvas.height = Math.floor(ih * cellH * dpr);
    // Sizing the canvas resets its context (transform included), so the
    // padded transform is (re)installed per frame in draw() — see setInset.
    setInset(p, iw * cellW, ih * cellH);
  }

  // refitLayout re-hangs the panes on the grid we just derived, without waiting
  // for the server to send a layout for it.
  //
  // Why it has to exist: pane geometry is authored by the server in cells and
  // arrives over the socket, so between "the terminal area just changed size"
  // and "the new layout landed" the panes are still wearing the old area's
  // pixels. Folding the sidebar away leaves a bare strip where the panes have
  // not grown into the space yet; bringing it back is the ugly direction, since
  // the panes are now wider than #panes and the right-hand one is cut off by its
  // overflow:hidden — measured at 276px of a 904px area on a 1200px window. It
  // is one round trip and it corrects itself, but it is a visible lurch every
  // time the column moves, and on a slow or reconnecting socket it is not brief.
  //
  // What it does: scales the last layout's rects from the grid they were
  // authored for onto the current one. Edges are scaled and rounded, then widths
  // taken as the difference between neighbouring edges, so panes stay exactly
  // adjacent — rounding each width independently would open one-cell seams or
  // overlaps between them. Chrome and border insets are carried across in cells
  // rather than scaled, because they are fixed furniture: a header is one row
  // whatever the grid does.
  //
  // This is provisional. layoutMsg is never written back to, so the estimate is
  // always taken from the server's own last word rather than compounding on a
  // previous estimate, and the real layout overwrites all of it on arrival.
  function refitLayout() {
    if (!layoutMsg || !layoutMsg.panes || !layoutMsg.panes.length) return;
    // The grid the current rects were authored for is the extent they tile.
    let oldCols = 0, oldRows = 0;
    for (const pr of layoutMsg.panes) {
      oldCols = Math.max(oldCols, pr.rect[0] + pr.rect[2]);
      oldRows = Math.max(oldRows, pr.rect[1] + pr.rect[3]);
    }
    if (oldCols < 1 || oldRows < 1) return;
    if (oldCols === cols && oldRows === rows) return;   // nothing moved
    const sx = cols / oldCols, sy = rows / oldRows;
    for (const pr of layoutMsg.panes) {
      const p = panes.get(pr.pane);
      if (!p) continue;
      const [rx, ry, rw, rh] = pr.rect, [ix, iy, iw, ih] = pr.inner;
      const nx = Math.round(rx * sx), ny = Math.round(ry * sy);
      const nw = Math.max(1, Math.round((rx + rw) * sx) - nx);
      const nh = Math.max(1, Math.round((ry + rh) * sy) - ny);
      // Insets are held in cells: left/top gaps stay put, and the right/bottom
      // gaps are taken off the new size so the inner box never outgrows its pane.
      const niw = Math.max(1, nw - (rw - iw)), nih = Math.max(1, nh - (rh - ih));
      placePane(p, [nx, ny, nw, nh], [nx + (ix - rx), ny + (iy - ry), niw, nih]);
      // The canvas was just resized, which clears it; repaint from the cells we
      // already hold rather than leaving a blank pane until the server speaks.
      scheduleDraw(p);
    }
  }

  function applyLayout(msg) {
    layoutMsg = msg;
    syncWindowURL(msg);
    // Zoom is tab state but renders as pane state: a zoomed tab sends only its
    // focused pane, whose header carries the ZOOM chip (see renderChrome).
    tabZoomed = msg.tabs.some((t) => t.active && t.zoomed);
    const seen = new Set();
    for (const pr of msg.panes) {
      const p = pane(pr.pane);
      seen.add(pr.pane);
      p.pub = pr.pub; p.info = pr;
      placePane(p, pr.rect, pr.inner);
      p.el.classList.toggle("focused", !!pr.focused);
      renderChrome(p);
      scheduleDraw(p);
    }
    for (const [id, p] of panes) {
      if (!seen.has(id)) {
        if (p === copyModePane) exitCopyMode();
        p.el.remove(); panes.delete(id);
      }
    }
    // Structure changed: re-render the inventory-fed sections (Workspaces rows
    // included), then re-query the inventory behind them.
    refreshPaneList();
    markFocusedAgent();
    markLockedAgents(); // a lock flip rebroadcasts the layout, not the rollup
    renderTabbar(msg);
    renderBorders(msg);
  }

  // ---- Pane text inset ------------------------------------------------------
  //
  // Panes tile the cell grid exactly (the server's rects leave no gutter), so a
  // pane's canvas is precisely inner-cols × inner-rows of glyph boxes and text
  // lands flush against the pane edge. Rather than shrink the canvas — which
  // would clip the last column/row — the canvas keeps the full inner rect and
  // the *grid inside it* is drawn a hair smaller and centred, turning the
  // leftover pixels into a margin on all four sides:
  //
  //   ┌─ canvas = iw*cellW × ih*cellH ─────────┐
  //   │  ox                                    │   ox = boxW*(1-s)/2 ≥ PAD_X
  //   │ ┌─ grid, uniformly scaled by s ──────┐ │   oy = boxH*(1-s)/2 ≥ PAD_Y
  //   │ │ $ text no longer touches the edge  │ │
  //   │ └────────────────────────────────────┘ │
  //   └────────────────────────────────────────┘
  //
  // The scale is uniform so glyph aspect ratio — and, critically, the way
  // box-drawing characters butt against their neighbours — is preserved; cells
  // stay contiguous because every coordinate scales together. s lands near 0.98
  // for ordinary panes (a sub-pixel type-size change) and is floored so a very
  // small pane loses padding rather than legibility. Drawing happens at full
  // device resolution, so nothing is resampled: only the glyph em size changes.
  const PAD_X = 6, PAD_Y = 4; // px of breathing room per side, best effort
  const MIN_INSET_SCALE = 0.92;
  function setInset(p, boxW, boxH) {
    const s = Math.max(MIN_INSET_SCALE,
      Math.min(1, boxW > 0 ? 1 - (2 * PAD_X) / boxW : 1, boxH > 0 ? 1 - (2 * PAD_Y) / boxH : 1));
    p.gs = s;
    p.ox = (boxW * (1 - s)) / 2;
    p.oy = (boxH * (1 - s)) / 2;
  }

  // Canvas CSS px → grid user-space px (the coordinate system draw() renders
  // in). Every hit test goes through these so the inset stays invisible to the
  // rest of the code.
  function userX(p, clientX) { return (clientX - p.canvas.getBoundingClientRect().left - (p.ox || 0)) / (p.gs || 1); }
  function userY(p, clientY) { return (clientY - p.canvas.getBoundingClientRect().top - (p.oy || 0)) / (p.gs || 1); }

