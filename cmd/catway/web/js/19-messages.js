  // ---- Down messages ----
  function onMessage(msg) {
    switch (msg.t) {
      case "welcome":
        bootPhase("welcome", msg.error || "");
        if (msg.error) setStatus("rejected: " + msg.error, true);
        else {
          setStatus("connected");
          // The chip only exists when the server serves chat — without the
          // cap, chat.send would vanish into an unknown-command error.
          chatBtnEl.style.display = (msg.caps || []).includes("chat") ? "" : "none";
          // Runbooks are files on the server's disk with no change stream, so
          // the section has to ask. A fresh connection is one of the few
          // moments this window can be sure it does not know: it may have been
          // away for hours, and a reconnect re-runs this.
          refreshRunbooks(false);
        }
        break;
      // The first layout is the end of startup as far as the native launcher is
      // concerned: it is the message that puts a workspace on screen.
      case "layout": applyLayout(msg); bootReady(); break;
      // The census: how many clients, and what each of them is looking at. Only
      // the sidebar reads it, so a re-render of the workspace list is the whole
      // reaction.
      case "clients":
        clientsMsg = msg;
        renderWorkspacesSoon();
        break;
      case "pane_frame": {
        const p = pane(msg.pane);
        p.W = msg.w; p.H = msg.h; p.cells = msg.cells; p.links = msg.links || [];
        p.cur = msg.cur || null; p.defFg = msg.def_fg; p.defBg = msg.def_bg;
        p.scroll = msg.scroll || null;
        scheduleDraw(p); break;
      }
      case "pane_diff": {
        const p = pane(msg.pane);
        // Each changed cell marks its row for the incremental painter (see
        // paint in 18-render). Diff cells arrive in row-major order, so a
        // run of cells on one row costs one Set insert, not one per cell.
        const W = p.W || 1;
        // A shift scrolls the grid BEFORE the cells land: row r takes row
        // r+shift, and the rows vacated at the bottom become blanks in the
        // frame's default colours (wire.PaneDiff). Every row moved, so it is
        // a full repaint; the saving is on the wire, where a line of output
        // now costs that line instead of the whole grid.
        if (msg.shift > 0 && p.cells) {
          const n = msg.shift * W;
          p.cells.copyWithin(0, n);
          p.cells.fill(BLANK_CELL, p.cells.length - n);
          p.full = true;
        }
        let lastY = -1;
        if (msg.cells) for (const dc of msg.cells) {
          p.cells[dc.i] = dc;
          const y = (dc.i / W) | 0;
          if (y !== lastY) { markRow(p, y); lastY = y; }
        }
        if (msg.cur) p.cur = msg.cur;
        // Every frame carries current scroll, absent ⇒ no scrollback: reset, don't
        // retain (a stale max misplaces read coords after the buffer shrinks, e.g. clear).
        // A scroll that MOVED repaints everything: the scrollbar thumb spans
        // rows the diff did not touch.
        const sc = msg.scroll || null;
        if (!sameScroll(p.scroll, sc)) p.full = true;
        p.scroll = sc;
        requestPaint(p); break;
      }
      // A title push is the chattiest message there is — an agent retitles its
      // pane on every spinner tick — and it is only ever sent for panes on
      // screen, whose rows wsPaneRows already reads from local state. So it
      // redraws from what is here and does NOT re-query pane.list: that round
      // trip (and the second full rebuild its reply triggers) could only bring
      // back what this message just delivered.
      //
      // One reader does take titles from the pane.list snapshot even for panes
      // on screen: the workspace todo marks, which count a cats-todo pane's
      // "todo: … (3)" title. So the snapshot entry is patched in place, which
      // is exactly what the query would have returned. The pushed title is the
      // EFFECTIVE one (a custom name wins), so it can stand in for the raw
      // terminal title only when the pane has no custom name; a named pane
      // still takes the query.
      case "pane_title": {
        const p = pane(msg.pane);
        if (p.title === msg.title) break;
        p.title = msg.title; renderChrome(p);
        const pi = paneInv.find((x) => x.pane === msg.pane);
        if (pi && !pi.name) { pi.title = msg.title; renderInventoryViews(); }
        else refreshPaneList();
        break;
      }
      case "pane_cwd": { const p = pane(msg.pane); p.cwd = msg.cwd; renderChrome(p); break; }
      // The branch arrives on its own message rather than with the cwd: a
      // checkout changes it without the pane ever moving, so the two are pushed
      // independently (see browserproto's PaneBranch).
      case "pane_branch": { const p = pane(msg.pane); p.branch = msg.branch; renderChrome(p); break; }
      case "pane_agent": {
        const p = pane(msg.pane); p.agent = msg.agent; p.agentState = msg.state;
        // Absent (an agent with no resolvable model) clears it — a stale model
        // must not outlive the agent the server reported it for.
        p.agentModel = msg.model || "";
        p.agentBlank = msg.blank || ""; // "new" | "cleared" while nothing has been asked
        // How the conversation began ("startup" | "clear" | "resume"), and the
        // instant the pane entered it — converted from the relative origin_ms
        // on arrival, as the agents rollup's since_ms is. 0 = unknown.
        p.agentOrigin = msg.origin || "";
        p.agentOriginAt = msg.origin_ms > 0 ? Date.now() - msg.origin_ms : 0;
        renderChrome(p); refreshPaneList(); break;
      }
      case "pane_modes": {
        const p = pane(msg.pane); p.modes = { mouse: !!msg.mouse, alt: !!msg.alt_screen, kitty: msg.kitty | 0 };
        break;
      }
      // autoclose_ms is what is LEFT of the server's tidy-exit countdown, and it
      // is re-sent rather than sent once: this same message arrives with the
      // field absent when somebody cancels the countdown (pane.keep, from any
      // window), which is how a cancel reaches every header. So it is applied
      // unconditionally — absent means "not counting", not "unchanged".
      case "pane_exited": {
        const p = pane(msg.pane); p.exited = msg.code;
        startAutoclose(p, msg.autoclose_ms || 0);
        renderChrome(p); refreshPaneList(); break;
      }
      // The inverse: the pane's PTY came back (cathost restart, host move), so
      // the red header comes off. Needed because the exit is REMEMBERED here —
      // a live pane's chrome simply omits pane_exited, which retracts nothing.
      case "pane_respawned": {
        const p = pane(msg.pane); p.exited = null;
        p.autocloseAt = 0; // the server cancelled its timer too — nothing to count down to
        renderChrome(p); refreshPaneList(); break;
      }
      case "agents": renderAgents(msg.items, msg.plugins); break;
      case "ws_git": applyWorkspaceGit(msg.workspaces || []); break;
      case "hosts": renderHosts(msg.items); break;
      case "history": renderHistory(msg.entries || []); break;
      // The macro recorder is session state, not this window's: the server
      // pushes it on every transition, on every captured step, and once in the
      // connect burst, so the indicator is right no matter who armed it.
      case "record": applyRecord(msg); break;
      // …and so is a runbook run. The server pushes the whole set of runs in
      // flight on every start and every finish, so a row marks itself for a run
      // started by another window, by catctl, or by an `on:` trigger firing
      // with nobody watching — which is the case a query could never have
      // caught, since the file on disk does not change while it runs.
      case "runbook_runs": applyRunbookRuns(msg); break;
      // …and so is a default plugin that failed to install: the seed runs in
      // the background and can fail minutes after this page connected, so
      // the server pushes the list (and once in the connect burst) rather
      // than the page polling for it.
      case "plugin_notice": applyPluginNotice(msg); break;
      case "ui_prefs": applyUIPrefsPush(msg); break;
      case "usage": renderUsage(msg); break;
      case "clipboard": // OSC 52 write from a pane app — no user activation
        // Empty data would be a pane clearing the clipboard. catway already
        // drops those (cmd/catway/daemon.go), and this is the wire contract's
        // "treat it as a no-op" for a page attached to a catway that predates
        // that rule. Writing "" through the mac bridge runs pbcopy with no
        // input, which empties the pasteboard and leaves no trace of why.
        if (!msg.data) break;
        try { clipWrite(b64decode(msg.data)).catch(() => {}); } catch (e) {}
        break;
      case "notify": handleNotify(msg); break;
      case "title": document.title = msg.title ? ("Cats Mux · " + msg.title) : "Cats Mux"; break;
      case "update_ready":
        updateInfo = { version: msg.version || "", command: msg.command || "" };
        gearEl.classList.add("badge");
        showUpdateBanner();
        break;
      case "error": toast("error: " + msg.msg); break;
      // The server pushes the full resolved palette on any theme change
      // (settings save here or elsewhere, catctl theme/reload, theme.save) so
      // every connected client restyles live, not on its next page load.
      case "theme": applyThemeInline(msg.colors || {}, msg.font || ""); break;
      case "shutdown": setStatus("server shut down", true); break;
      case "cmd_result": {
        const cb = cmdCbs.get(msg.id);
        if (cb) { cmdCbs.delete(msg.id); cb(msg); }
        else if (!msg.ok) toast("cmd failed: " + (msg.error || "unknown"));
        break;
      }
      case "chat_state": chatApplyState(msg); break;
      case "chat_snapshot": chatApplySnapshot(msg); break;
      case "chat_row": chatUpsertRow(msg.row); break;
      case "chat_delta": chatAppendDelta(msg.id, msg.text); break;
      case "chat_perm": chatPermUpdate(msg); break;
    }
  }

  function sameScroll(a, b) {
    if (!a || !b) return a === b;
    return a.off === b.off && a.max === b.max && a.rows === b.rows;
  }

  function b64decode(s) {
    try { return decodeURIComponent(escape(atob(s))); } catch (e) { return atob(s); }
  }

  // ---- Send helpers ----
  function sendMsg(m) { if (ws && ws.readyState === 1) ws.send(JSON.stringify(m)); }
  function sendCmd(name, params) { sendMsg({ t: "cmd", name, params }); }

  // followLink handles a click on an OSC 8 hyperlink. Everything the browser
  // can open, it opens; a file:// URI it hands to the session's editor instead.
  //
  // That second half is the whole point. A file:// link emitted by a compiler,
  // a test runner or a linter names a file on the machine the PANE is on, which
  // in a multi-host session is very often not this one — and even when it is,
  // a browser "opening" a source file means rendering it as text in a tab,
  // which is not what anybody clicking a stack trace wanted. pane.open_file
  // resolves the editor on that pane's own host and asks it.
  function followLink(pane, href) {
    const target = fileLinkTarget(href);
    if (!target) { window.open(href, "_blank", "noopener"); return; }
    sendCmd("pane.open_file", { path: target.path, line: target.line, pane: pane.id });
  }

  // fileLinkTarget picks a path and line out of a file:// URI, or returns null
  // for anything else. The line may be written #L42 (the GitHub spelling, which
  // is what most tools emit) or #42; a URI with neither opens the file wherever
  // the editor last left it.
  function fileLinkTarget(href) {
    if (!/^file:\/\//i.test(href)) return null;
    let u;
    try { u = new URL(href); } catch (_) { return null; }
    // A host component is a file on ANOTHER machine, which this side cannot
    // name — the path would be resolved against the editor's own disk and
    // silently open the wrong file, or none. Better to let the browser refuse
    // it visibly.
    if (u.host && u.host !== "localhost") return null;
    let path;
    try { path = decodeURIComponent(u.pathname); } catch (_) { path = u.pathname; }
    if (!path) return null;
    const m = /^#L?(\d+)$/.exec(u.hash || "");
    return { path, line: m ? parseInt(m[1], 10) : 0 };
  }

  // ---- Plain-text URLs (⌘-click) ----
  //
  // OSC 8 links are opt-in: the program marks the cell, the frame carries the
  // URI, and a plain click follows it (the mouseup path in attachMouse). Most
  // programs never emit OSC 8 — a server printing "listening on http://…" just
  // prints text. Those URLs are found by scanning the cells the client already
  // holds, and are followed only with the link modifier held, the convention
  // of Terminal.app, iTerm2, Ghostty and VS Code: a bare click on a pane means
  // focus / select / a click for the app inside, and stealing it whenever the
  // pointer happens to sit on something URL-shaped would break all three.
  //
  // The scan is entirely client-side. A server round trip (the §7 read) would
  // see soft-wrap flags the cells do not carry, but it would put latency on
  // every hover, and the heuristic below gets wrapped URLs right in practice.

  // linkModHeld reports whether the event carries the "follow link" modifier:
  // ⌘ on a Mac, Ctrl elsewhere. Not "either": on a Mac, Ctrl-click IS a right
  // click — it fires contextmenu, and the pane menu has to keep opening for it.
  function linkModHeld(ev) {
    const mac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || "");
    return mac ? !!ev.metaKey : !!ev.ctrlKey;
  }

  // URL_MAX_ROWS caps how many rows a wrapped URL is followed across, each way
  // from the hovered row. A token-bearing URL in an 80-column pane runs to two
  // or three rows; eight is generous without letting one hover walk a whole
  // screen of dense text.
  const URL_MAX_ROWS = 8;

  // plainUrlAt finds the URL in pane p's viewport that covers cell (x, y), or
  // returns null. The result names the URL and its first and last cell, as
  // indexes into p.cells, so a hover can underline exactly that span.
  //
  // Wrapping. The VT grid's soft-wrap flag does not ride the frame, so a
  // "logical line" is approximated the way most terminals' URL detectors do
  // it: a row whose LAST cell is non-blank is assumed to continue on the next.
  //
  //   row 3 │ first visit: http://127.0.0.1:7420/?token=12db11a9d96│ ← last cell filled
  //   row 4 │5ca2449b996bab0183e7aebb8aae8d98056d3                  │ ← so it joins
  //
  // The rows top..bot are joined into one string, with `at` mapping each
  // character back to its cell; the regex runs over the string and the match
  // covering the clicked character wins. A hard line that merely happens to
  // fill its row is joined too — harmless unless a URL ends exactly at the
  // right edge and the next line begins with URL-legal characters, which is
  // the price of not asking the server.
  function plainUrlAt(p, x, y) {
    const W = p.W, H = p.H, cells = p.cells;
    if (!W || !H || !cells || x < 0 || y < 0 || x >= W || y >= H) return null;
    const filled = (row) => {
      const c = cells[row * W + W - 1];
      return !!(c && c.s && c.s !== " ");
    };
    let top = y, bot = y;
    while (top > 0 && y - top < URL_MAX_ROWS && filled(top - 1)) top--;
    while (bot < H - 1 && bot - y < URL_MAX_ROWS && filled(bot)) bot++;

    // text is built per CODE UNIT so regex indexes map straight back: at[k]
    // is the cell that code unit k came from. A cell may hold a multi-unit
    // grapheme (an emoji); every unit of it maps to that one cell. A wide
    // glyph's spacer cell holds "" and contributes nothing, so a click on it
    // resolves to the glyph to its left. A missing cell reads as a blank.
    let text = "";
    const at = [];
    let hit = -1;
    for (let r = top; r <= bot; r++) {
      for (let cx = 0; cx < W; cx++) {
        const i = r * W + cx;
        const c = cells[i];
        const s = c ? (c.s || "") : " ";
        if (r === y && cx === x) hit = s ? text.length : text.length - 1;
        for (let k = 0; k < s.length; k++) at.push(i);
        text += s;
      }
    }
    if (hit < 0) return null;

    // Schemes are listed rather than matched generically (`\w+://`) so a
    // "Cargo.toml://" or a log line's "key://" is not a link; file:// is in
    // because followLink hands it to the pane's editor. The body stops at
    // whitespace, quotes, angle brackets, backticks and box drawing
    // (U+2500–U+257F) — a TUI framing a URL in │ … │ must not drag the border
    // into it.
    const re = new RegExp("\\b(?:https?|ftp|file)://[^\\s<>\"'`\\u2500-\\u257f]+", "gi");
    let m;
    while ((m = re.exec(text)) !== null) {
      const start = m.index;
      if (start > hit) break; // matches run left to right; the rest are past the click
      const url = trimUrlTail(m[0]);
      const end = start + url.length; // exclusive
      if (hit < end && url.length > url.indexOf("://") + 3) {
        return { url, a: at[start], b: at[end - 1] };
      }
    }
    return null;
  }

  // trimUrlTail drops what prose puts after a URL but the URL does not own:
  // sentence punctuation ("see http://x/.") and a closing bracket or quote
  // that has no opener inside the URL ("(at http://x/)"). A balanced closer
  // stays — Wikipedia's "Go_(programming_language)" is the classic case.
  function trimUrlTail(u) {
    const pairs = { ")": "(", "]": "[", "}": "{" };
    const count = (s, ch) => s.split(ch).length - 1;
    for (;;) {
      const last = u[u.length - 1];
      if (".,;:!?'\"*".indexOf(last) >= 0) { u = u.slice(0, -1); continue; }
      const open = pairs[last];
      if (open && count(u, last) > count(u, open)) { u = u.slice(0, -1); continue; }
      return u;
    }
  }

  // linkAt is what a ⌘-click at cell (x, y) would follow: an OSC 8 link on
  // that cell first (the program said exactly what it means), else a
  // plain-text URL found around it. Returns { url, a, b } or null; an OSC 8
  // link's span is just its own cell, which is enough for a pointer cursor.
  function linkAt(p, x, y) {
    if (!p.cells || !p.W) return null;
    const i = y * p.W + x;
    const c = p.cells[i];
    if (c && c.h && p.links && p.links[c.h - 1]) return { url: p.links[c.h - 1], a: i, b: i };
    return plainUrlAt(p, x, y);
  }

  // sendCmdAwait issues a command under a fresh id and resolves cb with its
  // cmd_result (read/capture and the §7 query commands carry result data).
  let cmdSeq = 0;
  const cmdCbs = new Map();
  function sendCmdAwait(name, params, cb) {
    const id = "b" + (++cmdSeq);
    cmdCbs.set(id, cb);
    sendMsg({ t: "cmd", id, name, params });
  }

