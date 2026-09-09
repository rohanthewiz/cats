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

  // hideTip is the pointer's own teardown: the card goes, the row it was about
  // gets its native tooltips back, and a dwell that never earned its card is
  // dropped. It deliberately leaves the warm window standing (see TIP_WARM_MS)
  // — the hand is still on the list, so the next row it reaches is part of the
  // same read rather than a fresh arrival.
  function hideTip() {
    if (paneTipEl.classList.contains("show")) tipWarmUntil = Date.now() + TIP_WARM_MS;
    cancelTip(); paneTipEl.classList.remove("show"); restoreTitles();
  }

  // dropTip is hideTip for everything that is NOT the pointer moving on — the
  // window going to the background, the tab being hidden, a key being pressed,
  // a menu or a dialog opening. Each of those means the hand has left the list
  // entirely, so the warm window goes down with the card: coming back should
  // pay the dwell again rather than find a card waiting the instant the pointer
  // lands. Cleared after hideTip, since hideTip is what sets it.
  function dropTip() { hideTip(); tipWarmUntil = 0; }

  // ---- Dwell before the card, warm window after it ----
  //
  // The card used to open on the first mousemove over a row, which made a pass
  // through the sidebar a run of popups: crossing WORKSPACES on the way to the
  // PANES list built and tore down a card per row, each one landing under the
  // pointer and covering the rows still to be crossed. A short dwell is what
  // separates "the pointer is on its way past this row" from "the pointer is
  // asking about this row", and it is the same bargain the native tooltips the
  // card replaced (see muteTitles) always made.
  //
  // The dwell is only ever paid on the way *in*. Once a card is up, moves
  // within the row go straight through, so the card keeps riding the pointer
  // and keeps re-reading live state at the frame rate it always did.
  //
  // The warm window is the other half of that bargain. One dwell per row is the
  // right price for a pointer arriving from somewhere else and the wrong one
  // for a pointer already reading the list: comparing two workspaces, or
  // walking the PANES list looking for a flag's note, would mean holding still
  // over every row in turn. So for TIP_WARM_MS after a card comes down, the
  // next row's card opens on contact.
  //
  //   pointer  ──past──▶│ row A │──── rest ────▶│ row B │──▶│ row C │──▶
  //   card               ·       400ms           ███████    ███████
  //                    (nothing) dwell           opens on   still warm:
  //                                              the dwell  opens at once
  //
  // Only the pointer keeps the window warm. Every other way of leaving the list
  // clears it (see dropTip), because a card that opens on contact when the hand
  // comes back from a keyboard or another app is exactly the eagerness the
  // dwell was added to fix.
  const TIP_DELAY_MS = 400;
  const TIP_WARM_MS = 800;
  let tipTimer = null;  // the pending show, or null when nothing is waiting
  let tipArmed = null;  // {ev, show} — what that timer will run when it fires
  let tipWarmUntil = 0; // Date.now() past which the next card pays the dwell again

  // armTip defers show(ev) until the pointer has rested TIP_DELAY_MS — unless a
  // card is already up, or one came down recently enough that the warm window
  // is still open, in which cases it shows straight away.
  //
  // The event itself cannot be kept: currentTarget is nulled once dispatch
  // ends, so the deferred call gets a plain snapshot of the three fields the
  // card builders read (the position to place at, and the row to build from and
  // mute). The snapshot is refreshed by every move within the row *without*
  // restarting the clock, so the card is placed where the pointer came to rest
  // while the dwell is still measured from when the row was entered — a hand
  // that drifts a cell or two would otherwise never wait long enough anywhere.
  function armTip(e, show) {
    const ev = { clientX: e.clientX, clientY: e.clientY, currentTarget: e.currentTarget };
    if (paneTipEl.classList.contains("show") || Date.now() < tipWarmUntil) { show(ev); return; }
    if (tipArmed && tipArmed.ev.currentTarget === ev.currentTarget) { tipArmed.ev = ev; return; }
    cancelTip(); // a different row: its wait starts over
    tipArmed = { ev, show };
    tipTimer = setTimeout(() => {
      const a = tipArmed;
      tipTimer = null; tipArmed = null;
      // The row can have gone during the wait: these lists are rebuilt under a
      // stationary pointer on every rollup, and a removed node dispatches no
      // mouseleave to cancel with. Building from it would place a card
      // describing a row that is no longer on screen and strip title
      // attributes off marks nobody can reach. The replacement row arms its own
      // wait from the mouseenter it gets on arrival, so nothing is lost.
      if (a.ev.currentTarget && !a.ev.currentTarget.isConnected) return;
      a.show(a.ev);
    }, TIP_DELAY_MS);
  }

  // cancelTip drops a wait that never earned its card. Called from hideTip, so
  // every teardown path — mouseleave, mousedown, a row that stopped qualifying,
  // and everything routed through dropTip below — also disarms a pending one:
  // a card must never open after the pointer, or the hand, has already left.
  function cancelTip() {
    if (tipTimer) clearTimeout(tipTimer);
    tipTimer = null; tipArmed = null;
  }

  // ---- Teardown that is not the pointer ----
  //
  // Every way the card came down used to be a pointer event on the row itself:
  // mouseleave, mousedown, a row that stopped qualifying. That covers the hand
  // moving on and nothing else, so any way of leaving the sidebar that does not
  // move the pointer left the card standing — and, being fixed to the viewport
  // above everything else, standing over whatever came next:
  //
  //   • the window going to the background (⌘-tab, another app taking the
  //     front, the mac app losing key). No mouseleave is dispatched for a
  //     pointer that never moved, so the card was still there on the way back,
  //     describing a row whose flag or agent state had moved on without it.
  //   • the tab being hidden, which is the same thing by another route.
  //   • the hand going back to the keyboard. In catway that means typing into a
  //     pane with the pointer parked on the sidebar — the card then sits over
  //     the list the keys are moving through, which is the failure cats-todo's
  //     card already answers by clearing on any keystroke.
  //   • a context menu or a dialog opening from a key, or from a press on a row
  //     that has no mousedown teardown of its own.
  //
  // The context menu has closed itself on window blur since it was written
  // (28-ctxmenu.js); the card is the same kind of transient floating surface
  // and now keeps the same promise. All of these go through dropTip rather than
  // hideTip: they are the hand leaving, not the pointer travelling, so the warm
  // window closes with the card.
  window.addEventListener("blur", dropTip);
  document.addEventListener("visibilitychange", () => { if (document.hidden) dropTip(); });
  // Capture phase, so the card is gone before the key reaches 20-keys.js — that
  // handler preventDefaults and returns early on a good many chords, and which
  // branch a key takes is no business of the card's.
  window.addEventListener("keydown", dropTip, true);
  // A null relatedTarget means the pointer left the document itself rather than
  // crossing into another element — the one departure a row cannot see for
  // itself, since it gets no mouseleave when the pointer exits the window over
  // the row's own edge. hideTip, not dropTip: the pointer is still what is
  // doing the leaving, and a hand that slips off the window edge and comes
  // straight back is mid-read.
  document.addEventListener("mouseout", (e) => { if (!e.relatedTarget) hideTip(); });

  // ---- Native tooltips vs. the hover card ----
  //
  // Every mark inside a row carries a title attribute — the flag's note, the
  // paw print's count, the lock's meaning — and for a row that never opens a
  // card that attribute is the whole story, so it stays. But once a card *is*
  // up, the browser's own tooltip is a second popup, a second later, saying a
  // subset of what the card already says in full, in its own box wherever the
  // pointer happens to sit — two competing surfaces for one row. So the row
  // that owns the visible card gives up its titles for exactly as long as it
  // owns it, and gets them back the moment the card goes.
  //
  // The old value is stashed on the node itself (data-tiptitle) rather than in
  // a map keyed by element: these lists are rebuilt under a stationary pointer
  // on every rollup, and an attribute travels with the node and dies with it,
  // so a record can never outlive the row it describes. Restoring into a row
  // that has since been replaced is therefore harmless — it writes to a
  // detached node nobody will see again.
  let mutedRow = null;

  // titleNodes: the element and its descendants that carry attr. The root is
  // included as well as its marks, since a row that titles itself competes with
  // the card exactly as a glyph inside it does.
  function titleNodes(el, attr) {
    const out = el.hasAttribute(attr) ? [el] : [];
    for (const n of el.querySelectorAll("[" + attr + "]")) out.push(n);
    return out;
  }

  // muteTitles takes the row's native tooltips away. Idempotent per row, since
  // the hover handlers run on every mousemove: re-muting an already-muted row
  // would otherwise stash the empty string over the real title and give the
  // row back a blank tooltip.
  function muteTitles(el) {
    if (!el || el === mutedRow) return;
    restoreTitles();
    for (const n of titleNodes(el, "title")) {
      n.dataset.tiptitle = n.getAttribute("title");
      n.removeAttribute("title");
    }
    mutedRow = el;
  }

  // restoreTitles is called from hideTip, so "no card showing" and "titles
  // live" are the same state no matter which way the card went away —
  // mouseleave, mousedown, or a row that stopped qualifying for a card.
  function restoreTitles() {
    if (!mutedRow) return;
    for (const n of titleNodes(mutedRow, "data-tiptitle")) {
      n.setAttribute("title", n.dataset.tiptitle);
      delete n.dataset.tiptitle;
    }
    mutedRow = null;
  }

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
    // The hover card is the one surface with room for the note in full — every
    // other place it appears is a tooltip on a 12px glyph — so it gets its own
    // row rather than being folded into the flag's label.
    if (row.flag) {
      items.push(["Flag", flagGlyph(row.flag) + " " + flagLabel(row.flag)
        + (row.flag.at ? " · " + fmtAge(Date.now() - row.flag.at) : "")]);
      items.push(["Note", row.flag.note]);
    }
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
    // The card is the row's tooltip now — same bargain the workspace rows make
    // (see muteTitles): the flag's note is already spelled out here in full, so
    // the flag mark's own title would only repeat a slice of it, a second later,
    // in a second box. Unconditional here where it is conditional there, since
    // a pane row always opens a card.
    muteTitles(e.currentTarget);
  }


  // joinTrunc packs a list of short strings into ONE tooltip row: entries joined
  // with ", " until the character budget runs out, then "+N more" for the rest.
  // Budgeted in characters rather than pixels because the row is only ever
  // measured after it is in the DOM, and by then a too-long list has already
  // wrapped — #panetip's max-width would turn a "one row" list into four. ~64
  // chars is what fits the 340px card at 12px in one line, with the "+N more"
  // tail always kept so the row never lies about how much it is showing.
  function joinTrunc(parts, budget) {
    if (!parts.length) return "";
    const max = budget || 64;
    let out = "", shown = 0;
    for (const p of parts) {
      const next = out ? out + ", " + p : p;
      // Always take the first entry, however long: a row reading only "+3 more"
      // says less than a truncated name does.
      if (shown && next.length > max) break;
      out = next; shown++;
    }
    if (shown < parts.length) out += " +" + (parts.length - shown) + " more";
    return out;
  }

  // ---- Workspace hover card ----
  //
  // The WORKSPACES rows carry their extra state as glyphs — a flag, a lock, a
  // paw print with a count — each of which is at most a 12px mark with a title
  // attribute. That is enough to notice something, and not enough to read it:
  // the flag's note, and *which* panes inside the workspace hold the unfinished
  // todos, have nowhere to be said. So a row with any of that gets the same
  // multi-row card the PANES rows get, built from the same showTip primitive.
  //
  // The card is deliberately conditional (workspaceTipItems returns null for a
  // plain row). A workspace with no annotation has nothing the row does not
  // already say in full, and a popup that appears over every row on the way to
  // somewhere else is noise. Two things qualify a row:
  //
  //   annotations — the workspace's own flag, plus any flags pinned to panes
  //                 inside it, which the workspace row otherwise never shows
  //   todos       — the paw print's count, expanded into which panes owe what
  //
  // Everything else in the card (host, lock, agent states) is context that only
  // rides along once one of those has already earned the popup.
  function workspaceTipItems(w) {
    const f = flagOf(w);
    const todos = wsTodoPanes(w.id);
    const flagged = wsFlaggedPanes(w.id);
    const parked = w.parked || [];
    // A sleeping row earns the card too: the moon says "asleep", and only the
    // card has room to say what a wake brings back.
    if (!f && !todos.length && !flagged.length && !w.asleep) return null;

    const items = [["Workspace", w.name || w.id, "pub"]];
    if (f) {
      items.push(["Flag", flagGlyph(f) + " " + flagLabel(f)
        + (f.at ? " · " + fmtAge(Date.now() - f.at) : "")]);
      // The note is why the flag was pinned; with no note the vocabulary's own
      // meaning ("come back to this") stands in, so the row still explains the
      // glyph to someone who did not pin it.
      const d = flagDef(f);
      items.push(["Note", f.note || (d ? d.meaning : "")]);
    }
    // One row, comma separated: "cats:p3 ×2, cats:p7 ×1". The pane handle rather
    // than the todo title because the handle is what you act on — it is the
    // pane to go read the list in — and because every one of these titles begins
    // with the same word.
    if (todos.length) {
      const total = todos.reduce((n, t) => n + t.n, 0);
      items.push([total === 1 ? "1 todo" : total + " todos",
        joinTrunc(todos.map((t) => t.ref + " ×" + t.n)), "oneline"]);
    }
    // Pane flags, one row each up to a few, since the note is the whole point of
    // showing them and notes do not survive being packed into a shared row. Past
    // that the count stands in — a workspace with a dozen flagged panes is a
    // question for the PANES list, not for a hover card.
    const MAXF = 4;
    flagged.slice(0, MAXF).forEach((x, i) => {
      const note = x.flag.note || flagLabel(x.flag);
      items.push([i ? "" : "Flagged", flagGlyph(x.flag) + " " + x.ref + " — " + note]);
    });
    if (flagged.length > MAXF) items.push(["", "+" + (flagged.length - MAXF) + " more flagged panes"]);

    if (multiHost() && w.host) items.push(["Host", "@" + hostLabel(w.host)]);
    if (w.locked) items.push(["Locked", "no plugins or agents here"]);
    if (w.asleep) {
      items.push(["Asleep", "nothing running — click to wake"]);
      // One row, comma separated, like the todos: "claude (w2:p3), codex (w2:p5)".
      // The pane handle is history — the pane is gone — but it is how the user
      // remembers which conversation this was.
      if (parked.length) {
        items.push([nOf(parked.length, "parked agent"),
          joinTrunc(parked.map((p) => p.agent + (p.pane ? " (" + p.pane + ")" : ""))), "oneline"]);
      }
    }
    // The agent rollup the row shows as "●2 ●1", spelled out.
    const c = agentStateCounts().get(w.id);
    if (c) {
      const parts = [];
      for (const st of ["blocked", "done", "working", "idle"]) {
        if (c[st]) parts.push(c[st] + " " + (st === "done" ? "done (unseen)" : st));
      }
      if (parts.length) items.push(["Agents", parts.join(", ")]);
    }
    const wins = windowsOnWorkspace(w.id);
    if (wins) items.push(["Windows", "also open in " + wins + (wins === 1 ? " other window" : " other windows")]);
    return items;
  }

  // showWorkspaceTip is the hover handler itself: rebuilt on every move, since
  // the flags, the todo counts and the agent states all change underneath a
  // stationary pointer.
  function showWorkspaceTip(e, w) {
    const items = workspaceTipItems(w);
    // A row that earns no card keeps its glyph tooltips — they are all it has.
    // hideTip restores them, so a row that loses its flag while hovered hands
    // its titles back rather than staying silent until the pointer leaves.
    if (!items) { hideTip(); return; }
    showTip(e, items);
    // The card is the row's tooltip now: silence the browser's, so the flag
    // note and the todo counts are said once, in the card, and not again in a
    // box that opens over it a second later.
    muteTitles(e.currentTarget);
  }
