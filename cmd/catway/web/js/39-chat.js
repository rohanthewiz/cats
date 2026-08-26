  // ---- Chat panel (the ACP side panel) ----
  // The server broadcasts the whole conversation to every client (chat_row /
  // chat_delta appends, chat_snapshot replaces), so this code only renders —
  // it never owns conversation state beyond what is on screen. Open/closed is
  // a per-browser display preference like the sidebar width.
  const CHAT_KEY = "cats.chat_open";
  function chatIsOpen() { return document.body.classList.contains("chat-open"); }
  function setChatOpen(open) {
    document.body.classList.toggle("chat-open", open);
    try {
      if (open) localStorage.setItem(CHAT_KEY, "1"); else localStorage.removeItem(CHAT_KEY);
    } catch (e) { /* not persisted */ }
    // The chat column changes #main's width exactly like a sidebar drag does:
    // the pane grid must re-derive cols/rows and tell the server.
    sidebarResized(true);
    if (open) {
      chatBtnEl.classList.remove("badge");
      chatLogEl.scrollTop = chatLogEl.scrollHeight;
      chatInputEl.focus();
    }
  }
  chatBtnEl.addEventListener("click", () => setChatOpen(!chatIsOpen()));
  try { if (localStorage.getItem(CHAT_KEY)) document.body.classList.add("chat-open"); } catch (e) { /* closed */ }

  // chatNudge marks the chip when something worth seeing (a permission
  // prompt, a dead agent) happens while the panel is closed.
  function chatNudge() { if (!chatIsOpen()) chatBtnEl.classList.add("badge"); }

  // chatMutate keeps the transcript pinned to the bottom across a mutation —
  // but only when it already was: a user scrolled up reading history must not
  // be yanked down by every streamed token.
  function chatMutate(f) {
    const pinned = chatLogEl.scrollTop + chatLogEl.clientHeight >= chatLogEl.scrollHeight - 8;
    f();
    if (pinned) chatLogEl.scrollTop = chatLogEl.scrollHeight;
  }

  function chatFillRow(el, row) {
    el.textContent = "";
    if (row.role === "tool") {
      el.appendChild(document.createTextNode("⚙ " + (row.text || "")));
      if (row.status) {
        const st = document.createElement("span");
        st.className = "cstat";
        st.textContent = " — " + (row.status === "failed" ? "✗ failed" : row.status);
        el.appendChild(st);
      }
      return;
    }
    const t = document.createElement("span");
    t.className = "ctext";
    t.textContent = row.text || "";
    el.appendChild(t);
    if (row.action && row.action.argv && row.action.argv.length) {
      el.appendChild(document.createTextNode(" "));
      const b = document.createElement("button");
      b.className = "cbtn allow";
      b.textContent = row.action.label || "run";
      // The remedy opens as a real pane: the server named an argv (e.g.
      // `copilot login`), and tab.create's Command is the existing seam for
      // running one in a fresh tab.
      b.addEventListener("click", () => sendCmd("tab.create", { command: row.action.argv }));
      el.appendChild(b);
    }
  }

  function chatRowEl(row) {
    const el = document.createElement("div");
    el.className = "crow " + (row.role || "info");
    el.id = "chat-row-" + row.id;
    chatFillRow(el, row);
    return el;
  }

  // chat_row is an upsert: a known id re-renders in place (tool rows change
  // status this way), an unknown one appends.
  function chatUpsertRow(row) {
    if (!row) return;
    chatMutate(() => {
      const el = document.getElementById("chat-row-" + row.id);
      if (el) chatFillRow(el, row);
      else chatLogEl.appendChild(chatRowEl(row));
    });
  }

  // chat_delta appends streamed text to an existing row — a text-node append,
  // not a re-render, so a long answer costs O(1) per flush.
  function chatAppendDelta(id, text) {
    const el = document.getElementById("chat-row-" + id);
    const t = el && el.querySelector(".ctext");
    if (!t) return;
    chatMutate(() => t.appendChild(document.createTextNode(text)));
  }

  function chatPermEl(msg) {
    const el = document.createElement("div");
    el.className = "cperm";
    el.id = "chat-perm-" + msg.req_id;
    const title = document.createElement("div");
    title.className = "ctitle";
    title.textContent = msg.title || "permission request";
    el.appendChild(title);
    const opts = document.createElement("div");
    opts.className = "copts";
    for (const o of (msg.options || [])) {
      const b = document.createElement("button");
      b.className = "cbtn" + (String(o.kind || "").startsWith("allow") ? " allow" : "");
      b.textContent = o.name || o.id;
      b.addEventListener("click", () => sendCmd("chat.permission", { req_id: msg.req_id, option_id: o.id }));
      opts.appendChild(b);
    }
    el.appendChild(opts);
    return el;
  }

  // chat_perm opens a prompt or, with resolved, collapses it into its verdict
  // everywhere — any client may have answered, so buttons must not outlive
  // the question.
  function chatPermUpdate(msg) {
    if (msg.resolved) {
      const el = document.getElementById("chat-perm-" + msg.req_id);
      if (el) chatMutate(() => {
        el.textContent = "";
        const v = document.createElement("span");
        v.className = "verdict";
        v.textContent = msg.outcome === "allowed" ? "✓ allowed"
          : msg.outcome === "rejected" ? "⊘ rejected" : "— cancelled";
        el.appendChild(v);
      });
      return;
    }
    chatMutate(() => chatLogEl.appendChild(chatPermEl(msg)));
    chatNudge();
  }

  function chatApplyState(st) {
    chatTitleEl.textContent = st.backend || "Chat";
    const model = st.model ? modelLabel(st.model) : "";
    let s = st.status || "idle";
    if (st.detail) s += " · " + st.detail;
    chatStatusEl.textContent = (model ? model + " · " : "") + s;
    chatStatusEl.title = (st.cwd ? "cwd: " + st.cwd : "") + (st.detail ? "\n" + st.detail : "");
    chatStopEl.disabled = st.status !== "turn";
    if (st.status === "dead") chatNudge();
  }

  // chat_snapshot replaces the whole panel: sent on connect (joining
  // mid-conversation) and on chat.clear (empty).
  function chatApplySnapshot(msg) {
    chatApplyState(msg.state || {});
    chatLogEl.textContent = "";
    for (const r of (msg.rows || [])) chatLogEl.appendChild(chatRowEl(r));
    for (const p of (msg.perms || [])) chatLogEl.appendChild(chatPermEl(p));
    chatLogEl.scrollTop = chatLogEl.scrollHeight;
    if ((msg.perms || []).length) chatNudge();
  }

  function chatSend() {
    const text = chatInputEl.value.trim();
    if (!text) return;
    chatInputEl.value = "";
    sendCmd("chat.send", { text });
  }
  chatInputEl.addEventListener("keydown", (e) => {
    e.stopPropagation(); // belt to onKey's braces: nothing composed reaches a PTY
    if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); chatSend(); }
    else if (e.key === "Escape") { e.preventDefault(); chatInputEl.blur(); }
  });
  chatInputEl.addEventListener("keyup", (e) => e.stopPropagation());
  chatStopEl.addEventListener("click", () => sendCmd("chat.cancel"));
  chatClearEl.addEventListener("click", () => sendCmd("chat.clear"));

