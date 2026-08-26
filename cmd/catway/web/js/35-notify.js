  // ---- Notifications (WS6): agent needs-attention / finished ----
  // cats parity: suppressed entirely when the pane is on screen and the window
  // is focused (the user is already looking at it). Otherwise a toast; and when
  // the page is unfocused/hidden, additionally a native Notification (if the
  // user granted permission) whose click reveals the pane — agent.focus crosses
  // workspaces and tabs.
  function handleNotify(msg) {
    const opts = msg.id && msg.actions && msg.actions.length
      ? { id: msg.id, actions: msg.actions } : null;
    // Non-agent kinds (e.g. an update notice, or anything raised through
    // ui.notify) always toast — there is no pane whose visibility could make
    // them redundant.
    if (msg.kind !== "attention" && msg.kind !== "finished") {
      toast(msg.message + (msg.body ? " — " + msg.body : ""), opts);
      return;
    }
    const visible = msg.pane ? panes.has(msg.pane) : false;
    const focused = document.hasFocus();
    // Suppression exists because a toast is redundant with a pane the user is
    // already looking at. A BUTTON is not redundant with anything, so an
    // answerable notification is never suppressed.
    if (focused && visible && !opts) return;
    toast(msg.message + (msg.body ? " — " + msg.body : ""), opts);
    if (focused || !("Notification" in window) || Notification.permission !== "granted") return;
    const n = new Notification(msg.message, { body: msg.body || "", tag: "cats-pane-" + (msg.pane || 0) });
    n.onclick = () => {
      window.focus();
      if (msg.pane) sendCmd("agent.focus", { pane: msg.pane });
      n.close();
    };
  }

  // The Notification API only grants permission from a user gesture, so ask
  // once on the first click/keypress.
  (function armNotificationPermission() {
    if (!("Notification" in window) || Notification.permission !== "default") return;
    const req = () => {
      window.removeEventListener("pointerdown", req);
      window.removeEventListener("keydown", req);
      Notification.requestPermission();
    };
    window.addEventListener("pointerdown", req);
    window.addEventListener("keydown", req);
  })();

