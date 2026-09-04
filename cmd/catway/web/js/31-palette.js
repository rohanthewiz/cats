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
      // The keyboard route into the recorder. Only the verb that applies is
      // offered: "start recording" while one is already running would fail on
      // the server (the recorder is one at a time), and a palette that lists
      // commands it knows will be refused is a palette you stop trusting.
      recState.recording
        ? { label: "stop recording…", fn: openStopRecordingDialog }
        : { label: "start recording (macro)", fn: startRecording },
      { label: sidebarHidden() ? "show sidebar" : "hide sidebar", fn: () => setSidebarHidden(!sidebarHidden()) },
      { label: "settings", fn: openSettings },
      { label: "reload config", fn: () => sendCmd("server.reload_config", {}) },
      { label: "keyboard shortcuts", fn: openHelp },
      { label: "stop server…", fn: confirmStopServer },
    ];
    if (recState.recording) items.push({ label: "cancel recording…", fn: confirmCancelRecording });
    // One entry per runbook, from the listing the sidebar already fetched — no
    // round trip, and nothing offered that the section is not also showing.
    // Broken files are left out for the same reason "start recording" is hidden
    // while one is running: the palette must not list something it knows the
    // server will refuse. They are still visible in the section, where the row
    // carries the parse error that makes them actionable.
    for (const rb of runbookItems) {
      if (rb.error) continue;
      // The listing carries an outline, so the entry can say what the runbook
      // DOES and not only what somebody called it. Three depths, because a
      // palette row is one line and an outline is up to two dozen:
      //
      //   sub    the first step — always visible, and what tells "deploy" from
      //          "deploy-staging" when the names have stopped doing it.
      //   meta   the total, which is what makes the sub readable as ONE of N
      //          rather than as the whole runbook — and, while a run is in
      //          flight, the position within that total instead.
      //   title  the outline entire, for the mouse. Enter reaches the same list
      //          in the gate, so this is the hover shortcut, not the only route.
      //
      // Without an outline (a server too old to send one) all three stay empty
      // and the entry is exactly the one it was before this existed — the same
      // fallback runbookOutline takes in the dialogs.
      const lead = runbookLead(rb);
      // A runbook that is ALREADY RUNNING has no run left in it. The server's
      // concurrency slot is per runbook name, so a second start comes back
      // "already in flight" — startRunbookRun knows this and refuses before
      // sending, which from the palette means an entry whose Enter is a toast
      // saying no. That is the exact mistake "start recording" avoids
      // twenty lines up, and the fix is the same one: offer the verb that
      // applies. While a run is in flight that verb is PREVIEW — which is also
      // what somebody watching panes appear by themselves actually wants, since
      // the question is no longer "shall I run this" but "what is it doing".
      const run = runbookRunOf(rb.name);
      if (run) {
        // No outline means no preview either — runbookHasPreview is the one
        // place that is decided, so this cannot promise what the menu would not
        // build — and a running runbook has no third verb to fall back on. The
        // entry therefore drops out for the duration, exactly as "start
        // recording" does while the recorder is busy. Reachable only from a
        // server too old to send the field, and the sidebar row still shows the
        // run either way, with its dot, its position and its tooltip.
        if (!runbookHasPreview(rb, false)) continue;
        items.push({
          label: "preview runbook: " + rb.name + "…",
          // Step ONE, not the step the run is on. run.step indexes the file
          // this run started from, while the outline came from the last
          // listing, so a file edited mid-run makes the two disagree — and a
          // wrong step line shown as fact is worse than a right one that is
          // merely not the current one. The sub's job here is to say WHICH
          // runbook this is; where it has got to is the meta's job, and the
          // meta reads the run's own numbers.
          sub: lead,
          meta: runbookRunNote(rb, run),
          title: runbookOutlineText(rb),
          fn: () => previewRunbook(rb),
        });
        continue;
      }
      items.push({
        label: "run runbook: " + rb.name + "…",
        sub: lead,
        meta: lead ? stepCount(rb.steps) : "",
        // The name is already in the label, so the hover is the steps and
        // nothing else; repeating the row above it would push the tail note
        // ("…and N more steps") further from the eye that needs it.
        title: lead ? runbookOutlineText(rb) : "",
        fn: () => startRunbookRun(rb),
      });
    }
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
        { label: "clean workspace (close idle panes)", fn: () => cleanWorkspace(aw, "") },
        { label: "clean workspace, park idle agents", fn: () => cleanWorkspace(aw, "park") },
        { label: "sleep workspace…", fn: () => sleepWorkspace(aw, "") },
        { label: "sleep workspace, park idle agents…", fn: () => sleepWorkspace(aw, "park") },
        { label: "flag workspace…", fn: () => openFlagDialog(wsFlagTarget(aw)) },
        { label: "close workspace…", fn: () => confirmCloseWorkspace(aw) },
      );
    }
    // Commands carried no columns but their label until the runbook entries
    // gained an outline to show, so the extras are defaulted here rather than
    // spelled out on the twenty-odd entries that have none.
    return items.map((it) => ({
      kind: "cmd", label: it.label,
      meta: it.meta || "", sub: it.sub || "", title: it.title || "",
      fn: it.fn,
    }));
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
        // A sleeping workspace is listed like any other; focusing it wakes it.
        items.push({ kind: "ws", label: w.name + " (" + w.id + ")", meta: w.active ? "active" : w.asleep ? "asleep" : "",
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
      // The haystack is exactly what the row RENDERS — label, meta, kind, and
      // now the sub. A field that is searched but not shown produces the worst
      // row in a fuzzy list: one that matched for a reason you cannot see. That
      // rule also decides the question the outline raises: only the first step
      // is searchable, because only the first step is on screen. The other
      // twenty-three are one Enter away in the gate, which is a list you read
      // rather than one you filter.
      return all
        .map((it) => ({ it, s: fuzzyScore(query, it.label + " " + it.meta + " " + it.kind + " " + (it.sub || "")) }))
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
        // Between the label and the right-aligned meta, and the only part of
        // the row that gives way when the modal runs out of width (the
        // stylesheet's min-width:0) — a clipped step line still reads, a
        // clipped runbook name does not.
        if (it.sub) {
          const sub = document.createElement("span");
          sub.className = "sub"; sub.textContent = it.sub;
          row.appendChild(sub);
        }
        // Only where a row asked for one. A title on every row would put a
        // tooltip under the pointer as it travels the list, and the list is
        // navigated with the keyboard anyway.
        if (it.title) row.title = it.title;
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

