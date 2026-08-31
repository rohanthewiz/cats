  // Toolbar affordances: the palette hint opens it, plugins opens its manager
  // directly (it earns the top-level slot — installing and running plugins is
  // routine work, not a settings excursion), rec arms and disarms the macro
  // recorder while showing that it is armed, and the gear opens the global
  // launcher menu (cats's bottom-corner menu: settings / keybinds / reload
  // config / update line when one is pending / stop server standing in for
  // detach — the web has no client to detach). The zoom chip moved into the
  // zoomed pane's own header (renderChrome).
  palHintEl.addEventListener("click", () => openPalette());
  pluginsBtnEl.addEventListener("click", () => openPluginsDialog());
  // The recorder is the one toolbar item whose click depends on what it is
  // currently showing: idle it arms (nothing exists yet, so there is nothing to
  // confirm), armed it opens the menu that holds the two ways out. Both ways
  // out are gated in 40-record.js — stop needs a name, cancel destroys work.
  recBtnEl.addEventListener("click", (e) => {
    if (recState.recording) openRecordMenu(); else startRecording();
    e.stopPropagation();
  });
  gearEl.addEventListener("click", (e) => {
    const items = [
      { label: "settings", fn: openSettings },
      { label: "keybinds", fn: openHelp },
      { label: "reload config", fn: () => sendCmd("server.reload_config", {}) },
      { label: "attach host…", fn: openAttachHostDialog },
    ];
    if (updateInfo) items.push({ label: "update ready" + (updateInfo.version ? " — " + updateInfo.version : ""), fn: showUpdateBanner });
    items.push("-", { label: "stop server…", danger: true, fn: confirmStopServer });
    // The gear rides at the top of the window now, so the menu drops below it
    // (openCtx only clamps against the far edges, it will not flip upward).
    const r = gearEl.getBoundingClientRect();
    openCtx(r.right, r.bottom + 4, items);
    e.stopPropagation();
  });

  measure();
  gridSize();
  connect();
