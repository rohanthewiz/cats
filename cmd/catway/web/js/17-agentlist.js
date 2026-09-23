  // Agents has no groups to fold, so its heading carries the section arrow and
  // nothing else — the one control every section now has, in the one position it
  // is the same distance from every heading's right edge.
  // Plugins takes the same single control, for the same reason.
  initSectionFold("sec-agents", "agent-hctl", "agents");
  initSectionFold("sec-plugins", "plug-hctl", "plugins");

  // One rollup feeds two sections. Agents take turns and have a state worth
  // watching; plugins are tools the user drives. The rollup already keeps them
  // in separate lists (items / plugins), and each plugin pane carries its type,
  // so the split is one field read:
  //
  //   AGENTS
  //   ● claude opus 5        cats:p1 · 2m ago · idle
  //   ● codex                cats:p3 · 9s ago · working
  //   PLUGINS
  //   ● ced        main.go — ced      editor · cats:p2
  //   ────────────────────────────────────────────────
  //   ● cats-todo  todo: cats (3)      todos · cats:p4
  //   ────────────────────────────────────────────────
  //
  // A plugin whose manifest declares type "agent" is the exception, and stays
  // in AGENTS (as its own block under the detected agents, the way every plugin
  // used to be drawn). It is what the user would look for there, even though
  // its row has no state for cats to report.
  //
  // The server has already ordered the plugin panes by plugin id, and a filter
  // keeps that order, so each section can cut its blocks by walking its share
  // once. Every block gets a rule *under* it, the last one included: the rule
  // reads as the block's own closing edge rather than as a join between two of
  // them, which is what keeps a single plugin's block looking finished instead
  // of cut off.
  function renderAgents(items, plugins) {
    agentItems = items || [];
    const plugs = plugins || [];
    const agentPlugs = plugs.filter(isAgentPlugin);
    const toolPlugs = plugs.filter((p) => !isAgentPlugin(p));
    agentAttentionSweep(agentItems);
    if (layoutMsg) { renderWorkspaces(layoutMsg); renderTabbar(layoutMsg); } // ws badges + tab markers derive from the rollup
    const foc = focusedPaneId();

    agentListEl.innerHTML = "";
    if (!agentItems.length && !agentPlugs.length) {
      const li = document.createElement("li"); li.className = "empty"; li.textContent = "none";
      agentListEl.appendChild(li);
    } else {
      for (const it of agentItems) agentListEl.appendChild(agentRow(it, foc));
      // The rule closing the agent block is drawn only when a plugin block
      // follows it, so with no agent plugins the section is exactly what it
      // always was.
      appendPluginBlocks(agentListEl, agentPlugs, agentItems.length > 0, foc);
    }

    // PLUGINS hides rather than saying "none", like Hosts and Runbooks: it is
    // a section most sessions have nothing in, and an always-empty heading is
    // clutter. Nothing above it closes a block, so its first block draws no
    // leading rule.
    pluginListEl.innerHTML = "";
    pluginSecEl.hidden = !toolPlugs.length;
    appendPluginBlocks(pluginListEl, toolPlugs, false, foc);
    refreshPaneList(); // pane rows take agent identity + state from the rollup
  }

  // isAgentPlugin reports whether a plugin pane belongs in AGENTS. It must
  // match wire.IsAgentPluginType. Only the exact word "agent" counts, so an
  // untyped plugin or a type this page does not know goes to PLUGINS: those
  // are tools until they say otherwise.
  function isAgentPlugin(p) { return p.type === "agent"; }

  // appendPluginBlocks draws plugin rows into list, one block per plugin, each
  // closed by a hairline. ruleFirst draws a rule above the first block too, to
  // close the rows already in the list (the agents, in AGENTS).
  function appendPluginBlocks(list, plugs, ruleFirst, foc) {
    let group = null;
    for (const p of plugs) {
      if (group === null ? ruleFirst : p.plugin !== group) list.appendChild(agentSep());
      group = p.plugin;
      list.appendChild(pluginRow(p, foc));
    }
    if (group !== null) list.appendChild(agentSep());
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

  // pluginTypeLabel is the short word a plugin row shows for its type. The
  // role suffixes "_mgr" and "_client" are dropped: the section heading already
  // says these are tools, and "todos" / "http" read faster than "todos_mgr" /
  // "http_client" in 10px type. An unknown type is shown as sent (less a
  // suffix), so a plugin newer than this page still says what it is.
  function pluginTypeLabel(t) {
    if (!t) return "";
    return t.replace(/_(mgr|client)$/, "");
  }

  // pluginRow builds one plugin-pane row. Same three fields as an agent row,
  // filled with what a plugin can actually say:
  //
  //   ● cats-todo  todo: cats (3)                  todos · cats:p4
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
    li.dataset.ptype = p.type || ""; // the untrimmed type, for CSS/debug reach
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
    // The type goes before the handle, where an agent row puts its age and
    // state: it is the one thing a plugin row can say about what kind of
    // thing is in the pane. An untyped plugin shows the handle alone.
    const tl = pluginTypeLabel(p.type);
    meta.textContent = (tl ? tl + " · " : "") + paneRef(p.pub, p.pane);
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
  // Both sections are walked: a plugin row is marked the same way an agent row
  // is, whichever of the two it landed in.
  function markFocusedAgent() {
    const foc = focusedPaneId();
    for (const list of [agentListEl, pluginListEl]) {
      for (const li of list.children) {
        if (li.dataset.pane) li.classList.toggle("focused", li.dataset.pane === foc);
      }
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
    for (const list of [agentListEl, pluginListEl]) {
      for (const li of list.children) {
        if (li.dataset.ws) setAgentLocked(li, wsLocked(li.dataset.ws));
      }
    }
  }

