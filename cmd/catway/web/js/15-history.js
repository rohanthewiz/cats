  // ---- History (the command ledger) ----
  //
  // The sidebar's only backward-looking section. Rows come from ledger.list, a
  // query rather than a push: commands are recorded by the pane's own cathost
  // and there is no stream of them, so the section refreshes on the events that
  // could have changed it rather than on a timer.
  //
  // A click JUMPS — ledger.jump reveals the pane and scrolls its viewport to
  // that command's output — because that is the verb a history row implies, and
  // the copy is one level down in the context menu where a destructive-feeling
  // read belongs.
  initSectionFold("sec-history", "hist-hctl", "history");

  function renderHistory(entries) {
    // Hidden until there is something in it: a session whose shells have no
    // integration installed should see the sidebar it always had, not an empty
    // section inviting a question it cannot answer.
    histSecEl.hidden = entries.length === 0;
    histListEl.innerHTML = "";
    for (const e of entries) {
      const li = document.createElement("li");
      const failed = e.exit !== undefined && e.exit !== null && e.exit !== 0;
      const unknown = e.exit === undefined || e.exit === null;
      // A block is live terminal state: an entry with none is one whose pane
      // never pinned it, or one that outlived the pane it ran in. It still
      // lists — it is history — but it does not offer a click.
      li.className = (failed ? "failed" : "") + (unknown ? " unknown" : "") + (e.block ? "" : " gone");

      const dot = document.createElement("span");
      dot.className = "cdot";
      dot.textContent = failed ? "✕" : (unknown ? "·" : "✓");
      const cmd = document.createElement("span");
      cmd.className = "ccmd"; cmd.textContent = e.cmd;
      li.appendChild(dot); li.appendChild(cmd);

      if (e.origin && e.origin !== "human") {
        const o = document.createElement("span");
        o.className = "corigin"; o.textContent = e.origin;
        li.appendChild(o);
      }
      const dur = document.createElement("span");
      dur.className = "cdur"; dur.textContent = fmtDuration(e.duration_ms || 0);
      li.appendChild(dur);

      li.title = e.cmd + "\n" + (e.cwd || "") +
        "\n" + (e.handle || ("pane " + e.pane)) + (e.host ? " · " + e.host : "") +
        (unknown ? "\nfinished, status unknown" : "\nexit " + e.exit) +
        (e.block ? "\nclick to jump · right-click to copy the output" : "\nits output has scrolled away");

      if (e.block) {
        li.addEventListener("click", () => sendCmd("ledger.jump", { pane: e.pane, block: e.block }));
        li.addEventListener("contextmenu", (ev) => {
          ev.preventDefault();
          openCtx(ev.clientX, ev.clientY, historyMenuItems(e));
        });
      }
      histListEl.appendChild(li);
    }
  }

  // historyMenuItems: the verbs that need a round trip, kept off the plain click.
  function historyMenuItems(e) {
    return [
      { label: "Jump to output", fn: () => sendCmd("ledger.jump", { pane: e.pane, block: e.block }) },
      { label: "Copy output", fn: () => copyBlockOutput(e) },
      { label: "Copy command", fn: () => clipWrite(e.cmd) },
    ];
  }

  // copyBlockOutput asks the pane's cathost for the block's text and puts it on
  // the clipboard. The "gone" case is a toast rather than a silent no-op: the
  // row looked clickable, so something has to say why nothing happened.
  function copyBlockOutput(e) {
    sendCmdAwait("ledger.output", { pane: e.pane, block: e.block }, (res) => {
      if (!res || !res.ok || !res.data) {
        toast("could not read that command's output");
        return;
      }
      if (!res.data.found) {
        toast("that command's output has scrolled out of the pane's buffer");
        return;
      }
      clipWrite(res.data.text);
      toast("copied " + res.data.text.length + " characters");
    });
  }

  // fmtDuration renders a command's runtime the way a person reads it: a
  // sub-second command is noise, so it says nothing at all rather than "0ms".
  function fmtDuration(ms) {
    if (!ms) return "";
    if (ms < 1000) return ms + "ms";
    if (ms < 60000) return (ms / 1000).toFixed(1) + "s";
    const m = Math.floor(ms / 60000);
    return m + "m" + Math.round((ms % 60000) / 1000) + "s";
  }

