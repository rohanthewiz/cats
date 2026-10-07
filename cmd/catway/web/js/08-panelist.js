
  // Each workspace row in the sidebar carries a dropdown of its own panes: a
  // caret at the row's far right folds the list open beneath it. This file is
  // that dropdown — the rows, the caret that opens them, the open/shut state,
  // and the inventory they are drawn from. renderWorkspaces (07-workspaces.js)
  // decides where they go; nothing here touches the DOM outside what it builds.
  //
  //   WORKSPACES                  ⊞ ⊟ ▼
  //     ● cats  ⚑       ●1 ●2   ▼        the caret, hard against the right edge
  //         p1  vim                       its panes, indented under the name
  //         p2  build  claude
  //     ○ api           ●1      ▶        shut: the row alone
  //     ○ old ☾                            asleep: nothing to list, no caret
  //     + workspace
  //
  // Why the panes moved into the workspace rows instead of keeping a section of
  // their own: the Panes section was already grouped by workspace, under header
  // rows that repeated every name the Workspaces section directly above it had
  // just listed. Two lists keyed the same way meant reading the session twice
  // (once for the workspace, once more for its panes) and keeping two sets of
  // folds, shelves and scroll caps in step. Hanging the pane rows off the row
  // they belong to says the same thing once, in the session's own order.
  //
  // What that order gives up is the Panes section's attention sort — the current
  // workspace pinned first, idle ones folded behind a "more panes…" shelf. The
  // first half survives as the default open state (wsPanesAreOpen): the
  // workspace you are in is the one whose dropdown starts open. The second has
  // nothing left to do — the workspace rows already carry each one's agent
  // states (the ●N badge), so an idle workspace is one you can see is idle
  // without opening it, and a shut dropdown costs it no rows at all.

  // wsPaneRows merges the session's pane inventory into display rows and
  // buckets them by workspace id, in inventory order. Session.ListPanes walks
  // workspace, then tab, then pane, so each bucket reads tab by tab — the same
  // order the hover card's itemized lists and the todo paw's jump use.
  //
  // The inventory is the cached pane.list snapshot rather than the layout
  // message, which only ever carries the active tab's panes: a workspace's
  // dropdown has to list its other tabs too, and other workspaces' dropdowns
  // have nothing in the layout at all.
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
  // Every awake workspace's rows are built on every render, open or shut: the
  // caret's tooltip counts them, and the merge is a pass over a list already in
  // memory. Only the DOM is skipped for a shut dropdown (renderWorkspaces).
  function wsPaneRows() {
    const vis = new Map((layoutMsg ? layoutMsg.panes : []).map((pr) => [pr.pane, pr]));
    // Until the first snapshot lands (page load) the layout's own panes stand in,
    // so the current workspace's dropdown is never briefly empty.
    const inv = paneInv.length ? paneInv
      : Array.from(vis.values()).map((pr) => ({ pane: pr.pane, handle: pr.pub, focused: pr.focused }));
    const byPane = new Map(agentItems.map((a) => [a.pane, a]));
    const out = new Map();
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

      const wsID = wsOf(row.pub);
      // A sleeping workspace's one pane is a placeholder with no terminal —
      // the shell it will get on wake. Listing it would offer a row that
      // cannot be typed into; the workspace row itself is how a sleeping
      // workspace is reached (a click wakes it), so it gets no dropdown.
      if (wsAsleep(wsID)) continue;
      let rows = out.get(wsID);
      if (!rows) out.set(wsID, rows = []);
      rows.push(row);
    }
    return out;
  }

  // Count with its noun, singular or plural — "1 agent", "3 panes". Shared by the
  // caret's tooltip and the workspace rows' own wording so the two read as one
  // register.
  function nOf(c, w) { return c + " " + w + (c === 1 ? "" : "s"); }

  // wsPanesAreOpen: is this workspace's pane dropdown open?
  //
  // An explicit choice wins: a caret press records true or false for that
  // workspace (wsPanesOpen, persisted), and it sticks across renders, switches
  // and reloads. A workspace nobody has toggled follows the focus instead —
  // open while it is the workspace this window is showing, shut otherwise.
  //
  // That default is the half of the old Panes section's ordering worth keeping
  // (see the note at the top of this file). Its panes are the ones on screen and
  // the likeliest target of a click in the sidebar, so an untouched session
  // opens on exactly one dropdown, the one you are in, and switching workspace
  // carries it along. Every other workspace is one press away, which is the
  // same distance its group used to be behind a folded header.
  function wsPanesAreOpen(w) {
    const v = wsPanesOpen[w.id];
    return v === undefined ? !!w.active : v;
  }

  // togglePanes flips one workspace's dropdown, or, with all set (Alt held on
  // the press), sets every workspace's to the state this one is flipping to.
  // Alt is the modifier macOS outline views already give a disclosure triangle
  // for "more than just this one" (there it opens recursively; a flat list has
  // no depth, so here it reaches across instead). It stands in for the
  // expand-all/collapse-all pair the Panes heading used to carry: the
  // Workspaces heading's ⊞/⊟ already mean "fold the shelves", and a second pair
  // beside them would be the same two glyphs with a different scope.
  //
  // Writing every id explicitly, rather than clearing the map back to the
  // default, is what makes Alt-collapse stick: with the map cleared, the current
  // workspace would fall straight back to "open because it is current".
  function togglePanes(w, all) {
    const open = !wsPanesAreOpen(w);
    if (all && layoutMsg) for (const x of layoutMsg.workspaces) wsPanesOpen[x.id] = open;
    else wsPanesOpen[w.id] = open;
    saveWsPanesOpen();
    if (layoutMsg) renderWorkspaces(layoutMsg);
  }

  // prunePanesOpen drops remembered choices for workspaces the session no longer
  // has. Without it the map only ever grows, and a choice made for a closed
  // workspace would be inherited by whichever new one is later handed the same
  // id. Saved only when something was actually dropped, since this runs on every
  // workspace render and most renders drop nothing.
  function prunePanesOpen(workspaces) {
    const live = new Set(workspaces.map((w) => w.id));
    let dropped = false;
    for (const id of Object.keys(wsPanesOpen)) {
      if (!live.has(id)) { delete wsPanesOpen[id]; dropped = true; }
    }
    if (dropped) saveWsPanesOpen();
  }

  // paneCaretEl builds the dropdown arrow at a workspace row's far right.
  //
  // Its press must not also reach the row. The row's own mousedown arms the
  // switch-or-reorder gesture (beginReorderDrag), and a caret that opened the
  // list AND switched workspace would be two answers to one press. The todo paw
  // has the same arrangement for the same reason, and, like the paw, takes
  // hideTip with it, since stopping the press also stops the row's own. The
  // default is prevented as well, which is what beginReorderDrag would have done
  // for the row: a press on a glyph is not the start of a text selection. A
  // double press would reach the row's rename the same way, so dblclick stops
  // here too. Right-click is left alone: the row's menu is still the menu for
  // the row the caret sits in.
  //
  // The tooltip carries the counts the old Panes group header showed in full
  // ("2 agents / 4 panes"). The row has no room left for them at sidebar widths
  // down to 150px, and the arrow is exactly where someone who wants to know
  // what is behind it is already pointing.
  function paneCaretEl(w, rows, open) {
    const car = document.createElement("span");
    car.className = "car";
    car.textContent = open ? "▼" : "▶";
    const agents = rows.filter((r) => r.agent).length;
    car.title = (open ? "hide " : "show ") + nOf(rows.length, "pane")
      + (agents ? " (" + agents + " running an agent)" : "")
      + " · Alt+click: every workspace";
    car.setAttribute("role", "button");
    car.setAttribute("aria-expanded", open ? "true" : "false");
    car.addEventListener("mousedown", (e) => {
      if (e.button !== 0) return; // right-click still reaches the row's menu
      e.stopPropagation();
      e.preventDefault();
      hideTip();
    });
    car.addEventListener("dblclick", (e) => e.stopPropagation());
    // On the press, like every other fold in the sidebar: the row is rebuilt on
    // every rollup and title push, and a press that a rebuild interrupts never
    // becomes a click. The mouseup event pressActivate hands back is where Alt
    // is read, so the modifier counts if it is held at release.
    pressActivate(car, (e) => togglePanes(w, !!(e && e.altKey)));
    return car;
  }

  // paneLocalRef is a pane's handle with the workspace taken off — "p3" for
  // "w1:p3". A dropdown row sits directly under the row naming its workspace,
  // so the "cats:" every other pane reference carries (paneRef) would only say
  // again what the row above has just said, once per pane. It also cost the
  // most where room is scarcest: a long workspace name ran the handle into
  // the title, and at narrow widths clipped the pane number itself — the one
  // part of the handle that tells the rows apart.
  //
  // Only the row's label drops it. The hover card still names the pane in
  // full (showPaneTip): it floats free of the list, and it is where you read
  // a handle off to type into catctl, which wants the whole thing.
  //
  // No handle yet (a pane the layout has not named) falls back to paneRef's
  // own "#id" stand-in, so the two can't disagree about an unnamed pane.
  function paneLocalRef(pub, paneID) {
    if (!pub) return paneRef(pub, paneID);
    return pub.slice(pub.indexOf(":") + 1);
  }

  function paneRowEl(row) {
    const li = document.createElement("li");
    li.className = "pn" + (row.visible && row.focused ? " focused" : "") + (row.visible ? "" : " off");
    // No focus marker glyph: li.focused's background says it, and a per-row
    // gutter for a mark only one row ever carries indents the whole list.
    const pub = document.createElement("span"); pub.className = "pub"; pub.textContent = paneLocalRef(row.pub, row.pane);
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
    // Both through armTip, so the card waits out a short dwell before it opens
    // and a pass down the list on the way somewhere else pops nothing up; once
    // it is up, moves within the row go straight through and it rides along.
    const tip = (e) => armTip(e, (ev) => showPaneTip(ev, row));
    li.addEventListener("mouseenter", tip);
    li.addEventListener("mousemove", tip);
    li.addEventListener("mouseleave", hideTip);
    // And down on the press, as the WORKSPACES rows have always done: the press
    // either focuses the pane or begins a swap drag, and a card riding the
    // pointer through that drag covers the pane rects it is being dragged onto.
    // This row was the one that lacked it, so a click here — the ordinary way
    // to reach a pane from the sidebar — left a card standing over whatever the
    // click brought up, with the pointer never moving again to take it down.
    li.addEventListener("mousedown", hideTip);
    return li;
  }

  // refreshPaneList redraws the pane dropdowns now from what the browser already
  // knows, then re-queries pane.list for the parts only the server has
  // (off-screen titles, panes that appeared or closed elsewhere). Every caller is
  // a push that could have changed the inventory; the query is debounced and
  // single-flight, so a burst of them costs one round trip plus at most one
  // follow-up.
  //
  // The name outlived the Panes section it was written for; every push handler
  // in 19-messages calls it, and what it means to them — "the inventory may have
  // moved" — has not changed.
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

  // What the pane inventory feeds, all of it now drawn by one renderer: the pane
  // dropdowns under each workspace row, and the todo marks on those rows.
  //
  // Coalesced to one frame, because the callers arrive in bursts. Switching tab
  // lands a layout, an agents rollup, and then pane_title/pane_agent/pane_exited
  // for every pane that just came into view — each of which asks for a redraw.
  // The list is a full wipe-and-rebuild over the whole session's inventory, so
  // rebuilding per message made a switch cost O(panes in tab × panes in session)
  // to paint the last one anyway. The 120ms query debounce below never covered
  // this; it guards only the round trip.
  //
  // The same frame also absorbs pushes that only move the workspace rows (the
  // client census, git sync, the agents rollup, the host roster): those used to
  // rebuild the section synchronously on arrival, so an agent state change —
  // which lands an agents rollup AND the pane_agent that goes with it — rebuilt
  // the list three times in one burst.
  //
  // Two entry points are kept although they now owe the same redraw: the
  // callers name what changed (the inventory, or only the workspace rows), and
  // that distinction is free to keep and expensive to rediscover should the two
  // views ever be drawn separately again.
  let invFrame = 0;
  function renderInventoryViews() {
    scheduleInventoryFrame();
  }
  // renderWorkspacesSoon is renderWorkspaces for pushes: coalesced into the
  // inventory frame instead of run on the spot. User-driven redraws (a fold
  // toggle, a heading control) still call renderWorkspaces directly — the
  // click should answer in the same frame, and it does not arrive in bursts.
  function renderWorkspacesSoon() {
    scheduleInventoryFrame();
  }
  function scheduleInventoryFrame() {
    if (invFrame) return;
    invFrame = requestAnimationFrame(() => { invFrame = 0; renderInventoryViewsNow(); });
  }
  // Before the first layout there is no workspace list to hang anything off, and
  // nothing is lost by waiting: the layout that brings the list re-queries the
  // inventory itself (applyLayout → refreshPaneList), which lands back here.
  function renderInventoryViewsNow() {
    if (layoutMsg) renderWorkspaces(layoutMsg);
  }

