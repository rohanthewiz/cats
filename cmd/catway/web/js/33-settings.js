  // ---- Settings modal (WS8): theme + copy-mode keys over config.get/set ----
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

  function openSettings() {
    sendCmdAwait("config.get", {}, (res) => {
      if (!res.ok) { toast("config.get failed: " + (res.error || "unknown")); return; }
      buildSettingsModal(res.data || {});
    });
  }

  function buildSettingsModal(cfg) {
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
      const body = document.createElement("div"); body.className = "body";
      const section = (title) => { const t = document.createElement("h3"); t.textContent = title; body.appendChild(t); };
      const row = (labelText) => {
        const r = document.createElement("div"); r.className = "row";
        const l = document.createElement("label"); l.textContent = labelText; r.appendChild(l);
        body.appendChild(r);
        return r;
      };
      const isHex = (v) => /^#[0-9a-fA-F]{6}$/.test(v);

      // -- theme picker ----------------------------------------------------
      section("theme");
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

      // The action set comes from config.get, so every row is a known action.
      section("copy-mode keys");
      const keyInputs = {};
      for (const action of Object.keys(copyMode).sort()) {
        const r = row(action);
        const inp = document.createElement("input"); inp.type = "text"; inp.spellcheck = false;
        inp.title = "comma-separated KeyboardEvent.key values";
        inp.value = (copyMode[action] || []).join(", ");
        r.appendChild(inp);
        keyInputs[action] = inp;
      }

      section("server (read-only)");
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
      note.textContent = "server settings come from flags/config file and take effect on restart";
      body.appendChild(note);

      const errEl = document.createElement("div"); errEl.className = "errline"; body.appendChild(errEl);
      m.appendChild(body);

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
        sendCmdAwait("config.set", { theme: { name: selName, colors: outColors, font: outFont }, copy_mode: outKeys }, (res) => {
          if (!res.ok) { errEl.textContent = res.error || "save failed"; return; }
          saved = true;
          // This page already wears the previewed look; the server's theme
          // broadcast re-applies the authoritative resolution everywhere.
          closeModal();
          toast("settings saved");
          if (Object.keys(outKeys).some((a) => outKeys[a].join(",") !== (copyMode[a] || []).join(","))) {
            toast("copy-mode rebinds apply after a page reload");
          }
        });
      };

      const btns = document.createElement("div"); btns.className = "btns";
      btns.appendChild(mkModalBtn("cancel", "", closeModal));
      btns.appendChild(mkModalBtn("save", "primary", save));
      m.appendChild(btns);
      m.addEventListener("keydown", (e) => {
        e.stopPropagation();
        if (e.key === "Enter") { e.preventDefault(); save(); }
        else if (e.key === "Escape") { e.preventDefault(); closeModal(); }
      });
      ov.appendChild(m);
      // Any close path (cancel, Escape, backdrop) rolls the preview back unless
      // it was saved.
      modalCleanup = () => { if (!saved) rollback(); };
    });
  }

