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
      if (p.exited !== null) seg("exited", "exited (" + p.exited + ")");
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

