  // ---- Sidebar activation: presses, not clicks -------------------------------
  //
  // Every list in this sidebar is rebuilt wholesale — renderWorkspaces,
  // renderTabbar, renderPaneList and renderAgents each wipe their container and
  // re-create every child — and all four run off the agents rollup, which the
  // server broadcasts on each agent state transition. A busy session therefore
  // repaints these lists several times a second, while a mouse press is held for
  // ~100ms.
  //
  // That is a lost click. The browser dispatches "click" on the nearest common
  // ancestor of the mousedown and mouseup targets, so when a rebuild lands
  // mid-press the old row takes the mousedown, its replacement takes the mouseup,
  // and the click surfaces on the <ul> — where nothing is listening. The row does
  // nothing and the press looks ignored, most often exactly when the session is
  // busy enough for the user to want to switch away from it.
  //
  //   mousedown ──► row A ──┐
  //                          ├── rebuild ──► click lands on <ul>  (dropped)
  //   mouseup   ──► row A' ─┘
  //
  // So activation hangs off the press itself: mousedown remembers where it began
  // and a window-level mouseup — registered on the window, closing over the row's
  // own identity — decides what it meant. Neither the listener nor the closure
  // cares whether the element still exists, so the rebuild is irrelevant.
  //
  // DRAG_SLOP is what separates the two readings of a press. Under it, the press
  // is an activation; at or over it, the rows that can be dragged read it as the
  // start of a drag instead. One number for both so the two can never both claim
  // the same press — which is what makes a drag's own release safe to ignore here
  // without any suppression flag.
  const DRAG_SLOP = 4;

  // pressActivate: fn runs when a press on el is released without travelling
  // DRAG_SLOP. For rows that also drag, pass onClick to the drag helper instead —
  // it already tracks the same threshold.
  //
  // The mouseup listener unregisters itself, so no press leaves one behind (a
  // window listener outlives the element that armed it — see the pane selection
  // note below for what that leak cost when it was permanent).
  function pressActivate(el, fn) {
    el.addEventListener("mousedown", (ev) => {
      if (ev.button !== 0) return; // right-click belongs to the context menu
      const sx = ev.clientX, sy = ev.clientY;
      const up = (e) => {
        window.removeEventListener("mouseup", up);
        if (Math.abs(e.clientX - sx) >= DRAG_SLOP || Math.abs(e.clientY - sy) >= DRAG_SLOP) return;
        fn(e);
      };
      window.addEventListener("mouseup", up);
    });
  }

  // ---- Drag-reorder (WS8): tabs (horizontal) + workspaces (vertical) ----
  //
  // Same shape as beginBorderDrag: mousedown arms a potential drag with
  // window-level move/up listeners, and only DRAG_SLOP of travel makes it one —
  // a plain press stays an activation (focus). While dragging, the pointer maps
  // to an insertion gap (0..=len, the tab.move / workspace.move convention)
  // shown as a 2px accent bar; drop sends the move and the server's layout
  // rebroadcast re-renders both lists (the server is the source of truth).
  //
  // A press that never became a drag calls cfg.onClick from the same
  // window-level mouseup, for the reason pressActivate exists: these rows are
  // rebuilt under the pointer, and a "click" listener on them loses any press a
  // rebuild interrupts. Rows that can drag take their activation here rather
  // than through pressActivate, so a press is read exactly once.
  //
  // This is also what retired dragConsumedClick, the flag that used to suppress
  // the click trailing a completed drag: with both readings measured against the
  // one threshold, a drag's release can no longer reach an activation to
  // suppress. Drags that end on a foreign target — beginPaneSwapDrag's drop onto
  // a pane — act on that target directly and never went through a click either.
  function beginReorderDrag(ev, cfg) {
    if (ev.button !== 0) return;
    // A press that may become a drag must not also start a text selection: the
    // browser would paint its selection highlight across the tab name (or the
    // workspace row) the whole way, on top of the .dragging + drop-bar cues
    // that are the actual feedback. Nothing in this chrome is prose worth
    // selecting, and the terminal draws its own selection on the canvas, so
    // the default gains nothing here. beginBorderDrag does the same.
    ev.preventDefault();
    const sx = ev.clientX, sy = ev.clientY;
    let dragging = false, bar = null, gap = -1;
    const items = () => Array.from(cfg.container.querySelectorAll(cfg.itemSel));
    const move = (e) => {
      if (!dragging) {
        if (Math.abs(e.clientX - sx) < DRAG_SLOP && Math.abs(e.clientY - sy) < DRAG_SLOP) return;
        dragging = true;
        cfg.el.classList.add("dragging");
        document.body.classList.add("grabbing");
        bar = document.createElement("div");
        bar.className = "dropbar " + (cfg.horizontal ? "h" : "v");
      }
      // A mid-drag re-render replaces the container's children (and drops the
      // bar); re-query and re-attach every move so the drag survives it.
      if (!bar.isConnected) cfg.container.appendChild(bar);
      const els = items();
      gap = els.length;
      for (let i = 0; i < els.length; i++) {
        const r = els[i].getBoundingClientRect();
        const mid = cfg.horizontal ? r.left + r.width / 2 : r.top + r.height / 2;
        if ((cfg.horizontal ? e.clientX : e.clientY) < mid) { gap = i; break; }
      }
      const cr = cfg.container.getBoundingClientRect();
      const edge = gap < els.length
        ? els[gap].getBoundingClientRect()[cfg.horizontal ? "left" : "top"]
        : (els.length ? els[els.length - 1].getBoundingClientRect()[cfg.horizontal ? "right" : "bottom"]
                      : (cfg.horizontal ? cr.left : cr.top));
      if (cfg.horizontal) bar.style.left = (edge - cr.left + cfg.container.scrollLeft - 1) + "px";
      else bar.style.top = (edge - cr.top + cfg.container.scrollTop - 1) + "px";
    };
    const up = () => {
      window.removeEventListener("mousemove", move);
      window.removeEventListener("mouseup", up);
      cfg.el.classList.remove("dragging");
      document.body.classList.remove("grabbing");
      if (bar) bar.remove();
      if (dragging) {
        if (gap >= 0) cfg.onDrop(gap);
      } else if (cfg.onClick) {
        // Under DRAG_SLOP this was a plain press, so it means what a click
        // means. Released anywhere: the pointer travelled at most DRAG_SLOP, so
        // it is still over the row it started on, whatever the DOM did in
        // between.
        cfg.onClick();
      }
    };
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
  }

  // ---- Pane drag-reorder: drag a sidebar pane row onto a pane → pane.swap_with.
  // Same shape as beginReorderDrag: mousedown arms a potential drag with a 4px
  // threshold, so a plain press stays a click (focus). While dragging, the
  // pane under the pointer highlights as the drop target; releasing anywhere
  // else cancels. Layout rebroadcasts mid-drag are safe — the hit test reads
  // live pane rects and the listeners live on window.
  function beginPaneSwapDrag(ev, srcId) {
    if (ev.button !== 0) return;
    ev.preventDefault(); // no text selection under the drag (see beginReorderDrag)
    const sx = ev.clientX, sy = ev.clientY;
    let dragging = false, target = null;
    const clearTarget = () => { if (target) { target.el.classList.remove("droptarget"); target = null; } };
    const paneAt = (x, y) => {
      for (const [id, p] of panes) {
        if (id === srcId || !p.info) continue;
        const r = p.el.getBoundingClientRect();
        if (x >= r.left && x < r.right && y >= r.top && y < r.bottom) return p;
      }
      return null;
    };
    const move = (e) => {
      if (!dragging) {
        if (Math.abs(e.clientX - sx) < DRAG_SLOP && Math.abs(e.clientY - sy) < DRAG_SLOP) return;
        dragging = true;
        const src = panes.get(srcId);
        if (src) src.el.classList.add("dragging");
        document.body.classList.add("grabbing");
      }
      const p = paneAt(e.clientX, e.clientY);
      if (p !== target) { clearTarget(); if (p) { target = p; p.el.classList.add("droptarget"); } }
    };
    const up = () => {
      window.removeEventListener("mousemove", move);
      window.removeEventListener("mouseup", up);
      const src = panes.get(srcId);
      if (src) src.el.classList.remove("dragging");
      document.body.classList.remove("grabbing");
      const tgt = target;
      clearTarget();
      if (dragging && tgt) {
        // No click to suppress: dragging means the pointer passed DRAG_SLOP,
        // which is exactly what stops the row's pressActivate from firing.
        sendCmd("pane.swap_with", { pane: srcId, target: tgt.id });
      }
    };
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
  }

