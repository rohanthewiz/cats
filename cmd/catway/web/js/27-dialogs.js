  // ---- Rename dialogs + close confirms (WS8): tab/workspace finally wired ----
  // Reachable from a sidebar row for any pane in the session, so the handle and
  // the prefilled name fall back to the pane.list snapshot when the pane is not on
  // screen and has no local state.
  function renamePane(id) {
    const p = panes.get(id), pi = paneInv.find((x) => x.pane === id);
    dialogInput({
      title: "rename pane " + paneRef((p && p.pub) || (pi && pi.handle), id),
      value: (p && p.info ? p.title : (pi && (pi.name || pi.title))) || "",
      hint: "enter saves · empty clears the custom name · esc cancels",
      onSubmit: (name) => sendCmd("pane.rename", { pane: id, name }),
    });
  }
  function renameTab(t) {
    dialogInput({
      title: "rename tab " + t.num,
      value: t.name === String(t.num) ? "" : t.name,
      hint: "enter saves · empty reverts to the tab number · esc cancels",
      onSubmit: (name) => sendCmd("tab.rename", { num: t.num, name }),
    });
  }
  // newWorkspace: name the workspace and pick where it starts up front, instead
  // of making the user create it and then fix both. Either field may be left
  // empty: no name keeps auto-naming (the label follows the workspace's cwd),
  // and a cleared start path starts the workspace in the user's home directory —
  // the escape hatch for a session whose default cwd is somewhere useless. The
  // path is prefilled with the focused pane's live cwd, the same neighbour
  // inheritance new tabs and splits use, so the common case is still one Enter,
  // and the field is a picker (attachPathPicker) for every case that is not:
  // recent directories and real subdirectory completion, so choosing where a
  // workspace lives never means typing a path from memory.
  //
  // The third field starts a plugin in the workspace as it opens. A workspace is
  // usually opened *to do something* — the same reflex that has the user reach
  // for the plugins dialog immediately after — and the plugin's own scoping makes
  // the pairing more than a keystroke saved: a plugin like cats-todo keys its
  // state off the directory it wakes up in, so starting it from here (with the
  // workspace's start path already chosen above) is the only way to launch it
  // against the new project without first switching to the workspace and waiting
  // for a pane cwd to report.
  //
  // The roster comes from plugin.list, one round trip before the dialog opens,
  // for the same reason openPluginsDialog takes one: the browser holds no plugin
  // state, and a stale list would offer actions that no longer resolve. A failed
  // or empty list drops the field rather than the dialog — plugins are an extra
  // here, and creating a workspace must not depend on the plugin host being
  // healthy.
  function newWorkspace() {
    sendCmdAwait("plugin.list", {}, (res) => {
      const runners = res.ok ? pluginRunners((res.data || {}).plugins || []) : [];
      openNewWorkspaceDialog(runners);
    });
  }

  // pluginRunners flattens the plugin list into the one-line choices a picker
  // offers: every action of every working plugin, labelled by plugin when that
  // is the whole story and by plugin + action when a plugin ships more than one.
  // Broken plugins are left out — their argv did not resolve, so offering them
  // is offering a tab that fails on spawn.
  function pluginRunners(plugins) {
    const runners = [];
    for (const p of plugins) {
      if (p.broken) continue;
      const actions = p.actions || [];
      for (const a of actions) {
        const label = actions.length === 1 ? p.id : p.id + " — " + (a.title || a.id);
        runners.push({ label, plugin: p, action: a });
      }
    }
    return runners;
  }

  function openNewWorkspaceDialog(runners) {
    const fields = [
      { label: "name", value: "", placeholder: "optional — auto-named after the start directory" },
      { label: "start path", value: focusedPaneCwd(), placeholder: "empty starts in your home directory", pick: true },
    ];
    // The host a workspace is pinned to is where every pane created in it runs,
    // so it belongs in the dialog that creates it rather than being re-chosen at
    // each split. Offered only when there is a choice to make.
    //
    // Choosing another host changes what the start path means: it is a path on
    // that machine. A host whose cathost can list its own directories completes
    // it there — the picker keeps working, against the right filesystem — and
    // one that cannot loses the picker and says whose disk the text will be read
    // against. Either way a prefilled local cwd is a wrong path there, so it is
    // cleared on the way out.
    let hostIdx = -1; // -1 = no host field, so its slot is never read below
    if (multiHost()) {
      hostIdx = fields.length;
      fields.push({
        label: "host",
        value: (hostItems.find((h) => h.is_default) || hostItems[0] || {}).id || "",
        choices: hostItems.map((h) => ({
          value: h.id,
          label: (h.label || h.id) + (h.connected ? "" : " (offline)"),
        })),
        onChange: (id, rows) => {
          const h = hostItems.find((x) => x.id === id);
          const local = !h || !!h.local;
          const lists = !h || !!h.lists_dirs;
          const path = rows[1];
          if (path.picker) {
            // Point it first, switch it on second: enabling re-renders, and a
            // render against the old machine's cache would flash the wrong
            // directories before the first reply for the new one lands.
            path.picker.setHost(local ? "" : id);
            path.picker.setEnabled(lists);
          }
          path.input.placeholder = local
            ? "empty starts in your home directory"
            : (lists
              ? "path on " + (h.label || h.id) + " — empty starts in that host's home directory"
              : "path on " + (h.label || h.id) + " — empty starts where that host starts panes");
          if (!local && path.input.value === focusedPaneCwd()) path.input.value = "";
        },
      });
    }
    // The runner is passed by index, dialogFields carrying strings only. "" is
    // the default option, so an untouched dialog behaves exactly as before.
    const plugIdx = fields.length;
    if (runners.length) {
      fields.push({
        label: "start plugin",
        value: "",
        choices: [{ value: "", label: "none" }].concat(
          runners.map((r, i) => ({ value: String(i), label: r.label })),
        ),
      });
    }
    dialogFields({
      title: "new workspace",
      fields,
      hint: "↑↓ picks a directory · tab drills in · enter creates · esc cancels",
      submitLabel: "create",
      // Read by index rather than by positional parameters: two of the fields
      // are conditional (host, plugin), so their slots move. sel is "" for
      // "none" and undefined when the field was dropped; the truthiness test
      // covers both, and "0" — a real index — is a truthy string, so the first
      // plugin in the list is not swallowed by it.
      onSubmit: (...vs) => {
        const sel = vs[plugIdx];
        createWorkspace(vs[0], vs[1], sel ? runners[Number(sel)] : null, hostIdx < 0 ? "" : vs[hostIdx]);
      },
    });
  }

  // createWorkspace sends workspace.create, turning its one recoverable failure
  // into an offer: a start path that does not exist re-opens as a confirm that
  // retries with mkdir set, so pointing a new workspace at a folder that is not
  // there yet costs one extra Enter instead of a trip to a shell. Same two-step
  // escalation shape as confirmRemoveWorktree — the first attempt never carries
  // the side effect, and the server keys the offer by its "no such directory"
  // error rather than the UI guessing at the disk from a stale picker cache.
  // runner (optional) is a {plugin, action} from the dialog's plugin field,
  // started in the workspace once it exists — see startPluginInWorkspace. It
  // rides both attempts because the mkdir retry is the *same* create: a user who
  // asked for a plugin and then confirmed the folder still asked for the plugin.
  // host (optional) pins the workspace to a cathost — every pane created in it
  // then runs there. It rides the mkdir retry for the same reason the runner
  // does: it is the same create, and the server reads the path against the host
  // it was given (a remote path is never stat'ed here, so the mkdir escalation
  // simply never fires for one).
  function createWorkspace(name, path, runner, host) {
    const created = (res) => {
      if (!runner) return;
      startPluginInWorkspace(runner, (res.data || {}).id);
    };
    sendCmdAwait("workspace.create", { name, path, host }, (res) => {
      if (res.ok) { created(res); return; }
      const err = res.error || "";
      if (err.startsWith("no such directory: ")) {
        dialogConfirm({
          title: "create folder?",
          message: err.slice("no such directory: ".length) +
            " does not exist — create it and start the workspace there?",
          confirmLabel: "create folder",
          onConfirm: () => sendCmdAwait("workspace.create", { name, path, host, mkdir: true }, (r2) => {
            if (!r2.ok) { toast("new workspace: " + (r2.error || "unknown")); return; }
            created(r2);
          }),
        });
      } else {
        toast("new workspace: " + (err || "unknown"));
      }
    });
  }

  // startPluginInWorkspace launches an action in a workspace named by id — the
  // one workspace.create just returned, which is why the result's id is read
  // here at all (the browser otherwise ignores it and follows the layout
  // broadcast). Sending the id rather than relying on "the new workspace is now
  // active" keeps the launch correct if anything focuses elsewhere in between.
  //
  // Deliberately no cwd, where pluginRunAction sends the focused pane's: the
  // point of this launch is the workspace that was just created, so the tab
  // should inherit *that* workspace's directory. tab.create does exactly that on
  // its own — the new workspace's root pane is the neighbor it inherits from —
  // and the focused pane's cwd would be the old workspace's, the one directory
  // this must not use.
  function startPluginInWorkspace(runner, wsID) {
    const params = { command: runner.action.argv, env: runner.plugin.env || {} };
    if (wsID) params.workspace = wsID;
    sendCmdAwait("tab.create", params, (res) => {
      if (!res.ok) toast("plugin run failed: " + (res.error || "unknown"));
    });
  }
  function renameWorkspace(w) {
    dialogInput({
      title: "rename workspace " + (w.name || w.id),
      value: w.name,
      hint: "enter saves · empty reverts to auto-naming · esc cancels",
      onSubmit: (name) => sendCmd("workspace.rename", { id: w.id, name }),
    });
  }
  // toggleWorkspaceLock flips a workspace between open and closed to plugins and
  // agents. No confirm either way: nothing is destroyed, the sidebar row shows
  // the result immediately (the server rebroadcasts the layout), and the toast
  // says which state was just entered — locking is a thing you do *to be able to
  // work*, so it should cost one click.
  function toggleWorkspaceLock(w) {
    const locked = !w.locked;
    sendCmdAwait("workspace.lock", { id: w.id, locked }, (res) => {
      if (!res.ok) { toast("workspace lock: " + (res.error || "unknown")); return; }
      toast((w.name || w.id) + (locked ? " locked — no plugins or agents" : " unlocked"));
    });
  }
  // ---- Clean / sleep / wake (workspace.clean / workspace.sleep / workspace.wake) ----
  //
  // cleanWorkspace closes a workspace's idle panes; sleepWorkspace closes all of
  // them and keeps the workspace in the list with no terminal. Both leave busy
  // panes alone, and both leave an IDLE agent alone unless told to park it —
  // an agent's context is the one thing in an idle pane worth more than the
  // resources it costs. The server decides what is idle (it can see the
  // foreground job and the agent state); the client only says which mode.
  //
  // No confirm on clean: nothing busy is touched, and the result toast says
  // exactly what went. Sleep confirms, since it closes every pane — but the
  // refusal case (something still busy) is the server's, and its message
  // names the panes in the way, so the dialog does not try to predict it.
  function cleanWorkspace(w, agents) {
    sendCmdAwait("workspace.clean", { id: w.id, agents: agents || "" }, (res) => {
      if (!res.ok) { toast("clean workspace: " + (res.error || "unknown")); return; }
      toast(cleanSummary(w, res.data));
    });
  }
  function sleepWorkspace(w, agents) {
    const parking = agents === "park";
    dialogConfirm({
      title: "sleep workspace",
      message: "Put “" + (w.name || w.id) + "” (" + w.id + ") to sleep? Every pane closes; the workspace stays in the list "
        + "with its name, flag and todos, and wakes with a fresh shell when you click it."
        + (parking ? " Idle agents are parked and resumed on wake." : " It will refuse while an agent or a job is still running."),
      confirmLabel: "sleep", danger: true,
      onConfirm: () => sendCmdAwait("workspace.sleep", { id: w.id, agents: agents || "" }, (res) => {
        if (!res.ok) { toast("sleep workspace: " + (res.error || "unknown")); return; }
        toast(cleanSummary(w, res.data));
      }),
    });
  }
  function wakeWorkspace(w) {
    sendCmdAwait("workspace.wake", { id: w.id }, (res) => {
      if (!res.ok) { toast("wake workspace: " + (res.error || "unknown")); return; }
      toast((w.name || w.id) + " awake");
    });
  }
  // cleanSummary phrases a clean/sleep result: "w2 asleep — 3 panes closed, 1
  // agent parked", or "w2: 2 panes closed, 1 kept". Zero counts are left out;
  // an empty result (nothing to do) still says so rather than showing nothing.
  function cleanSummary(w, d) {
    d = d || {};
    const parts = [];
    if (d.closed) parts.push(nOf(d.closed, "pane") + " closed");
    if (d.parked) parts.push(nOf(d.parked, "agent") + " parked");
    if (d.sent) parts.push("command sent to " + nOf(d.sent, "agent"));
    if (d.kept) parts.push(nOf(d.kept, "pane") + " kept");
    const name = w.name || w.id;
    if (d.asleep) return name + " asleep" + (parts.length ? " — " + parts.join(", ") : "");
    return name + ": " + (parts.length ? parts.join(", ") : "nothing to clean");
  }
  // ---- Flags (workspace.flag / pane.flag) ----
  //
  // One sender for both commands: a target names the command and the params that
  // address its subject (see paneFlagTarget / wsFlagTarget), so nothing below
  // has to know whether it is flagging a workspace or a pane.
  //
  // No confirm on any path, clearing included — nothing is destroyed, the mark
  // appears or vanishes from four lists at once, and the toast says which just
  // happened. A flag is something you set in passing; making it cost a dialog
  // would mean it doesn't get set.
  function sendFlag(target, kind, note) {
    sendCmdAwait(target.cmd, { ...target.params, kind, note: note || "" }, (res) => {
      if (!res.ok) { toast("flag: " + (res.error || "unknown")); return; }
      if (!kind) { toast(target.label + " unflagged"); return; }
      // The glyph goes in the toast, not just the name: it is what the user is
      // about to go looking for in the sidebar, so the toast doubles as "this is
      // the mark you just made".
      const f = { kind, note };
      toast(target.label + " flagged " + flagGlyph(f) + " " + flagLabel(f) + (note ? " — " + note : ""));
    });
  }

  // editFlagNote changes the note without touching the kind — the second half of
  // the one-click flagging in the menu. Only reachable on a flagged subject, so
  // target.flag is always there.
  function editFlagNote(target) {
    const cur = target.flag;
    dialogInput({
      title: "note for " + flagGlyph(cur) + " " + flagLabel(cur) + " on " + target.label,
      value: cur.note,
      hint: "enter saves · empty clears the note but keeps the flag · esc cancels",
      onSubmit: (note) => sendFlag(target, cur.kind, note),
    });
  }

  // openFlagDialog is the full form: pick a kind (or invent a glyph) and write a
  // note in one go.
  //
  // The glyph field only exists for the "custom glyph" choice, and is hidden
  // otherwise rather than being a permanent third row that means nothing five
  // times out of six. onChange fires once on open, so the field starts in the
  // right state — a dialog opened on a subject that already wears a custom glyph
  // shows it.
  // A sentinel no real kind can collide with: it has a space in it, and the
  // server refuses any kind containing whitespace (flags.ParseKind), so this can
  // never be mistaken for a value worth sending.
  const FLAG_CUSTOM = "custom glyph";
  function openFlagDialog(target) {
    const cur = target.flag;
    const named = cur && FLAG_BY_KIND.has(cur.kind);
    const choices = FLAG_DEFS.map((d) => ({ value: d.kind, label: d.glyph + "  " + d.label + " — " + d.meaning }));
    choices.push({ value: FLAG_CUSTOM, label: "custom glyph…" });
    dialogFields({
      title: "flag " + target.label,
      submitLabel: "flag",
      hint: "enter saves · esc cancels · clear the flag from the row's menu",
      fields: [
        {
          label: "flag", choices,
          value: cur ? (named ? cur.kind : FLAG_CUSTOM) : FLAG_DEFS[0].kind,
          onChange: (v, rows) => {
            // rows[1] is the glyph field; its .field wrapper is what carries the
            // label, so hiding the wrapper hides the whole row.
            const wrap = rows[1].input.parentElement;
            wrap.style.display = v === FLAG_CUSTOM ? "" : "none";
          },
        },
        { label: "glyph", value: cur && !named ? cur.kind : "", placeholder: "any single character, e.g. 🍕" },
        { label: "note", value: cur ? cur.note : "", placeholder: "optional — what you want to remember" },
      ],
      onSubmit: (kind, glyph, note) => {
        // The server validates the glyph (flags.ParseKind) rather than this
        // dialog, so the CLI and the browser refuse exactly the same things with
        // exactly the same words. All that happens here is the substitution.
        const k = kind === FLAG_CUSTOM ? glyph.trim() : kind;
        if (!k) { toast("flag: no glyph given"); return; }
        sendFlag(target, k, note);
      },
    });
  }

  function confirmCloseWorkspace(w) {
    dialogConfirm({
      title: "close workspace",
      message: "Close workspace “" + w.name + "” (" + w.id + ")? All of its tabs and panes will be closed.",
      confirmLabel: "close workspace", danger: true,
      onConfirm: () => sendCmd("workspace.close", { id: w.id }),
    });
  }
  function confirmStopServer() {
    dialogConfirm({
      title: "stop server",
      message: "Stop the cats server? Session state is saved and the persistent cathost daemon keeps your shells alive, but every browser disconnects.",
      confirmLabel: "stop server", danger: true,
      onConfirm: () => sendCmd("server.stop", {}),
    });
  }

