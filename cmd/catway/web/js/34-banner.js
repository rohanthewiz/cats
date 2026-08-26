  // ---- Update banner (WS8): the update_ready message ----
  // updateInfo persists past a dismissed banner so the launcher menu can
  // re-surface it ("update ready" item + gear badge, cats's launcher badge).
  let updateInfo = null;
  function showUpdateBanner() {
    showBanner("cats update ready" + (updateInfo.version ? " — " + updateInfo.version : "") +
      (updateInfo.command ? " · run: " + updateInfo.command : ""));
  }
  // showBanner(text, kind): kind is an optional extra class ("linkerr") that
  // recolors the strip; dismissing always clears both so the next banner starts
  // from the default look.
  function showBanner(text, kind) {
    bannerEl.innerHTML = "";
    const t = document.createElement("span"); t.textContent = text;
    const x = document.createElement("span"); x.className = "x"; x.textContent = "✕"; x.title = "dismiss";
    x.addEventListener("click", () => bannerEl.classList.remove("show", "linkerr"));
    bannerEl.appendChild(t); bannerEl.appendChild(x);
    bannerEl.classList.toggle("linkerr", kind === "linkerr");
    bannerEl.classList.add("show");
  }

  // ---- Status & toasts ----
  // The status bar no longer carries a connection label: link state is a field
  // on the pane hover card (showPaneTip) — read only when you go looking — and
  // failures escalate to the banner, which is unmissable. connState is the
  // single source both read, so a hover card opened mid-outage is accurate.
  let connState = { text: "connecting…", err: false };
  function setStatus(t, err) {
    connState = { text: t, err: !!err };
    if (err) {
      showBanner(t, "linkerr");
    } else if (bannerEl.classList.contains("linkerr")) {
      bannerEl.classList.remove("show", "linkerr"); // link recovered → drop the alarm
    }
  }
  // toast(text) is the four-second status line. toast(text, {id, actions}) is
  // the answerable one: it carries a notification's buttons (ui.notify) and
  // does NOT auto-dismiss, because a button that disappears after four seconds
  // is a button nobody can press. It leaves when it is answered or dismissed.
  function toast(text, opts) {
    opts = opts || {};
    const actions = opts.actions || [];
    const el = document.createElement("div");
    el.className = "toast";
    const line = document.createElement("div");
    line.textContent = text;
    el.appendChild(line);

    let timer = null;
    const dismiss = () => {
      if (timer) { clearTimeout(timer); timer = null; }
      el.classList.remove("show");
      setTimeout(() => el.remove(), 250);
    };

    if (actions.length) {
      const row = document.createElement("div");
      row.className = "toast-actions";
      for (const a of actions) {
        const b = document.createElement("button");
        b.className = "toast-btn";
        b.textContent = a.label || a.id;
        b.onclick = () => {
          // The whole row goes dead on the first click. A notification is
          // answered once server-side, so a second click could only earn a
          // refusal — and the click that earns it would look, to the user,
          // exactly like the one that worked.
          for (const other of row.querySelectorAll("button")) other.disabled = true;
          sendCmd("ui.action", { id: opts.id, action: a.id });
          dismiss();
        };
        row.appendChild(b);
      }
      const x = document.createElement("button");
      x.className = "toast-btn toast-dismiss";
      x.textContent = "Dismiss";
      x.onclick = dismiss;
      row.appendChild(x);
      el.appendChild(row);
    }

    toastsEl.appendChild(el);
    requestAnimationFrame(() => el.classList.add("show"));
    if (!actions.length) timer = setTimeout(dismiss, 4000);
    return el;
  }

