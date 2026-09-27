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
  // (panes.autoclose_exited, ten seconds by default), which the header's ✕
  // cancels if there is more to read. Ten seconds is enough to notice the
  // countdown, not to skim a long git log — so a run whose output matters is
  // kept with ✕ or by entering copy mode, both of which stop the clock. cwd
  // is optional and only matters for the link path (see focusedPaneCwd).
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
        // A removed plugin has no update to offer; drop it from the badge now
        // rather than at the next background check.
        pluginUpdates.delete(p.id);
        paintPluginBadge();
        openPluginsDialog(); // refresh the list in place
      }),
    });
  }

  // ---- Plugin update checks ----
  //
  // plugin.check_updates asks each installed plugin's git remote whether
  // `plugin update` would change anything (internal/plugin/check.go). The page
  // keeps the last answer here so two surfaces can read it: the toolbar's
  // plugins button (a count badge — the "let me know" part, visible without
  // opening anything) and the dialog's rows (which update, to what).
  //
  // When it asks:
  //
  //   connect ──8s──▶ check ──1h──▶ check ──1h──▶ …   (background, cached)
  //   dialog opens ─▶ check                           (cached)
  //   ↻ in dialog ──▶ check {force}                   (bypasses the cache)
  //   update tab ───▶ check at +30s, +90s, +4m        (badge clears on its own)
  //
  // Every ask but ↻ is answered from the server's cache while it is fresh
  // (cmd/catway/plugins.go), so the polling is nearly free and several windows
  // share one round of network traffic. The cache is keyed by the installed
  // commit, which is what lets the post-update rechecks work without any
  // invalidation: the update moves HEAD, the next ask misses and re-checks.
  let pluginUpdates = new Map(); // id → PluginUpdateInfo from the last answer
  let pluginUpdatesAt = 0;       // when that answer arrived (ms), 0 = never
  let pluginCheckBusy = false;
  let pluginCheckWaiters = [];   // callbacks of asks that joined an in-flight check
  let pluginCheckTimer = null;
  const PLUGIN_CHECK_FIRST_MS = 8000;         // let the connect burst settle first
  const PLUGIN_CHECK_EVERY_MS = 60 * 60 * 1000;
  // A cmd_result callback dies with its socket, so a check sent just before a
  // disconnect would never answer and busy would latch for the page's life.
  // The guard releases it; it only has to exceed the server's worst case
  // (a few 20s-timeout rounds across the worker pool).
  const PLUGIN_CHECK_GUARD_MS = 90 * 1000;

  // checkPluginUpdates runs (or joins) a check and calls done(res) with the
  // cmd_result — or null when the guard fired. A second ask while one is in
  // flight joins it rather than sending another, even a forced one: the reply
  // already on its way is at most seconds old, which is all ↻ asks for.
  function checkPluginUpdates(force, done) {
    if (done) pluginCheckWaiters.push(done);
    if (pluginCheckBusy) return;
    pluginCheckBusy = true;
    let settled = false;
    const finish = (res) => {
      if (settled) return; // the guard and a late reply can both arrive
      settled = true;
      clearTimeout(guard);
      pluginCheckBusy = false;
      if (res && res.ok) {
        const d = res.data || {};
        pluginUpdates = new Map((d.plugins || []).map((i) => [i.id, i]));
        pluginUpdatesAt = Date.now();
      }
      paintPluginBadge();
      const waiters = pluginCheckWaiters; pluginCheckWaiters = [];
      for (const fn of waiters) fn(res);
    };
    const guard = setTimeout(() => finish(null), PLUGIN_CHECK_GUARD_MS);
    sendCmdAwait("plugin.check_updates", force ? { force: true } : {}, finish);
  }

  // schedulePluginUpdateChecks (re)starts the background cadence; the session
  // calls it on every socket open, so a reconnect restarts the clock instead of
  // stacking a second timer chain on the first.
  function schedulePluginUpdateChecks() {
    clearTimeout(pluginCheckTimer);
    const tick = (delay) => {
      pluginCheckTimer = setTimeout(() => {
        checkPluginUpdates(false);
        tick(PLUGIN_CHECK_EVERY_MS);
      }, delay);
    };
    tick(PLUGIN_CHECK_FIRST_MS);
  }

  // pluginRecheckSoon follows an update tab (and a failed default's install
  // tab, whose landing clears the toolbar's warning mark the same way). There
  // is no event for "the catctl tab finished", so a few spaced rechecks stand
  // in for one: an update is
  // usually seconds of git plus a build of up to a few minutes, and whichever
  // recheck lands after HEAD moved clears the badge. The ones before it are
  // cache hits and cost nothing.
  function pluginRecheckSoon() {
    for (const ms of [30e3, 90e3, 240e3]) setTimeout(() => checkPluginUpdates(false), ms);
  }

  function pluginUpdateInfo(id) { return pluginUpdates.get(id) || null; }
  function pluginHasUpdate(id) {
    const u = pluginUpdates.get(id);
    return !!u && u.status === "available";
  }
  function pluginUpdateCount() {
    let n = 0;
    for (const u of pluginUpdates.values()) if (u.status === "available") n++;
    return n;
  }

  // ---- Plugin notice (failed default plugins) ----
  //
  // plugin_notice carries the ids of the default plugins the first-run seed
  // could not install. The server pushes it whenever that changes and once
  // per connect (cmd/catway/plugins.go), so this is a mirror, never a query.
  // It is the toolbar's half of the dialog's failed-default rows: the rows
  // say why, the mark says "look here" to a user who has no reason to open
  // the dialog, the one a fresh install without Go leaves wondering where
  // cats-todo went.
  let pluginFailedDefaults = []; // ids, in the server's order

  function applyPluginNotice(msg) {
    pluginFailedDefaults = msg.failed_defaults || [];
    paintPluginBadge();
  }

  // paintPluginBadge draws both of the plugins button's marks and its tooltip:
  //
  //   ⧉ plugins [!] [2]
  //             │    └ .n — updates available (accent chip, a count)
  //             └ .w — a default plugin could not be installed (warn chip)
  //
  // Both slots are server-rendered and hidden while empty. The warning is a
  // bare "!" rather than a count: it is rarely more than one plugin, and the
  // tooltip names each one. Both are said in the tooltip, since a mark on a
  // button is only obvious once you know what it means.
  function paintPluginBadge() {
    const n = pluginUpdateCount();
    const el = pluginsBtnEl.querySelector(".n");
    if (el) el.textContent = n ? String(n) : "";
    pluginsBtnEl.classList.toggle("has-updates", n > 0);
    const failed = pluginFailedDefaults.map((id) => pluginDisplayName({ id }));
    const w = pluginsBtnEl.querySelector(".w");
    if (w) w.textContent = failed.length ? "!" : "";
    pluginsBtnEl.classList.toggle("has-failed", failed.length > 0);
    const lines = [];
    if (failed.length) lines.push(failed.join(", ") + " could not be installed — open for details");
    if (n) lines.push(n + (n === 1 ? " update" : " updates") + " available");
    pluginsBtnEl.title = lines.length ? "plugins — " + lines.join("\n") : "plugins — install, run, update";
  }

  // pluginUpdateTarget is how an available update names where it goes: the
  // upstream version when the manifest was bumped, else the upstream commit —
  // new commits under an unchanged version string are still an update, and
  // repeating the same "v0.41.1" on both sides of the arrow would read as none.
  function pluginUpdateTarget(u) {
    if (u.latest_version && u.latest_version !== u.current_version) return "v" + u.latest_version;
    return u.latest_commit || "new commits";
  }

  // pluginAvatarVar picks one of the six agent hues for a plugin's monogram,
  // hashed from the id so a plugin keeps its colour across sessions and
  // machines. The agent palette is reused rather than inventing six more
  // colours: it is already tuned to sit on the panel background in every theme.
  function pluginAvatarVar(id) {
    let h = 0;
    for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0;
    return "var(--agent-" + (1 + (h % 6)) + ")";
  }

  // pluginDisplayName is the manifest name, falling back to the id's last
  // dotted segment — "rohanthewiz.cats-todo" reads as "cats-todo". The full id
  // is still on the row (small, muted) because it is what the CLI takes.
  function pluginDisplayName(p) {
    if (p.name) return p.name;
    const dot = p.id.lastIndexOf(".");
    return dot >= 0 ? p.id.slice(dot + 1) : p.id;
  }

  // ---- Plugins dialog ----
  //
  // Layout (one row per plugin; the dialog re-renders in place when a check
  // lands, so opening it never waits on the network):
  //
  //   plugins  2                      1 update available · 2m ago   ↻
  //   ┌──┐ cats-todo  not installed · gave up          rohanthewiz.cats-todo
  //   │ !│ cats-todo could not be installed: build step 1 (sh -c …): exit…
  //   └──┘ sh: go: command not found                        [install][dismiss]
  //   ┌──┐ cats-git  v0.1.0  git  ↑ 9bfe73e               rohanthewiz.cats-git
  //   │ C│ 78c85ac → 9bfe73e · fix(log): keep the cursor…    [run][update][…]
  //   └──┘
  //   ┌──┐ ced  v0.9.0  editor  linked
  //   │ C│ /Users/ro/projs/go/ced                           [run][rebuild][…]
  //   └──┘
  //                                                      [close] [add…]
  //
  // (update all joins the footer once more than one update is waiting.)
  //
  // The first kind of row is a default plugin the first-run seed failed to
  // install (plugin.list's failed_defaults), drawn above the installed ones
  // and not counted in the header. A fresh install is where it matters:
  // without it, a user with no Go toolchain finds cats-todo simply absent,
  // with the reason only in the daemon log.
  function openPluginsDialog() {
    sendCmdAwait("plugin.list", {}, (res) => {
      if (!res.ok) { toast("plugins: " + (res.error || "unknown")); return; }
      const info = res.data || {};
      const plugins = info.plugins || [];
      const failedDefaults = info.failed_defaults || [];
      let checking = false;
      let checkFailed = false;
      let ov = null;
      let listEl, statusEl, recheckBtn, btnsEl;

      // paint redraws everything that depends on the update answer: the
      // header's summary, every row, and the footer (update all appears and
      // disappears with the count). Rows are rebuilt rather than patched — a
      // plugin list is a handful of rows, and rebuilding keeps one code path.
      const paint = () => {
        if (!ov || modalEl !== ov) return; // the dialog closed while a check was out
        paintStatus();
        listEl.textContent = "";
        // Failure notices go first. They explain a gap in the list below, and
        // in the fresh-install case the list below is empty.
        for (const f of failedDefaults) listEl.appendChild(failedDefaultRow(f));
        if (!plugins.length) {
          const e = document.createElement("div"); e.className = "empty";
          e.textContent = "no plugins installed — add… one from GitHub or a local checkout";
          listEl.appendChild(e);
        }
        for (const p of plugins) listEl.appendChild(pluginRow(p));
        paintButtons();
      };

      const paintStatus = () => {
        statusEl.textContent = "";
        statusEl.className = "chk";
        const n = plugins.filter((p) => pluginHasUpdate(p.id)).length;
        if (checking) {
          const s = document.createElement("span"); s.className = "spin";
          statusEl.appendChild(s);
          statusEl.appendChild(document.createTextNode("checking for updates…"));
        } else if (n) {
          statusEl.classList.add("hot");
          statusEl.textContent = "↑ " + n + (n === 1 ? " update" : " updates") + " available";
        } else if (checkFailed) {
          statusEl.classList.add("warn");
          statusEl.textContent = "update check failed";
        } else if (pluginUpdatesAt) {
          statusEl.textContent = "all up to date · " + fmtAge(Date.now() - pluginUpdatesAt);
        }
        recheckBtn.disabled = checking;
      };

      const paintButtons = () => {
        btnsEl.textContent = "";
        btnsEl.appendChild(mkModalBtn("close", "", closeModal));
        // update all only when it saves clicks: with one update pending, the
        // row's own highlighted button is the same action in the same place.
        const pending = plugins.filter((p) => !p.broken && !p.linked && pluginHasUpdate(p.id));
        if (pending.length > 1) {
          btnsEl.appendChild(mkModalBtn("update all (" + pending.length + ")", "", () => {
            pluginCatctlTab(info.catctl, "update plugins", ["update"].concat(pending.map((p) => p.id)));
            pluginRecheckSoon();
          }));
        }
        btnsEl.appendChild(mkModalBtn("add…", "primary", () => pluginInstallDialog(info.catctl)));
      };

      // runCheck asks for a fresh answer and repaints when it lands. The first
      // paint does not wait: rows render from the last known answer (the
      // background check usually has one) and the spinner says a newer one is
      // on its way.
      const runCheck = (force) => {
        if (!plugins.some((p) => !p.broken && !p.linked)) return; // nothing checkable
        checking = true;
        checkFailed = false;
        paint();
        checkPluginUpdates(force, (r) => {
          checking = false;
          checkFailed = !(r && r.ok);
          paint();
        });
      };

      const pluginRow = (p) => {
        const u = pluginUpdateInfo(p.id);
        const hasUpd = pluginHasUpdate(p.id);
        const row = document.createElement("div");
        row.className = "row plg" + (hasUpd ? " upd" : "") + (p.broken ? " broken" : "");

        // The tooltip carries the long-form facts a row has no room for: the
        // description in full, where it lives, and what the last check said.
        const tip = [p.broken || p.description, p.id, p.dir];
        if (p.source) tip.push("source: " + p.source + (p.ref ? " @ " + p.ref : ""));
        if (u && u.status === "available") {
          tip.push("update: " + (u.current_commit || "?") + " → " + (u.latest_commit || "?") +
            (u.latest_subject ? " — " + u.latest_subject : ""));
        } else if (u && u.status === "error") {
          tip.push("update check failed: " + u.reason);
        }
        if (u && u.checked_at) tip.push("checked " + fmtAge(Date.now() - u.checked_at));
        row.title = tip.filter(Boolean).join("\n");

        // Monogram: first letter of the display name on the plugin's hash hue.
        // A broken plugin gets a neutral "!" instead — its colour identity is
        // less important than the fact that it cannot run.
        const av = document.createElement("span"); av.className = "av";
        av.textContent = p.broken ? "!" : (pluginDisplayName(p)[0] || "?").toUpperCase();
        if (!p.broken) av.style.setProperty("--av", pluginAvatarVar(p.id));
        row.appendChild(av);

        const main = document.createElement("div"); main.className = "main";
        const l1 = document.createElement("div"); l1.className = "l1";
        const pill = (text, cls) => {
          const s = document.createElement("span"); s.className = "pill " + cls;
          s.textContent = text; l1.appendChild(s);
        };
        const nm = document.createElement("span"); nm.className = "nm";
        nm.textContent = p.broken ? p.id : pluginDisplayName(p);
        l1.appendChild(nm);
        if (!p.broken) {
          pill("v" + (p.version || "?"), "ver");
          if (p.type) pill(p.type, "typ");
        }
        // Status pills, same rule as before: only when there is a status, so
        // the plain installed case adds nothing.
        const status = pluginStatus(p);
        if (status) pill(status, "st " + status);
        if (hasUpd) pill("↑ " + pluginUpdateTarget(u), "new");
        else if (checking && !p.broken && !p.linked) {
          const s = document.createElement("span"); s.className = "spin"; l1.appendChild(s);
        }
        // The full id trails the name as the part that gives way first (see
        // .pid in 21-plugins.css): the name is what you scan for, the id is
        // what you would type.
        if (!p.broken && p.id !== nm.textContent) {
          const pid = document.createElement("span"); pid.className = "pid";
          pid.textContent = p.id;
          l1.appendChild(pid);
        }
        main.appendChild(l1);

        // Second line: the one fact most worth reading for this row's state.
        // An update outranks the description (it is news; the description is
        // not), a linked plugin's checkout path outranks it too (which
        // worktree am I linked to? — the identifying fact for a dev link), and
        // a broken one shows its error.
        const l2 = document.createElement("div"); l2.className = "l2";
        if (p.broken) {
          l2.classList.add("err"); l2.textContent = p.broken;
        } else if (hasUpd) {
          l2.classList.add("new");
          const from = u.current_commit || "", to = u.latest_commit || "";
          l2.textContent = (from && to ? from + " → " + to : "update available") +
            (u.latest_subject ? " · " + u.latest_subject : "");
        } else if (p.linked && p.dir) {
          l2.classList.add("path"); l2.textContent = p.dir;
        } else if (u && u.status === "error") {
          l2.classList.add("warn"); l2.textContent = "couldn't check for updates: " + u.reason;
        } else {
          l2.textContent = p.description || "";
        }
        if (l2.textContent) main.appendChild(l2);
        row.appendChild(main);

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
          // Highlighted when upstream has something: the row's call to action
          // should be the button that acts on the news it is showing.
          const updTip = hasUpd ? "update to " + pluginUpdateTarget(u)
            : (u && u.status === "current") ? "already up to date — re-checks upstream and re-syncs bin links" : "";
          actBtn("update", hasUpd ? "hot" : "", () => {
            pluginCatctlTab(info.catctl, "update " + p.id, ["update", p.id]);
            pluginRecheckSoon();
          }, updTip);
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
        return row;
      };

      // failedDefaultRow draws a default plugin that could not be installed,
      // in the same card anatomy as an installed plugin's row so the list
      // reads as one set: "!" tile, name and id, the error, then the output
      // line that usually says *why* (git's or the build's own complaint;
      // the error alone often names just the failing step).
      //
      // install re-runs the same `catctl plugin install <source>` the seed
      // ran, in a tab, so this time the user watches the output. Once the
      // plugin is present the notice drops out of plugin.list by itself.
      // dismiss is "I don't want it": the server forgets the default, which
      // also stops the seed from retrying it on the next start.
      const failedDefaultRow = (f) => {
        const row = document.createElement("div");
        row.className = "row plg dflt";
        const when = f.gave_up
          ? "gave up after " + f.attempts + (f.attempts === 1 ? " attempt" : " attempts")
          : "failed " + f.attempts + (f.attempts === 1 ? " time" : " times") + " · retries when cats restarts";
        row.title = [
          "default plugin " + f.id + " could not be installed (" + when + ")",
          f.error, f.output, "source: " + f.source,
        ].filter(Boolean).join("\n");

        const av = document.createElement("span"); av.className = "av"; av.textContent = "!";
        row.appendChild(av);

        const main = document.createElement("div"); main.className = "main";
        const l1 = document.createElement("div"); l1.className = "l1";
        const nm = document.createElement("span"); nm.className = "nm";
        nm.textContent = pluginDisplayName({ id: f.id });
        l1.appendChild(nm);
        const pill = document.createElement("span"); pill.className = "pill st missing";
        pill.textContent = f.gave_up ? "not installed · gave up" : "not installed · will retry";
        l1.appendChild(pill);
        if (f.id !== nm.textContent) {
          const pid = document.createElement("span"); pid.className = "pid";
          pid.textContent = f.id;
          l1.appendChild(pid);
        }
        main.appendChild(l1);

        const l2 = document.createElement("div"); l2.className = "l2 warn";
        l2.textContent = nm.textContent + " could not be installed: " + (f.error || "unknown error");
        main.appendChild(l2);
        // The output's last line, when there is one: the build's final word
        // is usually the reason ("go: command not found"). The full tail is
        // in the tooltip.
        const lastOut = (f.output || "").trim().split("\n").pop();
        if (lastOut) {
          const l3 = document.createElement("div"); l3.className = "l2 path";
          l3.textContent = lastOut;
          main.appendChild(l3);
        }
        row.appendChild(main);

        const acts = document.createElement("div"); acts.className = "acts";
        const install = document.createElement("button"); install.className = "hot";
        install.textContent = "install";
        install.title = "catctl plugin install " + f.source + " — runs in a new tab";
        // The rechecks after the tab (as for an update) are what clear the
        // toolbar mark once the install lands: catway does not see a catctl
        // install, but the update check re-reads the notice (plugins.go).
        install.addEventListener("click", () => {
          pluginCatctlTab(info.catctl, "plugin install", ["install", f.source]);
          pluginRecheckSoon();
        });
        acts.appendChild(install);
        const dismiss = document.createElement("button");
        dismiss.textContent = "dismiss";
        dismiss.title = "hide this notice and stop retrying the install";
        dismiss.addEventListener("click", () =>
          sendCmdAwait("plugin.dismiss_default", { id: f.id }, (res) => {
            if (!res.ok) { toast("dismiss failed: " + (res.error || "unknown")); return; }
            openPluginsDialog(); // refresh the list in place
          }));
        acts.appendChild(dismiss);
        row.appendChild(acts);
        return row;
      };

      ov = openOverlay((ov) => {
        const m = document.createElement("div"); m.className = "modal pal plugins";
        const h = document.createElement("header"); h.className = "plg-head";
        const title = document.createElement("span"); title.textContent = "plugins";
        h.appendChild(title);
        if (plugins.length) {
          const cnt = document.createElement("span"); cnt.className = "cnt";
          cnt.textContent = String(plugins.length);
          h.appendChild(cnt);
        }
        statusEl = document.createElement("span"); statusEl.className = "chk";
        h.appendChild(statusEl);
        recheckBtn = document.createElement("button"); recheckBtn.className = "recheck";
        recheckBtn.textContent = "↻";
        recheckBtn.title = "check for updates now";
        recheckBtn.addEventListener("click", () => runCheck(true));
        h.appendChild(recheckBtn);
        m.appendChild(h);
        listEl = document.createElement("div"); listEl.className = "list"; m.appendChild(listEl);
        btnsEl = document.createElement("div"); btnsEl.className = "btns"; m.appendChild(btnsEl);
        m.addEventListener("keydown", (e) => {
          e.stopPropagation();
          if (e.key === "Escape") { e.preventDefault(); closeModal(); }
        });
        ov.appendChild(m);
      });
      paint();
      runCheck(false);
    });
  }
