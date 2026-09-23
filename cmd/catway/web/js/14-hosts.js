  // ---- Hosts: the cathost roster ----
  //
  // The server pushes it on connect and again whenever a host connects or drops,
  // so it is both the HOSTS section's data and the answer to the question every
  // host badge asks before drawing itself: is there more than one machine here?
  // With one host — the default, and what a config with no hosts: block gets —
  // every badge is the same word on every row, so nothing is drawn at all and
  // the section stays hidden.
  let hostItems = [];
  const multiHost = () => hostItems.length > 1;

  // hostBadgeKey is everything a host badge elsewhere in the UI is drawn from
  // (multiHost, hostLabel, hostUp): the roster's ids, labels and connection
  // state. The roster is re-pushed on every ping round (for the latency) and
  // on pane counts changing, neither of which any badge shows, so the pane
  // headers and workspace rows are redrawn only when this key moves.
  // A box so a unit test can reach it (web/jstest/testutil.mjs).
  const hostBadges = { key: "" };
  function hostBadgeKey() {
    return JSON.stringify(hostItems.map((h) => [h.id, h.label || "", !!h.connected]));
  }

  // hostLabel/hostUp resolve a host id against the roster. Both are deliberately
  // forgiving of an id the roster does not list: a layout can name a host for the
  // moment between its removal and the next roster push, and printing the raw id
  // (or declining to paint it as broken) beats printing nothing or a red herring.
  function hostLabel(id) {
    if (!id) return "";
    const h = hostItems.find((x) => x.id === id);
    return h ? (h.label || h.id) : id;
  }
  function hostUp(id) {
    const h = hostItems.find((x) => x.id === id);
    return !h || !!h.connected;
  }

  // localHostId names the host that is catway's OWN machine — the one whose
  // disk the server's own paths are on (the config directory, and so the
  // runbooks under it). It is not derivable from the address kind: a unix
  // address can be an ssh -L forward to another box, which is exactly how the
  // first remote host is reached, so the roster carries the flag explicitly.
  //
  // "" when the roster has not landed yet, which leaves a caller's `host` param
  // off and falls back to the anchor pane's machine — right in the single-host
  // session that is the only case where the roster can still be missing when
  // somebody clicks something.
  function localHostId() {
    const h = hostItems.find((x) => x.local);
    return h ? h.id : "";
  }

  // The heading carries one button of its own: attach. Detach is per-row (a
  // right-click on the host it would remove), because it is the destructive
  // half and the row is the only place that says what it holds.
  //
  // Both are hidden with the section when there is a single host — the whole
  // point of that rule is that a session nobody configured hosts for looks
  // exactly as it did before hosts existed — so the gear menu carries "attach
  // host…" as well. That is the entry point out of one-host life; without it
  // the first attach would be a config-file edit and a restart, which is the
  // thing this phase exists to remove.
  (function initHostHeadingCtl() {
    document.getElementById("host-hctl")
      .appendChild(mkBtn("＋", "attach a cathost", "", openAttachHostDialog));
    initSectionFold("sec-hosts", "host-hctl", "hosts");
  })();

