  // ---- Drag-and-drop upload ----
  //
  // Dropping a file on a pane writes it into that pane's working directory, on
  // that pane's machine. The destination is named by NOT naming it: file.put
  // resolves a relative path against the anchor pane's live cwd on the host the
  // pane runs on, so the browser sends a bare filename and never learns either.
  // A cwd read here would also be the cwd as of the last pane_cwd event, which
  // is a different directory from the one the pane is in now if somebody just
  // cd'd — and the file would land in the wrong place with no way to tell.
  //
  // The chunking is the same loop `catctl cp` runs, for the same reason (see
  // internal/filexfer): every hop to the destination disk is a whole-message
  // transport with a ceiling.
  const UPLOAD_CHUNK = 1 << 20;   // filexfer.MaxChunk; a larger chunk is refused
  const UPLOAD_TIMEOUT_MS = 60000; // one chunk, not one transfer

  // hasFiles distinguishes a file dragged in from the desktop from every
  // in-app drag (pane swaps, sidebar reordering), which carry no Files type and
  // must not light up a drop target that would try to upload them.
  function hasFiles(ev) {
    const dt = ev.dataTransfer;
    return !!dt && Array.prototype.indexOf.call(dt.types || [], "Files") >= 0;
  }

  function attachDrop(p) {
    // dragenter/dragleave fire for every child element the pointer crosses, so
    // a plain add/remove pair would flicker the highlight off the moment the
    // cursor moved from the canvas onto the chrome. Counting depth is the
    // standard fix and the only reliable one.
    let depth = 0;
    const clear = () => { depth = 0; p.el.classList.remove("filedrop"); };
    p.el.addEventListener("dragenter", (ev) => {
      if (!hasFiles(ev)) return;
      ev.preventDefault();
      if (depth++ === 0) p.el.classList.add("filedrop");
    });
    p.el.addEventListener("dragover", (ev) => {
      if (!hasFiles(ev)) return;
      ev.preventDefault();                  // without this the drop never fires
      ev.dataTransfer.dropEffect = "copy";
    });
    p.el.addEventListener("dragleave", () => { if (depth > 0 && --depth === 0) clear(); });
    p.el.addEventListener("drop", (ev) => {
      if (!hasFiles(ev)) return;
      ev.preventDefault();                  // else the browser navigates to the file
      clear();
      for (const f of ev.dataTransfer.files) uploadFile(p, f);
    });
  }

  // uploadFile sends one dropped file to the pane's cwd, a chunk at a time.
  //
  // Nothing is overwritten. A name already taken on the far side comes back as
  // a refusal from file.put and is shown as one — the alternative is a silent
  // replacement of somebody's file on a machine the person dropping may not
  // even be looking at.
  async function uploadFile(p, file) {
    // A basename, never a path. Browsers hand over the leaf name for a plain
    // file but a RELATIVE path for a directory drop, and a path here would
    // write outside the pane's cwd — including upward, given "..". Dotfiles
    // keep their dot: ".bashrc" is a name, while "." and ".." are not.
    let name = (file.name || "file").split(/[\\/]/).pop();
    if (!name || name === "." || name === "..") name = "file";
    const total = file.size;
    let offset = 0;
    try {
      do {
        const buf = new Uint8Array(await file.slice(offset, offset + UPLOAD_CHUNK).arrayBuffer());
        const more = offset + buf.length < total;
        await putChunk(p.id, name, buf, offset, more);
        offset += buf.length;
      } while (offset < total);
      toast(name + " → " + (p.pub || "pane " + p.id) + " (" + fmtBytes(total) + ")");
    } catch (err) {
      // The offset is in the message because a failed transfer leaves a
      // .name.cats-part fragment on the far side, and knowing how far it got is
      // the difference between "retry" and "go and look".
      toast("upload of " + name + " failed at " + fmtBytes(offset) + ": " + err.message);
    }
  }

  // putChunk is one file.put as a promise. The timeout is per chunk rather than
  // per transfer, and it exists because a dropped WebSocket resolves no
  // callback at all: without it a broken link would leave the upload hanging
  // with no toast either way.
  function putChunk(pane, path, bytes, offset, more) {
    return new Promise((resolve, reject) => {
      let done = false;
      const timer = setTimeout(() => {
        if (done) return;
        done = true;
        reject(new Error("timed out"));
      }, UPLOAD_TIMEOUT_MS);
      sendCmdAwait("file.put", { pane, path, offset, more, data: b64encodeBytes(bytes) }, (msg) => {
        if (done) return;
        done = true;
        clearTimeout(timer);
        if (msg.ok) resolve(msg.data);
        else reject(new Error(msg.error || "refused"));
      });
    });
  }

  // b64encodeBytes renders a byte array as base64, which is how Go decodes a
  // []byte field. Chunked through String.fromCharCode because spreading a
  // megabyte of bytes into one call overflows the argument stack.
  function b64encodeBytes(bytes) {
    let s = "";
    const step = 0x8000;
    for (let i = 0; i < bytes.length; i += step) {
      s += String.fromCharCode.apply(null, bytes.subarray(i, i + step));
    }
    return btoa(s);
  }

  function fmtBytes(n) {
    if (n < 1024) return n + " B";
    if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
    return (n / (1024 * 1024)).toFixed(1) + " MB";
  }

  function attachMouse(p) {
    p.canvas.addEventListener("mousedown", (ev) => {
      if (copyModePane) exitCopyMode(); // a mouse drag supersedes keyboard copy-mode
      if (p.info && !p.info.focused) sendCmd("pane.focus", { pane: p.id });
      // The scrollbar strip owns left-drags whenever it is drawn.
      if (ev.button === 0 && hasScrollbar(p)) {
        // The strip is drawn in grid space (drawScrollbar), so compare there.
        if (userX(p, ev.clientX) >= p.W * cellW - SB_W - 2) {
          ev.preventDefault();
          beginScrollDrag(p, ev);
          return;
        }
      }
      if (!p.modes.mouse) {
        // No capture: left-drag originates a browser-local selection (§7 read);
        // Alt makes it a rectangular block. A plain click (no drag) is resolved on
        // mouseup — hyperlink follow, per α.
        if (ev.button !== 0) return;
        ev.preventDefault();
        const [x, y] = cellOf(p, ev);
        p.sel = { anchor: { x, y }, cursor: { x, y }, rect: ev.altKey, moved: false };
        scheduleDraw(p);
        return;
      }
      if (ev.button > 2) return;
      ev.preventDefault();
      p.pressed = ev.button;
      sendMouse(p, "d", ev.button, ev);
    });
    p.canvas.addEventListener("mousemove", (ev) => {
      // A copied drag keeps its wash on screen (p.sel.done) until a keystroke
      // clears it — but it is finished, so the pointer must not go on extending
      // it. Requiring the button still be held also covers a mouseup we missed.
      if (p.sel && !p.sel.done && (ev.buttons & 1)) {
        const [x, y] = cellOf(p, ev);
        if (x === p.sel.cursor.x && y === p.sel.cursor.y) return; // one update per cell
        p.sel.cursor = { x, y };
        if (x !== p.sel.anchor.x || y !== p.sel.anchor.y) p.sel.moved = true;
        scheduleDraw(p);
        return;
      }
      if (!p.modes.mouse) return;
      const key = cellOf(p, ev).join(":");
      if (key === p.lastCell) return; // one report per cell
      p.lastCell = key;
      sendMouse(p, "m", p.pressed >= 0 ? p.pressed : 3, ev);
    });
    p.canvas.addEventListener("wheel", (ev) => {
      ev.preventDefault();
      const lines = ev.deltaMode === 1 ? Math.round(ev.deltaY)
        : Math.sign(ev.deltaY) * Math.max(1, Math.round(Math.abs(ev.deltaY) / (cellH * (p.gs || 1))));
      if (!lines) return;
      if (p.modes.mouse || p.modes.alt) sendMouse(p, "w", 3, ev, 0, lines);
      else {
        if (p.sel) { p.sel = null; scheduleDraw(p); } // scrolling invalidates the fixed-viewport wash
        sendCmd("scroll", { pane: p.id, delta: lines });
      }
    }, { passive: false });
    // Right-click: mouse-capturing apps get the encoded button; otherwise the
    // pane menu (with paste on top) replaces the browser's default menu.
    p.canvas.addEventListener("contextmenu", (ev) => {
      ev.preventDefault();
      if (!p.modes.mouse) openCtx(ev.clientX, ev.clientY, paneMenuItems(p.id, true));
    });
  }

  // Release is watched on the window, not the canvas: a drag that ends outside
  // the pane still has to finish its selection, and a mouse-capturing app still
  // has to see the button go up.
  //
  // One listener for all panes, iterating the live map. Per-pane registration
  // leaked: applyLayout tears every pane down on each tab switch (the layout
  // carries the active tab only) and an element removal does not unregister a
  // window listener, so dead panes kept firing here — and kept their cell grids
  // alive — for the life of the page.
  window.addEventListener("mouseup", (ev) => {
    for (const p of panes.values()) {
      // Without !done, a lingering copied wash would re-read and re-copy itself
      // on the next click made anywhere.
      if (p.sel && !p.sel.done) {
        const sel = p.sel;
        if (sel.moved) {
          finishSelection(p);            // a real drag → read + copy, keep the wash
        } else {
          p.sel = null;                  // a click → follow a hyperlink under it
          scheduleDraw(p);
          const c = p.cells[sel.anchor.y * p.W + sel.anchor.x];
          if (c && c.h && p.links[c.h - 1]) followLink(p, p.links[c.h - 1]);
        }
        continue;
      }
      if (p.pressed < 0 || ev.button !== p.pressed) continue;
      const btn = p.pressed;
      p.pressed = -1;
      if (p.modes.mouse) sendMouse(p, "u", btn, ev);
    }
  });

