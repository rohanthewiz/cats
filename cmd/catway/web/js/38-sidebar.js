  // ---- Sidebar splitter ----
  // The width is a per-browser display preference like the terminal font size,
  // so it lives in localStorage rather than in the server-persisted session.
  // Setting --sidebar-w on the root overrides the stylesheet's scale-derived
  // default, and everything that has to clear the sidebar (the banner) follows
  // for free because it reads the same variable.
  const SBW_KEY = "cats.sidebar_w", SBW_MIN = 150;
  const sbwMax = () => Math.max(SBW_MIN, Math.round(window.innerWidth * 0.6));
  function setSidebarWidth(px) {
    px = Math.min(sbwMax(), Math.max(SBW_MIN, Math.round(px)));
    document.documentElement.style.setProperty("--sidebar-w", px + "px");
    try { localStorage.setItem(SBW_KEY, String(px)); } catch (e) { /* not persisted */ }
  }
  try {
    const storedW = parseInt(localStorage.getItem(SBW_KEY), 10);
    if (storedW >= SBW_MIN) document.documentElement.style.setProperty("--sidebar-w", storedW + "px");
  } catch (e) { /* storage disabled — keep the stylesheet default */ }

  // Panes are laid out from cols/rows, so a drag has to re-derive the grid and
  // tell the server. That is debounced while the pointer moves (the same 120ms
  // the window resize uses) and flushed once on release.
  // refitLayout runs on the same beat as gridSize, not only on the flush: the
  // panes have to follow the gutter live while it is being dragged, exactly as
  // they follow the fold. It reads the layout the server last sent, so it is
  // cheap enough to run per debounce tick.
  let sbwTimer = null;
  function sidebarResized(now) {
    clearTimeout(sbwTimer);
    const fire = () => { gridSize(); refitLayout(); sendMsg({ t: "resize", cols, rows }); };
    if (now) fire(); else sbwTimer = setTimeout(fire, 120);
  }
  // ---- Folding the sidebar away ----
  // Hidden/shown is a per-browser display preference like the width above and
  // the chat panel below, so it lives in localStorage, not in the
  // server-persisted session. The stored *width* is deliberately left alone
  // while folded: revealing has to land back on the column the user last sized,
  // not on the stylesheet default.
  const SBH_KEY = "cats.sidebar_hidden";
  // Dragging the gutter this far left of the column's 150px floor folds it
  // outright — the fold happens live, under the pointer, and dragging back past
  // the same mark brings the column straight back. Done as a preview rather than
  // as a decision taken on release, the gesture explains itself: the column you
  // dragged into nothing is the column that stays gone.
  const SBW_FOLD = Math.round(SBW_MIN * 0.55);
  function sidebarHidden() { return document.body.classList.contains("sb-hidden"); }
  function setSidebarHidden(hide) {
    if (hide === sidebarHidden()) return;
    document.body.classList.toggle("sb-hidden", hide);
    try {
      if (hide) localStorage.setItem(SBH_KEY, "1"); else localStorage.removeItem(SBH_KEY);
    } catch (e) { /* not persisted */ }
    if (hide) paintSplitterAttention();
    else splitterEl.classList.remove("attn-blocked", "attn-done");
    syncSplitterTitle();
    // Folding changes #main's width exactly as a drag does, so the pane grid has
    // to be re-derived and the server told. At once, not debounced: this is one
    // discrete step, not a stream of pointer moves.
    sidebarResized(true);
  }
  function syncSplitterTitle() {
    splitterEl.title = sidebarHidden()
      ? "click to show the sidebar (⌘B / Ctrl+Alt+B)"
      : "drag to resize the sidebar (double-click to reset, ⌘B to hide)";
  }
  // The strongest attention state among the agents, painted onto the folded
  // gutter. Only the top two tiers of attentionRank earn a mark: "working" is
  // the ordinary case and would leave the gutter permanently lit, which would
  // make the two states that are actually addressed to the user invisible again.
  function paintSplitterAttention() {
    let best = "";
    for (const it of agentItems) {
      const st = markerState(it);
      if (st === "blocked") { best = "blocked"; break; }
      if (st === "done") best = "done";
    }
    splitterEl.classList.toggle("attn-blocked", best === "blocked");
    splitterEl.classList.toggle("attn-done", best === "done");
  }
  try { if (localStorage.getItem(SBH_KEY)) document.body.classList.add("sb-hidden"); } catch (e) { /* shown */ }
  syncSplitterTitle();

  // The fold button in the brand row. One direction only — it lives *in* the
  // column, so it is gone the moment it has acted, and the reveal handle in the
  // gutter is what comes back. mousedown is swallowed so the press cannot also
  // read as the start of a gesture on anything behind it, the same guard the
  // pane-chrome buttons use (mkBtn).
  const sbFoldEl = document.getElementById("sb-fold");
  sbFoldEl.addEventListener("mousedown", (e) => e.stopPropagation());
  sbFoldEl.addEventListener("click", (e) => {
    e.stopPropagation(); e.preventDefault();
    setSidebarHidden(true);
  });

  // A click that only revealed the column must not have its second half land on
  // the width-reset below: the pair would read as one gesture that both restored
  // and re-defaulted the sidebar.
  let revealedAt = 0;
  splitterEl.addEventListener("pointerdown", (e) => {
    if (e.button !== 0) return;
    e.preventDefault();
    splitterEl.setPointerCapture(e.pointerId);
    splitterEl.classList.add("drag");
    document.body.classList.add("splitting");
    // Where in the gutter it was grabbed, so the splitter stays under the
    // cursor instead of jumping its own width on the first move.
    const grabDx = e.clientX - splitterEl.getBoundingClientRect().left;
    const startHidden = sidebarHidden(), startX = e.clientX;
    let moved = false;
    const move = (ev) => {
      if (Math.abs(ev.clientX - startX) > 3) moved = true;
      const want = ev.clientX - grabDx;
      if (want < SBW_FOLD) { setSidebarHidden(true); return; }
      setSidebarHidden(false);      // dragged back out of the fold zone
      setSidebarWidth(want);
      sidebarResized(false);
    };
    const up = () => {
      splitterEl.removeEventListener("pointermove", move);
      splitterEl.classList.remove("drag");
      document.body.classList.remove("splitting");
      // A press on the folded gutter that never became a drag is a click, and
      // the only thing a click there can mean is "show it again".
      if (startHidden && !moved) { setSidebarHidden(false); revealedAt = Date.now(); }
      sidebarResized(true);
    };
    splitterEl.addEventListener("pointermove", move);
    splitterEl.addEventListener("pointerup", up, { once: true });
    splitterEl.addEventListener("pointercancel", up, { once: true });
  });
  // Double-click hands the column back to the stylesheet's --sb-scale default.
  splitterEl.addEventListener("dblclick", () => {
    if (Date.now() - revealedAt < 600) return;   // this was the reveal click, twice
    document.documentElement.style.removeProperty("--sidebar-w");
    try { localStorage.removeItem(SBW_KEY); } catch (e) { /* nothing to clear */ }
    sidebarResized(true);
  });

