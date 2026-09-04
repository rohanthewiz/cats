  // ---- Context menus (WS8): pane / tab / workspace ----
  //
  // An item is {label, fn, danger?} or {label, sub:[items]} for a submenu — the
  // shape the host pickers need, where one row would otherwise become one row
  // per host and bury the ordinary actions. A submenu is a second menu element
  // built by the same function, so nesting costs nothing and every item type
  // (separators, danger rows) works at any depth.
  //
  // An item may also carry {icon: {text, cls}}: a leading glyph in its own span,
  // so it can take a colour the label does not. The flag menu is what wants it —
  // the colour is half of what a flag mark means, and a menu that teaches only
  // the shape leaves the reader to learn the palette from the sidebar by
  // induction. Everything else omits it and renders exactly as before.
  function openCtx(x, y, items) {
    closeCtx();
    ctxEl = buildCtx(x, y, items);
    // Only the root menu dims what is under it; buildCtx recurses for submenus
    // and must not, or opening one would re-dim an already-dimmed dialog.
    setCtxDim(true);
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
      if (it.icon) {
        const ic = document.createElement("span");
        ic.className = "ctxicon " + (it.icon.cls || "");
        ic.textContent = it.icon.text;
        el.appendChild(ic);
      }
      el.appendChild(document.createTextNode(it.label));
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
      { label: flagRowLabel(paneFlagFor(id)), sub: flagMenuItems(paneFlagTarget(id)) },
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

  // flagRowLabel names the submenu by what it will do: "flag…" when there is
  // nothing there yet, and the flag itself once there is, so the menu reports the
  // current state without a second row to say so.
  function flagRowLabel(f) {
    return f ? "flag: " + flagGlyph(f) + " " + flagLabel(f) + "…" : "flag…";
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
  // ---- Flags: one menu, three subjects ----
  //
  // A flag target is everything the menu needs to act without knowing whether it
  // is looking at a workspace or a pane: which command to send, the params that
  // address the subject, a name for the toasts, and whatever flag is on it now
  // (so "edit note…" starts from the current note rather than blank). Building
  // it at the call site rather than passing a bare id is what lets the sidebar,
  // the pane header, the pane toolbar and the palette all share one menu.
  function paneFlagTarget(id) {
    return { cmd: "pane.flag", params: { pane: id }, label: paneRefFor(id), flag: paneFlagFor(id) };
  }
  function wsFlagTarget(w) {
    return { cmd: "workspace.flag", params: { id: w.id }, label: w.name || w.id, flag: flagOf(w) };
  }

  // paneFlagFor finds a pane's current flag wherever this window happens to know
  // it. Three sources, most-live first: the layout (pushed the moment the flag
  // changes, but only for the active tab), then the agents rollup (global, but
  // only agent panes), then the pane.list snapshot (global, one round trip
  // behind). A pane in another workspace running no agent is exactly why the
  // third one is here.
  function paneFlagFor(id) {
    const p = panes.get(id);
    if (p && p.info) { const f = flagOf(p.info); if (f) return f; }
    const a = agentItems.find((x) => x.pane === id);
    if (a) { const f = flagOf(a); if (f) return f; }
    const pi = paneInv.find((x) => x.pane === id);
    return pi ? flagOf(pi) : null;
  }

  // paneRefFor names a pane for a toast, from whichever list knows its handle —
  // the same fallback chain, since a pane the layout does not carry still has a
  // handle in the rollup or the inventory.
  function paneRefFor(id) {
    const p = panes.get(id);
    if (p && p.pub) return paneRef(p.pub, id);
    const a = agentItems.find((x) => x.pane === id);
    if (a) return paneRef(a.pub, id);
    const pi = paneInv.find((x) => x.pane === id);
    return paneRef(pi && pi.handle, id);
  }

  // flagMenuItems is the whole flag vocabulary as a menu: one row per named
  // kind, then the dialog that covers a note and a custom glyph, then the two
  // rows that only make sense once something is flagged.
  //
  // Picking a kind is one click and keeps whatever note is already there — the
  // common motion is "mark this, I'll come back", and making that cost a dialog
  // would mean it doesn't get used. The note has its own row for when it is the
  // point.
  function flagMenuItems(target) {
    const cur = target.flag;
    const items = FLAG_DEFS.map((d) => ({
      icon: { text: d.glyph, cls: "fk-" + d.kind },
      label: d.label + (cur && cur.kind === d.kind ? "  (current)" : ""),
      fn: () => sendFlag(target, d.kind, cur ? cur.note : ""),
    }));
    items.push("-", { label: "flag with a note…", fn: () => openFlagDialog(target) });
    if (cur) {
      items.push({ label: "edit note…", fn: () => editFlagNote(target) });
      items.push({ label: "clear flag", fn: () => sendFlag(target, "", "") });
    }
    return items;
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
      { label: flagRowLabel(flagOf(w)), sub: flagMenuItems(wsFlagTarget(w)) },
      // Clean and sleep are submenus for the one decision they need: what to do
      // with idle agents. The plain entry leaves them; the second parks them
      // for the wake. A sleeping row offers the way back instead.
      ...(w.asleep ? [{ label: "wake workspace", fn: () => wakeWorkspace(w) }] : [
        { label: "clean workspace", sub: [
          { label: "close idle panes", fn: () => cleanWorkspace(w, "") },
          { label: "close idle panes, park idle agents", fn: () => cleanWorkspace(w, "park") },
        ] },
        { label: "sleep workspace…", sub: [
          { label: "sleep (refuse if anything runs)", fn: () => sleepWorkspace(w, "") },
          { label: "sleep, park idle agents", fn: () => sleepWorkspace(w, "park") },
        ] },
      ]),
      "-",
      { label: "new worktree…", fn: openNewWorktreeDialog },
      { label: "open worktree…", fn: openWorktreeOpenDialog },
      { label: "delete worktree checkout…", danger: true, fn: () => removeWorktreeFor(w) },
      "-",
      { label: "close workspace…", danger: true, fn: () => confirmCloseWorkspace(w) },
    ];
  }

