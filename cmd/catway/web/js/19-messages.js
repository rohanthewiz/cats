  // ---- Down messages ----
  function onMessage(msg) {
    switch (msg.t) {
      case "welcome":
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
      case "layout": applyLayout(msg); break;
      // The census: how many clients, and what each of them is looking at. Only
      // the sidebar reads it, so a re-render of the workspace list is the whole
      // reaction.
      case "clients":
        clientsMsg = msg;
        if (layoutMsg) renderWorkspaces(layoutMsg);
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
        if (msg.cells) for (const dc of msg.cells) p.cells[dc.i] = dc;
        if (msg.cur) p.cur = msg.cur;
        // Every frame carries current scroll, absent ⇒ no scrollback: reset, don't
        // retain (a stale max misplaces read coords after the buffer shrinks, e.g. clear).
        p.scroll = msg.scroll || null;
        scheduleDraw(p); break;
      }
      case "pane_title": { const p = pane(msg.pane); p.title = msg.title; renderChrome(p); refreshPaneList(); break; }
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
        renderChrome(p); refreshPaneList(); break;
      }
      case "pane_modes": {
        const p = pane(msg.pane); p.modes = { mouse: !!msg.mouse, alt: !!msg.alt_screen, kitty: msg.kitty | 0 };
        break;
      }
      case "pane_exited": { const p = pane(msg.pane); p.exited = msg.code; renderChrome(p); refreshPaneList(); break; }
      // The inverse: the pane's PTY came back (cathost restart, host move), so
      // the red header comes off. Needed because the exit is REMEMBERED here —
      // a live pane's chrome simply omits pane_exited, which retracts nothing.
      case "pane_respawned": { const p = pane(msg.pane); p.exited = null; renderChrome(p); refreshPaneList(); break; }
      case "agents": renderAgents(msg.items); break;
      case "hosts": renderHosts(msg.items); break;
      case "history": renderHistory(msg.entries || []); break;
      // The macro recorder is session state, not this window's: the server
      // pushes it on every transition, on every captured step, and once in the
      // connect burst, so the indicator is right no matter who armed it.
      case "record": applyRecord(msg); break;
      case "usage": renderUsage(msg); break;
      case "clipboard": // OSC 52 write from a pane app — no user activation
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

  // sendCmdAwait issues a command under a fresh id and resolves cb with its
  // cmd_result (read/capture and the §7 query commands carry result data).
  let cmdSeq = 0;
  const cmdCbs = new Map();
  function sendCmdAwait(name, params, cb) {
    const id = "b" + (++cmdSeq);
    cmdCbs.set(id, cb);
    sendMsg({ t: "cmd", id, name, params });
  }

