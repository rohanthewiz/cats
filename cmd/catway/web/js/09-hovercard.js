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

