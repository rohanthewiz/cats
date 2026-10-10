// The pane header's recent-prompts dropdown (06-chrome.js promptsMenuItems and
// promptRowLabel): how a pane.prompts cmd_result becomes context-menu rows.
//
// The rows are the whole feature from the browser's side — the server has
// already chosen which records are prompts — so these cases pin the shaping:
// a prompt previews on one row and keeps its full text for the tooltip and the
// copy, an age is the bare figure, and every way of having nothing to show
// (a failure, an agent with no readable history, an empty one) opens as a
// sentence rather than as an empty box.

import { loadFns, eq, ok, has, report } from "./testutil.mjs";

const copied = [];
const toasts = [];
const { promptsMenuItems, promptRowLabel } = loadFns({
  files: ["06-chrome.js", "13-agents.js"],
  names: ["promptsMenuItems", "promptRowLabel", "fmtAgeNum"],
  consts: ["PROMPT_ROW_CHARS"],
  env: {
    clipWrite: (t) => { copied.push(t); return Promise.resolve(); },
    toast: (t) => toasts.push(t),
  },
});

// One row per prompt, folded onto one line and cut with a marker.
eq(promptRowLabel("fix\n\n  the   build\n"), "fix the build", "whitespace folds to single spaces");
const long = "x".repeat(200);
eq(promptRowLabel(long).length, 72, "cut to the row budget");
ok(promptRowLabel(long).endsWith("…"), "a cut row is marked");
eq(promptRowLabel(""), "", "empty");

const at = new Date(Date.now() - 2 * 60 * 1000).toISOString();
const items = promptsMenuItems({
  ok: true,
  data: { pane: 3, agent: "claude", prompts: [
    { text: "commit and push", at },
    { text: "line one\nline two" },
  ] },
});
eq(items[0], { head: "last 2 prompts" }, "a heading counts the rows");
eq(items[1].label, "commit and push", "label");
eq(items[1].hint, "2m", "the age is the bare figure");
eq(items[2].hint, "", "no time, no age");
eq(items[2].label, "line one line two", "multi-line prompt previews on one row");
has(items[2].title, "line one\nline two", "the tooltip keeps the full text");

items[2].fn();
eq(copied, ["line one\nline two"], "a click copies the full prompt, newlines and all");

eq(promptsMenuItems({ ok: true, data: { prompts: [{ text: "only" }] } })[0], { head: "last prompt" }, "singular heading");

// Nothing to show is always a sentence.
eq(promptsMenuItems({ ok: true, data: { prompts: [], note: "cats cannot read codex's prompt history" } }),
  [{ note: "cats cannot read codex's prompt history" }], "the server's note");
eq(promptsMenuItems({ ok: true, data: { prompts: [] } }), [{ note: "no prompts yet" }], "empty without a note");
eq(promptsMenuItems({ ok: false, error: "unknown pane 9" }), [{ note: "could not read prompts: unknown pane 9" }], "a failure");
eq(promptsMenuItems(null), [{ note: "could not read prompts: no answer" }], "no answer");

report("prompts");
