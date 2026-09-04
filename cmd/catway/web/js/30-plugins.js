  // ---- Plugins dialog ----
  //
  // One list over plugin.list with per-row actions. The instant verbs
  // (uninstall/unlink) resolve over §7 commands; install/link/rebuild spawn a
  // `catctl plugin …` tab instead — they run git plus a build whose live
  // output the user wants to watch, and cmd_result is one-shot, so a pane is
  // the streaming surface (the server resolves the catctl path for us).

  // pluginSourceIsPath mirrors the host's resolveSourceURL heuristic
  // (internal/plugin/install.go): a source shaped like a filesystem path is a
  // local checkout, which means `plugin link` — register it in place, edits
  // stay live — rather than `plugin install`, which clones into the plugins
  // root. Keeping the two heuristics identical means what the dialog decides
  // to do matches what catctl would have done with the same string.
  //
  // The leading marker is what makes it a path: "./x", "../x", "~/x", "/x".
  // A bare "cmd/cats-todo" is deliberately NOT one — two segments is the
  // "owner/repo" GitHub shorthand, and that ambiguity is why the prefix is
  // required rather than inferred from whether the directory happens to exist
  // (which the browser could not check anyway).
  function pluginSourceIsPath(s) {
    return /^[.~/]/.test(s);
  }

  // focusedPaneCwd is "the directory the user is looking at": the focused
  // pane's live cwd (tracked from pane_cwd events), rather than whatever
  // directory the server happened to start in. It anchors a *relative* link
  // path ("./cmd/cats-todo" means relative to the pane on screen) and roots
  // plugin action tabs in the current project (pluginRunAction). "" — no
  // focused pane, or its cwd not yet reported — leaves tab.create's default
  // cwd alone.
  function focusedPaneCwd() {
    const id = focusedPaneId();
    if (id == null) return "";
    const p = panes.get(id);
    return (p && p.cwd) || "";
  }

  function pluginStatus(p) {
    if (p.broken) return "broken";
    if (p.linked) return "linked";
    return ""; // installed is the default case; "" renders no status tag at all
  }

  // pluginRunAction launches one manifest action in a fresh tab: resolved argv
  // and identity env come straight from plugin.list (same shape catctl plugin
  // run sends). The tab is anchored to the focused pane's cwd for the same
  // reason `catctl plugin run` sends the invoker's cwd: plugins scope
  // per-project state (cats-todo's .cats-todo backlog) off the directory they
  // wake up in, and "the project I'm looking at" is what a user means by
  // "here". Without it, tab.create falls back to the server's start directory
  // and the action lands in a project the user never chose. "" (no focused
  // pane / cwd not yet reported) omits the field and keeps that default.
  // Deliberately no title: pinning the manifest label would block tab
  // auto-naming forever; the plugin's own OSC title names the tab live.
  function pluginRunAction(p, a) {
    closeModal();
    const params = { command: a.argv, env: p.env || {} };
    const cwd = focusedPaneCwd();
    if (cwd) params.cwd = cwd;
    sendCmdAwait("tab.create", params, (res) => {
      if (!res.ok) toast("plugin run failed: " + (res.error || "unknown"));
    });
  }

  // pluginRunActionAll starts one action in every workspace at once — the
  // "open my todo list / my dev server / my log tail everywhere" case, which
  // otherwise costs a switch and a menu per workspace and leaves the viewport
  // wherever the last one landed. Nothing is focused and nothing is switched to:
  // each launch names its workspace (tab.create's workspace param), so the user
  // stays exactly where they were and finds the tabs already open when they
  // arrive.
  //
  // Locked workspaces are filtered out here rather than left to be refused by
  // the server. The lock means "no automation lands in this one", and a fan-out
  // is the automation it was written for — so a skip is the *expected* outcome,
  // not a failure, and reporting six launches and one error would read as
  // something having gone wrong. The count says so instead.
  //
  // No cwd, for the same reason as startPluginInWorkspace: each tab inherits its
  // own workspace's directory, so a per-project plugin scopes to the project it
  // landed in. Sending the focused pane's cwd would start every copy against the
  // one project the user happens to be looking at, which is the opposite of what
  // "in all workspaces" asks for.
  function pluginRunActionAll(p, a) {
    closeModal();
    const wss = (layoutMsg && layoutMsg.workspaces) || [];
    // Sleeping workspaces are skipped alongside locked ones: a tab.create there
    // is refused (the server will not wake a workspace as a side effect), and a
    // fan-out that woke every workspace you had put to bed would undo the
    // reason you put them there.
    const targets = wss.filter((w) => !w.locked && !w.asleep);
    const label = a.title || a.id;
    if (!targets.length) {
      toast(label + ": every workspace is locked or asleep");
      return;
    }
    // One reply per launch, summarised once at the end: N in-flight commands
    // would otherwise raise N toasts, and the useful fact is the tally.
    let ok = 0, failed = 0;
    const locked = wss.length - targets.length;
    const settle = () => {
      if (ok + failed < targets.length) return;
      const parts = [label + " started in " + ok + (ok === 1 ? " workspace" : " workspaces")];
      if (locked) parts.push(locked + " locked, skipped");
      if (failed) parts.push(failed + " failed");
      toast(parts.join(" · "));
    };
    for (const w of targets) {
      sendCmdAwait("tab.create", { workspace: w.id, command: a.argv, env: p.env || {} }, (res) => {
        if (res.ok) ok++; else failed++;
        settle();
      });
    }
  }

  // pluginPickAction is the run buttons' shared "which action?" step: one action
  // launches straight away (the catctl default), several open a picker at the
  // button — the ctx menu coexists with the modal. run and run-all differ only
  // in what they do with the answer, so the choosing lives here once and the two
  // buttons cannot drift into asking it differently.
  function pluginPickAction(p, e, launch) {
    if (p.actions.length === 1) { launch(p.actions[0]); return; }
    const r = e.target.getBoundingClientRect();
    openCtx(r.left, r.bottom + 4, p.actions.map((a) => (
      { label: a.title || a.id, fn: () => launch(a) }
    )));
    e.stopPropagation();
  }

  // pluginCatctlTab spawns `catctl plugin <args…>` in a fresh tab. The pane
  // stays on screen after exit (exited chrome), so the git/build output — or
  // the failure — remains readable. A FAILED run stays until the user closes
  // it; a clean one tidies itself away after the countdown on its header
  // (panes.autoclose_exited, twenty seconds — set with this output in mind),
  // which the header's ✕ cancels if there is more to read. cwd is optional
  // and only matters for the link path (see focusedPaneCwd).
  function pluginCatctlTab(catctl, title, args, cwd) {
    closeModal();
    const params = { title, command: [catctl, "plugin"].concat(args) };
    if (cwd) params.cwd = cwd;
    sendCmdAwait("tab.create", params, (res) => {
      if (!res.ok) toast("plugin: " + (res.error || "unknown"));
    });
  }

  // One prompt covers both ways in, dispatching on the shape of the source:
  // a path links a local checkout, anything else installs from git. Two
  // buttons would have made the user classify their own input; the host
  // already draws that line, so the dialog just follows it.
  function pluginInstallDialog(catctl) {
    dialogInput({
      title: "add plugin",
      hint: "owner/repo or git URL to install · --ref <branch|tag> to pin · " +
        "or a local path (./dir, ../dir, ~/dir, /dir) to link a checkout in place · runs in a new tab",
      submitLabel: "add",
      onSubmit: (v) => {
        const args = v.trim().split(/\s+/).filter(Boolean);
        if (!args.length) { toast("plugin: a source is required"); return; }
        if (pluginSourceIsPath(args[0])) {
          // --ref has no meaning for a link: there is nothing to check out,
          // the checkout is whatever the developer has on disk.
          if (args.length > 1) { toast("plugin link: takes a single directory"); return; }
          pluginCatctlTab(catctl, "plugin link", ["link", args[0]], focusedPaneCwd());
          return;
        }
        pluginCatctlTab(catctl, "plugin install", ["install"].concat(args));
      },
    });
  }

  function confirmUninstallPlugin(p) {
    dialogConfirm({
      title: "uninstall plugin",
      message: p.linked
        ? "Unlink “" + p.id + "”? Only the link is removed — your checkout at " + p.dir + " is untouched."
        : "Uninstall “" + p.id + "”? Its directory (" + p.dir + ") is deleted.",
      confirmLabel: p.linked ? "unlink" : "uninstall", danger: !p.linked,
      onConfirm: () => sendCmdAwait("plugin.uninstall", { id: p.id }, (res) => {
        if (!res.ok) { toast("uninstall failed: " + (res.error || "unknown")); return; }
        toast((res.data && res.data.message) || (p.id + " uninstalled"));
        openPluginsDialog(); // refresh the list in place
      }),
    });
  }

  function openPluginsDialog() {
    sendCmdAwait("plugin.list", {}, (res) => {
      if (!res.ok) { toast("plugins: " + (res.error || "unknown")); return; }
      const info = res.data || {};
      const plugins = info.plugins || [];
      openOverlay((ov) => {
        const m = document.createElement("div"); m.className = "modal pal plugins";
        const h = document.createElement("header"); h.textContent = "plugins"; m.appendChild(h);
        const listEl = document.createElement("div"); listEl.className = "list"; m.appendChild(listEl);
        if (!plugins.length) {
          const e = document.createElement("div"); e.className = "empty";
          e.textContent = "no plugins installed";
          listEl.appendChild(e);
        }
        for (const p of plugins) {
          const row = document.createElement("div"); row.className = "row";
          row.title = [p.broken || p.description, p.dir].filter(Boolean).join("\n");
          // The status tag exists only when there is a status: the default
          // installed case appends nothing, so the label starts at the row's
          // left edge instead of after an empty reserved column (see the
          // .modal.plugins .kind rule).
          const status = pluginStatus(p);
          if (status) {
            const kind = document.createElement("span"); kind.className = "kind";
            kind.textContent = status;
            row.appendChild(kind);
          }
          const lbl = document.createElement("span"); lbl.className = "lbl";
          lbl.textContent = p.broken ? p.id : p.id + " v" + (p.version || "?") +
            (p.name && p.name !== p.id ? " — " + p.name : "");
          row.appendChild(lbl);
          // A linked row shows its checkout inline: for a local plugin *where*
          // it lives is the identifying fact (which worktree am I linked to?),
          // whereas an installed plugin's dir is always the plugins root and
          // says nothing — that one stays in the tooltip.
          if (p.linked && p.dir) {
            const sub = document.createElement("span"); sub.className = "sub";
            sub.textContent = p.dir;
            row.appendChild(sub);
          }
          const acts = document.createElement("div"); acts.className = "acts";
          const actBtn = (label, cls, fn, tip) => {
            const b = document.createElement("button");
            b.textContent = label; if (cls) b.className = cls;
            if (tip) b.title = tip;
            b.addEventListener("click", fn);
            acts.appendChild(b);
          };
          if (!p.broken && (p.actions || []).length) {
            actBtn("run", "", (e) => pluginPickAction(p, e, (a) => pluginRunAction(p, a)));
            // The fan-out is its own button rather than a row in run's menu:
            // launching here is by far the common case, and folding both targets
            // into one menu would charge every multi-workspace session an extra
            // click for it. It appears only when there is more than one
            // workspace — with a single workspace it is the same launch under a
            // longer name, and offering it would be inventing a decision.
            if (((layoutMsg && layoutMsg.workspaces) || []).length > 1) {
              actBtn("run all", "", (e) => pluginPickAction(p, e, (a) => pluginRunActionAll(p, a)),
                "start in all workspaces (locked and sleeping ones are skipped)");
            }
          }
          if (!p.broken && !p.linked) {
            actBtn("update", "", () => pluginCatctlTab(info.catctl, "update " + p.id, ["update", p.id]));
          }
          if (!p.broken && p.linked && p.dir) {
            // The linked analogue of update. `plugin link` on the same checkout
            // is idempotent and re-runs the manifest's build steps, which is
            // exactly how a developer picks up their edits — update refuses on
            // linked plugins by design, there being no remote to pull from.
            // p.dir is the resolved symlink target, so no cwd is needed.
            actBtn("rebuild", "", () => pluginCatctlTab(info.catctl, "rebuild " + p.id, ["link", p.dir]));
          }
          actBtn(p.linked ? "unlink" : "uninstall", "danger", () => confirmUninstallPlugin(p));
          row.appendChild(acts);
          listEl.appendChild(row);
        }
        const btns = document.createElement("div"); btns.className = "btns";
        btns.appendChild(mkModalBtn("close", "", closeModal));
        btns.appendChild(mkModalBtn("add…", "primary", () => pluginInstallDialog(info.catctl)));
        m.appendChild(btns);
        m.addEventListener("keydown", (e) => {
          e.stopPropagation();
          if (e.key === "Escape") { e.preventDefault(); closeModal(); }
        });
        ov.appendChild(m);
      });
    });
  }

