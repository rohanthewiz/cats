  // ---- Chrome: per-pane header (styled HTML, WS8) ----
  // The identity line — pane id · title · path · branch · agent · agent state —
  // followed by the flexible gap the CSS opens, and then the pane toolbar at the
  // right edge. Every field is its own span with its own class, so the row can
  // colour them apart and decide which ones give up width (see the .info rules).
  //
  // Plus the pane-scoped mode chips: keyboard copy-mode replaces the identity
  // while active — its hints need the room, and the mode is what you need to
  // know about the pane just then — and a zoomed pane carries a clickable ZOOM
  // chip.
  function renderChrome(p) {
    p.chromeInfo.innerHTML = "";
    p.el.classList.toggle("dead", p.exited !== null && p.exited !== undefined);
    const add = (cls, text) => {
      const s = document.createElement("span");
      if (cls) s.className = cls;
      s.textContent = text;
      p.chromeInfo.appendChild(s);
      return s;
    };
    // seg() owns the separators so no field has to know its own position: the
    // dot is emitted between two fields that are actually present, which is what
    // keeps a pane with no title (or no repo, or no agent) from showing a run of
    // orphaned dots. Empty text drops the field and its separator together.
    let placed = 0;
    const seg = (cls, text) => {
      if (!text) return null;
      if (placed++) add("sep", "·");
      return add(cls, text);
    };
    seg("pub", paneRef(p.pub, p.id));
    // The user's flag, immediately after the handle and before anything the
    // terminal reports. It is the one field in this row nobody but the user put
    // there, and it is the reason they are looking at this pane rather than
    // another. It survives copy mode as well, for the same reason: a "come back
    // to this" that vanishes while you are selecting the thing you came back for
    // is a reminder that hides exactly when it is being acted on.
    //
    // Unlike the sidebar's marks this one carries the note inline — the header
    // has the width, and this is the pane you are actually looking at, so the
    // note is worth reading without a hover. The CSS truncates it.
    const pflag = flagOf(p.info);
    if (pflag) {
      const fs = seg("pflag " + (FLAG_BY_KIND.has(pflag.kind) ? "fk-" + pflag.kind : "fk-custom"),
        flagGlyph(pflag) + (pflag.note ? " " + pflag.note : ""));
      if (fs) {
        fs.title = flagTitle(pflag) + " — click to change or clear";
        // stopPropagation on the mousedown so the header's own press handler
        // (focus + swap drag) does not start a drag out from under the menu.
        fs.addEventListener("mousedown", (e) => e.stopPropagation());
        fs.addEventListener("click", (e) => {
          e.stopPropagation();
          openCtx(e.clientX, e.clientY, flagMenuItems(paneFlagTarget(p.id)));
        });
      }
    }
    if (p.cm) {
      seg("mode", "COPY");
      seg("hint", "hjkl/arrows move · v select · r rect · y copy · Esc exit");
    } else {
      // Which machine, before what is on it: the host is the outermost
      // coordinate of the identity, and the path below is only meaningful once
      // you know whose filesystem it names. Drawn only in a multi-host session,
      // and turned red while its host is disconnected — that is the header's
      // whole explanation for a pane that has stopped moving. Copy mode's branch
      // above deliberately omits it: the hints need that room, and the machine
      // is not what you are deciding about while selecting text.
      if (multiHost() && p.info && p.info.host) {
        const hs = seg("host", "@" + hostLabel(p.info.host));
        if (hs && !hostUp(p.info.host)) {
          hs.classList.add("off");
          hs.title = hostLabel(p.info.host) + " is not connected";
        }
      }
      seg("ttl", p.title);
      // Path then branch, reading outside-in: the path says which tree, the
      // branch says where in it — the same order a prompt puts them in, and the
      // order that keeps the two panes-in-sibling-worktrees case legible, since
      // the differing path segment and the branch end up side by side.
      seg("cwd", shortPath(p.cwd));
      // "@a1b2c3d" is gitBranch's spelling for a detached HEAD (see the server's
      // gitbranch.go); the leading @ is the only thing that can't be a branch
      // name's first character in what it sends, so it doubles as the flag.
      const br = seg("branch", p.branch);
      if (br && p.branch[0] === "@") br.classList.add("detached");
      // Agent and state are separate fields now rather than "claude opus 5:idle":
      // the state is the half that changes minute to minute, and as its own span
      // it can carry the same colour the sidebar gives that state. Both hang off
      // the agent — a model without an agent to run it is a leftover, not a
      // fact about the pane.
      if (p.agent) {
        seg("agent", agentLabel(p.agent, p.agentModel));
        seg("astate " + stClass(p.agentState), p.agentState);
      }
      if (p.exited !== null) {
        seg("exited", "exited (" + p.exited + ")");
        drawAutoclose(p, add);
      }
    }
    const zoom = p.chrome.querySelector(".zoom");
    if (tabZoomed && p.info && p.info.focused) {
      if (!zoom) {
        const z = document.createElement("span");
        z.className = "zoom"; z.textContent = "ZOOM ⤢"; z.title = "zoomed — click to unzoom";
        z.addEventListener("mousedown", (e) => e.stopPropagation());
        z.addEventListener("click", (e) => { e.stopPropagation(); sendCmd("pane.zoom", { pane: p.id }); });
        p.chrome.insertBefore(z, p.chrome.querySelector(".ctl"));
      }
    } else if (zoom) {
      zoom.remove();
    }
  }

  // ---- The auto-close countdown on an exited header ----
  //
  //   pane 3 · build · ~/src · exited (0) — close in 7s ✕
  //
  // A cleanly exited pane closes itself after a few seconds (panes.autoclose_
  // exited). The countdown is the server's — this only DISPLAYS it, from the
  // remaining time pane_exited carried — so what is drawn here is what is
  // actually going to happen, and every window watching agrees.
  //
  // ✕ sends pane.keep rather than clearing a local timer: the countdown being
  // shared is the whole point, so cancelling has to be shared too. The server
  // answers by re-sending pane_exited with no countdown, which is what takes
  // this run off every header.
  // `add` is renderChrome's own span-appender, passed in rather than
  // reimplemented so this run lands in the same info row as every other field.
  function drawAutoclose(p, add) {
    if (!p.autocloseAt) return;
    const left = Math.max(0, Math.ceil((p.autocloseAt - Date.now()) / 1000));
    const s = add("autoclose", "— close in " + left + "s ");
    const x = document.createElement("button");
    x.className = "keep";
    x.textContent = "✕";
    x.title = "keep this pane open";
    // The header's own mousedown starts a focus + swap drag; without this the
    // press that cancels a countdown would also try to drag the pane.
    x.addEventListener("mousedown", (e) => e.stopPropagation());
    x.addEventListener("click", (e) => {
      e.stopPropagation(); e.preventDefault();
      keepPane(p.id);
    });
    s.appendChild(x);
  }

  // keepPane cancels a pane's countdown. The local clear is optimistic — the
  // server's re-broadcast is what makes it true — because the click and the
  // round trip are far enough apart to show one more tick, and a countdown
  // that keeps counting after you told it to stop reads as a click that missed.
  function keepPane(id) {
    const p = panes.get(id);
    if (!p || !p.autocloseAt) return;
    p.autocloseAt = 0;
    sendCmd("pane.keep", { pane: id });
    renderChrome(p);
  }

  // One interval for every counting pane rather than a timer each: the ticker
  // exists only to redraw a number once a second, and it stops itself as soon
  // as no pane is counting, so an idle session runs nothing.
  //
  // It also expires deadlines that have passed. The pane_removed that follows
  // the server's close is the real end of the story, but it arrives a network
  // hop later, and "close in 0s" sitting on a header is a countdown that looks
  // stuck.
  // A box rather than a bare `let` so the ticker handle is reachable from a
  // unit test, which lifts these functions out of the bundle one at a time and
  // can bind a const but not a mutable free variable (web/jstest/testutil.mjs).
  const autocloseTick = { timer: null };
  function tickAutoclose() {
    let live = 0;
    const now = Date.now();
    for (const p of panes.values()) {
      if (!p.autocloseAt) continue;
      if (p.autocloseAt <= now) p.autocloseAt = 0;
      else live++;
      renderChrome(p);
    }
    if (!live && autocloseTick.timer) { clearInterval(autocloseTick.timer); autocloseTick.timer = null; }
  }

  // startAutoclose is what pane_exited calls with the remaining milliseconds the
  // server reported; 0 (or an absent field) stops the pane counting, which is
  // how a cancel from another window lands here.
  function startAutoclose(p, ms) {
    p.autocloseAt = ms > 0 ? Date.now() + ms : 0;
    if (p.autocloseAt && !autocloseTick.timer) autocloseTick.timer = setInterval(tickAutoclose, 500);
  }
