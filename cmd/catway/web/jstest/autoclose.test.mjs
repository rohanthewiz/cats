// The tidy-exit countdown on a pane header (06-chrome.js).
//
//   pane 3 · build · ~/src · exited (0) — close in 7s ✕
//
// The countdown itself belongs to the server (reap.go): these cover the four
// things the BROWSER is responsible for — drawing the remaining seconds it was
// given, cancelling through pane.keep rather than locally, ticking only while
// something is counting, and letting a deadline that has passed go quiet
// instead of sitting on "close in 0s".

import { loadFns, ok, eq, has, report } from "./testutil.mjs";

// A DOM stub small enough to read: the countdown builds one span, one button,
// and hangs a click handler on the button. Nothing here needs layout, so an
// element is its tag, its text, its children, and its listeners.
function el(tag) {
  return {
    tag, textContent: "", className: "", title: "", children: [], on: {},
    appendChild(c) { this.children.push(c); return c; },
    addEventListener(ev, fn) { this.on[ev] = fn; },
    // text() is the rendered run, the way a reader sees it.
    text() { return this.textContent + this.children.map((c) => c.text()).join(""); },
  };
}

// world builds the bindings the lifted functions close over, plus the spies the
// assertions read back.
function world() {
  const sent = [], rendered = [], intervals = [];
  const panes = new Map();
  const fns = loadFns({
    files: ["06-chrome.js"],
    names: ["drawAutoclose", "keepPane", "tickAutoclose", "startAutoclose"],
    consts: ["autocloseTick"],
    env: {
      panes,
      document: { createElement: el },
      sendCmd: (name, params) => sent.push([name, params]),
      renderChrome: (p) => rendered.push(p.id),
      setInterval: (fn, ms) => { intervals.push({ fn, ms, live: true }); return intervals.length; },
      clearInterval: (h) => { if (intervals[h - 1]) intervals[h - 1].live = false; },
    },
  });
  return { ...fns, sent, rendered, intervals, panes };
}

// A pane with a countdown draws the remaining whole seconds and a ✕ that keeps
// it. Ceil rather than floor: 6.4s left is "7s", so the number never reaches 0
// while the pane is still there.
{
  const w = world();
  const p = { id: 3, autocloseAt: Date.now() + 6400 };
  w.panes.set(3, p);
  const row = el("div");
  w.drawAutoclose(p, (cls, text) => { const s = el("span"); s.className = cls; s.textContent = text; return row.appendChild(s); });

  has(row.text(), "close in 7s", "the countdown names the seconds left");
  const span = row.children[0];
  eq(span.className, "autoclose", "the run is classed for the header CSS");
  const btn = span.children[0];
  eq(btn.textContent, "✕", "the keep button is the last thing in the run");

  // The click cancels through the SERVER, so every window watching the pane
  // stops counting — not just this one.
  btn.on.click({ stopPropagation() {}, preventDefault() {} });
  eq(w.sent, [["pane.keep", { pane: 3 }]], "✕ sends pane.keep for the pane");
  eq(p.autocloseAt, 0, "the click clears the local deadline optimistically");
  eq(w.rendered, [3], "and redraws the header, so the run disappears at once");
}

// A pane with no countdown running — a non-zero exit, or one already kept —
// draws nothing at all, and a second ✕ (two windows racing the same click)
// sends nothing.
{
  const w = world();
  const p = { id: 4, autocloseAt: 0 };
  w.panes.set(4, p);
  const row = el("div");
  w.drawAutoclose(p, () => { throw new Error("drawAutoclose emitted a run for a pane that is not counting"); });
  eq(row.children.length, 0, "no countdown, no run");
  w.keepPane(4);
  eq(w.sent, [], "keeping a pane that is not counting sends nothing");
}

// startAutoclose is the wire's remaining-ms turned into a local deadline, and
// it is what a cancel from another window lands on: pane_exited arrives again
// with no countdown, which must stop this window counting.
{
  const w = world();
  const p = { id: 5, autocloseAt: 0 };
  w.panes.set(5, p);

  w.startAutoclose(p, 10000);
  ok(p.autocloseAt > Date.now() + 9000, "the remaining ms became a local deadline");
  eq(w.intervals.length, 1, "the first counting pane starts the one ticker");

  const q = { id: 6, autocloseAt: 0 };
  w.panes.set(6, q);
  w.startAutoclose(q, 10000);
  eq(w.intervals.length, 1, "a second counting pane rides the same ticker");

  w.startAutoclose(p, 0);
  eq(p.autocloseAt, 0, "an exit with no countdown stops the pane counting");
}

// The ticker redraws what is counting, expires what is due, and stops itself
// once nothing is left — an idle session runs no timers.
{
  const w = world();
  const counting = { id: 7, autocloseAt: 0 };
  const due = { id: 8, autocloseAt: 0 };
  w.panes.set(7, counting);
  w.panes.set(8, due);
  w.startAutoclose(counting, 10000);
  w.startAutoclose(due, 10000);
  due.autocloseAt = Date.now() - 1; // its moment has passed

  w.tickAutoclose();
  eq(due.autocloseAt, 0, "a deadline that has passed stops counting rather than sitting at 0s");
  eq(w.rendered, [7, 8], "both headers are redrawn");
  ok(w.intervals[0].live, "the ticker keeps running while a pane still counts");

  counting.autocloseAt = 0;
  w.tickAutoclose();
  ok(!w.intervals[0].live, "the ticker stops once nothing is counting");
}

report("autoclose");
