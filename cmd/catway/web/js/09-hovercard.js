  // ---- Hover card ----
  // A single reused element (cheaper than per-row popups, and only ever one is
  // visible), shared by every hover target. Callers pass [label, value, valueCls]
  // rows; empty values drop out, so a caller can list optional fields flatly.
  const paneTipEl = document.createElement("div");
  paneTipEl.id = "panetip";
  document.body.appendChild(paneTipEl);

  function showTip(e, items) {
    paneTipEl.innerHTML = "";
    for (const [k, v, vcls] of items) {
      if (v === undefined || v === null || v === "") continue;
      const kk = document.createElement("span"); kk.className = "k"; kk.textContent = k;
      const vv = document.createElement("span"); vv.className = "v" + (vcls ? " " + vcls : ""); vv.textContent = v;
      paneTipEl.appendChild(kk); paneTipEl.appendChild(vv);
    }
    // Position the popup to the right of the cursor, clamped into the viewport
    // (measured after making it visible so its size is known).
    paneTipEl.classList.add("show");
    const r = paneTipEl.getBoundingClientRect();
    let x = e.clientX + 14, y = e.clientY + 8;
    if (x + r.width > window.innerWidth - 4) x = e.clientX - r.width - 8;
    if (y + r.height > window.innerHeight - 4) y = window.innerHeight - r.height - 4;
    paneTipEl.style.left = Math.max(4, x) + "px";
    paneTipEl.style.top = Math.max(4, y) + "px";
  }

  function hideTip() { paneTipEl.classList.remove("show"); }

  // Pane-list rows truncate title/agent to fit their narrow column, so the full
  // details are only reachable on hover. Rebuilt on each show since the
  // underlying pane state is live. row is a renderPaneList row — the merged view
  // of one pane, on screen or not; the fields only the viewport has (model, exit
  // code, grid size) come from local pane state and are simply absent for a pane
  // sitting in another tab.
  function showPaneTip(e, row) {
    const p = row.visible ? panes.get(row.pane) : null;
    const items = [["Pane", paneRef(row.pub, row.pane), "pub"]];
    items.push(["Title", row.title]);
    items.push(["Dir", row.cwd, "mono"]);
    // The hover card is the one surface with room for the note in full — every
    // other place it appears is a tooltip on a 12px glyph — so it gets its own
    // row rather than being folded into the flag's label.
    if (row.flag) {
      items.push(["Flag", flagGlyph(row.flag) + " " + flagLabel(row.flag)
        + (row.flag.at ? " · " + fmtAge(Date.now() - row.flag.at) : "")]);
      items.push(["Note", row.flag.note]);
    }
    if (row.agent) items.push(["Agent", row.agent + " · " + (row.state || "unknown")]);
    // The LLM the agent is running under — with the reasoning effort it last ran
    // at appended, when the transcript named one — read from that transcript
    // server-side (agentmodel.go), already one display string. Only claude
    // reports one, and only once it has answered.
    // Off-screen panes get it from the pane.list snapshot, same as their agent.
    if (row.model) items.push(["Model", row.model]);
    if (p && p.exited !== null && p.exited !== undefined) items.push(["Exited", "code " + p.exited, "exited"]);
    items.push(["Focus", row.visible ? (row.focused ? "focused" : "") : "off screen"]);
    // Geometry + link state, relocated here from the status bar: the pane's own
    // terminal grid (from its last frame, falling back to the layout's inner
    // rect before the first frame lands) and then the window grid it sits in.
    const inner = (p && p.info && p.info.inner) || [0, 0, 0, 0];
    const gw = p ? (p.W || inner[2]) : 0, gh = p ? (p.H || inner[3]) : 0;
    if (gw && gh) items.push(["Size", gw + "×" + gh + " cells"]);
    if (cols && rows) items.push(["Window", cols + "×" + rows + " cells"]);
    items.push(["Link", connState.text, connState.err ? "err" : "ok"]);
    showTip(e, items);
  }


  // joinTrunc packs a list of short strings into ONE tooltip row: entries joined
  // with ", " until the character budget runs out, then "+N more" for the rest.
  // Budgeted in characters rather than pixels because the row is only ever
  // measured after it is in the DOM, and by then a too-long list has already
  // wrapped — #panetip's max-width would turn a "one row" list into four. ~64
  // chars is what fits the 340px card at 12px in one line, with the "+N more"
  // tail always kept so the row never lies about how much it is showing.
  function joinTrunc(parts, budget) {
    if (!parts.length) return "";
    const max = budget || 64;
    let out = "", shown = 0;
    for (const p of parts) {
      const next = out ? out + ", " + p : p;
      // Always take the first entry, however long: a row reading only "+3 more"
      // says less than a truncated name does.
      if (shown && next.length > max) break;
      out = next; shown++;
    }
    if (shown < parts.length) out += " +" + (parts.length - shown) + " more";
    return out;
  }

  // ---- Workspace hover card ----
  //
  // The WORKSPACES rows carry their extra state as glyphs — a flag, a lock, a
  // paw print with a count — each of which is at most a 12px mark with a title
  // attribute. That is enough to notice something, and not enough to read it:
  // the flag's note, and *which* panes inside the workspace hold the unfinished
  // todos, have nowhere to be said. So a row with any of that gets the same
  // multi-row card the PANES rows get, built from the same showTip primitive.
  //
  // The card is deliberately conditional (workspaceTipItems returns null for a
  // plain row). A workspace with no annotation has nothing the row does not
  // already say in full, and a popup that appears over every row on the way to
  // somewhere else is noise. Two things qualify a row:
  //
  //   annotations — the workspace's own flag, plus any flags pinned to panes
  //                 inside it, which the workspace row otherwise never shows
  //   todos       — the paw print's count, expanded into which panes owe what
  //
  // Everything else in the card (host, lock, agent states) is context that only
  // rides along once one of those has already earned the popup.
  function workspaceTipItems(w) {
    const f = flagOf(w);
    const todos = wsTodoPanes(w.id);
    const flagged = wsFlaggedPanes(w.id);
    if (!f && !todos.length && !flagged.length) return null;

    const items = [["Workspace", w.name || w.id, "pub"]];
    if (f) {
      items.push(["Flag", flagGlyph(f) + " " + flagLabel(f)
        + (f.at ? " · " + fmtAge(Date.now() - f.at) : "")]);
      // The note is why the flag was pinned; with no note the vocabulary's own
      // meaning ("come back to this") stands in, so the row still explains the
      // glyph to someone who did not pin it.
      const d = flagDef(f);
      items.push(["Note", f.note || (d ? d.meaning : "")]);
    }
    // One row, comma separated: "cats:p3 ×2, cats:p7 ×1". The pane handle rather
    // than the todo title because the handle is what you act on — it is the
    // pane to go read the list in — and because every one of these titles begins
    // with the same word.
    if (todos.length) {
      const total = todos.reduce((n, t) => n + t.n, 0);
      items.push([total === 1 ? "1 todo" : total + " todos",
        joinTrunc(todos.map((t) => t.ref + " ×" + t.n)), "oneline"]);
    }
    // Pane flags, one row each up to a few, since the note is the whole point of
    // showing them and notes do not survive being packed into a shared row. Past
    // that the count stands in — a workspace with a dozen flagged panes is a
    // question for the PANES list, not for a hover card.
    const MAXF = 4;
    flagged.slice(0, MAXF).forEach((x, i) => {
      const note = x.flag.note || flagLabel(x.flag);
      items.push([i ? "" : "Flagged", flagGlyph(x.flag) + " " + x.ref + " — " + note]);
    });
    if (flagged.length > MAXF) items.push(["", "+" + (flagged.length - MAXF) + " more flagged panes"]);

    if (multiHost() && w.host) items.push(["Host", "@" + hostLabel(w.host)]);
    if (w.locked) items.push(["Locked", "no plugins or agents here"]);
    // The agent rollup the row shows as "●2 ●1", spelled out.
    const c = agentStateCounts().get(w.id);
    if (c) {
      const parts = [];
      for (const st of ["blocked", "done", "working", "idle"]) {
        if (c[st]) parts.push(c[st] + " " + (st === "done" ? "done (unseen)" : st));
      }
      if (parts.length) items.push(["Agents", parts.join(", ")]);
    }
    const wins = windowsOnWorkspace(w.id);
    if (wins) items.push(["Windows", "also open in " + wins + (wins === 1 ? " other window" : " other windows")]);
    return items;
  }

  // showWorkspaceTip is the hover handler itself: rebuilt on every move, since
  // the flags, the todo counts and the agent states all change underneath a
  // stationary pointer.
  function showWorkspaceTip(e, w) {
    const items = workspaceTipItems(w);
    if (!items) { hideTip(); return; }
    showTip(e, items);
  }
