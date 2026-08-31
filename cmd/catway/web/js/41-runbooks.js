  // ---- RUNBOOKS (runbook.list / runbook.run) ----
  //
  // The shelf the macro recorder writes onto, and the section that finally makes
  // a saved recording findable: before this, `catctl record stop deploy` wrote a
  // file the browser had no way to see, list or run.
  //
  // A QUERY section, not a pushed one. Runbooks are files — written by the
  // recorder, and just as often by an editor the session knows nothing about —
  // so there is no stream of changes to subscribe to, and the server re-scans
  // the directory on every runbook.list (see runbook.Set for why it caches
  // nothing: an "edit, run" that silently ran the previous version is a bug
  // whose symptom is a correct-looking run of the wrong steps). The section
  // therefore refreshes on the moments that can have changed it and on a button
  // for everything else:
  //
  //   connect          a reconnecting window has been away and cannot know
  //   recording ended  the one moment this UI itself creates a runbook
  //   run finished     the files are unchanged, but trigger_status is not
  //   the ⟳ button     an edit in $EDITOR, a file dropped in by hand, a delete
  //
  // Hidden until there is something in it, like Hosts and History: an install
  // that never recorded a macro sees exactly the sidebar it always had, and the
  // section appears by itself the first time a recording is saved.
  //
  // A click RUNS — that is the verb a runbook row implies — but never straight
  // away. Every other clickable row in this column moves the viewport; this one
  // splits panes and types into shells, with no undo, so the click opens a
  // dialog that says what is about to happen first. A runbook with vars has to
  // ask anyway, which is what makes the gate one rule rather than an exception.

  let runbookItems = [];              // the last runbook.list, newest wins
  let runbookDir = "";                // the directory it was read from
  // The runs THIS window is waiting on, by name. A set rather than a flag
  // because nothing stops two different runbooks overlapping, and a run keyed
  // by name is the only thing a row can match itself against.
  const runbookRunning = new Set();

  let rbBtnEl = null;
  (function initRunbookHeadingCtl() {
    const el = document.getElementById("rb-hctl");
    rbBtnEl = mkBtn("", "re-read the runbook directory", "uref", () => refreshRunbooks(true));
    rbBtnEl.appendChild(refreshMark());
    el.appendChild(rbBtnEl);
    initSectionFold("sec-runbooks", "rb-hctl", "runbooks");
  })();

  // refreshRunbooks re-reads the directory. Single-flight with a trailing
  // re-run, the same shape refreshPaneList uses and for the same reason: the
  // callers arrive in bursts (a reconnect lands a welcome and a layout
  // together), and the answer costs the server a readdir plus a parse of every
  // file in it, so a burst has to collapse to one round trip and at most one
  // follow-up.
  //
  // spin lights the heading control. Only the button passes it — the automatic
  // refreshes are not something the user asked for, and a mark that turns on
  // its own every time a recording stops reads as the section doing work rather
  // than as the section being current.
  let rbBusy = false, rbAgain = false;
  function refreshRunbooks(spin) {
    if (spin) rbBtnEl.classList.add("busy");
    if (rbBusy) { rbAgain = true; return; }
    rbBusy = true;
    sendCmdAwait("runbook.list", {}, (res) => {
      rbBusy = false;
      rbBtnEl.classList.remove("busy");
      // A failure leaves the last good listing on screen. The only failure this
      // command has is "no config directory is resolvable", which is a property
      // of the machine rather than of this call — retrying it would say the
      // same thing, and wiping the section to report it would take away the
      // rows the user was reading.
      if (res.ok && res.data) {
        runbookDir = res.data.dir || "";
        runbookItems = res.data.runbooks || [];
        // The directory is only known once the server has answered, and it is
        // the answer to "where do these live?" — a question the section
        // otherwise leaves the user to work out from a row's own tooltip.
        rbBtnEl.title = "re-read " + (runbookDir || "the runbook directory");
        renderRunbooks();
      }
      if (rbAgain) { rbAgain = false; refreshRunbooks(false); }
    });
  }

  function renderRunbooks() {
    // An empty directory hides the section outright rather than showing an
    // empty one inviting a question it cannot answer. A BROKEN file counts as
    // something: the whole reason runbook.list reports the files that failed to
    // parse is that a runbook missing from a listing is indistinguishable from
    // one that was never written.
    rbSecEl.hidden = runbookItems.length === 0;
    rbListEl.innerHTML = "";
    for (const rb of runbookItems) {
      const broken = !!rb.error;
      const running = runbookRunning.has(rb.name);
      const triggered = !broken && !!(rb.triggers && rb.triggers.length);

      const li = document.createElement("li");
      const cls = [];
      if (broken) cls.push("broken");
      if (running) cls.push("running");
      // Amber only when the runbook HAS triggers and they are not armed. A
      // runbook with no `on:` clause has no trigger status to be wrong about.
      if (triggered && rb.trigger_status) cls.push("trig-off");
      li.className = cls.join(" ");

      const dot = document.createElement("span");
      dot.className = "rdot";
      dot.textContent = broken ? "✕" : (running ? "●" : "▸");
      const name = document.createElement("span");
      name.className = "rname"; name.textContent = rb.name;
      li.appendChild(dot); li.appendChild(name);

      if (triggered) {
        const t = document.createElement("span");
        t.className = "rtrig"; t.textContent = "⚡";
        li.appendChild(t);
      }
      // No count on a broken file: it never parsed, so there is no step count
      // to report and a 0 there would read as "an empty runbook", which is a
      // different and valid thing.
      if (!broken && rb.steps) {
        const s = document.createElement("span");
        s.className = "rsteps"; s.textContent = String(rb.steps);
        li.appendChild(s);
      }

      li.title = runbookTitle(rb, broken, running);
      // A broken file's click OPENS it, because that is the only useful verb
      // for a row whose whole content is an error message.
      li.addEventListener("click", () => {
        if (broken) openRunbookFile(rb); else startRunbookRun(rb);
      });
      li.addEventListener("contextmenu", (ev) => {
        ev.preventDefault();
        openCtx(ev.clientX, ev.clientY, runbookMenuItems(rb, broken));
      });
      rbListEl.appendChild(li);
    }
  }

  // runbookTitle is everything an 11px row cannot hold: what the runbook is
  // for, what it will ask for, and — the one fact nothing else in the UI
  // reports — why its triggers are not currently armed.
  function runbookTitle(rb, broken, running) {
    if (broken) {
      return rb.name + "\n" + rb.path + "\n" + runbookError(rb) +
        "\nclick to open it in the editor";
    }
    const lines = [rb.name];
    if (rb.description) lines.push(rb.description);
    lines.push(stepCount(rb.steps) +
      (rb.vars && rb.vars.length ? " · vars: " + rb.vars.join(", ") : ""));
    if (rb.triggers && rb.triggers.length) {
      lines.push("runs itself on: " + rb.triggers.join(", ") +
        (rb.trigger_status ? "\ntriggers not armed: " + rb.trigger_status : ""));
    }
    lines.push(running ? "running…" : "click to run · right-click for more");
    return lines.join("\n");
  }

  function stepCount(n) { return n + (n === 1 ? " step" : " steps"); }

  // runbookError drops the leading file path a load error carries. Parse
  // prefixes every message with the file it came from ("<path>: step 1: …")
  // because a CLI listing has no other way to say which file it means — but a
  // row's title has already given the path its own line, and repeating an
  // absolute path pushes the half that matters (which step, and what about it)
  // off the readable end of the tooltip.
  //
  // Only the TITLE trims it. "copy error" in the menu keeps the original,
  // because a message pasted into an editor or an issue has lost the row it
  // came from and needs to name its own file.
  function runbookError(rb) {
    const e = rb.error || "";
    return e.startsWith(rb.path + ": ") ? e.slice(rb.path.length + 2) : e;
  }

  // startRunbookRun is the gate between a click and a sequence of side effects
  // on a live desktop.
  //
  // Two dialogs rather than one: a runbook that declares vars has to be asked
  // about them, and dialogFields with an empty field list is a prompt with
  // nothing to type into and nowhere to put focus. They say the same thing —
  // this is what it is, and it runs N steps against this session — so a user
  // meets one gate either way.
  function startRunbookRun(rb) {
    // A second click on a row already running would start a genuine second run;
    // the server allows it (the concurrency slot is per name and this window
    // holds it) only in the sense that it refuses it, and a refusal toast is a
    // worse answer than not asking.
    if (runbookRunning.has(rb.name)) { toast(rb.name + " is already running"); return; }
    if (!rb.vars || !rb.vars.length) {
      dialogConfirm({
        title: "run runbook",
        message: "Run “" + rb.name + "”? " + (rb.description ? rb.description + " — " : "") +
          "it runs " + stepCount(rb.steps) + " against this session.",
        confirmLabel: "run",
        onConfirm: () => runRunbook(rb, {}),
      });
      return;
    }
    dialogFields({
      title: "run " + rb.name,
      submitLabel: "run",
      hint: (rb.description ? rb.description + " · " : "") + stepCount(rb.steps) +
        " · a blank field keeps the runbook's own default",
      fields: rb.vars.map((v) => ({ label: v, value: "", placeholder: "default" })),
      onSubmit: (...vals) => {
        // Only the fields the user actually filled in are sent. An empty field
        // means "leave the declared default alone"; sending "" would OVERRIDE
        // that default with an empty string, which substitutes nothing into a
        // step that needed something and fails several steps later.
        const vars = {};
        rb.vars.forEach((v, i) => {
          const s = (vals[i] || "").trim();
          if (s) vars[v] = s;
        });
        runRunbook(rb, vars);
      },
    });
  }

  // runRunbook issues the run and reports what became of it.
  //
  // runbook.run answers when the LAST step has finished, not when the run
  // starts, so the row stays marked for the whole of it — which is the only
  // reason the mark is worth painting: a runbook that waits on a build can sit
  // there for minutes.
  //
  // Runs started anywhere ELSE — catctl, a plugin, an `on:` trigger, another
  // window — are not marked, because the session broadcasts no runbook state.
  // That gap is deliberate and it is the same one the recorder documents
  // (broadcastRecord, cmd/catway/record.go): closing it means a new
  // down-message, and a per-step one would feed emitEvent → fireRunbookTriggers
  // and give a runbook an event to trigger on that its own steps produce. The
  // ⚡ mark and the refresh button cover the part that matters — whether the
  // triggers are armed — without inventing that loop.
  function runRunbook(rb, vars) {
    runbookRunning.add(rb.name);
    renderRunbooks();
    sendCmdAwait("runbook.run", { name: rb.name, vars }, (res) => {
      runbookRunning.delete(rb.name);
      renderRunbooks();
      // The listing is stale by the end of a run whether or not the files
      // changed: a run in flight is one of the things trigger_status reports,
      // so the ⚡ marks were amber for the duration and have to come back.
      refreshRunbooks(false);
      if (!res.ok) { toast(rb.name + ": " + (res.error || "run failed")); return; }
      const steps = (res.data || {}).steps || [];
      if (!(res.data || {}).failed) {
        toast(rb.name + ": " + stepCount(steps.length) + " ok");
        return;
      }
      // The first failure is the whole story: a run stops at its first
      // untolerated one, and a run that continued past a tolerated failure said
      // so at the step. The step's own index and command name are what a reader
      // needs to find it in the file.
      const bad = steps.find((s) => s.error);
      toast(bad
        ? rb.name + ": step " + bad.index + " (" + bad.run + ") failed — " + bad.error
        : rb.name + ": failed");
    });
  }

  // runbookMenuItems: the verbs that are not the click. Editing and the CLI
  // spelling live here rather than on the row for the reason History's copy
  // does — the click is the one thing worth doing without a menu, and putting a
  // second verb beside it would make the row a toolbar.
  function runbookMenuItems(rb, broken) {
    const items = [];
    if (!broken) items.push({ label: "run…", fn: () => startRunbookRun(rb) });
    items.push({ label: "open in editor", fn: () => openRunbookFile(rb) });
    items.push({ label: "copy path", fn: () => clipWrite(rb.path) });
    // A runbook worth running from the sidebar is usually one worth putting in
    // a script, a keybinding or another machine's shell, and the CLI spelling
    // is not guessable from the row.
    if (!broken) {
      items.push({ label: "copy catctl command", fn: () => clipWrite("catctl runbook " + rb.name) });
    }
    if (broken) items.push({ label: "copy error", fn: () => clipWrite(rb.error) });
    return items;
  }

  // openRunbookFile hands the path to the session's editor. That is the only
  // "edit" this surface can honestly offer — the browser has no text editor,
  // and writing one to edit YAML that the server already validates on load
  // would be a second, worse validator.
  //
  // The host is named EXPLICITLY as the local one rather than left to the
  // anchor pane's machine. Runbooks live under the catway process's own config
  // directory, so in a multi-host session the focused pane is very often on a
  // different box — and pane.open_file resolves the path against whatever
  // machine it lands on, where the same string is a different file or none
  // (see OpenFileParams). "" (the roster has not arrived yet) falls back to the
  // anchor, which is right in the single-host case that "" almost always means.
  function openRunbookFile(rb) {
    const params = { path: rb.path };
    const host = localHostId();
    if (host) params.host = host;
    sendCmdAwait("pane.open_file", params, (res) => {
      if (!res.ok) toast("could not open " + rb.path + ": " + (res.error || "no editor"));
    });
  }
