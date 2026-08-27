  // ---- Packed u32 colors (D2): 0x02_RR_GG_BB → CSS ----
  function rgbOf(v) { return [(v >> 16) & 255, (v >> 8) & 255, v & 255]; }
  function css(v) { const [r, g, b] = rgbOf(v); return `rgb(${r},${g},${b})`; }
  function blend(a, b, t) { // mix packed a toward packed b
    const pa = rgbOf(a), pb = rgbOf(b);
    return `rgb(${Math.round(pa[0] + (pb[0] - pa[0]) * t)},${Math.round(pa[1] + (pb[1] - pa[1]) * t)},${Math.round(pa[2] + (pb[2] - pa[2]) * t)})`;
  }

  function newPane(id) {
    const el = document.createElement("div");
    el.className = "pane";
    const chrome = document.createElement("div");
    chrome.className = "chrome";
    const info = document.createElement("div");
    info.className = "info";
    const ctl = document.createElement("div");
    ctl.className = "ctl";
    // Glyphs mirror the resulting layout: ◫ = vertical divider (side by side),
    // ⊟ = horizontal divider (stacked).
    ctl.appendChild(mkBtn("◫", "split left/right", "", () => sendCmd("pane.split", { pane: id, direction: "h" })));
    ctl.appendChild(mkBtn("⊟", "split top/bottom", "", () => sendCmd("pane.split", { pane: id, direction: "v" })));
    ctl.appendChild(mkBtn("⤢", "toggle zoom", "", () => sendCmd("pane.zoom", { pane: id })));
    ctl.appendChild(mkBtn("⬚", "copy mode (keyboard select)", "", () => enterCopyMode(id)));
    ctl.appendChild(mkBtn("⧉", "copy scrollback to clipboard", "", () => copyScrollback(id)));
    ctl.appendChild(mkBtn("✎", "rename pane", "", () => renamePane(id)));
    // The flag button is always here, flagged or not: the chip in the identity
    // line only exists once a flag has been set, so without a permanent
    // affordance the header would offer no way to set the first one. It opens
    // the same menu the chip and the context menus do — one vocabulary, one
    // place it is spelled out.
    ctl.appendChild(mkBtn("⚑", "flag this pane", "flagbtn", (e) =>
      openCtx(e.clientX, e.clientY, flagMenuItems(paneFlagTarget(id)))));
    ctl.appendChild(mkBtn("✕", "close pane", "close", () => sendCmd("pane.close", { pane: id })));
    chrome.appendChild(info); chrome.appendChild(ctl);
    const canvas = document.createElement("canvas");
    el.appendChild(chrome); el.appendChild(canvas);
    panesEl.appendChild(el);
    const p = { id, el, chrome, chromeInfo: info, canvas, ctx: canvas.getContext("2d", { alpha: false }),
      pub: "", info: null, W: 0, H: 0, cells: [], links: [], cur: null,
      gs: 1, ox: 0, oy: 0,   // grid inset: scale + centring offsets (setInset)
      defFg: THEME_FG, defBg: THEME_BG, modes: { mouse: false, alt: false, kitty: 0 },
      title: "", cwd: "", branch: "", agent: "", agentState: "", agentModel: "", exited: null,
      pressed: -1, lastCell: "", dirty: false, scroll: null, sel: null, cm: null };
    // Clicking the header (not its buttons) focuses the pane; double-click
    // renames, right-click opens the pane menu (chrome or canvas); holding
    // and moving drags the pane onto another to swap slots. The sidebar pane
    // rows keep the same affordances — the header is also the escape hatch on
    // mouse-capturing apps, where right-clicking the canvas goes to the app.
    chrome.addEventListener("mousedown", (e) => {
      if (p.info && !p.info.focused) sendCmd("pane.focus", { pane: id });
      beginPaneSwapDrag(e, id);
    });
    chrome.addEventListener("dblclick", () => renamePane(id));
    chrome.addEventListener("contextmenu", (e) => { e.preventDefault(); openCtx(e.clientX, e.clientY, paneMenuItems(id)); });
    attachMouse(p);
    attachDrop(p);
    panes.set(id, p);
    return p;
  }

  // fn receives the click event, which the handlers that open a menu need in
  // order to place it under the pointer. Every other caller ignores the extra
  // argument, so this costs them nothing.
  function mkBtn(label, title, cls, fn) {
    const b = document.createElement("button");
    b.textContent = label; b.title = title; if (cls) b.className = cls;
    b.addEventListener("mousedown", (e) => e.stopPropagation());
    b.addEventListener("click", (e) => { e.stopPropagation(); e.preventDefault(); fn(e); });
    return b;
  }

  // initSectionFold hangs the section-level fold arrow on one sidebar heading:
  // the third control, outboard of whatever pair that section already carries,
  // which hides the section's entire list instead of one group within it.
  //
  //   PANES                    ⊞ ⊟ ▼      open: the section's own controls, then
  //     ws-a          3 panes              the arrow hard against the right edge
  //     α  vim
  //   PANES                        ▶      folded: the arrow, alone
  //
  // Called last in each heading's setup so the arrow is appended after that
  // section's other buttons — DOM order is the visual order inside .hctl.
  //
  // The arrow glyphs are the ones the group headers already fold with (▼ open,
  // ▶ shut), so one mark means one thing everywhere in the sidebar; only its
  // scope changes with where it sits. Hiding is CSS (see #sidebar section.folded)
  // rather than a render-time skip: the lists keep rebuilding behind the fold,
  // which costs a hidden section's rows but means unfolding is instant and every
  // renderer stays free of a "am I visible?" branch it would otherwise need in
  // each of its own draw paths.
  function initSectionFold(secID, hctlID, noun) {
    const sec = document.getElementById(secID);
    const btn = mkBtn("", "", "secfold", () => {
      if (sectCollapsed.has(secID)) sectCollapsed.delete(secID); else sectCollapsed.add(secID);
      saveSectCollapsed(); apply();
    });
    function apply() {
      const folded = sectCollapsed.has(secID);
      sec.classList.toggle("folded", folded);
      btn.textContent = folded ? "▶" : "▼";
      btn.title = (folded ? "show " : "hide all ") + noun;
      btn.setAttribute("aria-expanded", folded ? "false" : "true");
    }
    document.getElementById(hctlID).appendChild(btn);
    apply(); // adopt whatever the last session left folded
  }

  function pane(id) { return panes.get(id) || newPane(id); }

