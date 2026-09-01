  // ---- Path picker (a directory field, cdx-style) ----
  //
  // A start path is where a workspace lives for the rest of its life, so typing
  // one blind — with no view of what is actually on the server's disk — was the
  // weakest part of the new-workspace dialog. attachPathPicker turns a text
  // field into a directory picker over the same two sources cdx (the user's TUI
  // cd assistant) offers: the directories they visit most, frecency-ranked, and
  // the subdirectories of wherever the path currently points.
  //
  // Filtering runs here, not on the server. path.list answers with one
  // directory's entire listing, which we cache and fuzzy-match per keystroke, so
  // a round trip happens only when the directory being completed *inside*
  // changes — worth caring about when the browser is a WAN away from the server.
  //
  // Three modes. An untouched prefill (see pristine) is not a fragment at all:
  // the field already names a directory, so the drop-down opens on the recents
  // plus that directory's own children — the history is one arrow key away, with
  // nothing to type and nothing to backspace. Once the user takes the field over,
  // it follows cdx: with no "/" typed yet the fragment matches recents and the
  // anchor directory's children together; once a path is under way it completes
  // inside the named directory, prefix matches first. The selection starts on the
  // best match while a fragment is being completed, and on *nothing* when the
  // text already names a directory (a trailing "/", or that untouched prefill) —
  // there, what the field already says is the answer.
  function attachPathPicker(input, wrap) {
    const listEl = document.createElement("div");
    listEl.className = "picker";
    wrap.appendChild(listEl);

    let recents = [];        // absolute dirs, best-first; asked for once per dialog
    let recentsAsked = false;
    let home = "";           // resolved server-side, so "~" means the same thing on both ends
    const cache = new Map(); // typed base ("" | "~/projs/" | "/usr/") -> path.list result
    const inflight = new Set();
    let rows = [], sel = -1;
    // enabled is the picker's own on/off switch (see setEnabled): a field whose
    // directory lives on a machine that cannot list it keeps the input and
    // loses the picker.
    let enabled = true;
    // host is the machine the listing is taken on ("" = the anchor pane's own).
    // It is carried on the request rather than inferred server-side because the
    // new-workspace dialog chooses a host BEFORE anything exists there: with
    // only a pane to go by, a path being typed for devbox would be completed
    // against the local disk and every suggestion would be a directory that
    // does not exist where the workspace is about to be created.
    let host = "";
    // A prefilled field arrives naming a directory the user never typed, so its
    // text is not a fragment being completed — it is where they already are.
    // While it stays untouched the picker lists *inside* it and offers the
    // recents alongside, which is the whole point of a drop-down: somewhere else
    // to go, visible the moment the dialog opens, with nothing to type and
    // nothing to backspace first.
    let pristine = !!input.value.trim();

    const shortHome = (p) => (home && (p === home ? "~" : (p.startsWith(home + "/") ? "~" + p.slice(home.length) : p))) || p;
    const joinPath = (dir, name) => (dir.endsWith("/") ? dir + name : dir + "/" + name);

    // splitBase divides the typed text into the directory part (as typed, with
    // its trailing slash) and the fragment being completed. A "" base is "no
    // separator yet" — the anchor directory, matched fuzzily.
    function splitBase(text) {
      if (pristine) return [text.replace(/\/+$/, "") + "/", ""]; // all base, no fragment
      if (text === "~" || text === "." || text === "..") return [text + "/", ""];
      const i = text.lastIndexOf("/");
      return i < 0 ? ["", text] : [text.slice(0, i + 1), text.slice(i + 1)];
    }

    // listing returns the cached path.list answer for a typed base, firing the
    // request (once) and re-rendering on arrival when it is not cached yet.
    function listing(base) {
      if (cache.has(base)) return cache.get(base);
      if (!inflight.has(base)) {
        inflight.add(base);
        const wantRecents = !recentsAsked;
        recentsAsked = true;
        sendCmdAwait("path.list", { dir: base, recents: wantRecents, host }, (res) => {
          inflight.delete(base);
          if (!res.ok) { if (wantRecents) recentsAsked = false; return; }
          const d = res.data || {};
          cache.set(base, d);
          if (d.home) home = d.home;
          if (wantRecents) recents = d.recents || [];
          if (input.isConnected) render();
        });
      }
      return null;
    }

    // rowsFor is the candidate list for the current text: {kind, label, path}.
    // null means the directory it needs has not been listed yet.
    function rowsFor(text) {
      const [base, frag] = splitBase(text);
      const d = listing(base);
      if (!d) return null;
      // A dot-fragment is the only thing that asks for dotfiles, the way it is
      // the only thing that does in a shell.
      const dirs = (d.dirs || []).filter((n) => frag.startsWith(".") || !n.startsWith("."));
      const dirRow = (n) => ({ kind: "dir", label: n + "/", path: joinPath(d.dir, n) });

      if (base && !pristine) { // completing inside a named directory
        const lf = frag.toLowerCase();
        let names = dirs.filter((n) => n.toLowerCase().startsWith(lf));
        if (!names.length && frag) names = fuzzyRank(frag, dirs, (n) => n);
        return names.map(dirRow);
      }
      const cands = [];
      for (const p of recents) {
        if (p === d.dir) continue; // "here" is not somewhere to go
        cands.push({ kind: "recent", label: shortHome(p), path: p });
      }
      for (const n of dirs) cands.push(dirRow(n));
      return frag ? fuzzyRank(frag, cands, (c) => c.label) : cands;
    }

    // fuzzyRank keeps the palette's scorer as the one fuzzy behaviour in the UI.
    function fuzzyRank(q, items, labelOf) {
      return items
        .map((it) => ({ it, s: fuzzyScore(q, labelOf(it)) }))
        .filter((x) => x.s >= 0)
        .sort((a, b) => b.s - a.s)
        .map((x) => x.it);
    }

    function note(text, cls) {
      const n = document.createElement("div");
      n.className = "note" + (cls ? " " + cls : "");
      n.textContent = text;
      listEl.appendChild(n);
    }

    function render(keepSel) {
      if (!enabled) return; // switched off for a remote path — nothing to list
      const text = input.value;
      const [base, frag] = splitBase(text);
      const next = rowsFor(text);
      const loading = next === null;
      rows = next || [];
      if (!keepSel) sel = frag && rows.length ? 0 : -1;
      if (sel >= rows.length) sel = rows.length - 1;

      listEl.innerHTML = "";
      const d = cache.get(base);
      if (d) {
        const head = document.createElement("div"); head.className = "head";
        head.textContent = "in " + shortHome(d.dir);
        listEl.appendChild(head);
      }
      // A path that is not on disk yet is not a dead end: submitting the
      // dialog offers to create it (see createWorkspace), so the error note
      // carries the way forward alongside the problem.
      if (d && !d.exists) {
        note(d.error || "not a directory", "err");
        if (!d.error || d.error.startsWith("no such directory")) note("enter offers to create this folder");
      }
      rows.slice(0, 60).forEach((r, i) => {
        const row = document.createElement("div");
        row.className = "row" + (i === sel ? " sel" : "");
        const kind = document.createElement("span"); kind.className = "kind"; kind.textContent = r.kind;
        const lbl = document.createElement("span"); lbl.className = "lbl"; lbl.textContent = r.label;
        row.appendChild(kind); row.appendChild(lbl);
        // mousedown, not click: acting before the field can lose focus keeps the
        // caret (and the picker) alive for the next keystroke.
        row.addEventListener("mousedown", (e) => { e.preventDefault(); sel = i; drill(r); });
        listEl.appendChild(row);
      });
      if (loading && !rows.length) note("listing…");
      // A fragment with no match is the other spelling of "folder that does
      // not exist yet" — same forward path as the missing-base note above.
      else if (!rows.length && d && d.exists) note(frag ? "no match — enter offers to create this folder" : "no subdirectories");
      if (d && d.truncated) note("first " + (d.dirs || []).length + " entries only — this directory is very large");
      const cur = listEl.querySelector(".row.sel");
      if (cur && cur.scrollIntoView) cur.scrollIntoView({ block: "nearest" });
    }

    // drill completes the field to a candidate and lists inside it — cdx's Tab.
    // The value it writes is always an absolute (~-shortened) path, so what the
    // dialog finally submits can never depend on which directory a relative path
    // would have been resolved against.
    function drill(r) {
      pristine = false; // the field is theirs now, not the prefill's
      input.value = shortHome(r.path) + "/";
      render();
    }

    function move(delta) {
      if (!rows.length) return;
      sel = Math.max(-1, Math.min(rows.length - 1, sel + delta));
      render(true);
    }

    input.addEventListener("input", () => { pristine = false; render(); });
    input.addEventListener("focus", () => { wrap.classList.add("focus"); render(); });
    input.addEventListener("blur", () => wrap.classList.remove("focus"));

    return {
      // setEnabled turns the picker off for a field whose path nobody here can
      // complete — a host whose cathost is too old to list its own directories,
      // or one that is offline. Off means: no listing requests, no drop-down, no
      // key capture, and no commit rewriting what was typed, so the field
      // becomes a plain text box carrying the path verbatim to the host that
      // will interpret it.
      setEnabled(on) {
        enabled = on;
        if (!on) { listEl.innerHTML = ""; rows = []; sel = -1; }
        else render();
      },
      // setHost points the picker at another machine. Everything cached
      // describes the previous one — its directories, its home, its recents —
      // so all of it is dropped rather than filtered: a listing of /home/me on
      // this laptop says nothing about /home/me on devbox, and the two are
      // indistinguishable once they are in the same map.
      setHost(id) {
        if (host === id) return;
        host = id;
        cache.clear(); inflight.clear();
        recents = []; recentsAsked = false; home = "";
        rows = []; sel = -1;
        if (enabled) render();
      },
      // key handles the picker's own keys and reports whether it consumed the
      // event; Enter stays with the dialog (see commit).
      key(e) {
        if (!enabled) return false;
        if (e.key === "ArrowDown" || (e.ctrlKey && e.key === "n")) { move(1); return true; }
        if (e.key === "ArrowUp" || (e.ctrlKey && e.key === "p")) { move(-1); return true; }
        if (e.key === "Tab" && !e.shiftKey && rows[sel]) { drill(rows[sel]); return true; }
        return false;
      },
      // commit resolves the field to the directory the user actually chose,
      // just before the dialog reads it: the highlighted candidate, or else the
      // absolute form of what they typed. An empty field stays empty — clearing
      // it is how a caller asks for its own default.
      commit() {
        const text = input.value;
        if (!enabled || !text.trim()) return;
        if (rows[sel]) { input.value = rows[sel].path; return; }
        const [base, frag] = splitBase(text);
        const d = cache.get(base);
        if (d) input.value = frag ? joinPath(d.dir, frag) : d.dir;
      },
    };
  }

  // dialogLines renders opts.lines: a numbered list of what a dialog's action
  // is about to do, one short line each. Shared by both dialogs rather than
  // written into either, because "confirm this" and "fill these in, then do it"
  // are the same question about the same list.
  //
  // An <ol> so the numbering is the browser's and matches how the thing being
  // previewed numbers itself — a runbook failure reported as "step 4" names the
  // fourth line here. opts.linesNote is the tail for a list that was cut short
  // ("…and 180 more steps"): it sits outside the list because it is not one of
  // the items, and numbering it would claim there is a step that says that.
  //
  // The lines arrive already truncated. This deliberately does not shorten them
  // further — the sender knows what it is describing and how much of it matters,
  // and a second truncation with a different budget would cut mid-escape.
  function dialogLines(body, opts) {
    if (!opts.lines || !opts.lines.length) return;
    const ol = document.createElement("ol"); ol.className = "steplist";
    for (const line of opts.lines) {
      const li = document.createElement("li");
      li.textContent = line;
      // The full line in the title too: it is already clipped, but a reader who
      // wants the tail of a long one should not have to open the file for it.
      li.title = line;
      ol.appendChild(li);
    }
    body.appendChild(ol);
    if (opts.linesNote) {
      const more = document.createElement("div");
      more.className = "hint steplist-more"; more.textContent = opts.linesNote;
      body.appendChild(more);
    }
  }

  // dialogFields: a prompt over one or more text fields (opts.fields:
  // [{label?, value?, placeholder?, pick?}]). Enter submits from any field and
  // Esc cancels, so a multi-field dialog still costs one keystroke for anyone who
  // only cares about the first; Tab walks the fields natively. onSubmit gets the
  // values in field order. Labels are shown only when a dialog has more than the
  // one obvious field. pick makes a field a directory picker (attachPathPicker),
  // which takes over the arrows and Tab while it is focused and normalizes the
  // field's value on submit.
  function dialogFields(opts) {
    openOverlay((ov) => {
      const m = document.createElement("div"); m.className = "modal";
      const h = document.createElement("header"); h.textContent = opts.title; m.appendChild(h);
      const body = document.createElement("div"); body.className = "body";
      const pickers = new Map(); // input -> picker api
      // A field is a text input unless it carries `choices`, in which case it is
      // a <select> over [{value,label}]. Both expose .value, so submit still
      // reads the row uniformly and callers stay unaware of which kind they got
      // — the point of putting the choice here rather than hand-rolling a
      // second dialog for the one prompt that needs a list (new workspace's
      // plugin). A choice field takes no path picker and no placeholder: the
      // first option is the empty/default one and says what "nothing" means.
      const inputs = opts.fields.map((f) => {
        const wrap = document.createElement("div"); wrap.className = "field" + (f.pick ? " pick" : "");
        if (f.label) { const l = document.createElement("label"); l.textContent = f.label; wrap.appendChild(l); }
        let input;
        if (f.choices) {
          input = document.createElement("select");
          for (const c of f.choices) {
            const o = document.createElement("option");
            o.value = c.value; o.textContent = c.label;
            input.appendChild(o);
          }
          input.value = f.value || "";
        } else {
          input = document.createElement("input");
          input.type = "text"; input.value = f.value || ""; input.spellcheck = false;
          if (f.placeholder) input.placeholder = f.placeholder;
        }
        wrap.appendChild(input); body.appendChild(wrap);
        if (f.pick && !f.choices) pickers.set(input, attachPathPicker(input, wrap));
        return input;
      });
      // onChange lets one field reshape another — the new-workspace dialog's
      // host choice switching the start-path picker off, which it can only do
      // once both fields exist. The callback gets the field's value and the
      // dialog's rows ({input, picker}), and runs once up front so the initial
      // selection is applied rather than only the first change.
      opts.fields.forEach((f, i) => {
        if (!f.onChange) return;
        const rows = inputs.map((inp) => ({ input: inp, picker: pickers.get(inp) || null }));
        const fire = () => f.onChange(inputs[i].value, rows);
        inputs[i].addEventListener("change", fire);
        inputs[i].addEventListener("input", fire);
        fire();
      });
      if (opts.hint) { const t = document.createElement("div"); t.className = "hint"; t.textContent = opts.hint; body.appendChild(t); }
      // Last, below the fields, in both dialogs. The fields are what the user
      // has come to do and where focus lands; the list is the reference they
      // scan before committing, and a ten-line preview above the inputs would
      // push the thing they are typing into off the top of a small window.
      dialogLines(body, opts);
      m.appendChild(body);
      const submit = () => {
        for (const pk of pickers.values()) pk.commit();
        const vs = inputs.map((i) => i.value); closeModal(); opts.onSubmit(...vs);
      };
      const btns = document.createElement("div"); btns.className = "btns";
      btns.appendChild(mkModalBtn("cancel", "", closeModal));
      btns.appendChild(mkModalBtn(opts.submitLabel || "save", "primary", submit));
      m.appendChild(btns);
      for (const input of inputs) {
        input.addEventListener("keydown", (e) => {
          e.stopPropagation();
          const pk = pickers.get(input);
          if (pk && pk.key(e)) { e.preventDefault(); return; }
          if (e.key === "Enter") { e.preventDefault(); submit(); }
          else if (e.key === "Escape") { e.preventDefault(); closeModal(); }
        });
      }
      ov.appendChild(m);
      // The prefill starts selected so the first keystroke replaces it; the
      // select() guard lives in focusField, since a choice field in slot 0 is
      // a <select> and has no such method.
      focusField(inputs[0], true);
    });
  }

  // dialogInput: a rename-style prompt. Enter saves, Esc cancels; an empty
  // value is passed through (cats semantics: empty clears the custom name).
  function dialogInput(opts) {
    dialogFields({ ...opts, fields: [{ value: opts.value }] });
  }

  // dialogNotice: a dialog with nothing to agree to — a title, a message, and
  // whatever dialogLines was given, dismissed by its one button, by Enter or by
  // Esc. It is dialogConfirm with the choice taken out rather than a second
  // modal, because everything that makes the two look alike (the header, the
  // body, the lines, the keys) is worth having identical: a preview and the
  // gate it previews are the same panel showing the same list, and a reader
  // should recognise the second from the first.
  //
  // onClose is optional and usually absent. A notice that has to run something
  // when it closes is a confirm wearing a disguise; this exists for the case
  // where closing is genuinely the only outcome.
  function dialogNotice(opts) {
    dialogConfirm({
      ...opts,
      confirmLabel: opts.closeLabel || "close",
      noCancel: true,
      onConfirm: opts.onClose || (() => {}),
    });
  }

  // dialogConfirm: an explicit yes/no gate for destructive commands. Enter
  // confirms (the confirm button holds focus), Esc cancels.
  //
  // opts.noCancel drops the cancel button (see dialogNotice). Both keys still
  // close the dialog and Esc still reaches closeModal directly, so the one
  // button is a convenience rather than the only way out.
  function dialogConfirm(opts) {
    openOverlay((ov) => {
      const m = document.createElement("div"); m.className = "modal";
      const h = document.createElement("header"); h.textContent = opts.title; m.appendChild(h);
      const body = document.createElement("div"); body.className = "body";
      const p = document.createElement("p"); p.textContent = opts.message; body.appendChild(p);
      if (opts.warn) { // red emphasis line (e.g. the dirty-worktree escalation)
        const wp = document.createElement("p"); wp.className = "errline"; wp.textContent = opts.warn;
        body.appendChild(wp);
      }
      dialogLines(body, opts);
      m.appendChild(body);
      const go = () => { closeModal(); opts.onConfirm(); };
      const btns = document.createElement("div"); btns.className = "btns";
      if (!opts.noCancel) btns.appendChild(mkModalBtn("cancel", "", closeModal));
      const ok = mkModalBtn(opts.confirmLabel || "confirm", opts.danger ? "danger" : "primary", go);
      btns.appendChild(ok);
      m.appendChild(btns);
      m.addEventListener("keydown", (e) => {
        e.stopPropagation();
        if (e.key === "Enter") { e.preventDefault(); go(); }
        else if (e.key === "Escape") { e.preventDefault(); closeModal(); }
      });
      ov.appendChild(m);
      focusField(ok);
    });
  }

