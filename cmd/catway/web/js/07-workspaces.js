  // ---- Sidebar (WS8) ----
  function stClass(state) {
    return state === "working" ? "st-working" : state === "blocked" ? "st-blocked"
      : state === "done" ? "st-done" : state === "idle" ? "st-idle" : "st-unknown";
  }

  // markerState folds a rollup item's seen flag into the display state:
  // idle+unseen renders as the "done" attention tier (cats's teal dot).
  function markerState(it) { return it.state === "idle" && !it.seen ? "done" : it.state; }

  // paneRef renders a server handle ("w1:p3") for display, swapping the opaque
  // workspace id for the workspace's name ("cats:p3"). Every pane reference the
  // user sees goes through here — the pane header, tooltip, dialog title, agents
  // rollup, palette, copy-mode bar and the sidebar PANES rows — so one form is
  // worth learning. Before the first layout arrives there is no name to resolve;
  // the id stands in, and applyLayout re-renders each header once it does.
  function paneRef(pub, paneID) {
    if (!pub) return "#" + paneID;
    const i = pub.indexOf(":");
    if (i < 0) return pub;
    return wsName(pub.slice(0, i)) + pub.slice(i);
  }

  // The workspace's display name, falling back to its id until a layout arrives.
  function wsName(id) {
    const w = layoutMsg && layoutMsg.workspaces.find((x) => x.id === id);
    return (w && w.name) || id;
  }

  // Is this workspace closed to plugins and agents? The lock lives on the layout,
  // while the agents rollup is its own message keyed by workspace id, so a row in
  // AGENTS has to look the flag up rather than carry it. Unknown workspace (no
  // layout yet) reads as unlocked — the layout that answers the question also
  // re-marks the rows, so the honest "don't know" costs nothing.
  function wsLocked(id) {
    const w = layoutMsg && layoutMsg.workspaces.find((x) => x.id === id);
    return !!(w && w.locked);
  }

  // attentionRank orders marker states for aggregation (cats's
  // workspace_attention_priority): blocked > done(unseen) > working > idle.
  function attentionRank(st) {
    return st === "blocked" ? 4 : st === "done" ? 3 : st === "working" ? 2 : st === "idle" ? 1 : 0;
  }

  // workspaceRollups buckets both of the lists a workspace row reads — the
  // agents rollup and the pane inventory — by workspace, in one pass each.
  // Asking per row instead cost O(workspaces × agents + workspaces × panes) on
  // every render, and these lists are already grouped by workspace.
  function workspaceRollups() {
    const states = new Map(), todos = new Map();
    let globalTodos = 0;
    for (const it of agentItems) {
      let c = states.get(it.workspace);
      if (!c) states.set(it.workspace, c = { blocked: 0, done: 0, working: 0, idle: 0 });
      const st = markerState(it);
      if (c[st] !== undefined) c[st]++;
    }
    for (const pi of paneInv) {
      if (!pi.handle) continue;
      const n = todoOpenCount(pi.title);
      if (!n) continue;
      // A manager counting the global backlog is counting the same list from
      // every workspace, so its count belongs to no workspace row. Crediting
      // it to the pane's host made the work migrate when the pane moved, and
      // two global managers in different workspaces wore the same items twice.
      // Max rather than sum for the same reason: every such pane advertises
      // one list, so two of them agreeing is not twice the work.
      if (isGlobalTodoTitle(pi.title)) {
        globalTodos = Math.max(globalTodos, n);
        continue;
      }
      const i = pi.handle.indexOf(":"); // "w1:p3" → "w1"
      const ws = i < 0 ? pi.handle : pi.handle.slice(0, i);
      todos.set(ws, (todos.get(ws) || 0) + n);
    }
    return { states, todos, globalTodos };
  }

  // workspaceSummary compresses a workspace's detected-agent states into a
  // compact badge ("●1 ●2" colored by state), computed client-side from the
  // agents rollup — the server's per-item workspace field keys the grouping.
  function workspaceSummary(counts) {
    if (!counts) return null;
    const frag = document.createDocumentFragment();
    for (const st of ["blocked", "done", "working", "idle"]) {
      if (!counts[st]) continue;
      const s = document.createElement("span");
      s.className = stClass(st);
      s.textContent = "●" + counts[st];
      s.title = counts[st] + " " + (st === "done" ? "done (unseen)" : st);
      frag.appendChild(s);
    }
    return frag.childNodes.length ? frag : null;
  }

  // todoOpenCount reads the unfinished-item count a cats-todo manager advertises
  // in its terminal title — "todo: cats (3)", "todo: global (1)", or bare "todo
  // (2)" (the plugin's model.paneTitle). A manager whose backlog is empty or all
  // done drops the suffix, so it answers 0 exactly as a pane that is not a
  // manager at all does; only the presence of work is being counted here.
  //
  // The title is the one channel every launch carries. The plugin host's
  // CATS_PLUGIN_ID reaches the process but not the pane record, a manager started
  // by hand from a shell has no plugin id to begin with, and neither would say
  // how much is left in the backlog — which is the whole question.
  function todoOpenCount(t) {
    const m = /^todo(?:: .+?)? \((\d+)\)$/.exec(t || "");
    return m ? Number(m[1]) : 0;
  }

  // isGlobalTodoTitle says whether a manager's advertised count is the global
  // backlog rather than a project's. "todo: global (N)" is a --global launch;
  // bare "todo (N)" is a launch that resolved no project, whose count falls
  // back to the global store (cats-todo's model.openCount) — the project name
  // in the title is the only thing that marks a count as project-scoped.
  function isGlobalTodoTitle(t) {
    return /^todo(?:: global)? \(/.test(t || "");
  }

  // todoMark: a cat's paw print, worn by a workspace whose cats-todo backlog
  // still has unfinished items in it — the cat has left its mark on work not
  // yet done — carrying the count of what is left as a superscript. A manager
  // sitting open on a cleared backlog earns nothing: a mark every workspace
  // wears all day is decoration.
  //
  // Counts come from the pane inventory rather than the agents rollup, which
  // never sees a todo pane (the manager is a TUI, not an agent), and from its raw
  // title rather than the pane's live one — the live title is the *effective*
  // title, so a pane.rename would otherwise blank the count.
  function todoMark(n) {
    if (!n) return null;
    const svg = document.createElementNS(SVGNS, "svg");
    svg.setAttribute("class", "todo");
    svg.setAttribute("viewBox", "0 0 16 16");
    // Filled rather than stroked (unlike most sidebar marks): the paw's toe
    // pads are ~3px across at render size, where a stroked outline collapses
    // into a smudge — solid shapes are what keep a paw print readable.
    svg.setAttribute("fill", "currentColor");
    // Four toe pads fanned over the main pad — inner pair high, outer pair
    // low and spread — which is the whole silhouette of a paw print; claws
    // or pad lobes would dissolve at 16px.
    for (const [cx, cy, r] of [[3.0, 6.8, 1.5], [6.2, 4.6, 1.6],
                               [9.8, 4.6, 1.6], [13.0, 6.8, 1.5]]) {
      const c = document.createElementNS(SVGNS, "circle");
      c.setAttribute("cx", cx);
      c.setAttribute("cy", cy);
      c.setAttribute("r", r);
      svg.appendChild(c);
    }
    // The main pad: an oval broadened at the base, so it reads as a pad
    // rather than a fifth toe.
    const pad = document.createElementNS(SVGNS, "path");
    pad.setAttribute("d", "M8 7.8c1.4 0 2.9.9 3.6 2.2.8 1.5.3 3.2-1.2 3.8" +
      "-.8.3-1.6.5-2.4.5s-1.6-.2-2.4-.5c-1.5-.6-2-2.3-1.2-3.8C5.1 8.7 6.6 7.8 8 7.8z");
    svg.appendChild(pad);
    const wrap = document.createElement("span");
    wrap.className = "todo-mark";
    wrap.title = n === 1 ? "1 unfinished todo" : n + " unfinished todos";
    wrap.appendChild(svg);
    const sup = document.createElement("sup");
    sup.className = "todo-n";
    sup.textContent = String(n);
    wrap.appendChild(sup);
    return wrap;
  }

  // ---- User flags (workspace.flag / pane.flag) ----
  //
  // A flag is a glyph with a meaning plus an optional note, pinned by the user
  // to a workspace or a pane so it can be found again tomorrow. It shows up in
  // four lists — WORKSPACES rows, AGENTS rows, PANES rows and the pane header —
  // all of which come through the two functions below, so the mark looks and
  // reads the same wherever it is drawn.
  //
  // FLAG_DEFS mirrors internal/flags. It is duplicated rather than fetched
  // because the browser needs the glyph and the label to *draw a menu* before
  // any flag exists, and a vocabulary round trip for six compile-time constants
  // would be a message for nothing. TestWebFlagVocabularyMatchesGo (web_test.go)
  // fails the build if the two lists ever drift.
  //
  // The kind field carries either one of these names or a literal glyph the
  // user typed, and both render through flagGlyph — a named kind resolves to
  // its glyph, and anything else already is one. That single path is the whole
  // reason the server keeps the two shapes in one string.
  const FLAG_DEFS = [
    { kind: "followup", glyph: "⚑", label: "follow-up", meaning: "come back to this" },
    { kind: "question", glyph: "?", label: "question", meaning: "waiting on an answer" },
    { kind: "star", glyph: "★", label: "important", meaning: "worth finding again" },
    { kind: "warn", glyph: "⚠", label: "problem", meaning: "something is wrong here" },
    { kind: "done", glyph: "✓", label: "done", meaning: "handled — nothing left to do" },
    { kind: "note", glyph: "✎", label: "note", meaning: "just a note" },
  ];
  const FLAG_BY_KIND = new Map(FLAG_DEFS.map((d) => [d.kind, d]));

  // flagOf normalizes the three flat wire fields into one object, from whichever
  // row carries them — a layout workspace, a layout pane, an agents rollup item
  // or a pane.list row all spell them the same way. null = unflagged.
  function flagOf(o) {
    if (!o || !o.flag) return null;
    return { kind: o.flag, note: o.flag_note || "", at: o.flag_at_ms || 0 };
  }
  function flagDef(f) { return f ? FLAG_BY_KIND.get(f.kind) || null : null; }
  function flagGlyph(f) { const d = flagDef(f); return d ? d.glyph : f.kind; }
  function flagLabel(f) { const d = flagDef(f); return d ? d.label : f.kind; }

  // flagTitle is the tooltip: what the mark means, then the note, then when it
  // was set. The note leads over the age because the note is the half the glyph
  // could not carry — the age is context for "is this still true?".
  function flagTitle(f) {
    let s = flagGlyph(f);
    const lbl = flagLabel(f);
    if (lbl !== s) s += " " + lbl;
    if (f.note) s += " — " + f.note;
    if (f.at) s += " · flagged " + fmtAge(Date.now() - f.at);
    return s;
  }

  // flagMark is the mark itself: the glyph in the kind's colour, carrying the
  // note in its tooltip. Text rather than an SVG (where the lock and the paw
  // print are drawn) for the reason the vocabulary is one field — a custom glyph
  // has no SVG to draw, so a text mark is the only shape both halves can share.
  //
  // The class carries the kind so the CSS can colour it; an unknown kind gets
  // fk-custom, which takes the default ink rather than borrowing some named
  // kind's meaning-by-colour.
  function flagMark(f) {
    if (!f) return null;
    const s = document.createElement("span");
    s.className = "flag-mark " + (FLAG_BY_KIND.has(f.kind) ? "fk-" + f.kind : "fk-custom");
    s.textContent = flagGlyph(f);
    s.title = flagTitle(f);
    return s;
  }

  // lockMark: the padlock a workspace wears while it is closed to plugins and
  // agents (workspace.lock). Two strokes — the shackle above, the body below —
  // which is all a padlock is at 12px; the body is filled so the mark still
  // reads as *shut* in a row that is otherwise dimmed out.
  function lockMark() {
    const svg = document.createElementNS(SVGNS, "svg");
    svg.setAttribute("class", "lock");
    svg.setAttribute("viewBox", "0 0 16 16");
    svg.setAttribute("fill", "none");
    svg.setAttribute("stroke", "currentColor");
    svg.setAttribute("stroke-width", "1.6");
    svg.setAttribute("stroke-linecap", "round");
    svg.setAttribute("stroke-linejoin", "round");
    const shackle = document.createElementNS(SVGNS, "path");
    shackle.setAttribute("d", "M5 7V4.8a3 3 0 0 1 6 0V7");
    svg.appendChild(shackle);
    const body = document.createElementNS(SVGNS, "rect");
    body.setAttribute("x", "3.2"); body.setAttribute("y", "7");
    body.setAttribute("width", "9.6"); body.setAttribute("height", "6.4");
    body.setAttribute("rx", "1.4");
    body.setAttribute("fill", "currentColor");
    svg.appendChild(body);
    const wrap = document.createElement("span");
    wrap.className = "lock-mark";
    wrap.title = "locked — no plugins or agents here, and clicking will not switch to it";
    wrap.appendChild(svg);
    return wrap;
  }

  // tabMarker: the highest-attention agent state among a tab's panes, as a
  // colored dot on the tab (cats's bell/activity markers; the rollup's tab
  // field keys the grouping — tab bar rows are the active workspace's tabs).
  //
  // tabAttention does the grouping once for the whole bar, for the same reason
  // workspaceRollups exists: per-tab scanning is O(tabs × agents) per render.
  function tabAttention() {
    const best = new Map();
    const aw = activeWorkspace();
    if (!aw) return best;
    for (const it of agentItems) {
      if (it.workspace !== aw.id) continue;
      const st = markerState(it);
      const cur = best.get(it.tab);
      if (cur === undefined || attentionRank(st) > attentionRank(cur)) best.set(it.tab, st);
    }
    return best;
  }
  function tabMarker(best) {
    if (!best || best === "idle" || best === "unknown") return null;
    const s = document.createElement("span");
    s.className = "tmark " + stClass(best);
    s.textContent = "●";
    s.title = best === "done" ? "agent done (unseen)" : "agent " + best;
    return s;
  }

  function renderWorkspaces(msg) {
    wsListEl.innerHTML = "";
    const { states, todos, globalTodos } = workspaceRollups();
    // The global backlog's mark sits on the section heading — the one element
    // scoped to everything, which is what the global list is. Same mark as the
    // rows below so it reads as the same kind of reminder; only the tooltip
    // says whose.
    wsGlobalTodoEl.innerHTML = "";
    const gmark = todoMark(globalTodos);
    if (gmark) {
      gmark.title = globalTodos === 1 ? "1 unfinished global todo" : globalTodos + " unfinished global todos";
      wsGlobalTodoEl.appendChild(gmark);
    }
    // Two shelves, in the order attention runs: the workspaces still in play,
    // then the ones set aside. "Locked" is already the strongest statement a
    // workspace row makes — nothing may be launched into it, and a click won't
    // even switch to it — so it is the one split worth spending a group header
    // on, and the one set of rows worth being able to fold out of the way once a
    // session has collected a few. The active workspace keeps its accent inside
    // the open shelf rather than being lifted into a shelf of its own: it is
    // always exactly one row, and a header over a single row is noise.
    //
    // Each row remembers its index in the session's order, because grouping
    // reorders the list on screen while workspace.move still speaks in gaps in
    // the session's order — see wsDropIndex for the translation back.
    const groups = [
      { id: WS_OPEN, label: "open", rows: [] },
      { id: WS_LOCKED, label: "locked", rows: [] },
    ];
    msg.workspaces.forEach((w, i) => groups[w.locked ? 1 : 0].rows.push({ w, idx: i }));
    // Headers are drawn only when the split is real. With nothing locked (the
    // common case) one shelf holds everything, and a header reading "open" over
    // the whole list is a row that says nothing — the section stays the flat list
    // it has always been.
    //
    // That used to leave the heading's ⊞/⊟ with nothing to act on, so ⊟ was a
    // no-op exactly when the list was most likely to be long. A fold does not
    // actually need a header to fold *into*; it needs somewhere to put the one
    // thing the fold still has to say. So when the split is absent the pair acts
    // on WS_ALL — the whole list at once — and the count that a shelf header
    // would have carried rides the section heading instead:
    //
    //   split          WORKSPACES        ⊞ ⊟      ⊟ folds each shelf behind its
    //                    open   2 workspaces      own header, which is on screen
    //                    locked 5 workspaces      to unfold it again
    //
    //   flat, folded   WORKSPACES            7 ⊞ ⊟    the heading IS the header
    //
    // The count only appears while it is standing in for hidden rows: expanded,
    // the rows are right there to be counted, and a permanent tally on a heading
    // that already carries the global todo mark and the fold pair is clutter.
    const drawn = groups.filter((g) => g.rows.length);
    const split = drawn.length > 1;
    wsGroupIDs = split ? drawn.map((g) => g.id) : [WS_ALL];
    const flatFolded = !split && wsCollapsed.has(WS_ALL);
    // The number alone, where a shelf header spells out "3 workspaces": the noun
    // is already the heading this rides, and the sidebar goes down to 150px wide,
    // where the spelled-out form wraps the heading onto two lines. The tooltip
    // carries the words for anyone the bare digit doesn't reach.
    const n = msg.workspaces.length;
    wsCountEl.textContent = flatFolded ? String(n) : "";
    wsCountEl.title = flatFolded ? n + (n === 1 ? " workspace" : " workspaces") + " — click to show them" : "";

    wsRenderOrder = [];
    wsRenderTotal = msg.workspaces.length;
    drawn.forEach((g, gi) => {
      if (split) wsListEl.appendChild(wsGroupEl(g, gi > 0));
      if (split ? wsCollapsed.has(g.id) : flatFolded) return;
      for (const ent of g.rows) {
        const w = ent.w;
        wsRenderOrder.push(ent.idx);
        const li = document.createElement("li");
        li.className = "ws" + (w.active ? " active" : "") + (w.locked ? " locked" : "");
        const name = document.createElement("span");
        name.textContent = (w.active ? "● " : "○ ") + w.name;
        li.appendChild(name);
        // The flag rides closest of all, ahead even of the lock: it is the one
        // mark in the row the user put there by hand, and its whole job is to
        // catch their own eye first.
        const wf = flagMark(flagOf(w));
        if (wf) li.appendChild(wf);
        // The lock rides next to it, ahead of the todo mark: it qualifies
        // the workspace itself — what may be started in it — where the todo mark
        // reports on the project inside.
        if (w.locked) li.appendChild(lockMark());
        // A workspace's host is where its NEW panes land — a property of the
        // workspace itself, like the lock, so it rides beside the name rather
        // than out at the right edge with the agent rollup. Multi-host only, for
        // the same reason the pane headers' badge is.
        if (multiHost() && w.host) {
          const hb = document.createElement("span"); hb.className = "wshost";
          hb.textContent = "@" + hostLabel(w.host);
          hb.title = "new panes in this workspace run on " + hostLabel(w.host);
          li.appendChild(hb);
        }
        // "Also open in another window" rides here for the same reason the host
        // badge does: it is a fact about the workspace, not about a pane.
        const win = otherWindowMark(windowsOnWorkspace(w.id));
        if (win) li.appendChild(win);
        // The todo mark sits with the name — it says something about the project,
        // not about a pane — while the agent counts stay pinned to the right edge.
        const todo = todoMark(todos.get(w.id) || 0);
        if (todo) li.appendChild(todo);
        const sum = workspaceSummary(states.get(w.id));
        if (sum) {
          const s = document.createElement("span"); s.className = "sum"; s.appendChild(sum);
          li.appendChild(s);
        }
        // A locked workspace does not take a click-to-switch: the lock says "leave
        // this one alone", and the sidebar is the one place a switch happens by
        // accident (a click meant for the row's name or its lock). The toast is not
        // optional — a click that silently does nothing reads as a broken row — and
        // it names the way out, since the row's own context menu holds the unlock.
        // Deliberate ways in are left alone: the context menu, the palette and the
        // keyboard still switch, so this narrows the accident, not the workspace.
        li.addEventListener("dblclick", () => renameWorkspace(w));
        li.addEventListener("contextmenu", (e) => { e.preventDefault(); openCtx(e.clientX, e.clientY, wsMenuItems(w)); });
        // The switch rides the drag helper's own mouseup rather than a "click"
        // listener on the row: this list is rebuilt on every agents rollup, and a
        // row replaced between press and release never sees its click (see
        // beginReorderDrag). Press-and-release on a row is a switch; DRAG_SLOP of
        // travel makes it a reorder instead.
        //
        // itemSel picks li.ws, so the group headers between the shelves are not
        // counted as drop positions — the gap the helper reports is an index into
        // the drawn workspace rows alone, which is exactly what wsDropIndex maps.
        li.addEventListener("mousedown", (e) => beginReorderDrag(e, {
          el: li, container: wsListEl, itemSel: "li.ws", horizontal: false,
          onDrop: (gap) => sendCmd("workspace.move", { id: w.id, index: wsDropIndex(gap) }),
          onClick: () => {
            if (w.active) return;
            if (w.locked) { toast((w.name || w.id) + " is locked — unlock it to switch"); return; }
            sendCmd("workspace.focus", { id: w.id });
          },
        }));
        wsListEl.appendChild(li);
      }
    });
    // The add row goes with the list. Folding a shelf leaves it — the section is
    // still a list of workspaces, one shelf of it just isn't showing — but folding
    // the whole list means the section is meant to be one line, and a fold that
    // leaves a row behind hasn't folded. The way in is still the context menu and
    // the palette, both of which reach newWorkspace without this row.
    if (!flatFolded) {
      const add = document.createElement("li");
      add.className = "add"; add.textContent = "+ workspace"; add.title = "new workspace";
      pressActivate(add, () => newWorkspace()); // rebuilt with the rows above it
      wsListEl.appendChild(add);
    }
  }

  // Where the last render put each drawn workspace row in the session's order,
  // and how many workspaces the session holds. Module-level rather than captured
  // per render because a mid-drag rebuild replaces every row: the drop then has
  // to be read against the list on screen at release, not the one that armed the
  // drag (beginReorderDrag re-queries for the same reason).
  let wsRenderOrder = [], wsRenderTotal = 0;

  // wsDropIndex turns a drop position in the drawn list into the gap
  // workspace.move wants. The list is grouped and a shelf can be folded shut, so
  // the Nth row on screen is not the Nth workspace in the session. For a session
  // of w0..w3 where w1 and w2 are locked:
  //
  //   drawn rows   [open]  w0    w3   [locked]  w1    w2
  //   drop gap           0    1     2         2    3     4  (beginReorderDrag)
  //   wsRenderOrder      0    3     1         1    2     —  (indices of the rows)
  //   sent as            0    3     1         1    2     4  (gap 4 = past the end)
  //
  // A gap means "insert ahead of the row drawn there", so the answer is that
  // row's own session index; past the last drawn row it means the end of the
  // list. Ungrouped and fully expanded, wsRenderOrder is [0,1,2,…] and this is
  // the identity mapping the list used before it had shelves.
  //
  // Dropping at the foot of one shelf and at the head of the next are the same
  // gap, and land the row wherever that next row sits in the session order —
  // the shelves are a view, not a second ordering, so a drag cannot lock or
  // unlock a workspace by dropping it across the divider.
  function wsDropIndex(gap) {
    return gap < wsRenderOrder.length ? wsRenderOrder[gap] : wsRenderTotal;
  }

  // wsGroupEl builds one shelf's header: what it holds, how many, and the caret
  // that folds it. The count rides the header in both states for the reason
  // paneGroupEl's rollup does — folded it stands in for the rows it hides, open
  // it saves counting them by eye.
  function wsGroupEl(g, sep) {
    const collapsed = wsCollapsed.has(g.id);
    const li = document.createElement("li");
    li.className = "wsgrp" + (sep ? " sep" : "");
    const name = document.createElement("span");
    name.textContent = g.label;
    li.appendChild(name);
    const s = document.createElement("span");
    s.className = "gsum";
    s.textContent = g.rows.length + (g.rows.length === 1 ? " workspace" : " workspaces");
    li.appendChild(s);
    const car = document.createElement("span");
    car.className = "car"; car.textContent = collapsed ? "▶" : "▼";
    li.appendChild(car);
    li.title = (collapsed ? "expand " : "collapse ") + g.label + " workspaces";
    // On the press, like the rows it folds: this list is rebuilt on every agents
    // rollup and every layout push, and a header replaced between press and
    // release never sees its click.
    pressActivate(li, () => {
      if (collapsed) wsCollapsed.delete(g.id); else wsCollapsed.add(g.id);
      saveWsCollapsed();
      if (layoutMsg) renderWorkspaces(layoutMsg);
    });
    return li;
  }

  // The Workspaces heading's controls: the same pair, in the same right-edge
  // position, that Usage and Panes carry. They act on whatever the last render
  // said was foldable — the shelves when the list is split, the list itself as
  // WS_ALL when it isn't — so ⊟ always folds something and ⊞ always has it back.
  //
  // Rewriting the whole set from wsGroupIDs is what keeps the two spellings apart:
  // ⊟ over a flat list drops any stale open/locked ids, and ⊟ over a split one
  // drops WS_ALL, so a set left over from the other shape can never fold rows
  // behind something that isn't drawn.
  //
  // The count itself is the third control, and the only one that appears exactly
  // when it does something: it is the header for a shelf that was never drawn, so
  // it unfolds on a press the way wsGroupEl's headers do. Bound once here rather
  // than per render — the span outlives every redraw, and pressActivate leaves a
  // mousedown listener behind that a redraw would stack.
  (function initWsHeadingCtl() {
    const el = document.getElementById("ws-hctl");
    pressActivate(wsCountEl, () => {
      if (!wsCollapsed.has(WS_ALL)) return; // empty chip: nothing folded to open
      wsCollapsed.delete(WS_ALL); saveWsCollapsed();
      if (layoutMsg) renderWorkspaces(layoutMsg);
    });
    el.appendChild(mkBtn("⊞", "show all workspaces", "", () => {
      wsCollapsed.clear(); saveWsCollapsed();
      if (layoutMsg) renderWorkspaces(layoutMsg);
    }));
    el.appendChild(mkBtn("⊟", "hide all workspaces", "", () => {
      wsCollapsed = new Set(wsGroupIDs); saveWsCollapsed();
      if (layoutMsg) renderWorkspaces(layoutMsg);
    }));
    initSectionFold("sec-workspaces", "ws-hctl", "workspaces");
  })();
