  // ---- Build badge ----
  // The short git hash of the commit this catway was built from, stamped at link
  // time and served as window.__catsBuild (see buildbadge/ + page.go). It sits
  // beside the brand because the common confusion is a stale install — an app
  // bundle still carrying an older catway — and the hash names the build at a
  // glance; hovering expands it to the commit's subject line and commit time.
  //
  // The badge itself — its text, the card's rows, the commit-time formatting —
  // is the shareable buildbadge package's (window.BuildBadge, injected by
  // renderPage beside the data), so cats and any other project carrying it
  // render the same thing. What stays here is the one cats-specific choice:
  // the card goes through armTip/showTip like the sidebar rows, one popup
  // element and one rule about when it opens — a badge that answered instantly
  // while the rows next to it waited would read as two different behaviours in
  // the same corner. Class "hash" keeps it on the #brand small.hash styling.
  (function initBuildBadge() {
    if (!window.BuildBadge) return; // no library means no data either — unknown build
    BuildBadge.mount(document.getElementById("brand"), window.__catsBuild, {
      tag: "small",
      className: "hash",
      show: (e, items) => armTip(e, (ev) => showTip(ev, items)),
      hide: hideTip, // also on mousedown: one popup, one set of rules
    });
  })();

  function renderTabbar(msg) {
    tabbarEl.innerHTML = "";
    const attention = tabAttention();
    for (const t of msg.tabs) {
      const el = document.createElement("div");
      el.className = "tab" + (t.active ? " active" : "");
      const label = document.createElement("span");
      label.className = "tname";
      label.textContent = t.name + (t.zoomed ? " ⤢" : "");
      el.appendChild(label);
      const mark = tabMarker(attention.get(t.num));
      if (mark) el.appendChild(mark);
      el.addEventListener("dblclick", () => renameTab(t));
      el.addEventListener("contextmenu", (e) => { e.preventDefault(); openCtx(e.clientX, e.clientY, tabMenuItems(t, msg.tabs.length)); });
      // Focus rides the drag helper's mouseup for the same reason the workspace
      // rows do — the tab bar is rebuilt on every agents rollup (its attention
      // markers derive from one), so a "click" listener loses the press that a
      // rebuild interrupts. The early return keeps the close button from both
      // dragging and focusing — the tab's focus now hangs off the same guard
      // its drag does, rather than off the button's stopPropagation.
      el.addEventListener("mousedown", (e) => {
        if (e.target.classList.contains("x")) return; // the close button is not a drag handle
        beginReorderDrag(e, {
          el, container: tabbarEl, itemSel: ".tab:not(.add)", horizontal: true,
          onDrop: (gap) => sendCmd("tab.move", { num: t.num, index: gap }),
          onClick: () => { if (!t.active) sendCmd("tab.focus", { num: t.num }); },
        });
      });
      if (msg.tabs.length > 1) {
        const x = document.createElement("span");
        x.className = "x"; x.textContent = "✕"; x.title = "close tab";
        // On the press like everything else in this bar; the mousedown above
        // already keeps a press here from arming the tab's drag, and the tab's
        // own activation now hangs off that same guard rather than off this
        // handler's propagation.
        pressActivate(x, () => sendCmd("tab.close", { num: t.num }));
        el.appendChild(x);
      }
      tabbarEl.appendChild(el);
    }
    const add = document.createElement("div");
    add.className = "tab add"; add.textContent = "+"; add.title = "new tab";
    pressActivate(add, () => sendCmd("tab.create", {})); // rebuilt with the tabs beside it
    tabbarEl.appendChild(add);
    // Overflow: keep the active tab in view when the bar scrolls horizontally.
    const act = tabbarEl.querySelector(".tab.active");
    if (act && act.scrollIntoView) act.scrollIntoView({ inline: "nearest", block: "nearest" });
  }
  // A vertical wheel over the tab bar scrolls it horizontally (overflow).
  tabbarEl.addEventListener("wheel", (e) => {
    if (!e.deltaY) return;
    e.preventDefault();
    tabbarEl.scrollLeft += e.deltaY;
  }, { passive: false });

