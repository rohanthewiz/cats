// What a sidebar RUNBOOK row does when you click it, and what its tooltip says
// it will do.
//
// The two have to agree. Until now they could not: a running row's click went
// to startRunbookRun, which refuses a second run of the same name before it is
// sent, so the whole outcome of the click was a toast — and the tooltip, being
// honest about it, offered the right-click instead. Both sides now ask
// runbookRowVerb, and these assertions pin the routing table it holds plus the
// tooltip line derived from it.
//
// No DOM: the row's click handler is three lines around runbookRowVerb, so the
// verb function is where the behaviour actually lives and it is a pure function
// over a listing row and a run. Run with `make jstest`.

import { loadFns, eq, ok, has, lacks, report } from "./testutil.mjs";

// The menu entries hold their callbacks lazily inside arrow functions, so the
// four verbs they point at can be spies — only the two this suite presses need
// to record anything.
const ran = [], previewed = [];
const {
  runbookRowVerb, runbookTitle, runbookMenuItems,
} = loadFns({
  files: ["41-runbooks.js"],
  names: ["runbookRowVerb", "runbookHasPreview", "runbookTitle", "stepCount",
    "runOrigin", "runbookError", "runbookMenuItems"],
  env: {
    startRunbookRun: (rb) => ran.push(rb.name),
    previewRunbook: (rb) => previewed.push(rb.name),
  },
  stubs: ["openRunbookFile", "clipWrite"],
});

// --- fixtures ----------------------------------------------------------------

const deploy = {
  name: "deploy",
  path: "/r/deploy.yaml",
  steps: 3,
  description: "bring up the dev stack",
  outline: [
    `built: pane.split direction="right" pane=1`,
    `pane.send_input pane=1 text="make all\\n"`,
    `pane.wait_for_output pane=1 pattern="done" timeout_ms=5000`,
  ],
};
// A listing from a server too old to send an outline: nothing to preview.
const old = { name: "legacy", path: "/r/legacy.yaml", steps: 4 };
const emptyOutline = { name: "blank", path: "/r/blank.yaml", steps: 2, outline: [] };
const broken = {
  name: "bad", path: "/r/bad.yaml", steps: 0,
  error: "/r/bad.yaml: step 2: unknown command",
};
// A run this window started, and one a trigger did — the tooltip tells them
// apart, the verb does not care.
const mine = { step: 2, steps: 3, source: "control", local: true };
const theirs = { step: 1, steps: 4, source: "trigger", trigger: "on_start" };

// --- runbookRowVerb ----------------------------------------------------------

eq(runbookRowVerb(deploy, false, null), "run",
  "an idle runbook's click is still the run gate");
eq(runbookRowVerb(broken, true, null), "open",
  "a broken file's click still opens it — the error is its whole content");
eq(runbookRowVerb(deploy, false, mine), "preview",
  "a running runbook's click is the preview, not a refusal");
eq(runbookRowVerb(deploy, false, theirs), "preview",
  "and it does not matter who started the run");

// The residual case: running, but the listing carries no outline, so there is
// nothing to preview and the click falls back to the gate's own refusal. A row
// showing a live run cannot simply vanish the way a palette entry does.
eq(runbookRowVerb(old, false, theirs), "run",
  "a running runbook with no outline has no preview to route to");
eq(runbookRowVerb(emptyOutline, false, theirs), "run",
  "an empty outline is the same case as none");

// Broken beats running: a file that never parsed cannot be mid-run in any way
// this row can act on, and the editor is still the only verb.
eq(runbookRowVerb(broken, true, theirs), "open",
  "broken wins over running — there is nothing to preview either way");

// --- the tooltip names the same verb -----------------------------------------

const idle = runbookTitle(deploy, false, null);
has(idle, "click to run · right-click to preview the steps",
  "an idle row still advertises both ways in");

const running = runbookTitle(deploy, false, theirs);
has(running, "click to preview the steps",
  "a running row's hint is the verb its click now takes");
lacks(running, "click to run",
  "and no longer the one that would only toast");
lacks(running, "right-click to preview",
  "the preview has moved to the click, so the hint moves with it");
has(running, "running step 1 of 4",
  "the position still leads — it says which step to go and look at");
has(running, "on_start", "and the origin still follows it");

// The outline-less running row promises no click at all: what is left is the
// menu, which still holds the file and the path.
const runningOld = runbookTitle(old, false, theirs);
eq(runningOld.split("\n").pop(), "right-click for more",
  "with nothing to preview, the row points at the menu and not at a toast");
lacks(runningOld, "click to preview",
  "it does not promise a preview the menu would not build");

// A broken row is unchanged by any of this.
has(runbookTitle(broken, true, null), "click to open it in the editor",
  "a broken row still says what its click does");
lacks(runbookTitle(broken, true, null), "click to preview",
  "and never offers a preview of a file that did not parse");

// --- the verbs are the only three --------------------------------------------
//
// The click handler switches on this string, with "run" as the default arm, so
// a fourth value invented later would silently start a runbook.
for (const [rb, brk, run] of [
  [deploy, false, null], [deploy, false, mine], [broken, true, null],
  [old, false, theirs], [emptyOutline, false, null],
]) {
  ok(["open", "preview", "run"].includes(runbookRowVerb(rb, brk, run)),
    `runbookRowVerb(${rb.name}) stays inside the three the handler knows`);
}

// --- the right-click menu ----------------------------------------------------
//
// One level down from the click and the same shape: a list of verbs, so a verb
// that is certain to be refused does not belong on it. openCtx has no disabled
// state to grey it with, and the palette's precedent is absence.

const labels = (rb, brk, run) => runbookMenuItems(rb, brk, run).map((i) => i.label);

eq(labels(deploy, false, null),
  ["run…", "preview steps", "open in editor", "copy path", "copy catctl command"],
  "an idle runbook's menu is unchanged");

eq(labels(deploy, false, theirs),
  ["preview steps", "open in editor", "copy path", "copy catctl command"],
  "a running one loses the run entry and leads with the live verb");

// Nothing to preview and nothing to run: the menu is still worth opening for
// the file and the path, which is what the running row's tooltip points at.
eq(labels(old, false, theirs),
  ["open in editor", "copy path", "copy catctl command"],
  "a running runbook with no outline keeps the verbs that still work");
eq(labels(old, false, null), ["run…", "open in editor", "copy path", "copy catctl command"],
  "and gets its run entry back the moment the run ends");

eq(labels(broken, true, null), ["open in editor", "copy path", "copy error"],
  "a broken file's menu is about the error, as it was");

// The entries point where they say. Pressing them is what tells a reordering
// apart from a relabelling.
const running_menu = runbookMenuItems(deploy, false, theirs);
running_menu.find((i) => i.label === "preview steps").fn();
eq(previewed, ["deploy"], "preview steps opens the preview for its own runbook");
eq(ran, [], "and a running runbook's menu cannot start a second run at all");

runbookMenuItems(deploy, false, null).find((i) => i.label === "run…").fn();
eq(ran, ["deploy"], "while an idle one still reaches the gate from the menu");

report("runbook-row");
