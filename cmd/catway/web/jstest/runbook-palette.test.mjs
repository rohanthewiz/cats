// What the palette says about a runbook.
//
// The listing already carries an `outline` — one pre-rendered line per step,
// capped by the server — and until now only the two run dialogs and the preview
// notice read it. These assertions pin the third reader: the palette entry,
// which has one line to work with and spends it on the FIRST step, the total,
// and (on hover only) the whole list.
//
// Run with `make jstest`.

import { loadFns, eq, ok, has, lacks, report } from "./testutil.mjs";

// The outline vocabulary, lifted on its own: these four are pure string
// functions over a listing row, so they need no bindings at all.
const rbFns = loadFns({
  files: ["41-runbooks.js"],
  names: ["runbookLead", "runbookOutline", "runbookOutlineText", "stepCount"],
});
const { runbookLead, runbookOutlineText, stepCount } = rbFns;

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

// --- the palette entry -------------------------------------------------------

// paletteCommands is a closure over half the front-end. Only the bindings this
// test exercises carry values; the rest are no-ops, which is enough because the
// entries hold them lazily inside arrow functions.
const ran = [];
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
    runbookItems: [deploy, big, old, emptyOutline, brokenRb],
    startRunbookRun: (rb) => ran.push(rb.name),
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

report("runbook palette entries");
