  // Toolbar affordances: the palette hint opens it, plugins opens its manager
  // directly (it earns the top-level slot — installing and running plugins is
  // routine work, not a settings excursion), and the gear opens the global
  // launcher menu (cats's bottom-corner menu: settings / keybinds / reload
  // config / update line when one is pending / stop server standing in for
  // detach — the web has no client to detach). The zoom chip moved into the
  // zoomed pane's own header (renderChrome).
  palHintEl.addEventListener("click", () => openPalette());
  pluginsBtnEl.addEventListener("click", () => openPluginsDialog());
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
