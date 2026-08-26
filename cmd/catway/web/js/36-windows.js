  // ---- Windows ----
  //
  // A window is a connection with a view. The server holds one session; each
  // connection says which workspace it is showing, and gets that workspace's
  // layout, frames and chrome. Nothing here is persisted — the URL IS the
  // window's state, which is what makes a bookmarked "?ws=w2" reopenable.

  // urlWorkspace is the workspace this window opens on, "" for the default. A
  // stale id (the workspace was closed since the bookmark was made) is not an
  // error: the server falls back to the primary view.
  function urlWorkspace() {
    return new URLSearchParams(location.search).get("ws") || "";
  }

  // openWindow opens a new browser window on a workspace. In a browser this is
  // a tab or a window per the user's own settings; in the Mac app the shell
  // intercepts window.open and makes it a native NSWindow.
  function openWindow(wsID) {
    const url = location.pathname + (wsID ? "?ws=" + encodeURIComponent(wsID) : "");
    const w = window.open(url, "_blank");
    if (!w) toast("your browser blocked the new window — allow popups for this site");
  }

  // syncWindowURL keeps this window's address in step with the workspace it is
  // showing. Two things depend on it and neither is cosmetic:
  //
  //   * a reload (⌘R, a reconnect after sleep) lands back on the same
  //     workspace instead of following the primary window somewhere else;
  //   * the Mac app reads ?ws= off each window's live URL to remember the
  //     window layout across launches — a workspace switch inside a window is
  //     recorded with no extra protocol at all.
  //
  // replaceState, not pushState: switching workspaces inside a window is not a
  // browser navigation, and filling the browser's back button with them would
  // tear down the WebSocket on every step back. ⌘[ / ⌘] mean something better
  // instead: cats-level navigation over the focus-location history (see the
  // nav.back/forward handler in onKey), which walks panes and workspaces
  // without ever reloading the page.
  function syncWindowURL(msg) {
    let active = "";
    for (const w of msg.workspaces) if (w.active) active = w.id;
    if (!active) return;
    const want = location.pathname + "?ws=" + encodeURIComponent(active) + location.hash;
    if (location.pathname + location.search + location.hash === want) return;
    try { history.replaceState(null, "", want); } catch (_) { /* file:// and the like */ }
  }

  // clientsMsg is the last census (the `clients` down-message): who else is
  // connected and, per connection, which workspace they are showing. It is what
  // lets a row say "already open in another window" instead of quietly opening
  // a duplicate.
  let clientsMsg = null;

  // windowsOnWorkspace counts the OTHER windows showing a workspace. Our own
  // view is excluded by dropping one entry for the workspace this window is
  // showing — the census does not name connections, and it does not need to:
  // exactly one of its entries is us, and it is on our workspace.
  function windowsOnWorkspace(wsID) {
    if (!clientsMsg || !clientsMsg.views) return 0;
    let n = 0;
    for (const v of clientsMsg.views) if (v.workspace === wsID && !v.viewer) n++;
    if (wsID === activeWorkspaceID()) n -= 1; // that one is this window
    return Math.max(0, n);
  }

  // activeWorkspaceID is the workspace THIS window is showing, per the layout
  // the server built for this connection.
  function activeWorkspaceID() {
    if (!layoutMsg) return "";
    for (const w of layoutMsg.workspaces) if (w.active) return w.id;
    return "";
  }

  // otherWindowMark is the "also open in another window" badge on a workspace
  // row. Deliberately quiet — it is information, not a warning: two windows on
  // one workspace mirror each other, which is legal and occasionally what you
  // want.
  function otherWindowMark(n) {
    if (n < 1) return null;
    const el = document.createElement("span");
    el.className = "wswin";
    el.textContent = n > 1 ? "\u29c9" + n : "\u29c9";
    el.title = n === 1 ? "also open in another window"
                       : "also open in " + n + " other windows";
    return el;
  }

