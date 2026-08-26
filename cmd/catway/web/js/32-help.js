  // ---- Keyboard help (WS8) ----
  function helpSection(title, rows) {
    const wrap = document.createElement("div");
    const h = document.createElement("h3"); h.textContent = title; wrap.appendChild(h);
    const tbl = document.createElement("table");
    for (const [what, keys] of rows) {
      const tr = document.createElement("tr");
      const td1 = document.createElement("td"); td1.textContent = what;
      const td2 = document.createElement("td");
      for (const k of keys) { const kb = document.createElement("span"); kb.className = "key"; kb.textContent = k; td2.appendChild(kb); }
      tr.appendChild(td1); tr.appendChild(td2); tbl.appendChild(tr);
    }
    wrap.appendChild(tbl);
    return wrap;
  }

  function openHelp() {
    openOverlay((ov) => {
      const m = document.createElement("div"); m.className = "modal help";
      const h = document.createElement("header"); h.textContent = "keyboard & mouse reference"; m.appendChild(h);
      const body = document.createElement("div"); body.className = "body";
      const cols = document.createElement("div"); cols.className = "helpcols";

      const left = document.createElement("div");
      left.appendChild(helpSection("global", [
        ["command palette", ["⌘K", "Ctrl+Alt+K"]],
        ["hide / show the sidebar", ["⌘B", "Ctrl+Alt+B"]],
        ["back / forward (pane & workspace history)", ["⌘[", "⌘]", "Ctrl+Alt+[ / ]"]],
        ["paste", ["⌘V", "Ctrl+Shift+V*"]],
        ["bigger / smaller font", ["⌘+", "⌘-"]],
        ["reset font size", ["⌘0"]],
        ["this help", ["⌘K → keyboard shortcuts"]],
        // Not a cats shortcut — a statement about who gets the key, and
        // the only place a user can find out why ⌘S stopped opening the
        // browser's save dialog over an editor pane.
        ["sent to an editor pane, not the browser", ["⌘C", "⌘Z", "⌘S", "⌘P", "⌘E†", "⌘F", "⌘D", "⌘G", "⌘/"]],
      ]));
      left.appendChild(helpSection("start-path picker (new workspace)", [
        ["move through directories", ["↑", "↓", "Ctrl+n", "Ctrl+p"]],
        ["complete, then list inside it", ["Tab", "click"]],
        ["create in the highlighted one", ["Enter"]],
        ["make a folder that doesn't exist yet", ["type its path, Enter, confirm"]],
        ["include dotted directories", ["type a leading ."]],
      ]));
      left.appendChild(helpSection("mouse", [
        ["focus pane", ["click"]],
        ["select + copy text", ["drag"]],
        ["rectangular selection", ["Alt+drag"]],
        ["pane / tab / workspace menu", ["right-click"]],
        ["resize splits", ["drag the border"]],
        ["hide the sidebar", ["drag its gutter to the left edge"]],
        ["show it again", ["click the handle at the left edge"]],
        ["swap panes", ["drag a pane header or sidebar row onto a pane"]],
        ["reorder tabs / workspaces", ["drag"]],
        ["rename pane / tab / workspace", ["double-click its title"]],
        ["scrollback", ["wheel", "drag the scrollbar"]],
        ["back / forward (pane & workspace history)", ["back / forward buttons"]],
        ["launcher menu", ["⚙ at the right of the tab row"]],
      ]));
      cols.appendChild(left);

      // Copy-mode bindings reflect the active table (config-injected or default).
      const actions = [
        ["move", ["move-left", "move-down", "move-up", "move-right"]],
        ["line start / end", ["line-start", "line-end"]],
        ["top / bottom", ["top", "bottom"]],
        ["begin selection", ["select"]],
        ["rectangle toggle", ["rect"]],
        ["yank to clipboard", ["yank"]],
        ["exit copy mode", ["exit"]],
      ];
      const table = (window.__catsKeys && window.__catsKeys.copyMode) || COPY_MODE_DEFAULT;
      const right = document.createElement("div");
      right.appendChild(helpSection("copy mode (⬚ on a pane header)",
        actions.map(([what, acts]) => [what, acts.flatMap((a) => table[a] || [])])));
      right.appendChild(helpSection("pane header buttons", [
        ["split left/right · top/bottom", ["◫", "⊟"]],
        ["zoom", ["⤢"]],
        ["copy mode · copy scrollback", ["⬚", "⧉"]],
        ["rename · close", ["✎", "✕"]],
      ]));
      cols.appendChild(right);

      body.appendChild(cols);
      const note = document.createElement("p");
      note.className = "hint";
      // The † caveat is the honest half of the row above it: a chord on the
      // allowlist still only arrives if the HOST hands it over, and Chrome
      // resolves ⌘E for a menu of its own before this page is offered the
      // keydown at all — so the row would otherwise promise something the
      // browser never lets cats deliver. The mac app has no ⌘E menu item
      // and does deliver it, which is why the entry stays. ⌘E is the only
      // row needing the mark: every other forwarded chord was hand-checked
      // in Chrome and the mac app and arrives in both.
      note.textContent = "* browser paste shortcuts vary; the ⌘V path asks for clipboard permission once. Everything else is typed straight into the focused terminal. † ⌘E reaches a pane in the mac app, but Chrome keeps it for a menu of its own and never offers it to this page.";
      body.appendChild(note);
      m.appendChild(body);
      const btns = document.createElement("div"); btns.className = "btns";
      btns.appendChild(mkModalBtn("close", "primary", closeModal));
      m.appendChild(btns);
      m.addEventListener("keydown", (e) => { e.stopPropagation(); if (e.key === "Escape") { e.preventDefault(); closeModal(); } });
      ov.appendChild(m);
    });
  }

