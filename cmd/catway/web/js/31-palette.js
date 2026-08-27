  // ---- Command palette / navigator (WS8) ----
  //
  // One fuzzy list over both surfaces cats splits in two: the Navigator
  // (jump to any pane/tab/workspace, with agent-state context) and a command
  // list built on the §7 vocabulary. Panes come from a live pane.list query so
  // the palette reaches across workspaces, not just the viewport.

  function focusedPaneId() {
    if (!layoutMsg) return null;
    const pr = layoutMsg.panes.find((p) => p.focused);
    return pr ? pr.pane : null;
  }
  function activeTab() { return layoutMsg ? layoutMsg.tabs.find((t) => t.active) : null; }
  function activeWorkspace() { return layoutMsg ? layoutMsg.workspaces.find((w) => w.active) : null; }

  function paletteCommands() {
    const f = focusedPaneId(), at = activeTab(), aw = activeWorkspace();
    const items = [
      { label: "new tab", fn: () => sendCmd("tab.create", {}) },
      { label: "new workspace", fn: newWorkspace },
      { label: "split left/right", fn: () => sendCmd("pane.split", { direction: "h" }) },
      { label: "split top/bottom", fn: () => sendCmd("pane.split", { direction: "v" }) },
      { label: "toggle zoom", fn: () => sendCmd("pane.zoom", {}) },
      { label: "close pane", fn: () => sendCmd("pane.close", {}) },
      { label: "new worktree", fn: openNewWorktreeDialog },
      { label: "open worktree", fn: openWorktreeOpenDialog },
      { label: "plugins", fn: openPluginsDialog },
      { label: sidebarHidden() ? "show sidebar" : "hide sidebar", fn: () => setSidebarHidden(!sidebarHidden()) },
      { label: "settings", fn: openSettings },
      { label: "reload config", fn: () => sendCmd("server.reload_config", {}) },
      { label: "keyboard shortcuts", fn: openHelp },
      { label: "stop server…", fn: confirmStopServer },
    ];
    if (f !== null) {
      items.push(
        { label: "rename pane…", fn: () => renamePane(f) },
        // The palette has no submenus, so it offers the dialog rather than the
        // one-click kinds the context menus carry — a keyboard route into
        // flagging wants the note anyway, which is the half the menu makes you
        // take a second step for.
        { label: "flag focused pane…", fn: () => openFlagDialog(paneFlagTarget(f)) },
        { label: "copy mode", fn: () => enterCopyMode(f) },
        { label: "copy scrollback", fn: () => copyScrollback(f) },
      );
    }
    if (at) {
      items.push({ label: "rename tab…", fn: () => renameTab(at) });
      if (layoutMsg.tabs.length > 1) items.push({ label: "close tab", fn: () => sendCmd("tab.close", { num: at.num }) });
    }
    if (aw) {
      items.push(
        { label: "rename workspace…", fn: () => renameWorkspace(aw) },
        { label: aw.locked ? "unlock workspace" : "lock workspace (no plugins or agents)", fn: () => toggleWorkspaceLock(aw) },
        { label: "flag workspace…", fn: () => openFlagDialog(wsFlagTarget(aw)) },
        { label: "close workspace…", fn: () => confirmCloseWorkspace(aw) },
      );
    }
    return items.map((it) => ({ kind: "cmd", label: it.label, meta: "", fn: it.fn }));
  }

  // fuzzyScore: subsequence match with a run bonus and a word-start bonus;
  // -1 = no match, higher = better.
  function fuzzyScore(q, s) {
    q = q.toLowerCase(); s = s.toLowerCase();
    if (!q) return 0;
    let qi = 0, score = 0, run = 0;
    for (let i = 0; i < s.length && qi < q.length; i++) {
      if (s[i] === q[qi]) {
        qi++; run++;
        score += run + (i === 0 || " :·.-_/".includes(s[i - 1]) ? 3 : 0);
      } else run = 0;
    }
    return qi === q.length ? score : -1;
  }

  function openPalette() {
    let items = [];   // static part: workspaces, tabs, commands
    let paneItems = []; // async part: pane.list across all workspaces
    let sel = 0, query = "";
    let listEl, inputEl;

    if (layoutMsg) {
      for (const w of layoutMsg.workspaces) {
        items.push({ kind: "ws", label: w.name + " (" + w.id + ")", meta: w.active ? "active" : "",
          fn: () => { if (!w.active) sendCmd("workspace.focus", { id: w.id }); } });
        items.push({ kind: "ws", label: "open " + w.name + " in new window", meta: w.id,
          fn: () => openWindow(w.id) });
      }
      for (const t of layoutMsg.tabs) {
        items.push({ kind: "tab", label: t.name, meta: t.active ? "active" : "",
          fn: () => { if (!t.active) sendCmd("tab.focus", { num: t.num }); } });
      }
    }
    items = items.concat(paletteCommands());

    const filtered = () => {
      const all = paneItems.concat(items);
      if (!query) return all;
      return all
        .map((it) => ({ it, s: fuzzyScore(query, it.label + " " + it.meta + " " + it.kind) }))
        .filter((x) => x.s >= 0)
        .sort((a, b) => b.s - a.s)
        .map((x) => x.it);
    };

    const render = () => {
      const rows = filtered();
      if (sel >= rows.length) sel = Math.max(0, rows.length - 1);
      listEl.innerHTML = "";
      if (!rows.length) {
        const e = document.createElement("div"); e.className = "empty"; e.textContent = "no matches";
        listEl.appendChild(e); return;
      }
      rows.slice(0, 60).forEach((it, i) => {
        const row = document.createElement("div");
        row.className = "row" + (i === sel ? " sel" : "");
        const kind = document.createElement("span"); kind.className = "kind"; kind.textContent = it.kind;
        const lbl = document.createElement("span"); lbl.className = "lbl"; lbl.textContent = it.label;
        row.appendChild(kind); row.appendChild(lbl);
        if (it.meta) {
          const meta = document.createElement("span");
          meta.className = "meta" + (it.stateClass ? " " + it.stateClass : "");
          meta.textContent = it.meta;
          row.appendChild(meta);
        }
        row.addEventListener("click", () => { closeModal(); it.fn(); });
        row.addEventListener("mousemove", () => { if (sel !== i) { sel = i; render(); } });
        listEl.appendChild(row);
      });
      const cur = listEl.children[sel];
      if (cur && cur.scrollIntoView) cur.scrollIntoView({ block: "nearest" });
    };

    openOverlay((ov) => {
      const m = document.createElement("div"); m.className = "modal pal";
      const q = document.createElement("div"); q.className = "query";
      inputEl = document.createElement("input");
      inputEl.type = "text"; inputEl.placeholder = "jump to a pane, tab, workspace — or run a command"; inputEl.spellcheck = false;
      q.appendChild(inputEl); m.appendChild(q);
      listEl = document.createElement("div"); listEl.className = "list"; m.appendChild(listEl);
      inputEl.addEventListener("input", () => { query = inputEl.value; sel = 0; render(); });
      inputEl.addEventListener("keydown", (e) => {
        e.stopPropagation();
        if (e.key === "ArrowDown") { e.preventDefault(); sel++; render(); }
        else if (e.key === "ArrowUp") { e.preventDefault(); sel = Math.max(0, sel - 1); render(); }
        else if (e.key === "Enter") {
          e.preventDefault();
          const rows = filtered();
          if (rows[sel]) { closeModal(); rows[sel].fn(); }
        } else if (e.key === "Escape") { e.preventDefault(); closeModal(); }
      });
      ov.appendChild(m);
      focusField(inputEl);
      render();
    });

    // Panes across all workspaces (agent.focus crosses workspace + tab).
    sendCmdAwait("pane.list", {}, (res) => {
      if (!res.ok || !modalEl) return;
      const byPane = new Map(agentItems.map((a) => [a.pane, a]));
      paneItems = (res.data && res.data.panes ? res.data.panes : []).map((pi) => {
        const a = byPane.get(pi.pane);
        return {
          kind: "pane",
          label: paneRef(pi.handle, pi.pane) + (pi.name ? " · " + pi.name : ""),
          meta: a ? a.agent + ":" + a.state : (pi.focused ? "focused" : ""),
          stateClass: a ? stClass(a.state) : "",
          fn: () => sendCmd("agent.focus", { pane: pi.pane }),
        };
      });
      render();
    });
  }

