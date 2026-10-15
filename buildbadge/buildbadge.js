// buildbadge.js — the build badge and its hover card.
//
// Shows the short git hash of the build a page was served by (with "*" for a
// modified tree) as a quiet label; hovering it opens a card with the hash, the
// commit subject, and the commit time in the viewer's zone plus its age:
//
//   e251787*            ┌──────────────────────────────────────────┐
//      ▲ hover ───────▶ │ BUILD   e251787 · modified tree          │
//                       │ COMMIT  Build badge popup shows WHEN     │
//                       │ WHEN    2026-10-15 09:41 · 3h ago        │
//                       └──────────────────────────────────────────┘
//
// The data comes from the Go side (buildbadge.DataScript) as a plain object:
//   { hash, subject, when (RFC 3339 UTC), dirty }
//
// API, on window.BuildBadge:
//   mount(target, info?, opts?) → the badge element, or null when there is no
//       hash to show. info defaults to window.__buildInfo. opts:
//         tag        element to create (default "small")
//         className  extra class(es) beside "build-badge"
//         show(e, rows), hide(e)
//                    replace the built-in card — for a host that already has
//                    a hover-card system and wants one popup with one set of
//                    rules (rows are [label, value] pairs; empty values are
//                    the host's to skip)
//   rows(info, now?)  the card's [label, value] pairs
//   fmtWhen(iso, now?) "2026-10-15 09:41 · 3h ago", or "" if unknown/invalid
//   fmtAge(ms)         "3h ago"
//
// Every element marked data-build-badge gets a badge appended automatically
// once the DOM is parsed, so the drop-in case is markup only.
//
// Written as a plain IIFE (no modules, no build step) so it can be inlined in
// a <script>, concatenated into an existing bundle, or loaded from a file.
(function (root) {
  "use strict";
  if (root.BuildBadge) return; // already loaded — a second copy would double-mount

  // fmtAge renders a duration as its single most useful unit. Rounding rather
  // than flooring: "2h" for 1h40m reads closer to the truth than "1h".
  function fmtAge(ms) {
    const s = Math.max(0, Math.round(ms / 1000));
    let v;
    if (s < 60) v = s + "s";
    else {
      const m = Math.round(s / 60);
      if (m < 60) v = m + "m";
      else {
        const h = Math.round(m / 60);
        v = h < 24 ? h + "h" : Math.round(h / 24) + "d";
      }
    }
    return v + " ago";
  }

  // fmtWhen renders the commit time as the viewer's local date and time plus
  // its age. The age is the part that answers "is this install stale?"; the
  // absolute stamp is there to compare against a git log. An unknown or
  // unparseable time returns "" so the row drops instead of printing
  // "Invalid Date". `now` is injectable for tests.
  function fmtWhen(iso, now) {
    if (!iso) return "";
    const t = new Date(iso);
    if (isNaN(t)) return "";
    const p2 = (n) => String(n).padStart(2, "0");
    const local = t.getFullYear() + "-" + p2(t.getMonth() + 1) + "-" + p2(t.getDate()) +
      " " + p2(t.getHours()) + ":" + p2(t.getMinutes());
    return local + " · " + fmtAge((now === undefined ? Date.now() : now) - t.getTime());
  }

  // rows is the card's content. Empty values stay in the list (rather than
  // being filtered here) so a host card that keys on row position sees a
  // stable shape; both the built-in card and cats' showTip skip them.
  function rows(info, now) {
    return [
      ["Build", info.hash + (info.dirty ? " · modified tree" : "")],
      ["Commit", info.subject || ""],
      ["When", fmtWhen(info.when, now)],
    ];
  }

  // ---- The built-in card ----
  // One shared element, created on first use, for however many badges a page
  // mounts: only one can be hovered at a time, and a single node is cheaper
  // than one per badge. It is position:fixed and follows the pointer, clamped
  // into the viewport — the badge usually sits in a corner, so a card anchored
  // to the badge's own box would often open off-screen.
  let tipEl = null;
  function card() {
    if (!tipEl) {
      tipEl = root.document.createElement("div");
      tipEl.className = "bb-tip";
      root.document.body.appendChild(tipEl);
      // The card is a transient surface: anything that means the hand has left
      // — the window losing focus, a key, a scroll moving the badge out from
      // under the pointer — takes it down, or it lingers over whatever is next.
      const hide = () => defaultHide();
      root.addEventListener("blur", hide);
      root.addEventListener("keydown", hide, true);
      root.addEventListener("scroll", hide, true);
    }
    return tipEl;
  }

  // shownFor remembers which rows the card was last built from, so the
  // per-mousemove call only repositions instead of rebuilding the DOM and
  // forcing a layout read at pointer rate.
  let shownFor = null;
  function defaultShow(e, items) {
    const el = card();
    if (shownFor !== items || !el.classList.contains("show")) {
      el.textContent = "";
      for (const [k, v] of items) {
        if (!v) continue;
        // textContent, never innerHTML: the subject is arbitrary commit text.
        const kk = root.document.createElement("span"); kk.className = "k"; kk.textContent = k;
        const vv = root.document.createElement("span"); vv.className = "v"; vv.textContent = v;
        el.appendChild(kk); el.appendChild(vv);
      }
      el.classList.add("show");
      shownFor = items;
    }
    const r = el.getBoundingClientRect();
    let x = e.clientX + 14, y = e.clientY + 8;
    if (x + r.width > root.innerWidth - 4) x = e.clientX - r.width - 8; // flip left at the right edge
    if (y + r.height > root.innerHeight - 4) y = root.innerHeight - r.height - 4;
    el.style.left = Math.max(4, x) + "px";
    el.style.top = Math.max(4, y) + "px";
  }
  function defaultHide() {
    if (tipEl) tipEl.classList.remove("show");
    shownFor = null;
  }

  // mount appends a badge to target and wires its card. The rows are computed
  // once, here: the build cannot change under a running page, and the age only
  // needs to be as fresh as the page load — a reload refreshes it. Building
  // them per hover would also change a fresh build's "12s ago" on every move,
  // defeating both cards' rebuild-only-on-change keying.
  function mount(target, info, opts) {
    info = info || root.__buildInfo;
    opts = opts || {};
    if (!target || !info || !info.hash) return null; // no identity — no badge
    const el = root.document.createElement(opts.tag || "small");
    el.className = "build-badge" + (opts.className ? " " + opts.className : "");
    el.textContent = info.hash + (info.dirty ? "*" : "");
    const items = rows(info);
    const show = opts.show || defaultShow;
    const hide = opts.hide || defaultHide;
    const onMove = (e) => show(e, items);
    el.addEventListener("mouseenter", onMove);
    el.addEventListener("mousemove", onMove);
    el.addEventListener("mouseleave", hide);
    el.addEventListener("mousedown", hide); // a press means "act", not "read"
    target.appendChild(el);
    return el;
  }

  // autoMount fills every [data-build-badge] placeholder once. The marker
  // attribute makes it idempotent, so a host that also calls it by hand (say,
  // after rendering a template late) cannot get two badges in one spot.
  function autoMount() {
    const doc = root.document;
    if (!doc || !doc.querySelectorAll) return;
    for (const t of doc.querySelectorAll("[data-build-badge]")) {
      if (t.hasAttribute("data-build-badge-mounted")) continue;
      t.setAttribute("data-build-badge-mounted", "");
      mount(t);
    }
  }

  root.BuildBadge = { mount, rows, fmtWhen, fmtAge, autoMount };

  // Loaded from <head>, the placeholders do not exist yet — wait for the parse.
  // Loaded at the end of <body> (or later), they already do.
  if (root.document) {
    if (root.document.readyState === "loading") root.document.addEventListener("DOMContentLoaded", autoMount);
    else autoMount();
  }
})(typeof window !== "undefined" ? window : globalThis);
