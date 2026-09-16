  // ---- Peers dialog: sync this backend with another catway ----
  //
  // Three surfaces, all over the §7 peer.* commands:
  //
  //   openPeersDialog   the roster (peer.list) with a "sync…" verb per row,
  //                     "forget" on the row's danger side, and "add…"
  //   peerSyncDialog    what to sync (workspaces / todos / plugins) and which
  //                     way (both / pull / push), then peer.sync
  //   peerReportDialog  the report: totals per side, then every item that
  //                     synced, was skipped, or failed — with its reason
  //
  // The sync request is one cmd_result, so the dialog shows "syncing…" until
  // the report lands; a peer that installs plugins can take a minute, and the
  // reply is the only progress there is. Nothing here interprets the report:
  // the server rendered its lines, the dialog draws its items, and both say
  // the same thing.

  function peerHasSelection(sel) { return sel.workspaces || sel.todos || sel.plugins; }

  // peerCheck builds one labelled checkbox row. Plain elements rather than
  // dialogFields' text/select fields: a choice that is on or off wants a box,
  // and three of them side by side read as the option set they are.
  function peerCheck(label, checked, hint) {
    const wrap = document.createElement("label"); wrap.className = "check";
    const box = document.createElement("input"); box.type = "checkbox"; box.checked = !!checked;
    const txt = document.createElement("span"); txt.textContent = label;
    wrap.appendChild(box); wrap.appendChild(txt);
    if (hint) { const h = document.createElement("span"); h.className = "sub"; h.textContent = hint; wrap.appendChild(h); }
    return { el: wrap, box };
  }

  // peerSyncDialog asks what to move and which way, then runs peer.sync and
  // hands the answer to peerReportDialog. The selection is remembered per
  // page load: a user who syncs todos with "home" every morning should not
  // have to tick the box every time.
  const peerSyncLast = { workspaces: true, todos: true, plugins: false, direction: "both" };
  function peerSyncDialog(peer) {
    openOverlay((ov) => {
      const m = document.createElement("div"); m.className = "modal peers-sync";
      const h = document.createElement("header"); h.textContent = "sync with " + (peer.label || peer.id); m.appendChild(h);
      const body = document.createElement("div"); body.className = "body";
      const url = document.createElement("div"); url.className = "hint"; url.textContent = peer.url; body.appendChild(url);
      const ws = peerCheck("workspaces", peerSyncLast.workspaces, "where the same folder exists on both machines");
      const td = peerCheck("todos", peerSyncLast.todos, "cats-todo backlogs, merged — nothing overwritten");
      const pl = peerCheck("plugins", peerSyncLast.plugins, "installs what the other side has, from its source");
      for (const c of [ws, td, pl]) body.appendChild(c.el);
      const dirWrap = document.createElement("div"); dirWrap.className = "field";
      const dirLbl = document.createElement("label"); dirLbl.textContent = "direction"; dirWrap.appendChild(dirLbl);
      const dir = document.createElement("select");
      for (const [v, l] of [["both", "both — pull theirs here, then push mine there"], ["pull", "pull — only bring theirs here"], ["push", "push — only send mine there"]]) {
        const o = document.createElement("option"); o.value = v; o.textContent = l; dir.appendChild(o);
      }
      dir.value = peerSyncLast.direction;
      dirWrap.appendChild(dir); body.appendChild(dirWrap);
      if (!peer.has_token) {
        const warn = document.createElement("div"); warn.className = "hint warn";
        warn.textContent = "this peer has no token_file — the sync will be refused; add one in config.yaml (peers:)";
        body.appendChild(warn);
      }
      const note = document.createElement("div"); note.className = "hint";
      note.textContent = "A sync only adds: workspaces, backlog rows and plugins the other side has. Nothing is deleted on either side.";
      body.appendChild(note);
      m.appendChild(body);
      const btns = document.createElement("div"); btns.className = "btns";
      btns.appendChild(mkModalBtn("cancel", "", closeModal));
      const go = mkModalBtn("sync", "primary", () => {
        const sel = { workspaces: ws.box.checked, todos: td.box.checked, plugins: pl.box.checked, direction: dir.value };
        if (!peerHasSelection(sel)) { toast("pick at least one of workspaces, todos, plugins"); return; }
        Object.assign(peerSyncLast, sel);
        go.disabled = true; go.textContent = "syncing…";
        sendCmdAwait("peer.sync", { peer: peer.id, ...sel }, (res) => {
          if (!res.ok) { go.disabled = false; go.textContent = "sync"; toast("sync: " + (res.error || "unknown")); return; }
          peerReportDialog(peer, res.data || {});
        });
      });
      btns.appendChild(go);
      m.appendChild(btns);
      m.addEventListener("keydown", (e) => {
        e.stopPropagation();
        if (e.key === "Escape") { e.preventDefault(); closeModal(); }
        if (e.key === "Enter" && !go.disabled) { e.preventDefault(); go.click(); }
      });
      ov.appendChild(m);
      focusField(ws.box, false);
    });
  }

  // peerReportDialog draws a peer.sync result. Two summary lines (this side,
  // the peer's), then the items — synced first, then skipped and failed with
  // their reasons, which is what the user opened the report to find. The
  // unchanged items are a count only: a list of things that were already fine
  // is noise. "copy" puts the server's rendered lines on the clipboard, the
  // same text `catctl sync` prints.
  function peerReportDialog(peer, rep) {
    openOverlay((ov) => {
      const m = document.createElement("div"); m.className = "modal pal peers-report";
      const h = document.createElement("header");
      h.textContent = "sync report — " + (peer.label || peer.id) + (rep.remote ? " (" + rep.remote + ")" : "");
      m.appendChild(h);
      const body = document.createElement("div"); body.className = "body";
      if (rep.error) {
        const e = document.createElement("div"); e.className = "hint err"; e.textContent = rep.error; body.appendChild(e);
      }
      const items = rep.items || [];
      const sides = [["here", "here"], ["peer", "on " + (peer.label || peer.id)]];
      for (const [side, label] of sides) {
        const mine = items.filter((it) => it.side === side);
        if (!mine.length && !(rep.direction === "both" || (side === "here" && rep.direction === "pull") || (side === "peer" && rep.direction === "push"))) continue;
        const counts = { synced: 0, unchanged: 0, skipped: 0, failed: 0 };
        for (const it of mine) counts[it.status] = (counts[it.status] || 0) + 1;
        const sum = document.createElement("div"); sum.className = "side";
        sum.textContent = label + ": " + counts.synced + " synced, " + counts.unchanged + " unchanged, " +
          counts.skipped + " skipped, " + counts.failed + " failed";
        body.appendChild(sum);
        const list = document.createElement("div"); list.className = "list";
        for (const st of ["synced", "skipped", "failed"]) {
          for (const it of mine) {
            if (it.status !== st) continue;
            const row = document.createElement("div"); row.className = "row " + st;
            const kind = document.createElement("span"); kind.className = "kind"; kind.textContent = st; row.appendChild(kind);
            const lbl = document.createElement("span"); lbl.className = "lbl"; lbl.textContent = it.kind + " · " + it.name; row.appendChild(lbl);
            if (it.detail) { const sub = document.createElement("span"); sub.className = "sub"; sub.textContent = it.detail; row.appendChild(sub); }
            row.title = [it.kind, it.name, it.detail].filter(Boolean).join("\n");
            list.appendChild(row);
          }
        }
        if (!list.childElementCount) {
          const e = document.createElement("div"); e.className = "empty";
          e.textContent = mine.length ? "nothing to do — already in sync" : "nothing applied";
          list.appendChild(e);
        }
        body.appendChild(list);
      }
      m.appendChild(body);
      const btns = document.createElement("div"); btns.className = "btns";
      btns.appendChild(mkModalBtn("copy", "", () => {
        const text = (rep.lines || []).join("\n");
        if (navigator.clipboard && navigator.clipboard.writeText) {
          navigator.clipboard.writeText(text).then(() => toast("report copied"), () => toast("copy failed"));
        } else { toast("clipboard unavailable"); }
      }));
      btns.appendChild(mkModalBtn("sync again…", "", () => peerSyncDialog(peer)));
      btns.appendChild(mkModalBtn("close", "primary", closeModal));
      m.appendChild(btns);
      m.addEventListener("keydown", (e) => {
        e.stopPropagation();
        if (e.key === "Escape" || e.key === "Enter") { e.preventDefault(); closeModal(); }
      });
      ov.appendChild(m);
    });
  }

  // peerAddDialog collects one peers: entry — the config file's fields in the
  // order an operator fills them. The token is a FILE path, never typed here:
  // the settings modal rewrites config.yaml wholesale, so a literal secret in
  // it is one commit away from being published (the same reason the hosts
  // dialog takes a token file).
  function peerAddDialog() {
    dialogFields({
      title: "add peer",
      submitLabel: "add",
      hint: "url: the other cats' browser address (https://box.lan:8421). token file: a file holding that cats' " +
        "CATS_PASSWORD. fingerprint: its self-signed certificate's SHA-256, from its startup log — required for https unless it has a real certificate.",
      fields: [
        { label: "id", placeholder: "home" },
        { label: "url", placeholder: "https://mini.lan:8421" },
        { label: "token file", placeholder: "~/.config/cats/peers/home.token" },
        { label: "fingerprint", placeholder: "(optional) pinned SHA-256" },
        { label: "label", placeholder: "(optional — defaults to the id)" },
      ],
      onSubmit: (id, url, tokenFile, fingerprint, label) => {
        id = (id || "").trim(); url = (url || "").trim();
        if (!id || !url) { toast("a peer needs an id and a url"); return; }
        sendCmdAwait("peer.attach", {
          id, url, label: (label || "").trim(),
          token_file: (tokenFile || "").trim(), fingerprint: (fingerprint || "").trim(),
        }, (res) => {
          if (!res.ok) { toast(res.error || "add peer failed"); return; }
          toast("added peer " + id);
          openPeersDialog();
        });
      },
    });
  }

  function confirmForgetPeer(p) {
    dialogConfirm({
      title: "forget peer",
      message: "Forget “" + p.id + "” (" + p.url + ")? Only the roster entry is removed — nothing that was synced is undone.",
      confirmLabel: "forget", danger: true,
      onConfirm: () => sendCmdAwait("peer.detach", { id: p.id }, (res) => {
        if (!res.ok) { toast(res.error || "forget failed"); return; }
        toast("forgot " + p.id);
        openPeersDialog();
      }),
    });
  }

  function openPeersDialog() {
    sendCmdAwait("peer.list", {}, (res) => {
      if (!res.ok) { toast("peers: " + (res.error || "unknown")); return; }
      const peers = (res.data && res.data.peers) || [];
      openOverlay((ov) => {
        const m = document.createElement("div"); m.className = "modal pal peers";
        const h = document.createElement("header"); h.textContent = "peers"; m.appendChild(h);
        const listEl = document.createElement("div"); listEl.className = "list"; m.appendChild(listEl);
        if (!peers.length) {
          const e = document.createElement("div"); e.className = "empty";
          e.textContent = "no peers yet — a peer is another cats whose workspaces, todos and plugins you sync with this one";
          listEl.appendChild(e);
        }
        for (const p of peers) {
          const row = document.createElement("div"); row.className = "row";
          row.title = p.url + (p.fingerprint ? "\npinned " + p.fingerprint : "") + (p.has_token ? "" : "\nno token configured");
          const lbl = document.createElement("span"); lbl.className = "lbl";
          lbl.textContent = p.id + (p.label && p.label !== p.id ? " — " + p.label : "");
          row.appendChild(lbl);
          const sub = document.createElement("span"); sub.className = "sub"; sub.textContent = p.url; row.appendChild(sub);
          const acts = document.createElement("div"); acts.className = "acts";
          const actBtn = (label, cls, fn, tip) => {
            const b = document.createElement("button");
            b.textContent = label; if (cls) b.className = cls; if (tip) b.title = tip;
            b.addEventListener("click", fn);
            acts.appendChild(b);
          };
          actBtn("sync…", "", () => peerSyncDialog(p), p.has_token ? "" : "no token configured — the sync will be refused");
          actBtn("forget", "danger", () => confirmForgetPeer(p));
          row.appendChild(acts);
          listEl.appendChild(row);
        }
        const btns = document.createElement("div"); btns.className = "btns";
        btns.appendChild(mkModalBtn("close", "", closeModal));
        btns.appendChild(mkModalBtn("add…", "primary", peerAddDialog));
        m.appendChild(btns);
        m.addEventListener("keydown", (e) => {
          e.stopPropagation();
          if (e.key === "Escape") { e.preventDefault(); closeModal(); }
        });
        ov.appendChild(m);
      });
    });
  }
