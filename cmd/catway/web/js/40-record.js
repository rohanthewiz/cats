  // ---- Macro recorder (runbook.record) ----
  //
  // The toolbar's one MODE indicator, and the only piece of chrome that moves
  // on its own. Everything else up there is a door: click it, something opens,
  // the button goes back to looking the way it did. The recorder is a state the
  // whole session is in, and one somebody can walk away from — so the affordance
  // that arms it is also the affordance that has to keep saying it is armed.
  //
  // State arrives as the `record` down-message and NEVER from the replies to
  // this window's own commands. There is one recorder per session and four ways
  // to reach it (this window, another window, `catctl record start`, a plugin or
  // relayed command), so a client that tracked its own clicks would be wrong as
  // soon as it was not the only one clicking. The server broadcasts on every
  // transition and on every captured step, and sends the state once on connect,
  // which is what lets this file hold no optimism at all: it draws what it was
  // last told and nothing else.
  //
  //   idle ──click──► runbook.record start ──► (broadcast) ──► armed
  //   armed ─click──► menu ─► stop and save… ─► name dialog ─► runbook.record stop
  //                        └► cancel recording ─► confirm ─► runbook.record cancel
  //
  // Arming takes a click and no confirmation — nothing exists yet, and the whole
  // design of the recorder is that nothing exists until it is named. Both ways
  // OUT are gated: stop needs a name, and cancel throws away work that cannot be
  // recovered, which is the one destructive thing this surface can do.

  let recState = { recording: false, steps: 0, startedAt: "", note: "" };

  function applyRecord(msg) {
    const prevNote = recState.note, wasRecording = recState.recording;
    recState = {
      recording: !!msg.recording,
      steps: msg.steps || 0,
      startedAt: msg.started_at || "",
      note: msg.note || "",
    };
    renderRecord();
    // The ceiling is announced once, on the edge. The server re-sends the note
    // with every subsequent broadcast (it is part of the state, not an event),
    // so toasting unconditionally would put a toast on screen for every command
    // run for the rest of the recording.
    if (recState.note && recState.note !== prevNote) toast("recording: " + recState.note);
    // A recording that ended is the one moment this UI itself writes a runbook,
    // so the RUNBOOKS section re-reads the directory on that edge.
    //
    // On the edge in EVERY window, rather than in stopRecording's own success
    // callback, because the recorder is session state and the window that saves
    // it is very often not the only one open — and a `catctl record stop` has
    // no callback here at all. A cancel takes the same path and finds nothing
    // new, which costs one readdir on a transition that happens a handful of
    // times a day; telling the two apart would mean putting the outcome on the
    // wire for no other reason.
    if (wasRecording && !recState.recording) refreshRunbooks(false);
  }

  function renderRecord() {
    if (!recBtnEl) return;
    const on = recState.recording;
    recBtnEl.classList.toggle("on", on);
    // "Armed but capturing nothing" gets its own class because it is the one
    // failure the recorder cannot report as an error: the user is doing things,
    // none of them are commands (typing into a shell is keystrokes, not
    // vocabulary), and the file they are about to name will be empty.
    recBtnEl.classList.toggle("empty", on && recState.steps === 0);
    const g = recBtnEl.querySelector(".g");
    if (g) g.textContent = on ? "●" : "◦";
    const n = recBtnEl.querySelector(".n");
    if (n) n.textContent = on ? String(recState.steps) : "";
    recBtnEl.title = on ? recArmedTitle() : "record a macro (runbook.record)";
  }

  // recArmedTitle is the detail the button itself has no room for. The start
  // time is formatted here rather than by the server because this is the side
  // that knows the reader's locale and time zone; the wire carries RFC3339.
  function recArmedTitle() {
    const n = recState.steps;
    const since = recStartedLabel();
    return "recording" + (since ? " since " + since : "") + " — " +
      (n === 0 ? "nothing captured yet" : n + (n === 1 ? " command" : " commands") + " captured") +
      " · click to stop or cancel";
  }

  function recStartedLabel() {
    if (!recState.startedAt) return "";
    const d = new Date(recState.startedAt);
    return isNaN(d.getTime()) ? "" : d.toLocaleTimeString();
  }

  function startRecording() { sendCmd("runbook.record", { action: "start" }); }

  // openRecordMenu is the armed click. A menu rather than a straight toggle:
  // stopping needs a name before it can happen at all, and "throw the recording
  // away" must not be the same gesture as "keep it" with a different outcome.
  function openRecordMenu() {
    const n = recState.steps;
    const items = [
      { label: n === 0 ? "stop and save… (nothing captured yet)" : "stop and save… (" + n + ")", fn: openStopRecordingDialog },
      "-",
      { label: "cancel recording…", danger: true, fn: confirmCancelRecording },
    ];
    const r = recBtnEl.getBoundingClientRect();
    openCtx(r.right, r.bottom + 4, items);
  }

  // openStopRecordingDialog names the recording and writes it.
  //
  // The name is the whole privacy story of this feature — the recording lives in
  // memory and becomes a file only here — so the dialog asks for it rather than
  // inventing one from a timestamp, which would put a file on disk that nobody
  // chose to keep.
  function openStopRecordingDialog() {
    dialogFields({
      title: "stop recording",
      submitLabel: "save runbook",
      hint: recState.steps === 0
        ? "nothing was captured — saving now writes an empty runbook · esc leaves it recording"
        : recState.steps + " captured · saved to ~/.config/cats/runbooks · esc leaves it recording",
      fields: [
        { label: "name", value: "", placeholder: "deploy" },
        { label: "description", value: "", placeholder: "optional — what this runbook does" },
      ],
      onSubmit: (name, description) => {
        const n = (name || "").trim();
        // Refused here rather than sent, because the server's refusal for a
        // missing name would be indistinguishable from its refusal for a name
        // that collides — and the recording would still be armed either way,
        // leaving the user to guess which of the two happened.
        if (!n) { toast("record: a name is needed to save the recording"); return; }
        stopRecording(n, description, false);
      },
    });
  }

  // stopRecording sends the stop, and handles the one failure that is not a
  // mistake: the name is already taken.
  //
  // The retry is offered rather than pre-empted with an "overwrite" checkbox
  // because the server is the only side that knows what is on disk — the browser
  // has no listing of the runbook directory, and a checkbox would make the user
  // answer a question about a collision that usually is not there. A failed stop
  // leaves the recording ARMED by design (see stopRecording in cmd/catway), so
  // saying no here loses nothing.
  function stopRecording(name, description, overwrite) {
    sendCmdAwait("runbook.record", { action: "stop", name, description, overwrite }, (res) => {
      if (res.ok) {
        const path = ((res.data || {}).path) || "";
        toast("saved runbook " + name + (path ? " → " + path : ""));
        return;
      }
      const err = res.error || "unknown error";
      if (!overwrite && /already exists/i.test(err)) {
        dialogConfirm({
          title: "replace runbook",
          message: "A runbook named “" + name + "” already exists. Replace it with this recording?",
          warn: "The existing file is overwritten, including any edits made to it by hand.",
          confirmLabel: "replace", danger: true,
          onConfirm: () => stopRecording(name, description, true),
        });
        return;
      }
      toast("record: " + err);
    });
  }

  function confirmCancelRecording() {
    const n = recState.steps;
    dialogConfirm({
      title: "cancel recording",
      message: n === 0
        ? "Discard the recording? Nothing has been captured yet."
        : "Discard the recording? " + n + (n === 1 ? " captured command is" : " captured commands are") + " thrown away.",
      confirmLabel: "discard", danger: true,
      onConfirm: () => sendCmd("runbook.record", { action: "cancel" }),
    });
  }
