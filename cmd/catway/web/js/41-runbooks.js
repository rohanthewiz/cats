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
  //   a run finished   the files are unchanged, but trigger_status is not
  //   the ⟳ button     an edit in $EDITOR, a file dropped in by hand, a delete
  //
  // The FILES are a query; the RUNS are pushed. Those are two different facts
  // and only the first one lives on disk: a run is session state, held in one
  // accounting of runs in flight that every start goes through, so the server
  // broadcasts the whole set on every start and every finish (runbook_runs).
  // That is what makes a row light up for a run this window did not start —
  // another window, `catctl runbook deploy`, a plugin, or an `on:` clause
  // firing by itself, which is the case that matters most because it is the
  // session acting while nobody asked it to.
  //
  // Each run carries its position, which the row shows where the step count
  // normally sits ("3/7") and as a hairline under it. A step can legitimately
  // sit on a build for minutes, so a mark that only blinks cannot tell a long
  // step from a wedged one; a number that moves can.
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
  // Every run in flight in the SESSION, name → {source, trigger, started_at},
  // replaced wholesale by each runbook_runs broadcast. Keyed by name because
  // the server allows one run per runbook at a time, which makes the name the
  // only thing a row has to match itself against.
  let runbookRuns = new Map();
  // The names THIS window has asked to run and not yet been answered about.
  //
  // Two things, neither of which the broadcast can do on its own. It covers the
  // round trip between the click and the server's first message, so a row marks
  // itself immediately and a second click cannot start a second run that the
  // server would only refuse. And it is how a row knows the run in flight is
  // OURS — the broadcast reports a run, not a requester, and it should not: the
  // server has no business tracking which socket asked for what, and this side
  // already knows.
  const runbookPending = new Set();

  // applyRunbookRuns takes one runbook_runs broadcast.
  //
  // The message is the WHOLE set, so this replaces rather than patches: a
  // message that went missing, or two that were coalesced, converge on the next
  // one instead of leaving a row marked for a run that ended. That is the same
  // property the connect burst relies on to un-mark a window that was away
  // across the end of a run.
  function applyRunbookRuns(msg) {
    const prev = runbookRuns;
    runbookRuns = new Map();
    for (const r of (msg.runs || [])) if (r && r.name) runbookRuns.set(r.name, r);
    // A run that ENDED is the edge the listing cares about. The files are
    // unchanged, but trigger_status is not: "a run is in flight" is one of the
    // things it reports, so the ⚡ marks were amber for the duration and have to
    // come back — and now that this fires for runs started anywhere, they come
    // back after a trigger's own run too, which is the case where nobody was
    // watching to press ⟳.
    //
    // Only the falling edge. Refreshing when a run STARTS would ask the server
    // to re-read the directory at the exact moment it is busiest, to learn
    // something this message just said.
    let ended = false;
    for (const name of prev.keys()) if (!runbookRuns.has(name)) { ended = true; break; }
    renderRunbooks();
    if (ended) refreshRunbooks(false);
  }

  // runbookRunOf is the run in flight for one runbook, or null. `local` is
  // this window's own claim on it, which is not on the wire — see runbookPending.
  function runbookRunOf(name) {
    const local = runbookPending.has(name);
    const run = runbookRuns.get(name) || (local ? { source: "control" } : null);
    return run ? { ...run, local } : null;
  }

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
      const run = runbookRunOf(rb.name);   // null unless something is running it
      const running = !!run;
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
      //
      // While a run is in flight the same column carries its POSITION — "3/7"
      // where the total was. The column rather than a new one because the number
      // there has always answered "how big is this?", and during a run the
      // honest answer to that is "this big, and here is where it has got to".
      // Two numbers side by side would make the row a readout.
      if (!broken && (rb.steps || (run && run.steps))) {
        const s = document.createElement("span");
        s.className = "rsteps";
        s.textContent = runbookCount(rb, run);
        if (run && run.step) s.classList.add("prog");
        li.appendChild(s);
      }
      // The progress bar reads the fraction off a custom property so the
      // stylesheet owns everything about how it looks and this owns only the
      // number. Set on the row rather than on a child because the bar is drawn
      // as the row's own ::after — a full-width element under a flex row would
      // have to be excluded from the layout it is not part of.
      if (run && run.step && run.steps) {
        li.style.setProperty("--rbprog", (100 * run.step / run.steps).toFixed(1) + "%");
      }

      li.title = runbookTitle(rb, broken, run);
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
  function runbookTitle(rb, broken, run) {
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
    if (run) {
      // The position goes first: on a run that has stopped moving, "step 4 of 7"
      // is the line that says WHICH step to go and look at, and the origin is
      // the follow-up question.
      lines.push(run.step ? "running step " + run.step + " of " + (run.steps || rb.steps) : "running…");
      lines.push(runOrigin(run));
    } else {
      lines.push("click to run · right-click for more");
    }
    return lines.join("\n");
  }

  // runOrigin says WHO started the run, which is the whole point of hearing
  // about runs this window did not start. "running…" alone answers a question
  // the user does not have — they can see the dot — and leaves the one they do:
  // panes are appearing and nobody in this window asked for them.
  //
  // The trigger's event name is named where there is one, because it is the
  // word to grep the YAML for.
  function runOrigin(run) {
    if (run.local) return "started here";
    if (run.source === "trigger") {
      return "started by its " + (run.trigger ? run.trigger + " trigger" : "own trigger");
    }
    // Not this window and not a trigger: another window, catctl, or a plugin.
    // They are one case on purpose — the session cannot tell them apart, and
    // inventing a distinction it does not have would be worse than the honest
    // "somebody else".
    return "started outside this window";
  }

  function stepCount(n) { return n + (n === 1 ? " step" : " steps"); }

  // runbookCount is what the numeric column holds: the document's length when
  // nothing is running, the run's position in it when something is.
  //
  // The run's own total wins over the listing's. They are normally the same
  // number, but a file edited while it runs makes the listing describe a
  // document this run is not executing — and the count beside a moving position
  // has to be the one that position is counting towards, or the row shows "4/3".
  //
  // A run that has taken its slot and not yet reached step 1 shows the plain
  // total: "0/5" reads as a run that is stuck rather than one that is a
  // millisecond old, and the pulsing dot has already said it is running.
  function runbookCount(rb, run) {
    const total = (run && run.steps) || rb.steps;
    if (run && run.step) return run.step + "/" + total;
    return String(total);
  }

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

  // runbookOutline turns runbook.list's pre-rendered step lines into the two
  // options both dialogs take.
  //
  // The server caps how many lines it sends (a 200-step document must not ride
  // along on a listing that re-reads after every run), so the tail says what was
  // left out — using rb.steps, which is always the TRUE count. Silence there
  // would be the worse failure: a preview that quietly stops at 24 of 200 does
  // not look truncated, it looks like the runbook.
  //
  // An empty outline yields empty options rather than a note, and the dialog is
  // then exactly the one it was before this existed. That is the case for a
  // listing from a server too old to send the field at all.
  function runbookOutline(rb) {
    const lines = rb.outline || [];
    if (!lines.length) return {};
    const rest = (rb.steps || 0) - lines.length;
    return {
      lines,
      linesNote: rest > 0 ? "…and " + rest + " more " + (rest === 1 ? "step" : "steps") : "",
    };
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
    // A click on a row already running is refused here rather than sent: the
    // server's concurrency slot is per runbook name, so the second run would
    // come back "already in flight", and a refusal toast is a worse answer than
    // not asking. Now that the mark covers runs started anywhere, so does this —
    // clicking a row a trigger is running says so instead of firing a doomed
    // command.
    const cur = runbookRunOf(rb.name);
    if (cur) { toast(rb.name + " is already running, " + runOrigin(cur)); return; }
    // Both gates show the STEPS, not just how many. "4 steps against this
    // session" is a number to agree to without knowing what it buys; the
    // outline is the same gate answering the question the number raises. The
    // values still carry their `{{ vars.x }}` references unresolved, which is
    // the point in the fields dialog — the reader can see where what they are
    // about to type ends up.
    const preview = runbookOutline(rb);
    if (!rb.vars || !rb.vars.length) {
      dialogConfirm({
        title: "run runbook",
        message: "Run “" + rb.name + "”? " + (rb.description ? rb.description + " — " : "") +
          "it runs " + stepCount(rb.steps) + " against this session.",
        confirmLabel: "run",
        ...preview,
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
      ...preview,
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
  // The mark itself comes from the server (runbook_runs), including for this
  // very run: the broadcast is sent when the run takes its concurrency slot,
  // which happens inside the dispatch of the command below, so it reaches this
  // window before the reply does. The local pending entry exists only to cover
  // the round trip and to remember that the run in flight is ours.
  //
  // Nothing here refreshes the listing. The run's END arrives as a broadcast
  // like everybody else's, and applyRunbookRuns refreshes on that edge — for
  // every run, not just the ones with a callback attached. Refreshing here too
  // would be a second read of the same directory for the same reason.
  function runRunbook(rb, vars) {
    runbookPending.add(rb.name);
    renderRunbooks();
    sendCmdAwait("runbook.run", { name: rb.name, vars }, (res) => {
      runbookPending.delete(rb.name);
      renderRunbooks();
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
