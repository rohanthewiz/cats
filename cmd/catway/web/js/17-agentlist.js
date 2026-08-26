  // Agents has no groups to fold, so its heading carries the section arrow and
  // nothing else — the one control every section now has, in the one position it
  // is the same distance from every heading's right edge.
  initSectionFold("sec-agents", "agent-hctl", "agents");

  function renderAgents(items) {
    agentItems = items || [];
    agentAttentionSweep(agentItems);
    if (layoutMsg) { renderWorkspaces(layoutMsg); renderTabbar(layoutMsg); } // ws badges + tab markers derive from the rollup
    agentListEl.innerHTML = "";
    if (!items || !items.length) {
      const li = document.createElement("li"); li.className = "empty"; li.textContent = "none";
      agentListEl.appendChild(li); return;
    }
    const foc = focusedPaneId();
    for (const it of items) {
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
      agentListEl.appendChild(li);
    }
    refreshPaneList(); // pane rows take agent identity + state from the rollup
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
  // trimmed label in the row dropped (the effort suffix, the exact id). The lock
  // displaces it: while a click is refused, saying so matters more.
  function setAgentLocked(li, locked) {
    li.classList.toggle("wslocked", locked);
    if (locked) li.title = "workspace locked — clicking will not reveal this agent";
    else if (li.dataset.model) li.title = li.dataset.model;
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

