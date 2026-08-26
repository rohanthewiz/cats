  // openAttachHostDialog collects one hosts: entry. The fields are the config
  // file's, in the order an operator fills them: what to call it, how to reach
  // it, then the two credentials only a tls:// host needs. Everything but id and
  // addr is optional, and a unix:// host (including an `ssh -L` forward) needs
  // neither credential.
  function openAttachHostDialog() {
    dialogFields({
      title: "attach cathost",
      submitLabel: "attach",
      hint: "addr: unix:///path/to.sock · tls://box:8422 · tcp://127.0.0.1:8422 (loopback only). " +
        "A tls host needs the token file and fingerprint cathost printed at startup.",
      fields: [
        { label: "id", placeholder: "devbox" },
        { label: "address", placeholder: "tls://devbox:8422" },
        { label: "label", placeholder: "(optional — defaults to the id)" },
        { label: "token file", placeholder: "(optional) /path/to/token" },
        { label: "fingerprint", placeholder: "(optional) pinned SHA-256" },
      ],
      onSubmit: (id, addr, label, tokenFile, fingerprint) => {
        id = (id || "").trim(); addr = (addr || "").trim();
        if (!id || !addr) { toast("a host needs an id and an address"); return; }
        sendCmdAwait("host.attach", {
          id, addr, label: (label || "").trim(),
          token_file: (tokenFile || "").trim(), fingerprint: (fingerprint || "").trim(),
        }, (res) => {
          if (!res.ok) { toast(res.error || "attach failed"); return; }
          // No "connected" claim here: the reply means the roster took it, and
          // the dial is still in flight. The row's dot is the honest report.
          toast("attached " + id + " — dialing");
        });
      },
    });
  }

  // hostMenuItems is the roster row's context menu. Detach is the only verb: a
  // host's address is edited in the config file (or re-attached), and a "make
  // default" that silently moved where new panes land is the kind of one-click
  // change that is only discovered later, from the wrong machine's shell.
  // The local host gets no menu at all (an empty list, which the row's handler
  // declines to open): it is synthesized from server.cathost_socket rather than
  // configured, so there is nothing to detach and nowhere to move its panes to.
  function hostMenuItems(h) {
    if (h.local) return [];
    const detach = (force) => sendCmdAwait("host.detach", { id: h.id, force: !!force }, (res) => {
      if (!res.ok) { toast(res.error || "detach failed"); return; }
      toast("detached " + (h.label || h.id));
    });
    if (!h.panes) {
      return [{ label: "detach host", danger: true, fn: () => detach(false) }];
    }
    return [{
      label: "detach host (" + h.panes + (h.panes === 1 ? " pane" : " panes") + ")…",
      danger: true,
      fn: () => dialogConfirm({
        title: "detach " + (h.label || h.id),
        message: "Detach this cathost? Its " + h.panes + (h.panes === 1 ? " pane" : " panes") +
          " cannot follow — the terminals stay on that machine and the panes respawn as new shells on the default host.",
        warn: "Anything running in them is left behind.",
        confirmLabel: "detach and respawn", danger: true,
        onConfirm: () => detach(true),
      }),
    }];
  }

  // fmtLatency renders a round trip at the precision the number deserves. A
  // local unix socket lands well under a millisecond, where the exact figure is
  // noise and the fact that it is sub-millisecond is the whole answer; a link
  // over 10 ms has nothing useful after the decimal point. The middle band keeps
  // one digit, which is where a busy local daemon and a container on the same
  // box actually differ.
  function fmtLatency(ms) {
    if (ms < 1) return "<1 ms";
    if (ms < 10) return ms.toFixed(1) + " ms";
    return Math.round(ms) + " ms";
  }

  function renderHosts(items) {
    hostItems = items || [];
    hostSecEl.hidden = !multiHost();
    hostListEl.innerHTML = "";
    for (const h of hostItems) {
      const li = document.createElement("li");
      li.className = "host " + (h.connected ? "up" : "down");
      li.dataset.host = h.id;
      const dot = document.createElement("span"); dot.className = "hdot"; dot.textContent = "●";
      const name = document.createElement("span"); name.className = "hname"; name.textContent = h.label || h.id;
      li.appendChild(dot); li.appendChild(name);
      // Which host an unqualified new pane lands on — the one fact about the
      // roster that changes what a click elsewhere in the UI will do.
      if (h.is_default) {
        const d = document.createElement("span"); d.className = "hdef"; d.textContent = "default";
        li.appendChild(d);
      }
      // Round trip to that cathost, when it could be measured. Only shown while
      // connected: a stale number beside a red dot would claim a link that is
      // not there.
      if (h.connected && h.latency_ms) {
        const lat = document.createElement("span"); lat.className = "hlat";
        lat.textContent = fmtLatency(h.latency_ms);
        li.appendChild(lat);
      }
      // Connected: what it is carrying. Disconnected: that it isn't — its panes
      // are still real and still counted in the tooltip, they just aren't moving.
      const meta = document.createElement("span"); meta.className = "hmeta";
      meta.textContent = h.connected ? (h.panes + (h.panes === 1 ? " pane" : " panes")) : "offline";
      li.appendChild(meta);
      li.title = h.id + " · " + (h.addr_kind || "?") +
        " · " + h.panes + (h.panes === 1 ? " pane" : " panes") +
        (h.connected && h.latency_ms ? " · " + h.latency_ms + " ms round trip" : "") +
        (h.connected ? "" : (h.error ? " · " + h.error : " · not connected")) +
        (h.local ? "" : " · right-click to detach");
      li.addEventListener("contextmenu", (e) => {
        const items = hostMenuItems(h);
        if (!items.length) return;
        e.preventDefault(); openCtx(e.clientX, e.clientY, items);
      });
      hostListEl.appendChild(li);
    }
    // Every host badge in the app is gated on this message, so a roster change
    // has to redraw the places that carry one: the pane headers and, through the
    // layout, the workspace rows.
    for (const p of panes.values()) renderChrome(p);
    if (layoutMsg) renderWorkspaces(layoutMsg);
  }

