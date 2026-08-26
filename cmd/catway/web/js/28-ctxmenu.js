  // ---- Context menus (WS8): pane / tab / workspace ----
  //
  // An item is {label, fn, danger?} or {label, sub:[items]} for a submenu — the
  // shape the host pickers need, where one row would otherwise become one row
  // per host and bury the ordinary actions. A submenu is a second menu element
  // built by the same function, so nesting costs nothing and every item type
  // (separators, danger rows) works at any depth.
  function openCtx(x, y, items) {
    closeCtx();
    ctxEl = buildCtx(x, y, items);
  }

  // buildCtx mounts one menu and returns it. Its child submenu, when one is
  // open, hangs off el._sub so closeCtx can tear the whole chain down — a
  // detached submenu left behind after the parent closes is the one failure
  // mode of hover-opened menus.
  function buildCtx(x, y, items) {
    const m = document.createElement("div");
    m.id = "ctxmenu";
    const closeSub = () => { if (m._sub) { m._sub.remove(); m._sub = null; } };
    for (const it of items) {
      if (it === "-") {
        const s = document.createElement("div"); s.className = "sep"; m.appendChild(s);
        continue;
      }
      const el = document.createElement("div");
      el.className = "item" + (it.danger ? " danger" : "") + (it.sub ? " sub" : "");
      el.textContent = it.label;
      if (it.sub) {
        // Hover opens it (and closes any sibling's), which is what a pointer
        // expects; click does the same so a tap reaches it too. The child is
        // placed at the row's right edge and clamped by buildCtx itself.
        const open = () => {
          closeSub();
          const r = el.getBoundingClientRect();
          m._sub = buildCtx(r.right - 2, r.top - 4, it.sub);
        };
        el.addEventListener("mouseenter", open);
        el.addEventListener("click", open);
      } else {
        el.addEventListener("mouseenter", closeSub);
        el.addEventListener("click", () => { closeCtx(); it.fn(); });
      }
      m.appendChild(el);
    }
    document.body.appendChild(m);
    // Clamp into the viewport after measuring.
    const r = m.getBoundingClientRect();
    m.style.left = Math.min(x, window.innerWidth - r.width - 4) + "px";
    m.style.top = Math.min(y, window.innerHeight - r.height - 4) + "px";
    return m;
  }
  window.addEventListener("mousedown", (e) => {
    if (ctxEl && !ctxChainHas(ctxEl, e.target)) closeCtx();
  }, true);
  window.addEventListener("blur", () => closeCtx());

  // The sidebar lists panes from every workspace, so this menu also opens on panes
  // that are not on screen. Those get the workspace-resolved actions — split,
  // capture, rename, close — plus a reveal; zoom and copy mode are left out, being
  // scoped to the active tab and to a rendered pane respectively.
  function paneMenuItems(id, onCanvas) {
    const p = panes.get(id);
    const onScreen = !!(p && p.info);
    const items = [];
    if (onCanvas) items.push({ label: "paste", fn: pasteText }, "-");
    if (!onScreen) items.push({ label: "reveal pane", fn: () => sendCmd("agent.focus", { pane: id }) }, "-");
    items.push(
      { label: "split left/right", fn: () => sendCmd("pane.split", { pane: id, direction: "h" }) },
      { label: "split top/bottom", fn: () => sendCmd("pane.split", { pane: id, direction: "v" }) },
    );
    // Where the pane lands is a second question, and only worth asking when the
    // session actually spans machines — so it is a submenu rather than two more
    // top-level rows, and it is absent entirely in the single-host case.
    if (multiHost()) {
      items.push(
        { label: "split left/right on…", sub: hostChoices((h) => sendCmd("pane.split", { pane: id, direction: "h", host: h })) },
        { label: "split top/bottom on…", sub: hostChoices((h) => sendCmd("pane.split", { pane: id, direction: "v", host: h })) },
      );
    }
    if (onScreen) items.push({ label: "toggle zoom", fn: () => sendCmd("pane.zoom", { pane: id }) });
    items.push("-");
    if (onScreen) items.push({ label: "copy mode", fn: () => enterCopyMode(id) });
    items.push(
      { label: "copy scrollback", fn: () => copyScrollback(id) },
      "-",
      { label: "rename pane…", fn: () => renamePane(id) },
      { label: "close pane", danger: true, fn: () => sendCmd("pane.close", { pane: id }) },
    );
    return items;
  }
  function tabMenuItems(t, count) {
    const items = [
      { label: "new tab", fn: () => sendCmd("tab.create", {}) },
    ];
    if (multiHost()) {
      items.push({ label: "new tab on…", sub: hostChoices((h) => sendCmd("tab.create", { host: h })) });
    }
    items.push({ label: "rename tab…", fn: () => renameTab(t) });
    // Move the tab, with its panes and their live terminals, to another
    // workspace — which is how a tab travels between WINDOWS, a window being a
    // view on one workspace. Offered only when there is somewhere to send it.
    const elsewhere = otherWorkspaces();
    if (elsewhere.length) {
      items.push({ label: "move tab to…", sub: elsewhere.map((w) => ({
        label: w.name + (windowsOnWorkspace(w.id) ? " (open in another window)" : ""),
        fn: () => sendCmd("tab.move_to_workspace", { workspace: w.id, num: t.num }),
      })) });
    }
    if (count > 1) items.push({ label: "close tab", danger: true, fn: () => sendCmd("tab.close", { num: t.num }) });
    return items;
  }

  // otherWorkspaces is every workspace except the one this window is showing —
  // the destinations a tab can be moved to.
  function otherWorkspaces() {
    if (!layoutMsg) return [];
    return layoutMsg.workspaces.filter((w) => !w.active);
  }

  // hostChoices turns the roster into submenu rows, one per host, calling fn
  // with the host id. A host that is down is still offered — the pane is created
  // in the model either way and the daemon's reconnect reconciles it, which is
  // the same contract a pane already has when its host drops mid-session — but
  // it is marked, so choosing one is a decision rather than a surprise.
  function hostChoices(fn) {
    return hostItems.map((h) => ({
      label: (h.label || h.id) + (h.connected ? "" : " (offline)"),
      fn: () => fn(h.id),
    }));
  }
  function wsMenuItems(w) {
    return [
      { label: "new workspace…", fn: newWorkspace },
      // A second window on this workspace, leaving the current one where it is.
      // Two windows on ONE workspace mirror each other; the useful shape is one
      // window per project, which is why this sits on the row rather than on
      // the section heading.
      { label: "open in new window", fn: () => openWindow(w.id) },
      { label: "rename workspace…", fn: () => renameWorkspace(w) },
      { label: w.locked ? "unlock workspace" : "lock workspace", fn: () => toggleWorkspaceLock(w) },
      "-",
      { label: "new worktree…", fn: openNewWorktreeDialog },
      { label: "open worktree…", fn: openWorktreeOpenDialog },
      { label: "delete worktree checkout…", danger: true, fn: () => removeWorktreeFor(w) },
      "-",
      { label: "close workspace…", danger: true, fn: () => confirmCloseWorkspace(w) },
    ];
  }

