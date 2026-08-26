  // ---- Split resize handles (WS8): draggable borders → pane.resize_border ----
  // Each border div covers a thin halo around the split line; dragging maps the
  // pointer back to the split's first-child ratio. The server rebroadcasts the
  // layout on every resize, recreating these divs — the drag survives because
  // its listeners live on window and the border's id/area are stable.
  function renderBorders(msg) {
    for (const el of panesEl.querySelectorAll(".split")) el.remove();
    const HIT = 5; // px grab halo each side of the line
    for (const b of (msg.borders || [])) {
      const el = document.createElement("div");
      el.className = "split " + (b.dir === 0 ? "h" : "v");
      const [ax, ay, aw, ah] = b.area;
      if (b.dir === 0) { // horizontal split: vertical divider at x = pos
        el.style.left = (b.pos * cellW - HIT) + "px";
        el.style.top = (ay * cellH) + "px";
        el.style.width = (2 * HIT) + "px";
        el.style.height = (ah * cellH) + "px";
      } else {           // vertical split: horizontal divider at y = pos
        el.style.top = (b.pos * cellH - HIT) + "px";
        el.style.left = (ax * cellW) + "px";
        el.style.height = (2 * HIT) + "px";
        el.style.width = (aw * cellW) + "px";
      }
      el.addEventListener("mousedown", (e) => beginBorderDrag(e, b, el));
      panesEl.appendChild(el);
    }
  }

  function beginBorderDrag(e, b, el) {
    if (e.button !== 0) return;
    e.preventDefault();
    el.classList.add("dragging");
    let sent = b.ratio;
    const move = (ev) => {
      const r = panesEl.getBoundingClientRect();
      let ratio;
      if (b.dir === 0) ratio = ((ev.clientX - r.left) / cellW - b.area[0]) / b.area[2];
      else ratio = ((ev.clientY - r.top) / cellH - b.area[1]) / b.area[3];
      ratio = Math.min(0.9, Math.max(0.1, ratio));
      if (Math.abs(ratio - sent) < 0.01) return; // ~one resize per cell of travel
      sent = ratio;
      sendCmd("pane.resize_border", { border: b.id, ratio });
    };
    const up = () => {
      window.removeEventListener("mousemove", move);
      window.removeEventListener("mouseup", up);
      for (const d of panesEl.querySelectorAll(".split.dragging")) d.classList.remove("dragging");
    };
    window.addEventListener("mousemove", move);
    window.addEventListener("mouseup", up);
  }

