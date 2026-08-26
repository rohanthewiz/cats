  // ---- Session ----
  function connect() {
    const proto = location.protocol === "https:" ? "wss" : "ws";
    ws = new WebSocket(`${proto}://${location.host}/ws`);
    ws.onopen = () => {
      // A pane.list still in flight when the socket died never gets its callback,
      // which would leave the single-flight guard latched and freeze the Panes
      // section for the rest of the session. The reconnect's layout re-queries.
      clearTimeout(paneInvWait); paneInvWait = null;
      paneInvBusy = false; paneInvAgain = false;
      // ?ws=<id> is what makes this window a window: the server treats each
      // connection as a view on one workspace, so a second tab opened on
      // "?ws=w2" shows w2 with its own tab, focus and size while this one stays
      // where it is. Omitted (the plain URL) means "whatever the primary window
      // is showing", which is the single-window behaviour. Re-read per connect
      // so a reconnect lands back on the same workspace.
      sendMsg({ t: "init", v: PV, cols, rows, dpr,
        cell_w_px: Math.round(cellW * dpr), cell_h_px: Math.round(cellH * dpr),
        workspace: urlWorkspace() });
      // The server assumes a fresh connection is in front; correct it straight
      // away if this window actually reconnected while in the background.
      if (!winFocused) sendMsg({ t: "focus", focused: false });
    };
    ws.onmessage = (ev) => onMessage(JSON.parse(ev.data));
    ws.onclose = () => { setStatus("disconnected — retrying…", true); setTimeout(connect, 1500); };
    ws.onerror = () => ws.close();
  }

  let resizeTimer = null;
  window.addEventListener("resize", () => {
    clearTimeout(resizeTimer);
    resizeTimer = setTimeout(() => {
      // A window narrowed since the last drag can leave the stored sidebar
      // width past its 60% ceiling; re-clamp before deriving the grid.
      const sbw = parseInt(document.documentElement.style.getPropertyValue("--sidebar-w"), 10);
      if (sbw > 0) setSidebarWidth(sbw);
      gridSize();
      // Same reason as the fold: the window has already been repainted at its
      // new size, and the panes are still wearing the old one until the server
      // answers. Dragging a window edge is the case where that lasts longest.
      refitLayout();
      sendMsg({ t: "resize", cols, rows });
      // (No status refresh here any more — the window grid size is reported by
      // the pane hover card, which reads cols/rows live when it opens.)
    }, 120);
  });

  // ---- Window focus ----
  // The window's own focus (app-level, not pane-level), tracked for two jobs:
  // the focused pane's cursor hollows out so the eye can tell at a glance the
  // app is in the background (draw), and the state is reported up so pane
  // programs that enabled focus reporting — Claude Code, bubbletea TUIs — stop
  // blinking their own carets while nobody is watching. Without the report,
  // the app-drawn blink keeps arriving as cell updates and no amount of
  // client-side cursor styling can still it.
  function onWinFocus(f) {
    if (f === winFocused) return;
    winFocused = f;
    sendMsg({ t: "focus", focused: f });
    // Restyle the drawn cursor now rather than on the next frame diff — a
    // backgrounded app may not produce one for minutes.
    for (const p of panes.values()) {
      if (p.info && p.info.focused) scheduleDraw(p);
    }
  }
  window.addEventListener("focus", () => onWinFocus(true));
  window.addEventListener("blur", () => onWinFocus(false));

