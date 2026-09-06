  // Agents has no groups to fold, so its heading carries the section arrow and
  // nothing else — the one control every section now has, in the one position it
  // is the same distance from every heading's right edge.
  initSectionFold("sec-agents", "agent-hctl", "agents");

  // The section is drawn in blocks, separated by hairlines: the coding agents
  // first, then one block per plugin whose panes are open.
  //
  //   ● claude opus 5        cats:p1 · 2m ago · idle
  //   ● codex                cats:p3 · 9s ago · working
  //   ────────────────────────────────────────────────
  //   ● cats-todo  todo: cats (3)                 cats:p4
  //   ────────────────────────────────────────────────
  //   ● gonotes    notes                          w2:p2
  //   ────────────────────────────────────────────────
  //
  // Agents lead because they are the rows that change on their own: they carry
  // the state, the age and the attention colour, and the section's whole reason
  // to be glanced at is up there. A plugin pane is something the user started
  // and will come back to on their own schedule — worth listing beside the
  // agents (it is the same question, "what have I got running"), but not worth
  // interleaving with rows that can go red while you read them.
  //
  // The plugin blocks are cut on the rollup's grouping, which the server has
  // already ordered by plugin id, so each plugin's panes stay together and the
  // blocks keep their places between rollups. Every block gets a rule *under*
  // it, the last one included: the rule reads as the block's own closing edge
  // rather than as a join between two of them, which is what keeps a single
  // plugin's block looking finished instead of cut off.
  function renderAgents(items, plugins) {
    agentItems = items || [];
    const plugs = plugins || [];
    agentAttentionSweep(agentItems);
    if (layoutMsg) { renderWorkspaces(layoutMsg); renderTabbar(layoutMsg); } // ws badges + tab markers derive from the rollup
    agentListEl.innerHTML = "";
    if (!agentItems.length && !plugs.length) {
      const li = document.createElement("li"); li.className = "empty"; li.textContent = "none";
      agentListEl.appendChild(li); return;
    }
    const foc = focusedPaneId();
    for (const it of agentItems) agentListEl.appendChild(agentRow(it, foc));
    // The rule closing the agent block is the same element as the one closing a
    // plugin block, and it is drawn only when a plugin block follows: with no
    // plugin panes open the section is exactly what it always was.
    let group = null;
    for (const p of plugs) {
      // The rule above a block is the one closing the block before it, so the
      // first plugin block draws one only when there are agent rows to close.
      if (group === null ? agentItems.length : p.plugin !== group) agentListEl.appendChild(agentSep());
      group = p.plugin;
      agentListEl.appendChild(pluginRow(p, foc));
    }
    if (group !== null) agentListEl.appendChild(agentSep());
    refreshPaneList(); // pane rows take agent identity + state from the rollup
  }

  // agentSep is the hairline between blocks. An <li> rather than a border on the
  // first row of the next block, because the rows carry a focus/hover background
  // that would paint across such a border — the same reason the PANES group
  // headers own their divider instead of leaning on the row below.
  function agentSep() {
    const li = document.createElement("li");
    li.className = "sep";
    return li;
  }

  // agentRow builds one detected-agent row.
  function agentRow(it, foc) {
    const li = document.createElement("li"); li.className = "agent";
    li.dataset.pane = it.pane; // markFocusedAgent re-marks rows without a re-render
    li.dataset.ws = it.workspace; // ditto markLockedAgents, when a lock flips
    li.dataset.model = it.model || ""; // the untrimmed string the row's tooltip shows
    li.dataset.agent = it.agent || ""; // identity, for CSS/debug reach without re-deriving it
    if (it.pane === foc) li.classList.add("focused");
    setAgentLocked(li, wsLocked(it.workspace));
    const dot = document.createElement("span"); dot.className = "adot " + stClass(markerState(it)); dot.textContent = "●";
    // Named agent-then-model, as the pane rows and pane headers are: this
    // section is the one list that spans every workspace, so it is the most
    // likely to hold two different agents at once, and the pair is what tells
    // those rows apart. The agent's own name stands alone when no model
    // resolved (an agent with no resolver, or one before its first answer).
    //
    // The two halves are separate spans here — everywhere else prints
    // agentLabel()'s single string — because only the agent half takes the
    // identity hue. Tinting the pair would colour "claude opus 5" entire, and
    // the model is the half that changes under a stable identity. They stay
    // nested inside one .aname so the row's flex gap still sees one item.
    const name = document.createElement("span"); name.className = "aname";
    const tool = document.createElement("span");
    tool.className = "atool " + hueClass(it.agent);
    // Agentless rows keep agentLabel's shape: the model alone, and hueClass
    // returns "" for it, so a shell pane is not handed some agent's colour.
    tool.textContent = it.agent || modelLabel(it.model);
    name.appendChild(tool);
    const mdl = it.agent ? modelLabel(it.model) : "";
    if (mdl) {
      const mo = document.createElement("span"); mo.className = "amodel";
      mo.textContent = " " + mdl; // the separator agentLabel used to join with
      name.appendChild(mo);
    }
    // The user's flag rides between the identity and the state — after the
    // name, because it qualifies *this* agent rather than announcing a new
    // row, and before the state, because the state is the half that changes on
    // its own while the flag is the half somebody chose.
    const af = flagMark(flagOf(it));
    if (af) name.appendChild(af);
    const meta = document.createElement("span"); meta.className = "ameta";
    const st = it.seen ? it.state : "done";
    meta.textContent = paneRef(it.pub, it.pane) + " · ";
    // Every state carries its age, working included. For a settled state it
    // answers "is this still true?"; for a working one it reads as "how long
    // has it been at this?" — the same instant (when the state last moved)
    // means run-time there, and a long-running "5m ago · working" is the row
    // most worth a glance. since_ms < 0 is the rollup's "no instant known".
    if (it.since_ms >= 0) {
      const age = document.createElement("span");
      age.dataset.at = String(Date.now() - it.since_ms); // absolute: the rollup won't come again until the state moves
      age.dataset.st = st; // the displayed state, so the 5s tick can warn without one
      paintAge(age, it.since_ms, st);
      meta.appendChild(age);
    }
    meta.appendChild(document.createTextNode(st));
    li.appendChild(dot); li.appendChild(name); li.appendChild(meta);
    // agent.focus, not pane.focus: the agents list is global, so the target
    // may sit in another workspace/tab and has to be revealed into the
    // viewport (pane.focus only moves the focus flag within the current one).
    //
    // Which is exactly why a locked workspace refuses this click as well: the
    // reveal *is* a workspace switch, so leaving it open would have made the
    // dimmed row the way around the refusal in WORKSPACES. Asked at click time
    // rather than read off the row's class, so a lock lifted a moment ago is
    // honoured without waiting for the row to be rebuilt.
    //
    // On the press (pressActivate): this list is the one most often rebuilt
    // under the pointer, since it is rendered directly from the rollup whose
    // arrival used to eat the click.
    pressActivate(li, () => {
      if (wsLocked(it.workspace)) {
        toast(wsName(it.workspace) + " is locked — unlock it to reach this agent");
        return;
      }
      sendCmd("agent.focus", { pane: it.pane });
    });
    // Right-click reaches the pane menu, which is where flagging lives. Not
    // gated on the workspace lock, unlike the click above: the lock is about
    // not *starting* things in a workspace, and pinning "come back to this"
    // to an agent inside one is exactly the note a person set the workspace
    // aside in order to write.
    li.addEventListener("contextmenu", (e) => {
      e.preventDefault();
      openCtx(e.clientX, e.clientY, paneMenuItems(it.pane, false));
    });
    return li;
  }

  // pluginShort trims a plugin id to the half that identifies it:
  // "rohanthewiz.cats-todo" -> "cats-todo". The vendor prefix is what makes the
  // ids unique across the world, and exactly what makes them all look alike in a
  // column eleven pixels tall. The full id stays in the row's tooltip.
  function pluginShort(id) {
    const i = id.lastIndexOf(".");
    return i < 0 ? id : id.slice(i + 1);
  }

  // pluginRow builds one plugin-pane row. Same three fields as an agent row,
  // filled with what a plugin can actually say:
  //
  //   ● cats-todo  todo: cats (3)                          cats:p4
  //
  // The identity is the plugin (hued like an agent's name, from the same six
  // slots — one colour per tool is the property being read, and a plugin is a
  // tool), and beside it, in the dim half where an agent prints its model, the
  // pane's own terminal title. That is the plugin's one channel: cats-todo puts
  // its open count there, a dev-server plugin its port. It changes under a
  // stable identity, which is exactly what the model half is for.
  //
  // The dot stays in the column for alignment but reports nothing — st-unknown,
  // the muted one. A plugin is a program, not an agent taking turns: it is not
  // idle, working or blocked, and colouring it green would put a row in the
  // section that never changes and always looks settled. For the same reason
  // there is no age: the server sends no instant, because it has none that would
  // mean what an agent's does.
  function pluginRow(p, foc) {
    const li = document.createElement("li"); li.className = "agent plug";
    li.dataset.pane = p.pane; // markFocusedAgent, as on an agent row
    li.dataset.ws = p.workspace; // ditto markLockedAgents
    li.dataset.plugin = p.plugin; // the untrimmed id the row's tooltip shows
    if (p.pane === foc) li.classList.add("focused");
    setAgentLocked(li, wsLocked(p.workspace));
    const dot = document.createElement("span"); dot.className = "adot st-unknown"; dot.textContent = "●";
    const name = document.createElement("span"); name.className = "aname";
    const tool = document.createElement("span");
    tool.className = "atool " + hueClass(p.plugin);
    tool.textContent = pluginShort(p.plugin);
    name.appendChild(tool);
    if (p.title) {
      const ti = document.createElement("span"); ti.className = "amodel";
      ti.textContent = " " + p.title;
      name.appendChild(ti);
    }
    const af = flagMark(flagOf(p));
    if (af) name.appendChild(af);
    const meta = document.createElement("span"); meta.className = "ameta";
    meta.textContent = paneRef(p.pub, p.pane);
    li.appendChild(dot); li.appendChild(name); li.appendChild(meta);
    // Reveal and right-click behave exactly as they do on an agent row — the
    // row's job is the same, "take me to that pane" — including the lock's
    // refusal, since revealing one is a workspace switch either way.
    pressActivate(li, () => {
      if (wsLocked(p.workspace)) {
        toast(wsName(p.workspace) + " is locked — unlock it to reach this pane");
        return;
      }
      sendCmd("agent.focus", { pane: p.pane });
    });
    li.addEventListener("contextmenu", (e) => {
      e.preventDefault();
      openCtx(e.clientX, e.clientY, paneMenuItems(p.pane, false));
    });
    return li;
  }

  // A focus move arrives in the layout, not in the agents rollup, so the marked
  // row is retargeted in place — re-running renderAgents would rebuild the list
  // (and re-query the pane inventory) for a class change.
  function markFocusedAgent() {
    const foc = focusedPaneId();
    for (const li of agentListEl.children) {
      if (li.dataset.pane) li.classList.toggle("focused", li.dataset.pane === foc);
    }
  }

  // setAgentLocked dims one AGENTS row for its workspace's lock, and says why in
  // the row's tooltip — dimming alone is ambiguous (an idle agent looks much the
  // same), and the padlock that explains it in WORKSPACES is a section away. The
  // tooltip leads with the consequence the user is about to meet (the click does
  // nothing) rather than with the lock's own definition.
  //
  // Unlocked, the row falls back to naming its full model, which is what the
  // trimmed label in the row dropped (the effort suffix, the exact id) — or, on
  // a plugin row, its full id, which is what pluginShort dropped. Same bargain
  // either way: the row shows the half that tells it apart, the tooltip holds
  // the whole string. The lock displaces it: while a click is refused, saying
  // so matters more.
  function setAgentLocked(li, locked) {
    li.classList.toggle("wslocked", locked);
    const full = li.dataset.model || li.dataset.plugin || "";
    if (locked) li.title = "workspace locked — clicking will not reveal this " + (li.dataset.plugin ? "pane" : "agent");
    else if (full) li.title = full;
    else li.removeAttribute("title");
  }

  // A lock flip arrives as a layout, not as an agents rollup — the same split
  // that makes markFocusedAgent necessary — so the rows are re-marked in place
  // rather than rebuilt. Each row remembers its workspace id for exactly this.
  function markLockedAgents() {
    for (const li of agentListEl.children) {
      if (li.dataset.ws) setAgentLocked(li, wsLocked(li.dataset.ws));
    }
  }

