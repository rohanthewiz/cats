// The pane dropdown under each workspace row (08-panelist.js).
//
// The sidebar used to give panes a section of their own; they now hang off
// the workspace row they belong to, behind a caret at the row's right edge.
// Three pieces of that are state rules a screenshot cannot pin down, so they
// are pinned here:
//
//   • the OPEN STATE — an explicit choice per workspace, falling back to "open
//     while it is the workspace on screen" for one nobody has touched. A set of
//     open ids would read the same in a still and lose the difference between
//     "shut on purpose" and "never touched", which is the whole default;
//   • ALT — one press setting every workspace, written out explicitly so that
//     an Alt-collapse is not undone by the current workspace's default;
//   • the BUCKETING — rows grouped by the workspace in their handle, in
//     inventory order, with a sleeping workspace's placeholder left out.
//
// And one interaction rule: the caret's press must stop at the caret. If it
// reached the row it would also arm the row's switch-or-reorder gesture, and a
// press meant to open a list would switch workspace as well.

import { loadFns, ok, eq, has, report } from "./testutil.mjs";

// ---- open state ----------------------------------------------------------------

function stateWorld(workspaces, chosen = {}) {
  const wsPanesOpen = Object.assign(Object.create(null), chosen);
  const calls = { saved: 0, rendered: 0 };
  const fns = loadFns({
    files: ["08-panelist.js"],
    names: ["wsPanesAreOpen", "togglePanes", "prunePanesOpen"],
    env: {
      wsPanesOpen,
      layoutMsg: { workspaces },
      saveWsPanesOpen: () => { calls.saved++; },
      renderWorkspaces: () => { calls.rendered++; },
    },
  });
  return { ...fns, wsPanesOpen, calls };
}

const W1 = { id: "w1", active: true }, W2 = { id: "w2", active: false }, W3 = { id: "w3", active: false };

// Untouched: the dropdown follows the focus.
{
  const f = stateWorld([W1, W2]);
  eq(f.wsPanesAreOpen(W1), true, "the workspace on screen starts open");
  eq(f.wsPanesAreOpen(W2), false, "every other workspace starts shut");
}

// An explicit choice wins over the default, in both directions.
{
  const f = stateWorld([W1, W2], { w1: false, w2: true });
  eq(f.wsPanesAreOpen(W1), false, "a current workspace shut on purpose stays shut");
  eq(f.wsPanesAreOpen(W2), true, "another workspace opened on purpose stays open");
}

// A plain toggle records one choice, saves, and redraws.
{
  const f = stateWorld([W1, W2]);
  f.togglePanes(W2, false);
  eq(f.wsPanesOpen.w2, true, "toggling a shut dropdown records it open");
  eq("w1" in f.wsPanesOpen, false, "...and leaves every other workspace at its default");
  eq(f.calls.saved, 1, "the choice is persisted");
  eq(f.calls.rendered, 1, "the list is redrawn at once");
  f.togglePanes(W1, false);
  eq(f.wsPanesOpen.w1, false, "toggling the current workspace's open default records it shut");
}

// Alt: every workspace takes the state the pressed one flips to — and it is
// written out for every id, or the current workspace would fall straight back
// to "open because it is current" after an Alt-collapse.
{
  const f = stateWorld([W1, W2, W3], { w2: true });
  f.togglePanes(W2, true); // w2 is open, so this is "shut them all"
  eq([f.wsPanesOpen.w1, f.wsPanesOpen.w2, f.wsPanesOpen.w3], [false, false, false],
    "Alt on an open dropdown shuts every workspace's, by explicit choice");
  eq(f.wsPanesAreOpen(W1), false, "...including the current one, whose default would have reopened it");
  f.togglePanes(W3, true);
  eq([f.wsPanesOpen.w1, f.wsPanesOpen.w2, f.wsPanesOpen.w3], [true, true, true],
    "Alt on a shut dropdown opens every workspace's");
}

// Pruning: a choice cannot outlive its workspace, and pruning nothing does not
// write storage on every render.
{
  const f = stateWorld([W1], { w1: true, w9: false });
  f.prunePanesOpen([W1]);
  eq(Object.keys(f.wsPanesOpen), ["w1"], "a closed workspace's choice is dropped");
  eq(f.calls.saved, 1, "...and the drop is persisted");
  f.prunePanesOpen([W1]);
  eq(f.calls.saved, 1, "a render that drops nothing does not write storage");
}

// ---- bucketing -----------------------------------------------------------------

function rowsWorld({ inv, layoutPanes = [], workspaces, agents = [], live = new Map() }) {
  return loadFns({
    files: ["07-workspaces.js", "08-panelist.js"],
    names: ["wsPaneRows", "markerState", "flagOf", "wsOf", "wsAsleep"],
    env: {
      layoutMsg: { panes: layoutPanes, workspaces },
      paneInv: inv,
      agentItems: agents,
      panes: live,
    },
  });
}

{
  const f = rowsWorld({
    workspaces: [{ id: "w1" }, { id: "w2" }, { id: "w3", asleep: true }],
    inv: [
      { pane: 1, handle: "w1:p1", title: "vim" },
      { pane: 2, handle: "w1:p2", title: "build", agent: "claude", agent_state: "working" },
      { pane: 3, handle: "w2:p1", name: "notes", title: "zsh" },
      { pane: 4, handle: "w3:p1", title: "zsh" }, // the sleeping workspace's placeholder
    ],
  });
  const by = f.wsPaneRows();
  eq([...by.keys()], ["w1", "w2"], "rows bucket by workspace, and a sleeping workspace gets none");
  eq(by.get("w1").map((r) => r.pane), [1, 2], "a bucket keeps inventory order");
  eq(by.get("w1")[1].agent, "claude", "an off-screen row takes its agent from the snapshot");
  eq(by.get("w2")[0].title, "notes", "a custom name wins over the terminal title");
  eq(by.get("w2")[0].visible, false, "a pane the layout does not carry is off screen");
}

// Before the first pane.list lands, the layout's own panes stand in, so the
// workspace on screen does not open onto an empty dropdown at page load.
{
  const f = rowsWorld({
    workspaces: [{ id: "w1", active: true }],
    inv: [],
    layoutPanes: [{ pane: 7, pub: "w1:p1", focused: true }],
  });
  const rows = f.wsPaneRows().get("w1");
  eq(rows && rows.map((r) => [r.pane, r.visible, r.focused]), [[7, true, true]],
    "the layout's panes stand in for the inventory until it arrives");
}

// ---- the row label --------------------------------------------------------------
//
// A dropdown row sits under its workspace's own name, so its label drops the
// workspace part of the handle; every other reference keeps it (paneRef).

{
  const f = loadFns({
    files: ["07-workspaces.js", "08-panelist.js"],
    names: ["paneLocalRef", "paneRef", "wsName"],
    env: { layoutMsg: { workspaces: [{ id: "w1", name: "a-workspace-with-a-long-name" }] } },
  });
  eq(f.paneLocalRef("w1:p3", 9), "p3", "a dropdown row is labelled by its pane alone");
  eq(f.paneRef("w1:p3", 9), "a-workspace-with-a-long-name:p3", "...while paneRef keeps the full handle everywhere else");
  eq(f.paneLocalRef("", 9), "#9", "no handle yet falls back to paneRef's #id stand-in");
  eq(f.paneLocalRef("w1", 9), "w1", "a handle with no pane part is shown as it is, as paneRef does");
}

// ---- the caret ------------------------------------------------------------------

function caretWorld() {
  const listeners = {};
  let pressed = null;
  const calls = { hid: 0, toggled: [] };
  const el = {
    className: "", textContent: "", title: "", attrs: {},
    setAttribute(k, v) { this.attrs[k] = v; },
    addEventListener(type, fn) { (listeners[type] = listeners[type] || []).push(fn); },
  };
  const fns = loadFns({
    files: ["08-panelist.js"],
    names: ["paneCaretEl", "nOf"],
    env: {
      document: { createElement: () => el },
      hideTip: () => { calls.hid++; },
      pressActivate: (_el, fn) => { pressed = fn; },
      togglePanes: (w, all) => { calls.toggled.push([w.id, all]); },
    },
  });
  return { ...fns, el, listeners, calls, press: (e) => pressed(e) };
}

{
  const f = caretWorld();
  const rows = [{ agent: "claude" }, { agent: "" }, { agent: "" }];
  const car = f.paneCaretEl({ id: "w1" }, rows, false);
  eq(car.textContent, "▶", "a shut dropdown shows the closed arrow");
  eq(car.attrs["aria-expanded"], "false", "...and says so to assistive tech");
  has(car.title, "show 3 panes", "the tooltip counts what the arrow would show");
  has(car.title, "1 running an agent", "...and how many of them run an agent");
  has(car.title, "Alt+click", "...and names the all-at-once gesture");

  // A left press stops at the caret: the row never arms its switch/reorder.
  let stopped = 0, prevented = 0;
  for (const fn of f.listeners.mousedown) {
    fn({ button: 0, stopPropagation: () => stopped++, preventDefault: () => prevented++ });
  }
  eq(stopped, 1, "a left press on the caret does not reach the row");
  eq(prevented, 1, "...and starts no text selection");
  eq(f.calls.hid, 1, "...and takes the hover card down, since the row's own hideTip never runs");

  // A right press is left alone, so the row's context menu still opens.
  stopped = 0;
  for (const fn of f.listeners.mousedown) fn({ button: 2, stopPropagation: () => stopped++, preventDefault() {} });
  eq(stopped, 0, "a right press still reaches the row's menu");

  // A double press must not reach the row's rename.
  let dblStopped = 0;
  for (const fn of f.listeners.dblclick) fn({ stopPropagation: () => dblStopped++ });
  eq(dblStopped, 1, "a double press on the caret does not rename the workspace");

  f.press({ altKey: false });
  f.press({ altKey: true });
  eq(f.calls.toggled, [["w1", false], ["w1", true]], "the release toggles one workspace, or all of them with Alt");
}

{
  const f = caretWorld();
  const car = f.paneCaretEl({ id: "w2" }, [{ agent: "" }], true);
  eq(car.textContent, "▼", "an open dropdown shows the open arrow");
  has(car.title, "hide 1 pane", "...and the tooltip offers to hide it, singular");
  ok(!car.title.includes("running an agent"), "no agent clause when none is running");
}

report("pane dropdown");
