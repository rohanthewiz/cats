  // ---- Settings screen: the whole config.json over config.get/set ----
  //
  // One tabbed screen for every option cats has, opened from the gear menu, the
  // palette, ⌘, / Ctrl+, in the page, and Cats › Settings… (⌘,) in the Mac
  // app's menu bar (which Evals window.catsOpenSettings — Cocoa eats ⌘, as a
  // menu key equivalent before the page sees it).
  //
  //   ┌ settings — ~/.config/cats/config.json ──────────────────────┐
  //   │ appearance │  (the selected tab's rows)                     │
  //   │ keys       │                                                │
  //   │ panes      │                                                │
  //   │ editor     │                                                │
  //   │ …          │                                                │
  //   │ server     │  read-only                                     │
  //   │ app        │  Mac app only — its own section, via the bridge│
  //   └────────────┴────────────────── errline ── [cancel] [save] ─┘
  //
  // Three channels feed it, and save writes back through the same three:
  //   * theme + copy-mode keys — config.set's typed params (below, unchanged);
  //   * every other file section — config.get's `options`, rendered from the
  //     OPTION_TABS table and sent back as config.set `options`, only the
  //     sections that actually changed;
  //   * the Mac app's own section (launch mode, saved catways) — not catway's
  //     at all: in remote mode the catway serving this page is on another
  //     machine, while the app's settings live in THIS machine's config.json.
  //     So it travels over the native bridge (window.catsAppSettingsGet/Set),
  //     and the tab exists only when that bridge does.
  //
  // The theme section is a picker over the server's theme registry (built-ins,
  // user themes, plugin themes — config.get's themes list carries each one's
  // full palette) plus per-color override rows. Picking a theme or editing a
  // color live-previews immediately (inline :root custom properties);
  // cancel/Escape rolls the preview back to the config.get state, save
  // persists via config.set — the server then broadcasts the theme so every
  // client restyles live. Only colors that differ from the picked theme's own
  // palette are sent (they become the config's sparse overrides), and "save as
  // theme" turns the current look into a named user theme file instead.
  // Copy-mode rebinds still apply on the next page load; server settings are
  // shown read-only — they need a restart.

  // lastSettingsTab remembers the tab the screen was left on, for this page's
  // lifetime (not persisted — it is navigation, not a preference).
  let lastSettingsTab = "";

  function openSettings() {
    sendCmdAwait("config.get", {}, (res) => {
      if (!res.ok) { toast("config.get failed: " + (res.error || "unknown")); return; }
      // The app section is fetched first, from the native side, when there is
      // one; a browser skips straight to building.
      loadAppSettings().then((app) => buildSettingsModal(res.data || {}, app));
    });
  }
  // The Mac app's Settings… menu item (⌘,) lands here; see catappOpenSettings.
  window.catsOpenSettings = () => { if (!uiOpen()) openSettings(); };

  function buildSettingsModal(cfg, appSettings) {
    const effective = (cfg.theme && cfg.theme.colors) || {};
    const effectiveFont = (cfg.theme && cfg.theme.font) || "";
    const themes = cfg.themes || [];
    const themeByName = {};
    for (const t of themes) themeByName[t.name] = t;
    const copyMode = cfg.copy_mode || {};
    const server = cfg.server || {};

    let saved = false;
    // Cancel restores the config.get snapshot (not removeProperty: an earlier
    // live theme push may already be sitting in the inline properties, and the
    // page's baked style underneath could predate it).
    const rollback = () => applyThemeInline(effective, effectiveFont);

    openOverlay((ov) => {
      const m = document.createElement("div"); m.className = "modal settings";
      const h = document.createElement("header"); h.textContent = "settings — " + (cfg.path || "config"); m.appendChild(h);
      // Tabs: `tab(name)` opens a new page and makes it the target of every
      // row()/sub() call after it, so each section's builder below reads as a
      // straight list of rows — the same shape it had when this was one long
      // scrolling modal.
      const shell = document.createElement("div"); shell.className = "sbody";
      const nav = document.createElement("nav"); nav.className = "stabs";
      const pages = document.createElement("div"); pages.className = "spages";
      shell.appendChild(nav); shell.appendChild(pages);
      let body = null;
      const tabBtns = [];
      const showTab = (i) => {
        tabBtns.forEach((b, j) => { b.classList.toggle("on", i === j); b._page.hidden = i !== j; });
        lastSettingsTab = tabBtns[i] ? tabBtns[i].textContent : "";
      };
      const tab = (name, hint) => {
        const page = document.createElement("div"); page.className = "body"; page.hidden = true;
        pages.appendChild(page);
        const b = document.createElement("button"); b.type = "button"; b.textContent = name;
        b._page = page;
        const idx = tabBtns.length;
        b.addEventListener("click", () => showTab(idx));
        nav.appendChild(b); tabBtns.push(b);
        body = page;
        if (hint) {
          const n = document.createElement("div"); n.className = "hint top"; n.textContent = hint;
          page.appendChild(n);
        }
        return page;
      };
      const sub = (title) => { const t = document.createElement("h3"); t.textContent = title; body.appendChild(t); };
      const row = (labelText) => {
        const r = document.createElement("div"); r.className = "row";
        const l = document.createElement("label"); l.textContent = labelText; r.appendChild(l);
        body.appendChild(r);
        return r;
      };
      const isHex = (v) => /^#[0-9a-fA-F]{6}$/.test(v);

      // -- theme picker ----------------------------------------------------
      tab("appearance");
      sub("theme");
      let selName = (cfg.theme && cfg.theme.name) || "";
      const selTheme = () => themeByName[selName] || { colors: {}, font: "" };

      const pr = row("theme");
      const picker = document.createElement("select");
      const groups = [["builtin", "built-in"], ["user", "custom"], ["plugin", "plugins"]];
      for (const [src, glabel] of groups) {
        const members = themes.filter((t) =>
          src === "plugin" ? t.source.startsWith("plugin:") : t.source === src);
        if (!members.length) continue;
        const og = document.createElement("optgroup"); og.label = glabel;
        for (const t of members) {
          const o = document.createElement("option");
          o.value = t.name;
          o.textContent = t.label + (t.dark ? "" : " (light)") +
            (t.source.startsWith("plugin:") ? " — " + t.source.slice(7) : "");
          og.appendChild(o);
        }
        picker.appendChild(og);
      }
      picker.value = selName;
      pr.appendChild(picker);
      const delBtn = document.createElement("button");
      delBtn.textContent = "delete";
      delBtn.title = "delete this custom theme";
      pr.appendChild(delBtn);

      // -- color override rows ----------------------------------------------
      // Rows carry the full canonical palette; edits preview live. Values that
      // match the picked theme's own palette are just "the theme" — only the
      // ones that differ are saved, as the config's sparse overrides.
      const colorInputs = {};
      const swatches = {};
      for (const key of Object.keys(effective).sort()) {
        const r = row(key);
        const swatch = document.createElement("input"); swatch.type = "color";
        const hex = document.createElement("input"); hex.type = "text"; hex.spellcheck = false;
        swatch.addEventListener("input", () => { hex.value = swatch.value; applyThemeInline({ [key]: swatch.value }, ""); });
        hex.addEventListener("input", () => {
          const v = hex.value.trim();
          if (isHex(v)) swatch.value = v;
          if (v) applyThemeInline({ [key]: v }, "");
        });
        r.appendChild(swatch); r.appendChild(hex);
        colorInputs[key] = hex;
        swatches[key] = swatch;
      }
      const fr = row("font");
      const fontInput = document.createElement("input"); fontInput.type = "text"; fontInput.spellcheck = false;
      fontInput.addEventListener("input", () => { if (fontInput.value.trim()) applyThemeInline({}, fontInput.value.trim()); });
      fr.appendChild(fontInput);

      const setRows = (colors, font) => {
        for (const key in colorInputs) {
          const v = colors[key] || "";
          colorInputs[key].value = v;
          if (isHex(v)) swatches[key].value = v;
        }
        fontInput.value = font;
      };
      setRows(effective, effectiveFont); // start from the live effective look

      const syncDelBtn = () => {
        const t = themeByName[selName];
        delBtn.style.display = t && t.source === "user" ? "" : "none";
      };
      syncDelBtn();

      picker.addEventListener("change", () => {
        selName = picker.value;
        const t = selTheme();
        setRows(t.colors, t.font); // a switch shows the theme clean, no overrides
        applyThemeInline(t.colors, t.font);
        syncDelBtn();
      });

      delBtn.addEventListener("click", () => {
        sendCmdAwait("theme.delete", { name: selName }, (res) => {
          if (!res.ok) { errEl.textContent = res.error || "delete failed"; return; }
          saved = true; // the server re-resolved; don't roll back over it
          closeModal();
          toast("theme deleted");
          openSettings(); // rebuild against the refreshed registry
        });
      });

      // -- save as a named user theme ---------------------------------------
      // Turns the current rows into a theme file and activates it (the rows
      // become the theme's own palette, so the config's overrides clear).
      const sr = row("save as");
      const saveAsInput = document.createElement("input");
      saveAsInput.type = "text"; saveAsInput.spellcheck = false;
      saveAsInput.placeholder = "my-theme (lowercase, digits, hyphens)";
      const saveAsBtn = document.createElement("button"); saveAsBtn.textContent = "save theme";
      sr.appendChild(saveAsInput); sr.appendChild(saveAsBtn);
      saveAsBtn.addEventListener("click", () => {
        const name = saveAsInput.value.trim();
        if (!/^[a-z0-9][a-z0-9-]{0,63}$/.test(name)) {
          errEl.textContent = "theme name: lowercase letters, digits, hyphens";
          return;
        }
        const colors = {};
        for (const k in colorInputs) {
          const v = colorInputs[k].value.trim();
          if (v) colors[k] = v;
        }
        sendCmdAwait("theme.save", { name, colors, font: fontInput.value.trim(), activate: true }, (res) => {
          if (!res.ok) { errEl.textContent = res.error || "save failed"; return; }
          saved = true;
          closeModal();
          toast("theme “" + name + "” saved");
        });
      });

      // -- front-end preferences (config.json "ui") ---------------------------
      // What each browser used to keep only in its own localStorage. 0/blank is
      // "unset": that browser keeps deciding for itself.
      sub("interface");
      const uiOpts = (cfg.options && cfg.options.ui) || {};
      const uiFont = numInput(row("font size"), uiOpts.font_px, 9, 32, "px — blank: each browser's own (⌘+ / ⌘-)");
      const uiSbw = numInput(row("sidebar width"), uiOpts.sidebar_width, 150, 2000, "px — blank: each browser's own (drag the gutter)");

      // The action set comes from config.get, so every row is a known action.
      tab("keys");
      sub("copy-mode keys");
      const keyInputs = {};
      for (const action of Object.keys(copyMode).sort()) {
        const r = row(action);
        const inp = document.createElement("input"); inp.type = "text"; inp.spellcheck = false;
        inp.title = "comma-separated KeyboardEvent.key values";
        inp.value = (copyMode[action] || []).join(", ");
        r.appendChild(inp);
        keyInputs[action] = inp;
      }

      // -- generic option sections -------------------------------------------
      const restart = new Set(cfg.restart_sections || []);
      const optCollect = []; // () => [section, newObj|null(no change)]
      for (const spec of OPTION_TABS) {
        const orig = (cfg.options && cfg.options[spec.section]);
        if (!orig) continue; // an older catway without this section: no tab
        tab(spec.tab, restart.has(spec.section)
          ? "saved to the file now; takes effect when catway restarts"
          : "applies as soon as you save");
        optCollect.push(buildOptionRows(spec, orig, row, sub));
      }

      tab("server");
      sub("server (read-only)");
      const ro = (label, val) => {
        const r = row(label);
        const s = document.createElement("span"); s.className = "ro"; s.textContent = val;
        r.appendChild(s);
      };
      ro("addr", server.addr || "");
      ro("auth", server.auth || "");
      ro("tls", server.tls ? "on" : "off");
      ro("cathost socket", server.cathost_socket || "");
      ro("control socket", server.control_socket || "(default)");
      ro("hook socket", server.hook_socket || "(disabled)");
      ro("session ttl", server.session_ttl || "");
      // The roster, one row per host — but only once there is a roster to speak
      // of. With a single host the line above already names its socket, and a
      // row reading "local (connected)" would be the modal restating it.
      if ((server.hosts || []).length > 1) {
        for (const h of server.hosts) {
          ro("host " + h.id, (h.label || h.id) + " · " + (h.addr_kind || "?") +
            (h.is_default ? " · default" : "") + (h.connected ? "" : " · not connected"));
        }
      }
      const note = document.createElement("div"); note.className = "hint";
      note.textContent = "server settings come from flags/config file and take effect on restart; hosts and peers have their own dialogs";
      body.appendChild(note);

      // -- the Mac app's own section ----------------------------------------
      let appCollect = null;
      if (appSettings) {
        tab("app", "this Mac's launcher — stored in this machine's config.json");
        appCollect = buildAppRows(appSettings, row, sub);
      }

      m.appendChild(shell);
      const errEl = document.createElement("div"); errEl.className = "errline"; m.appendChild(errEl);
      // Reopen on the tab the user last looked at: settings is visited to change
      // one thing, and it is usually the same thing as last time.
      const again = tabBtns.findIndex((b) => b.textContent === lastSettingsTab);
      showTab(again >= 0 ? again : 0);

      const save = () => {
        // Only the rows that differ from the picked theme's own palette are
        // overrides; the rest ride on the theme name. Sending the name makes
        // this a switch server-side, replacing any previous overrides.
        const base = selTheme();
        const outColors = {};
        for (const k in colorInputs) {
          const v = colorInputs[k].value.trim();
          if (v && v !== (base.colors[k] || "")) outColors[k] = v;
        }
        const font = fontInput.value.trim();
        const outFont = font && font !== base.font ? font : "";
        const outKeys = {};
        for (const a in keyInputs) {
          const keys = keyInputs[a].value.split(",").map((s) => s.trim()).filter(Boolean);
          if (!keys.length) { errEl.textContent = "copy-mode " + a + ": needs at least one key"; return; }
          outKeys[a] = keys;
        }
        // Option sections: only the ones that changed travel, so a save from
        // the appearance tab cannot rewrite (say) push with what the screen was
        // opened on, over a `catctl` edit made in the meantime.
        const options = {};
        for (const collect of optCollect) {
          const [section, obj, err] = collect();
          if (err) { errEl.textContent = err; return; }
          if (obj) options[section] = obj;
        }
        const ui = uiChanges(uiOpts, uiFont, uiSbw);
        if (ui.err) { errEl.textContent = ui.err; return; }
        if (ui.obj) options.ui = ui.obj;
        let appOut = null;
        if (appCollect) {
          const a = appCollect();
          if (a.err) { errEl.textContent = a.err; return; }
          appOut = a.obj;
        }

        const params = { theme: { name: selName, colors: outColors, font: outFont }, copy_mode: outKeys };
        if (Object.keys(options).length) params.options = options;
        sendCmdAwait("config.set", params, (res) => {
          if (!res.ok) { errEl.textContent = res.error || "save failed"; return; }
          const finish = () => {
            saved = true;
            // This page already wears the previewed look; the server's theme
            // broadcast re-applies the authoritative resolution everywhere.
            closeModal();
            toast("settings saved");
            if (Object.keys(outKeys).some((a) => outKeys[a].join(",") !== (copyMode[a] || []).join(","))) {
              toast("copy-mode rebinds apply after a page reload");
            }
            const later = Object.keys(options).filter((s) => restart.has(s));
            if (later.length) toast(later.join(", ") + ": takes effect when catway restarts");
            if (ui.obj) applyUIPrefs(ui.obj);
          };
          if (!appOut) { finish(); return; }
          // The app section is a second write, to a different file owner. It
          // goes after catway's so a refused config.set does not leave half a
          // save behind; an app-side failure keeps the screen open to say so.
          saveAppSettings(appOut).then((err) => {
            if (err) { saved = true; errEl.textContent = "app settings: " + err; return; }
            if (appOut.mode !== appSettings.mode) toast("launch mode applies the next time Cats starts");
            finish();
          });
        });
      };

      const btns = document.createElement("div"); btns.className = "btns";
      btns.appendChild(mkModalBtn("cancel", "", closeModal));
      btns.appendChild(mkModalBtn("save", "primary", save));
      m.appendChild(btns);
      m.addEventListener("keydown", (e) => {
        e.stopPropagation();
        // Enter in a text field saves (as it always has); Enter on a button,
        // checkbox or select is that control's own business — a tab button
        // pressed with the keyboard must switch tabs, not save the screen.
        if (e.key === "Enter" && e.target.tagName === "INPUT" && e.target.type !== "checkbox") { e.preventDefault(); save(); }
        else if (e.key === "Escape") { e.preventDefault(); closeModal(); }
      });
      ov.appendChild(m);
      // Any close path (cancel, Escape, backdrop) rolls the preview back unless
      // it was saved.
      modalCleanup = () => { if (!saved) rollback(); };
    });
  }


  // ---- generic option tabs ----------------------------------------------------
  //
  // One entry per config.json section the screen edits generically. Each field
  // is [key, label, kind, hint]; kind picks the control and how its value is
  // read back:
  //   text      free text              (sent as a string)
  //   duration  Go duration or "off"   (a string; the server validates)
  //   int       whole number           (a number; blank ⇒ 0)
  //   bool      checkbox
  //   list      comma-separated        (an array of strings)
  //   kinds     one checkbox per push kind (an array)
  //   priority  one select per push kind   (a {kind: level} object)
  // The labels and hints are the documentation that used to live in the YAML
  // file's comments — JSON has none, so the screen is where they went.
  const PUSH_KINDS = ["attention", "finished", "info"];
  const PUSH_LEVELS = ["", "min", "low", "default", "high", "urgent"];
  const OPTION_TABS = [
    { tab: "panes", section: "panes", fields: [
      ["reap_exited", "reap exited", "duration", "how long an exited pane is kept before it is closed — Go duration, or off"],
      ["autoclose_exited", "autoclose on 0", "duration", "countdown before a pane whose shell exited cleanly closes itself — or off"],
      ["agent_refresh", "agent refresh", "duration", "how often agent rows re-read model and context use while a pane stays in one state — at least 10s, or off"],
    ] },
    { tab: "editor", section: "editor", fields: [
      ["agents", "editor agents", "list", "agent labels that count as an editor (pane.open_file targets)"],
      ["command", "start command", "list", "argv to start one when none is running; the path is appended"],
      ["spawn", "start if none", "bool", "off: opening a file only reveals an editor that is already open"],
    ] },
    { tab: "worktrees", section: "worktrees", fields: [
      ["directory", "directory", "text", "where workspace › new worktree creates checkouts (~ allowed)"],
    ] },
    { tab: "notifications", section: "push", fields: [
      ["enabled", "push enabled", "bool", "also POST agent notifications to an ntfy-style webhook"],
      ["url", "url", "text", "e.g. https://ntfy.sh/<unguessable topic> — the topic is a secret"],
      ["kinds", "kinds", "kinds", "which notifications are pushed"],
      ["priority", "priority", "priority", "ntfy priority per kind"],
      ["min_interval", "min interval", "duration", "debounce per pane and kind"],
      ["click_url", "click url", "text", "deep-link base; the pane handle is appended"],
      ["actions", "actions", "bool", "tappable answers on an attention push — an inbound surface, off by default"],
      ["action_url", "action url", "text", "where a phone reaches this catway (required with actions)"],
    ] },
    { tab: "persistence", section: "persistence", fields: [
      ["enabled", "enabled", "bool", "restore workspaces, tabs and panes across a catway restart"],
      ["state_dir", "state dir", "text", "blank: $XDG_STATE_HOME/cats"],
      ["history_lines", "history lines", "int", "scrollback lines captured per pane (0 = whole buffer)"],
      ["resume_agents", "resume agents", "bool", "relaunch AI-agent panes into their own sessions on a cold restore"],
    ] },
    { tab: "ledger", section: "ledger", fields: [
      ["enabled", "enabled", "bool", "record every shell command (needs `catctl integration install shell`)"],
      ["retention", "retention", "int", "records kept; the oldest go first"],
    ] },
  ];

  // zeroFor is what an absent (omitempty) key means for a kind, so "absent" and
  // "explicitly empty" compare equal and do not count as an edit.
  function zeroFor(kind) {
    switch (kind) {
      case "bool": return false;
      case "int": return 0;
      case "list": case "kinds": return [];
      case "priority": return {};
      default: return "";
    }
  }

  // buildOptionRows renders one section's fields into the current tab and
  // returns its collector: () => [section, changedObjectOrNull, errorOrNull].
  // The object sent back is the ORIGINAL section with the edited keys laid
  // over it, so a key this table does not list (added server-side later)
  // round-trips instead of being dropped.
  function buildOptionRows(spec, orig, row, sub) {
    const readers = {};
    for (const [key, label, kind, hint] of spec.fields) {
      const r = row(label);
      const cur = orig[key] === undefined ? zeroFor(kind) : orig[key];
      readers[key] = optionControl(r, kind, cur);
      if (hint) r.title = hint;
      if (hint) {
        const hEl = document.createElement("div"); hEl.className = "hint field"; hEl.textContent = hint;
        r.after(hEl);
      }
    }
    return () => {
      const out = Object.assign({}, orig);
      let changed = false;
      for (const [key, label, kind] of spec.fields) {
        const v = readers[key]();
        if (v instanceof Error) return [spec.section, null, label + ": " + v.message];
        const before = orig[key] === undefined ? zeroFor(kind) : orig[key];
        if (JSON.stringify(v) !== JSON.stringify(before)) changed = true;
        out[key] = v;
      }
      return [spec.section, changed ? out : null, null];
    };
  }

  // optionControl appends the control for one field and returns its reader.
  function optionControl(r, kind, cur) {
    const text = (v) => {
      const i = document.createElement("input"); i.type = "text"; i.spellcheck = false; i.value = v;
      r.appendChild(i); return i;
    };
    switch (kind) {
      case "bool": {
        const c = document.createElement("input"); c.type = "checkbox"; c.checked = !!cur;
        r.appendChild(c);
        return () => c.checked;
      }
      case "int": {
        const i = text(String(cur || 0));
        return () => {
          const t = i.value.trim();
          if (t === "") return 0;
          if (!/^\d+$/.test(t)) return new Error("want a whole number");
          return parseInt(t, 10);
        };
      }
      case "list": {
        const i = text((cur || []).join(", "));
        return () => i.value.split(",").map((s) => s.trim()).filter(Boolean);
      }
      case "kinds": {
        const boxes = {};
        const g = chkGroup(r);
        for (const k of PUSH_KINDS) {
          const l = document.createElement("span"); l.className = "chk";
          const c = document.createElement("input"); c.type = "checkbox"; c.checked = (cur || []).includes(k);
          l.appendChild(c); l.appendChild(document.createTextNode(k));
          g.appendChild(l); boxes[k] = c;
        }
        return () => PUSH_KINDS.filter((k) => boxes[k].checked);
      }
      case "priority": {
        const sels = {};
        const g = chkGroup(r);
        for (const k of PUSH_KINDS) {
          const l = document.createElement("span"); l.className = "chk"; l.textContent = k;
          const sel = document.createElement("select");
          for (const lv of PUSH_LEVELS) {
            const o = document.createElement("option"); o.value = lv; o.textContent = lv || "—";
            sel.appendChild(o);
          }
          sel.value = (cur || {})[k] || "";
          l.appendChild(sel); g.appendChild(l); sels[k] = sel;
        }
        return () => {
          const out = {};
          for (const k of PUSH_KINDS) if (sels[k].value) out[k] = sels[k].value;
          return out;
        };
      }
      default: { // text, duration
        const i = text(cur || "");
        return () => i.value.trim();
      }
    }
  }

  // chkGroup is the wrapping container for a row of several small controls
  // (the push kinds and priorities), so a narrow dialog wraps them inside the
  // value column instead of under the label.
  function chkGroup(r) {
    const g = document.createElement("div"); g.className = "chkgrp";
    r.appendChild(g);
    return g;
  }

  // numInput is the interface rows' number box: blank means "unset" (0).
  function numInput(r, cur, min, max, hint) {
    const i = document.createElement("input"); i.type = "text"; i.spellcheck = false;
    i.value = cur ? String(cur) : ""; i.placeholder = hint; i.dataset.min = min; i.dataset.max = max;
    r.appendChild(i);
    return i;
  }

  // uiChanges reads the two interface boxes against the file's values.
  function uiChanges(orig, fontEl, sbwEl) {
    const read = (el, label) => {
      const t = el.value.trim();
      if (t === "") return 0;
      const n = parseInt(t, 10), lo = +el.dataset.min, hi = +el.dataset.max;
      if (!/^\d+$/.test(t) || n < lo || n > hi) return new Error(label + ": want " + lo + "–" + hi + ", or blank");
      return n;
    };
    const font = read(fontEl, "font size"), sbw = read(sbwEl, "sidebar width");
    if (font instanceof Error) return { err: font.message };
    if (sbw instanceof Error) return { err: sbw.message };
    if (font === (orig.font_px || 0) && sbw === (orig.sidebar_width || 0)) return {};
    return { obj: { font_px: font, sidebar_width: sbw } };
  }

  // ---- front-end preferences ↔ config.json ------------------------------------
  //
  // Font size and sidebar width start from the file (window.__catsUI, baked
  // into the page by catway) when it has them, else from this browser's
  // localStorage, else the built-in default — see 01-bootstrap.js and
  // 38-sidebar.js. A change made the ordinary way (⌘+, a gutter drag) is
  // written back here, debounced: a pinch-zoom or a drag is a stream of
  // values, and only where it comes to rest is a preference.
  const uiPrefsFile = Object.assign({}, window.__catsUI || {});
  const uiPrefTimers = {};
  function persistUIPref(key, value) {
    clearTimeout(uiPrefTimers[key]);
    uiPrefTimers[key] = setTimeout(() => {
      if ((uiPrefsFile[key] || 0) === value) return; // the file already says so
      uiPrefsFile[key] = value;
      sendCmdAwait("config.set", { options: { ui: { [key]: value } } }, (res) => {
        // Not a toast on every failure mode worth a shrug: the value is still
        // applied here and in localStorage. Only say something when it is the
        // file that refused (a read-only config dir, say).
        if (!res.ok) toast("could not save " + key.replace("_", " ") + ": " + (res.error || "unknown"));
      });
    }, 800);
  }

  // applyUIPrefs puts a just-saved "ui" section on screen in this window. A 0
  // leaves the current value alone: "unset" means "this browser decides", and
  // this browser has already decided.
  function applyUIPrefs(ui) {
    Object.assign(uiPrefsFile, ui);
    if (ui.font_px) setFontSize(ui.font_px);
    if (ui.sidebar_width) { setSidebarWidth(ui.sidebar_width); sidebarResized(true); }
  }

  // ---- the Mac app's section --------------------------------------------------
  //
  // window.catsAppSettingsGet/Set exist only inside the Mac app (injected by
  // window_darwin.m). Both return promises: Get resolves to the app section as
  // an object (or null when the bridge is absent or failed), Set to "" or an
  // error message.
  function loadAppSettings() {
    if (typeof window.catsAppSettingsGet !== "function") return Promise.resolve(null);
    return window.catsAppSettingsGet()
      .then((s) => (typeof s === "string" ? JSON.parse(s) : s) || null)
      .catch(() => null);
  }
  function saveAppSettings(obj) {
    return window.catsAppSettingsSet(JSON.stringify(obj))
      .then((err) => err || "")
      .catch((e) => String(e && e.message || e));
  }

  // buildAppRows renders the app tab: launch mode and the saved catways (the
  // Connect menu's list). The current connection and the window layout ride
  // along untouched — they are state the app keeps, not choices made here.
  function buildAppRows(app, row, sub) {
    sub("launch");
    const mr = row("mode");
    const mode = document.createElement("select");
    for (const [v, l] of [["local", "local — run cats on this Mac"], ["remote", "remote — connect to a catway elsewhere"]]) {
      const o = document.createElement("option"); o.value = v; o.textContent = l; mode.appendChild(o);
    }
    mode.value = app.mode === "remote" ? "remote" : "local";
    mr.appendChild(mode);

    sub("saved catways");
    const list = document.createElement("div"); list.className = "presets";
    row("").replaceWith(list);
    const presets = (app.presets || []).map((p) => ({ url: p.url, label: p.label || "" }));
    const draw = () => {
      list.textContent = "";
      if (!presets.length) {
        const n = document.createElement("div"); n.className = "hint"; n.textContent = "none yet — add one below, or use Connect › Connect to Another…";
        list.appendChild(n);
      }
      presets.forEach((p, i) => {
        const r = document.createElement("div"); r.className = "row";
        const lab = document.createElement("input"); lab.type = "text"; lab.spellcheck = false;
        lab.value = p.label; lab.placeholder = "label";
        lab.addEventListener("input", () => { p.label = lab.value; });
        const u = document.createElement("span"); u.className = "ro"; u.textContent = p.url; u.title = p.url;
        const del = document.createElement("button"); del.type = "button"; del.textContent = "forget";
        del.addEventListener("click", () => { presets.splice(i, 1); draw(); });
        r.appendChild(lab); r.appendChild(u); r.appendChild(del);
        list.appendChild(r);
      });
    };
    draw();
    const ar = row("add");
    const addLabel = document.createElement("input"); addLabel.type = "text"; addLabel.placeholder = "label"; addLabel.spellcheck = false;
    const addURL = document.createElement("input"); addURL.type = "text"; addURL.placeholder = "https://host:8421"; addURL.spellcheck = false;
    const addBtn = document.createElement("button"); addBtn.type = "button"; addBtn.textContent = "add";
    let addErr = "";
    addBtn.addEventListener("click", () => {
      const u = addURL.value.trim();
      if (!/^https?:\/\/[^\s/]+/.test(u)) { addErr = "a catway address is an http(s) URL"; addURL.focus(); return; }
      addErr = "";
      const at = presets.findIndex((p) => p.url === u);
      const item = { url: u, label: addLabel.value.trim() };
      if (at >= 0) presets[at] = item; else presets.push(item);
      addURL.value = ""; addLabel.value = "";
      draw();
    });
    ar.appendChild(addLabel); ar.appendChild(addURL); ar.appendChild(addBtn);

    return () => {
      if (addErr) return { err: addErr };
      // Anything typed into "add" but not added is added — leaving it behind
      // silently on save is the one outcome nobody means.
      const pending = addURL.value.trim();
      if (pending) {
        if (!/^https?:\/\/[^\s/]+/.test(pending)) return { err: "saved catways: a catway address is an http(s) URL" };
        if (!presets.some((p) => p.url === pending)) presets.push({ url: pending, label: addLabel.value.trim() });
      }
      const out = { mode: mode.value, presets: presets.map((p) => ({ url: p.url, label: p.label.trim() })) };
      const same = out.mode === (app.mode || "local") &&
        JSON.stringify(out.presets) === JSON.stringify((app.presets || []).map((p) => ({ url: p.url, label: p.label || "" })));
      return same ? {} : { obj: out };
    };
  }
