  // ---- Worktree dialogs (WS8): new / open / remove git-worktree checkouts ----
  //
  // Web adaptation of cats's prefix+w dialogs over the §7 worktree.* commands.
  // Everything anchors on the focused pane's repo (the server resolves it from
  // the pane's live cwd); worktree.list also reports which checkouts already
  // have an open workspace.

  // branchPathSlug mirrors the server's BranchToPathSlug so the new-worktree
  // dialog can preview the derived checkout path per keystroke.
  function branchPathSlug(branch) {
    let out = "", dash = false;
    for (const ch of branch) {
      if (/[a-zA-Z0-9]/.test(ch)) { out += ch.toLowerCase(); dash = false; }
      else if (!dash) { out += "-"; dash = true; }
    }
    out = out.replace(/^-+|-+$/g, "");
    return out || "worktree";
  }

  // generatedBranchSlug mirrors the server's GeneratedBranchSlug (same word
  // lists) for the dialog's prefill; the server would generate one too if the
  // branch were omitted.
  function generatedBranchSlug(seed) {
    const adj = ["brave", "calm", "clear", "green", "lucky", "quiet", "rapid", "silver"];
    const nouns = ["river", "cloud", "field", "forest", "harbor", "meadow", "stone", "valley"];
    const suffix = (seed & 0xffff).toString(16).padStart(4, "0");
    return "worktree/" + adj[seed % 8] + "-" + nouns[Math.floor(seed / 8) % 8] + "-" + suffix;
  }

  function worktreeStatus(e, repoRoot) {
    if (e.open_workspace) return "open";
    if (e.detached) return "detached";
    if (e.path === repoRoot) return "root";
    return "";
  }

  // worktreeTitle names the dialog: the repository, and — only when the session
  // has more than one host — the machine whose disk it is on. Every path in a
  // worktree.list answer belongs to that machine, including the checkout path
  // previewed below the branch field, so leaving the host out of a multi-host
  // session would put a remote path under a title that reads as local.
  function worktreeTitle(verb, info) {
    const name = verb + " worktree — " + info.repo_name;
    return multiHost() && info.host ? name + " @ " + hostLabel(info.host) : name;
  }

  // withWorktreeList runs fn with the focused pane's worktree.list result,
  // toasting (not dialoging) when the pane isn't in a git repo.
  function withWorktreeList(fn) {
    sendCmdAwait("worktree.list", {}, (res) => {
      if (!res.ok) { toast("worktree: " + (res.error || "unknown")); return; }
      fn(res.data || {});
    });
  }

  function openNewWorktreeDialog() {
    withWorktreeList((info) => {
      openOverlay((ov) => {
        const m = document.createElement("div"); m.className = "modal";
        const h = document.createElement("header"); h.textContent = worktreeTitle("new", info); m.appendChild(h);
        const body = document.createElement("div"); body.className = "body";
        const input = document.createElement("input");
        input.type = "text"; input.spellcheck = false;
        input.value = generatedBranchSlug(Date.now());
        body.appendChild(input);
        const pathEl = document.createElement("div"); pathEl.className = "hint"; body.appendChild(pathEl);
        const errEl = document.createElement("div"); errEl.className = "errline"; body.appendChild(errEl);
        m.appendChild(body);
        const syncPath = () => {
          pathEl.textContent = "checkout: " + info.worktree_root + "/" + info.repo_name + "/" +
            branchPathSlug(input.value.trim());
          errEl.textContent = "";
        };
        syncPath();
        let creating = false; // re-entry guard while git runs
        const create = () => {
          const branch = input.value.trim();
          if (!branch) { errEl.textContent = "branch is required"; return; }
          if (creating) return;
          creating = true;
          goBtn.disabled = true;
          sendCmdAwait("worktree.create", { branch }, (res) => {
            creating = false; goBtn.disabled = false;
            if (!res.ok) { errEl.textContent = res.error || "worktree add failed"; return; }
            closeModal();
            toast("worktree " + branch + " created");
          });
        };
        const btns = document.createElement("div"); btns.className = "btns";
        btns.appendChild(mkModalBtn("cancel", "", closeModal));
        const goBtn = mkModalBtn("create and open", "primary", create);
        btns.appendChild(goBtn);
        m.appendChild(btns);
        input.addEventListener("input", syncPath);
        input.addEventListener("keydown", (e) => {
          e.stopPropagation();
          if (e.key === "Enter") { e.preventDefault(); create(); }
          else if (e.key === "Escape") { e.preventDefault(); closeModal(); }
        });
        ov.appendChild(m);
        // Selected prefill: the first keystroke replaces the generated slug.
        focusField(input, true);
      });
    });
  }

  function openWorktreeOpenDialog() {
    withWorktreeList((info) => {
      const entries = (info.worktrees || []).filter((e) => !e.prunable);
      if (!entries.length) { toast("no git worktrees found for this repo"); return; }
      let sel = 0, query = "";
      let listEl, inputEl;

      // Filter: case-insensitive AND of whitespace tokens over branch+path+status.
      const rows = () => {
        const toks = query.toLowerCase().split(/\s+/).filter(Boolean);
        return entries.filter((e) => {
          const hay = ((e.branch || "") + " " + e.path + " " + worktreeStatus(e, info.repo_root)).toLowerCase();
          return toks.every((t) => hay.includes(t));
        });
      };
      const choose = (e) => {
        closeModal();
        sendCmdAwait("worktree.open", { path: e.path }, (res) => {
          if (!res.ok) { toast("open worktree failed: " + (res.error || "unknown")); return; }
          if (res.data && res.data.already_open) toast("switched to open worktree");
        });
      };
      const render = () => {
        const rs = rows();
        if (sel >= rs.length) sel = Math.max(0, rs.length - 1);
        listEl.innerHTML = "";
        if (!rs.length) {
          const el = document.createElement("div"); el.className = "empty"; el.textContent = "no matches";
          listEl.appendChild(el); return;
        }
        rs.forEach((e, i) => {
          const row = document.createElement("div");
          row.className = "row" + (i === sel ? " sel" : "");
          const kind = document.createElement("span"); kind.className = "kind";
          kind.textContent = worktreeStatus(e, info.repo_root);
          const lbl = document.createElement("span"); lbl.className = "lbl";
          lbl.textContent = e.branch || e.path.split("/").pop();
          const meta = document.createElement("span"); meta.className = "meta"; meta.textContent = e.path;
          row.appendChild(kind); row.appendChild(lbl); row.appendChild(meta);
          row.addEventListener("click", () => choose(e));
          row.addEventListener("mousemove", () => { if (sel !== i) { sel = i; render(); } });
          listEl.appendChild(row);
        });
        const cur = listEl.children[sel];
        if (cur && cur.scrollIntoView) cur.scrollIntoView({ block: "nearest" });
      };

      openOverlay((ov) => {
        const m = document.createElement("div"); m.className = "modal pal";
        const h = document.createElement("header"); h.textContent = worktreeTitle("open", info); m.appendChild(h);
        const q = document.createElement("div"); q.className = "query";
        inputEl = document.createElement("input");
        inputEl.type = "text"; inputEl.placeholder = "filter by branch, path, or status"; inputEl.spellcheck = false;
        q.appendChild(inputEl); m.appendChild(q);
        listEl = document.createElement("div"); listEl.className = "list"; m.appendChild(listEl);
        inputEl.addEventListener("input", () => { query = inputEl.value; sel = 0; render(); });
        inputEl.addEventListener("keydown", (e) => {
          e.stopPropagation();
          if (e.key === "ArrowDown") { e.preventDefault(); sel++; render(); }
          else if (e.key === "ArrowUp") { e.preventDefault(); sel = Math.max(0, sel - 1); render(); }
          else if (e.key === "Enter") { e.preventDefault(); const rs = rows(); if (rs[sel]) choose(rs[sel]); }
          else if (e.key === "Escape") { e.preventDefault(); closeModal(); }
        });
        ov.appendChild(m);
        focusField(inputEl);
        render();
      });
    });
  }

  // removeWorktreeFor gates the menu action on membership: the workspace must be
  // an open linked checkout of the focused pane's repo (worktree.list says so).
  function removeWorktreeFor(w) {
    withWorktreeList((info) => {
      const entry = (info.worktrees || []).find((e) => e.open_workspace === w.id && e.path !== info.repo_root);
      if (!entry) { toast(w.name + " is not an open worktree checkout of this repo"); return; }
      confirmRemoveWorktree(w, entry.path, false);
    });
  }

  // Two-step force escalation: a plain remove that fails on dirty/untracked
  // files re-opens the confirm with the red delete-anyway warning.
  function confirmRemoveWorktree(w, path, force) {
    dialogConfirm({
      title: "delete worktree checkout",
      message: "This removes the checkout folder: " + path +
        " — the branch is not deleted. The workspace will close.",
      warn: force ? "Dirty or untracked files will be permanently deleted." : "",
      confirmLabel: force ? "delete anyway" : "delete checkout", danger: true,
      onConfirm: () => sendCmdAwait("worktree.remove", { workspace: w.id, force }, (res) => {
        if (res.ok) { toast("worktree checkout removed"); return; }
        const err = res.error || "";
        if (!force && err.startsWith("dirty_worktree_requires_force")) {
          confirmRemoveWorktree(w, path, true);
        } else {
          toast("remove worktree failed: " + err);
        }
      }),
    });
  }

