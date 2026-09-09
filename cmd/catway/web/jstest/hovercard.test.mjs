// When the hover card opens, and when it goes (09-hovercard.js).
//
// The card's timing is two numbers and one rule about who is allowed to keep
// the second one alive:
//
//   • the DWELL — a row the pointer is merely passing over opens nothing;
//   • the WARM WINDOW — once a card has been read, the rows after it open on
//     contact, so walking a list does not mean holding still over every row;
//   • and the warm window belongs to the POINTER. Every other way of leaving —
//     the window blurring, a key, a menu — takes it down with the card, or the
//     dwell could be skipped simply by having read something a moment ago.
//
// These are lifted rather than driven in a browser because the interesting
// state is three module-level variables and a timer, none of which a screenshot
// can see: the difference between "opened at once" and "opened after 400ms" is
// invisible in a still, and it is the whole feature.

import { loadFns, ok, eq, report } from "./testutil.mjs";

// world builds the bindings the lifted functions close over: a clock the test
// advances by hand, a timer queue it fires by hand, and just enough of the card
// element for the "is a card up?" test the functions actually make of it.
function world() {
  let now = 1000;
  const timers = [];
  const cls = new Set();
  const shown = []; // the snapshots armTip let through to a show()

  const paneTipEl = {
    classList: {
      contains: (c) => cls.has(c),
      add: (c) => cls.add(c),
      remove: (c) => cls.delete(c),
    },
  };

  const fns = loadFns({
    files: ["09-hovercard.js"],
    names: ["armTip", "cancelTip", "hideTip", "dropTip", "restoreTitles", "titleNodes"],
    consts: ["TIP_DELAY_MS", "TIP_WARM_MS"],
    // The module-level state the lifted functions assign to. Declared with let
    // or every one of these is a TypeError instead of a behaviour.
    lets: ["tipTimer", "tipArmed", "tipWarmUntil", "mutedRow"],
    env: {
      paneTipEl,
      tipTimer: null, tipArmed: null, tipWarmUntil: 0, mutedRow: null,
      Date: { now: () => now },
      setTimeout: (fn, ms) => { timers.push({ fn, ms, live: true }); return timers.length; },
      clearTimeout: (h) => { if (timers[h - 1]) timers[h - 1].live = false; },
    },
  });

  // show is what a real call site passes: the thing that would build the card.
  // Here it records the snapshot and marks the element shown, which is exactly
  // what showTip does to the classList.
  const show = (ev) => { shown.push(ev); cls.add("show"); };

  // enter is one mouseenter/mousemove on a row.
  const enter = (row, x = 10, y = 20) =>
    fns.armTip({ clientX: x, clientY: y, currentTarget: row }, show);

  return {
    ...fns, shown, timers, enter, show,
    row: (n) => ({ id: n, isConnected: true }),
    pending: () => timers.filter((t) => t.live).length,
    fire: () => { const t = timers.filter((x) => x.live).pop(); t.live = false; t.fn(); },
    tick: (ms) => { now += ms; },
    up: () => cls.has("show"),
  };
}

// ---- the dwell ---------------------------------------------------------------

{
  const w = world();
  const a = w.row("a");
  w.enter(a);
  eq(w.shown.length, 0, "a cold arrival opened a card with no rest at all");
  eq(w.pending(), 1, "a cold arrival armed no dwell, so no card can ever appear");
  w.fire();
  eq(w.shown.length, 1, "no card after the dwell elapsed");
}

{
  // Drifting across the row keeps the same wait running and re-places the card,
  // or a hand that moves a cell or two while it reads never waits long enough
  // anywhere.
  const w = world();
  const a = w.row("a");
  w.enter(a, 10, 20);
  w.enter(a, 34, 20);
  eq(w.pending(), 1, "drifting across the row armed a second timer");
  w.fire();
  eq(w.shown[0].clientX, 34, "the card was placed where the pointer entered, not where it came to rest");
}

{
  // The lists are rebuilt under a stationary pointer on every rollup, and a
  // removed row dispatches no mouseleave to cancel with.
  const w = world();
  const a = w.row("a");
  w.enter(a);
  a.isConnected = false;
  w.fire();
  eq(w.shown.length, 0, "the dwell built a card from a row that had left the DOM");
}

// ---- the warm window ---------------------------------------------------------

{
  const w = world();
  const a = w.row("a"), b = w.row("b"), c = w.row("c");

  w.enter(a); w.fire();          // the first row pays the dwell
  ok(w.up(), "no card on the first row, so there is no warm window to test");

  w.hideTip();                   // mouseleave: the pointer moves on
  w.tick(100);
  w.enter(b);
  eq(w.shown.length, 2, "the next row's card did not open while the window was warm");
  eq(w.pending(), 0, "a warm arrival armed a dwell it was not supposed to wait out");

  w.hideTip();
  w.tick(100);
  w.enter(c);
  eq(w.shown.length, 3, "the window closed while the pointer was still walking the list");
}

{
  // And it does expire: a pointer that comes back later is a fresh arrival.
  const w = world();
  w.enter(w.row("a")); w.fire();
  w.hideTip();
  w.tick(2000); // well past TIP_WARM_MS
  w.enter(w.row("b"));
  eq(w.shown.length, 1, "a card opened on contact long after the window should have closed");
  eq(w.pending(), 1, "the cold arrival armed no dwell");
}

{
  // A row that earns no card (showWorkspaceTip's hideTip for a plain row) must
  // not refresh the window — the warmth is measured from the last card actually
  // read — but it must not close it either.
  const w = world();
  w.enter(w.row("a")); w.fire();
  w.hideTip();          // a real card going down: window opens
  w.tick(100);
  w.hideTip();          // nothing was up: no refresh, no close
  w.enter(w.row("b"));
  eq(w.shown.length, 2, "crossing a row with nothing to say closed the warm window");
}

// ---- the window belongs to the pointer ---------------------------------------

{
  // dropTip is the one teardown every non-pointer departure goes through — the
  // window blurring, the tab hiding, a key, a menu, a dialog — so testing it
  // once tests all of them; that they are wired to it at all is a listener
  // registration, which is the bundle's job to have and not this test's.
  const w = world();
  w.enter(w.row("a")); w.fire();
  ok(w.up(), "no card to lose");

  w.dropTip();
  ok(!w.up(), "the card outlived the hand leaving");

  w.tick(100);
  w.enter(w.row("b"));
  eq(w.shown.length, 1, "a card opened on contact after the hand had left");
  eq(w.pending(), 1, "the arrival after the hand left armed no dwell");
}

{
  // A pending dwell must never land after the hand has gone.
  const w = world();
  w.enter(w.row("a"));
  w.dropTip();
  eq(w.pending(), 0, "the departure left the dwell armed");
}

report("hovercard");
