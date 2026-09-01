// What the palette says about a runbook.
//
// The listing already carries an `outline` — one pre-rendered line per step,
// capped by the server — and until now only the two run dialogs and the preview
// notice read it. These assertions pin the third reader: the palette entry,
// which has one line to work with and spends it on the FIRST step, the total,
// and (on hover only) the whole list.
//
// And what the entry does when the run verb is already spoken for: a runbook
// the session is running cannot be run again, so the entry offers the preview
// instead of an Enter the server would refuse.
//
// Run with `make jstest`.

import { loadFns, eq, ok, has, lacks, report } from "./testutil.mjs";

// The outline vocabulary, lifted on its own: these four are pure string
// functions over a listing row, so they need no bindings at all.
const rbFns = loadFns({
  files: ["41-runbooks.js"],
  names: ["runbookLead", "runbookOutline", "runbookOutlineText", "stepCount",
    "runbookCount", "runbookHasPreview", "runbookRunNote"],
});
const { runbookLead, runbookOutlineText, runbookRunNote } = rbFns;

// --- fixtures ----------------------------------------------------------------
//
// Shaped like what cmd/catway/runbook.go's stepOutline actually sends: `id:
// command k=v`, params sorted, values quoted, capped at maxOutlineSteps.

const deploy = {
  name: "deploy",
  steps: 3,
  outline: [
    `built: pane.split direction="right" pane=1`,
    `pane.send_input pane=1 text="make all\\n"`,
    `pane.wait_for_output pane=1 pattern="done" timeout_ms=5000`,
  ],
};
// A document longer than the cap: 24 lines on the wire, 200 steps in the file.
const big = {
  name: "nightly",
  steps: 200,
  outline: Array.from({ length: 24 }, (_, i) => `step.${i + 1} pane=1`),
};
// A listing from a server too old to send the field.
const old = { name: "legacy", steps: 4 };
const emptyOutline = { name: "blank", steps: 2, outline: [] };
const brokenRb = { name: "bad", steps: 0, error: "/r/bad.yaml: step 2: unknown command" };
// Two runbooks the session is running RIGHT NOW — the case where the run verb
// is already spoken for. `release` has an outline and so has a preview to
// offer; `ancient` came from a server too old to send one and has nothing left.
const busy = {
  name: "release",
  steps: 5,
  outline: [
    `tag: pane.send_input pane=1 text="git tag\n"`,
    `pane.wait_for_output pane=1 pattern="tagged"`,
    `pane.send_input pane=1 text="make release\n"`,
    `pane.wait_for_output pane=1 pattern="ok" timeout_ms=60000`,
    `pane.send_input pane=1 text="git push --tags\n"`,
  ],
};
const oldBusy = { name: "ancient", steps: 4 };

// --- runbookLead -------------------------------------------------------------

eq(runbookLead(deploy), `built: pane.split direction="right" pane=1`,
  "the lead is the first outline line, id and all");
eq(runbookLead(old), "", "no outline yields no lead, not a placeholder");
eq(runbookLead(emptyOutline), "", "an empty outline is the same case as none");

// --- runbookOutlineText ------------------------------------------------------

eq(runbookOutlineText(deploy), deploy.outline.join("\n"),
  "the whole outline, one step per line");
lacks(runbookOutlineText(deploy), "more step",
  "nothing was left out of a 3-step document, so there is no tail");
has(runbookOutlineText(big), "\n…and 176 more steps",
  "a capped outline says how many steps it is not showing");
eq(runbookOutlineText(old), "", "no outline yields no text");
// The wording lives in runbookOutline and is read here, not restated — a second
// copy is how a dialog and a tooltip start disagreeing about the same number.
eq(runbookOutlineText({ name: "n", steps: 2, outline: ["a"] }), "a\n…and 1 more step",
  "the singular tail comes through unchanged");

// --- runbookRunNote ----------------------------------------------------------
//
// The palette's meta column while a run is in flight. Built on runbookCount, so
// these also pin the rule the row already depends on: the total beside a moving
// position is the RUN's, not the listing's.

eq(runbookRunNote(deploy, { step: 2, steps: 3 }), "running 2/3",
  "the position and the total it is counting towards");
eq(runbookRunNote(deploy, { step: 0 }), "running",
  "a run that has its slot but no step yet says so without inventing a position");
eq(runbookRunNote({ steps: 3 }, { step: 4, steps: 5 }), "running 4/5",
  "a file edited mid-run cannot produce 4/3 — the run's own total wins");

// --- the palette entry -------------------------------------------------------

// paletteCommands is a closure over half the front-end. Only the bindings this
// test exercises carry values; the rest are no-ops, which is enough because the
// entries hold them lazily inside arrow functions.
const ran = [], previewed = [];
// The runs in flight this fixture describes: `nightly` is mid-run and has an
// outline, `legacy` is mid-run on a server too old to send one.
const runs = new Map([
  ["release", { step: 2, steps: 5, source: "trigger", trigger: "on_start" }],
  ["ancient", { step: 1, steps: 4, source: "control" }],
]);
const { paletteCommands, fuzzyScore } = loadFns({
  files: ["31-palette.js"],
  names: ["paletteCommands", "fuzzyScore"],
  env: {
    ...rbFns,
    // No layout: the pane, tab and workspace sections drop out and what is left
    // is the command list this test is about.
    layoutMsg: null,
    focusedPaneId: () => null,
    activeTab: () => null,
    activeWorkspace: () => null,
    recState: { recording: false },
    sidebarHidden: () => false,
    runbookItems: [deploy, big, old, emptyOutline, brokenRb, busy, oldBusy],
    startRunbookRun: (rb) => ran.push(rb.name),
    // What is running is a per-name lookup the test drives from one map, so a
    // single paletteCommands() call can hold both branches at once.
    runbookRunOf: (name) => runs.get(name) || null,
    previewRunbook: (rb) => previewed.push(rb.name),
  },
  stubs: [
    "newWorkspace", "openNewWorktreeDialog", "openWorktreeOpenDialog", "openPluginsDialog",
    "startRecording", "openStopRecordingDialog", "confirmCancelRecording", "openSettings",
    "openHelp", "confirmStopServer", "setSidebarHidden", "sendCmd", "renamePane",
    "openFlagDialog", "paneFlagTarget", "enterCopyMode", "copyScrollback", "renameTab",
    "renameWorkspace", "toggleWorkspaceLock", "wsFlagTarget", "confirmCloseWorkspace",
  ],
});

const cmds = paletteCommands();
const entry = (name) => cmds.find((c) => c.label === "run runbook: " + name + "…");

ok(entry("deploy"), "the runbook still has an entry, labelled as before");
eq(entry("deploy").sub, `built: pane.split direction="right" pane=1`,
  "the visible line under the name is step one");
eq(entry("deploy").meta, "3 steps",
  "the total is what makes step one read as ONE of three");
eq(entry("deploy").title, deploy.outline.join("\n"),
  "the hover carries the outline entire");
lacks(entry("deploy").title, "deploy",
  "and not the name, which the label already holds");
has(entry("nightly").title, "…and 176 more steps",
  "a capped hover admits what it is not showing");

// The old-server fallback: the entry is exactly the one it was before the
// outline had a third reader. Asserted on all three fields, because a stray ""
// vs undefined would still render an empty span.
eq(entry("legacy").sub, "", "no outline, no sub");
eq(entry("legacy").meta, "", "no outline, no count either — a lone number would be new furniture");
eq(entry("legacy").title, "", "no outline, no hover");
eq(entry("blank").sub, "", "an empty outline takes the same fallback");

ok(!entry("bad"), "a broken runbook is still left out — the palette does not offer what the server will refuse");

entry("deploy").fn();
eq(ran, ["deploy"], "the entry still opens the run gate for its own runbook");

// --- a runbook that is already running ---------------------------------------
//
// The server's concurrency slot is per name, so the run verb is spoken for and
// startRunbookRun would refuse the Enter with a toast. The entry becomes the
// verb that still applies.
const prev = (name) => cmds.find((c) => c.label === "preview runbook: " + name + "…");

ok(!entry("release"), "a running runbook offers no run — that Enter was a refusal");
ok(prev("release"), "it offers a preview instead, and says so in the label");
eq(prev("release").meta, "running 2/5",
  "the meta trades the plain total for where the run has got to");
eq(prev("release").sub, `tag: pane.send_input pane=1 text="git tag\n"`,
  "the sub is still step one — it says WHICH runbook, not where the run is");
eq(prev("release").title, busy.outline.join("\n"),
  "the hover carries the whole outline, as it does on the run entry");
eq(prev("release").kind, "cmd", "still a command, not a new kind");

prev("release").fn();
eq(previewed, ["release"], "and Enter opens the preview for its own runbook");
eq(ran, ["deploy"], "without starting anything");

// No outline means no preview (runbookHasPreview is the one place that is
// decided) and a running runbook has no third verb, so the entry drops out for
// the duration — the same thing "start recording" does while the recorder is
// busy. The sidebar row still shows the run.
ok(!entry("ancient"), "no run entry for a running runbook…");
ok(!prev("ancient"), "…and no preview entry either, with nothing to preview");
ok(!cmds.some((c) => c.label.includes("ancient")),
  "it is out of the palette entirely rather than listed with a dead Enter");

// The other runbooks are untouched by either run.
ok(entry("deploy") && entry("nightly") && entry("legacy"),
  "a run in flight silences only its own entry");

// Plain commands keep the shape they had; the extra columns default rather than
// being spelled out on twenty-odd entries.
const settings = cmds.find((c) => c.label === "settings");
eq([settings.kind, settings.meta, settings.sub, settings.title], ["cmd", "", "", ""],
  "an ordinary command carries no columns");
eq(entry("deploy").kind, "cmd", "a runbook entry is still a command, not a new kind");

// --- searchable because visible ----------------------------------------------
//
// The row's haystack is label + meta + kind + sub, so a query that only appears
// in the step line still finds the runbook — and every match has something on
// screen to explain itself.
ok(fuzzyScore("split", entry("deploy").sub) > 0,
  "a word from step one matches the line the row shows");
ok(fuzzyScore("split", entry("deploy").label) < 0,
  "and would not have matched the label alone, which is the point");
// The same rule pays off on the meta: what a running entry shows is "running
// 2/5", so the word is a query, and typing it narrows the palette to whatever
// the session has in flight — a readout nothing else offers from the keyboard.
const hay = (it) => it.label + " " + it.meta + " " + it.kind + " " + (it.sub || "");
// A subsequence match is generous, and "running" is a poor query for this: it
// scores HIGHER against "run runbook: deploy…" than against the row that is
// actually running, because the label's word starts outweigh a mid-string hit.
// The position is the query that only an in-flight row can answer at all.
ok(fuzzyScore("2/5", hay(prev("release"))) > 0,
  "the run's position is in the text the row is scored on");
ok(fuzzyScore("2/5", hay(entry("deploy"))) < 0,
  "and nothing not running can match it");

report("runbook palette entries");
