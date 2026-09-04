
  // The Panes section is the session's whole pane inventory — every workspace and
  // tab, matching the global Agents rollup below it — so its rows come from the
  // cached pane.list snapshot rather than from the layout message, which only ever
  // carries the active tab's panes.
  //
  // Per row, two sources are merged. Viewport state (visible / focused) comes from
  // the layout: it is pushed, so a focus move lands without waiting on a query,
  // and it is what actually decides what is on screen (a zoomed tab hides its
  // other panes). Title/cwd/agent come from local pane state for on-screen panes,
  // where live pushes keep them fresher than any snapshot, and from the snapshot
  // for the rest — pane_title/pane_cwd are broadcast for visible panes only, so
  // off-screen titles exist nowhere else in the browser. The agents rollup wins on
  // agent state when it knows the pane: it carries the seen flag, which is what
  // renders a run that finished off-screen as "done".
  //
  // Rows are gathered per workspace before any DOM is built: the inventory already
  // arrives grouped by workspace then tab (Session.ListPanes), so a run of rows
  // sharing a handle prefix is exactly one group. Each group gets a header row that
  // folds it — a collapsed group's rows are never built, and its header carries the
  // rollup they would have shown.
  function renderPaneList() {
    paneListEl.innerHTML = "";
    const vis = new Map((layoutMsg ? layoutMsg.panes : []).map((pr) => [pr.pane, pr]));
    // Until the first snapshot lands (page load) the layout's own panes stand in,
    // so the section is never briefly blank.
    const inv = paneInv.length ? paneInv
      : Array.from(vis.values()).map((pr) => ({ pane: pr.pane, handle: pr.pub, focused: pr.focused }));
    const byPane = new Map(agentItems.map((a) => [a.pane, a]));
    const groups = [];
    for (const pi of inv) {
      const pr = vis.get(pi.pane), p = panes.get(pi.pane);
      const live = !!(pr && p); // on screen: prefer local state over the snapshot
      const row = {
        pane: pi.pane, pub: pi.handle || (pr && pr.pub) || "",
        visible: !!pr, focused: pr ? !!pr.focused : !!pi.focused,
      };
      if (live) {
        row.title = p.title; row.cwd = p.cwd;
        row.agent = p.agent; row.state = p.agentState; row.model = p.agentModel;
      } else {
        // A custom name overrides the terminal title, as effectiveTitle does
        // server-side for the panes whose titles are pushed.
        row.title = pi.name || pi.title || ""; row.cwd = pi.cwd || "";
        row.agent = pi.agent || ""; row.state = pi.agent_state || ""; row.model = pi.agent_model || "";
      }
      const a = byPane.get(pi.pane);
      if (a) { row.agent = a.agent; row.state = markerState(a); }
      // The flag is session state, so both sources carry it and either will do.
      // The layout wins where it has an answer: it is pushed the moment the flag
      // changes, while the snapshot arrives on the pane.list that push triggers —
      // a round trip later.
      row.flag = flagOf(pr) || flagOf(pi);

      const wsID = row.pub.split(":")[0];
      if (!groups.length || groups[groups.length - 1].ws !== wsID) groups.push({ ws: wsID, rows: [] });
      groups[groups.length - 1].rows.push(row);
    }

    // Two shelves, by whether the workspace is doing anything: the ones with work
    // in them stay up top, and the rest fold behind a single "more workspaces…"
    // row at the foot of the section. The inventory arrives in the session's
    // workspace order, which is the order the *Workspaces* section wants — a list
    // you reorder by hand and then read positionally — but Panes is read by
    // attention, and a session that has collected a dozen workspaces buries the
    // two you are actually running behind ten headers you never open.
    //
    // Active means "at least one pane here has a detected agent", in any state.
    // Not "an agent that wants something" (blocked/done/working): a workspace
    // would then drop through the floor the moment its agent went idle, which is
    // exactly when you are about to type into it. The test is deliberately about
    // the workspace as a whole rather than the pane — one row per workspace moves,
    // so the group you were reading does not reshuffle its own rows underneath
    // you as states change.
    //
    // The current workspace is pinned first whatever it holds. Its panes are the
    // ones on screen, so the group naming them is not something the user should
    // have to go looking for, and a shell-only workspace you are sitting in is
    // still the workspace you are sitting in.
    const aw = activeWorkspace();
    const curWS = aw ? aw.id : "";
    const hot = [], cold = [];
    for (const g of groups) ((g.ws === curWS || g.rows.some((r) => r.agent)) ? hot : cold).push(g);
    // Stable sort (ES2019+), so this pins the current workspace to the head and
    // leaves every other pair reading 0 — i.e. in the session's own order.
    hot.sort((a, b) => (b.ws === curWS) - (a.ws === curWS));

    // Collapse-all/expand-all act on every group, folded shelf or not: the pair
    // means "the whole section", and a set that quietly skipped the shelf would
    // leave groups half-folded the next time it was opened.
    paneGroupIDs = hot.concat(cold).map((g) => g.ws);
    const emit = (g, sep) => {
      paneListEl.appendChild(paneGroupEl(g, sep, g.ws === curWS));
      if (!paneCollapsed.has(g.ws)) for (const row of g.rows) paneListEl.appendChild(paneRowEl(row));
    };
    hot.forEach((g, i) => emit(g, i > 0));
    if (cold.length) {
      // The shelf row draws the hairline that separates it from the live
      // workspaces above, so the first group it reveals must not draw a second
      // one directly beneath it.
      paneListEl.appendChild(paneMoreEl(cold, hot.length > 0));
      if (paneMoreOpen) cold.forEach((g, i) => emit(g, i > 0));
    }
  }

  // Count with its noun, singular or plural — "1 agent", "3 panes". Shared by the
  // group headers and the shelf row below them so the two read as one register.
  function nOf(c, w) { return c + " " + w + (c === 1 ? "" : "s"); }

  // paneGroupEl builds one workspace's header row: how many of the group's panes
  // are running an agent, out of how many panes. The rollup rides the header in
  // both states — collapsed it stands in for the rows it hides, and expanded it
  // saves counting them by eye, since the agent count is a property of the group
  // that no single row reports.
  //
  // cur marks the workspace whose panes are the ones on screen. It earns a mark
  // because the section is no longer in session order: the current workspace is
  // pinned to the top (renderPaneList) and can sit above workspaces that are
  // busier than it is, so the first row has to say why it is first. Same ●/accent
  // the Workspaces section marks the same workspace with, since it is the same
  // fact being reported twice.
  function paneGroupEl(g, sep, cur) {
    const collapsed = paneCollapsed.has(g.ws);
    const li = document.createElement("li");
    li.className = "wsgrp" + (sep ? " sep" : "") + (cur ? " cur" : "");
    const name = document.createElement("span");
    name.textContent = (cur ? "● " : "") + (wsName(g.ws) || "—");
    li.appendChild(name);
    const agents = g.rows.filter((r) => r.agent).length;
    const s = document.createElement("span");
    s.className = "gsum";
    s.textContent = nOf(agents, "agent") + " / " + nOf(g.rows.length, "pane");
    li.appendChild(s);
    const car = document.createElement("span");
    car.className = "car"; car.textContent = collapsed ? "▶" : "▼";
    li.appendChild(car);
    li.title = (collapsed ? "expand " : "collapse ") + (wsName(g.ws) || "workspace");
    // On the press, like the rows it folds: the header is rebuilt on the same
    // pushes they are, and a fold that has to be clicked twice because a rollup
    // landed mid-press reads as a stuck group.
    pressActivate(li, () => {
      if (collapsed) paneCollapsed.delete(g.ws); else paneCollapsed.add(g.ws);
      savePaneCollapsed();
      renderPaneList();
    });
    return li;
  }

  // paneMoreEl builds the one row the idle workspaces fold into: a shelf, not a
  // group. It is built as a .wsgrp because it does a group header's job at the
  // tier above — it names what is behind it, counts it, and folds it with the
  // same caret — and it is set in italics so a list of workspace names is never
  // mistaken for holding one called "more workspaces".
  //
  //   PANES              ⊞ ⊟ ▼
  //     ● cats   2 agents / 4 panes     the current workspace, pinned
  //       cats:p2  build   claude
  //     api      1 agent / 3 panes      still working
  //   ─────────────────────────────
  //     more workspaces…  3 workspaces / 7 panes  ▶
  //
  // The tally is the bare number of workspaces behind the row. Not agents: every
  // workspace here is here precisely because it has none, so that column would be
  // zeroes. Not "2 workspaces / 3 panes" either — the noun is already in the
  // label this rides, and spelled out twice the row outgrows a sidebar that goes
  // down to 150px wide, which is the same trade the Workspaces heading makes with
  // its own folded count. The words are in the tooltip for anyone the digit
  // doesn't reach. It rides both states for the reason the group rollups do —
  // shut, it stands in for what is hidden; open, it saves counting headers.
  function paneMoreEl(cold, sep) {
    const li = document.createElement("li");
    li.className = "wsgrp more" + (sep ? " sep" : "");
    const name = document.createElement("span");
    name.textContent = "more workspaces…";
    li.appendChild(name);
    const panesN = cold.reduce((t, g) => t + g.rows.length, 0);
    const s = document.createElement("span");
    s.className = "gsum";
    s.textContent = String(cold.length);
    li.appendChild(s);
    const car = document.createElement("span");
    car.className = "car"; car.textContent = paneMoreOpen ? "▼" : "▶";
    li.appendChild(car);
    li.title = (paneMoreOpen ? "hide " : "show ") + nOf(cold.length, "workspace")
      + " with no agent running (" + nOf(panesN, "pane") + ")";
    // On the press, for the same reason every other fold in this list is: the row
    // is rebuilt on every rollup and title push, and a press that a rebuild
    // interrupts never becomes a click.
    pressActivate(li, () => {
      paneMoreOpen = !paneMoreOpen;
      savePaneMoreOpen();
      renderPaneList();
    });
    return li;
  }

  function paneRowEl(row) {
    const li = document.createElement("li");
    li.className = "pn" + (row.visible && row.focused ? " focused" : "") + (row.visible ? "" : " off");
    // No focus marker glyph: li.focused's background says it, and a per-row
    // gutter for a mark only one row ever carries indents the whole list.
    const pub = document.createElement("span"); pub.className = "pub"; pub.textContent = paneRef(row.pub, row.pane);
    li.appendChild(pub);
    // The flag sits right after the handle, ahead of the title: it is the mark
    // the eye is scanning this list for, and the title is the part that gets
    // truncated when the column is narrow.
    const pf = flagMark(row.flag);
    if (pf) li.appendChild(pf);
    if (row.title) { const t = document.createElement("span"); t.className = "ttl"; t.textContent = row.title; li.appendChild(t); }
    if (row.agent) {
      const ag = document.createElement("span"); ag.className = "ag " + stClass(row.state); ag.textContent = agentLabel(row.agent, row.model);
      li.appendChild(ag);
    }
    // A row can name a pane in another workspace or tab, which pane.focus cannot
    // reach (it only moves the focus flag inside the current viewport), so those
    // are revealed the way the agents list reveals its own: agent.focus.
    //
    // Activated on the press rather than on a click, since this list is rebuilt
    // on every rollup and pane_title/pane_agent push (pressActivate). The swap
    // drag below needs no suppressing here: it only begins once the pointer has
    // travelled DRAG_SLOP, which is the same distance that stops this from
    // firing, so a press is a reveal or a drag and never both.
    pressActivate(li, () => {
      if (!row.visible) sendCmd("agent.focus", { pane: row.pane });
      else if (!row.focused) sendCmd("pane.focus", { pane: row.pane });
    });
    // The pane header's affordances, mirrored: double-click renames,
    // right-click opens the pane menu, and dragging a row onto a pane on
    // screen swaps the two (beginPaneSwapDrag hit-tests the live pane
    // rects, so a sidebar origin works the same as a header origin). Swapping
    // slots is an active-tab operation (Session.SwapPanes), so only on-screen
    // rows arm the drag — an off-screen row has no slot to trade.
    li.addEventListener("dblclick", () => renamePane(row.pane));
    li.addEventListener("contextmenu", (e) => { e.preventDefault(); openCtx(e.clientX, e.clientY, paneMenuItems(row.pane)); });
    if (row.visible) li.addEventListener("mousedown", (e) => beginPaneSwapDrag(e, row.pane));
    // Reveal the untruncated pane details on hover. mousemove keeps the popup
    // riding just right of the pointer; mouseleave tears it down. showPaneTip
    // re-reads live pane state by id, so a re-render between enter and move
    // still shows the current title/agent/state.
    //
    // While the card is up it owns the row's tooltips too: showPaneTip strips
    // the title attributes off the row's marks (muteTitles) and hideTip puts
    // them back, so the flag's note is said once — in the card, in full —
    // rather than again a second later in a native tooltip over it.
    li.addEventListener("mouseenter", (e) => showPaneTip(e, row));
    li.addEventListener("mousemove", (e) => showPaneTip(e, row));
    li.addEventListener("mouseleave", hideTip);
    return li;
  }

  // The Panes heading's own controls: fold or unfold every workspace group at
  // once. They act on the groups the last render drew, so a workspace that no
  // longer has panes doesn't linger in the collapsed set.
  //
  // The "more workspaces…" shelf moves with them. ⊞ means "show me everything in
  // this section", and a shelf still holding half the session's workspaces shut
  // would make that a lie; ⊟ means the reverse, and leaving the shelf hanging
  // open over a list of folded headers is the same lie the other way round.
  (function initPaneHeadingCtl() {
    const el = document.getElementById("pane-hctl");
    el.appendChild(mkBtn("⊞", "expand all workspaces", "", () => {
      paneCollapsed.clear(); savePaneCollapsed();
      paneMoreOpen = true; savePaneMoreOpen();
      renderPaneList();
    }));
    el.appendChild(mkBtn("⊟", "collapse all workspaces", "", () => {
      paneCollapsed = new Set(paneGroupIDs); savePaneCollapsed();
      paneMoreOpen = false; savePaneMoreOpen();
      renderPaneList();
    }));
    initSectionFold("sec-panes", "pane-hctl", "panes");
  })();

  // refreshPaneList redraws the section now from what the browser already knows,
  // then re-queries pane.list for the parts only the server has (off-screen
  // titles, panes that appeared or closed elsewhere). Every caller is a push that
  // could have changed the inventory; the query is debounced and single-flight, so
  // a burst of them costs one round trip plus at most one follow-up.
  //
  // The workspace rows ride along: their todo marks read the same inventory, so
  // a manager opening or closing has to reach both sections at once.
  function refreshPaneList() {
    renderInventoryViews();
    if (paneInvBusy) { paneInvAgain = true; return; }
    if (paneInvWait) return;
    paneInvWait = setTimeout(() => {
      paneInvWait = null; paneInvBusy = true;
      sendCmdAwait("pane.list", {}, (res) => {
        paneInvBusy = false;
        if (res.ok && res.data) { paneInv = res.data.panes || []; renderInventoryViews(); }
        if (paneInvAgain) { paneInvAgain = false; refreshPaneList(); }
      });
    }, 120);
  }

  // The two views the pane inventory feeds: the Panes section, and the todo marks
  // on the Workspaces rows above it.
  //
  // Coalesced to one frame, because the callers arrive in bursts. Switching tab
  // lands a layout, an agents rollup, and then pane_title/pane_agent/pane_exited
  // for every pane that just came into view — each of which asks for these views.
  // Both are full wipe-and-rebuilds over the whole session's inventory, so
  // rebuilding per message made a switch cost O(panes in tab × panes in session)
  // to paint the last one anyway. The 120ms query debounce below never covered
  // this; it guards only the round trip.
  let invFrame = 0;
  function renderInventoryViews() {
    if (invFrame) return;
    invFrame = requestAnimationFrame(() => { invFrame = 0; renderInventoryViewsNow(); });
  }
  function renderInventoryViewsNow() {
    renderPaneList();
    if (layoutMsg) renderWorkspaces(layoutMsg);
  }

