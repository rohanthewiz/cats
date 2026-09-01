// What the preview notice says about how a runbook's steps are JUDGED.
//
// `expect:` and `continue_on_error:` are the two fields that change what a run
// MEANS while changing what no step DOES, and for five sessions no surface
// mentioned either: a runbook whose step 3 is allowed to fail previewed exactly
// like one where a failure aborts everything after it. They stay out of the
// outline LINES — a line clipped to 72 characters has room for what a step does
// or for how it is judged, not both — so the server sends their POSITIONS and
// the preview spends the space under the list on them.
//
// The gates do not get the notes, and that separation is asserted here too: a
// dialog asking "run this?" is answered by what the steps do, while this dialog
// exists only to be read.
//
// Run with `make jstest`.

import { loadFns, readPart, sliceConst, eq, ok, has, lacks, report } from "./testutil.mjs";

// The whole preview path, with the two things it reaches outside itself as
// spies: the run set it consults for the "running" bit, and the dialog it opens.
const notices = [];
let running = null;
const { runbookJudgement, stepNumbers, previewRunbook } = loadFns({
  files: ["41-runbooks.js"],
  names: ["runbookJudgement", "stepNumbers", "runbookOutline", "stepCount",
    "previewRunbook"],
  // The budget is lifted with the functions rather than re-declared here, so
  // the cap assertions below test the number the browser really uses.
  consts: ["maxNoteSteps"],
  env: {
    runbookRunOf: () => running,
    dialogNotice: (opts) => notices.push(opts),
  },
});

// The same value again, for the assertions that have to DO arithmetic with it.
// Read from the source for the same reason it is lifted above: a 12 written out
// here would keep passing after the shipped one moved.
const maxNoteSteps = Number(/=\s*(\d+)\s*;/.exec(
  sliceConst(readPart("41-runbooks.js"), "maxNoteSteps"))[1]);

// --- fixtures ----------------------------------------------------------------
//
// Shaped like what cmd/catway/runbook.go sends: an `outline` of pre-rendered
// lines, plus 1-based positions for the two judgement fields.

const guarded = {
  name: "release",
  path: "/r/release.yaml",
  description: "tag and ship",
  steps: 4,
  outline: [
    `tag: pane.send_input pane=1 text="git tag\\n"`,
    `waited: pane.wait_for_output pane=1 pattern="tagged"`,
    `pane.send_input pane=1 text="make clean\\n"`,
    `again: pane.wait_for_output pane=1 pattern="ok"`,
  ],
  expect_steps: [2, 4],
  continue_on_error_steps: [3, 4],
};
// Neither field used: the two lists are absent from the JSON entirely.
const plain = {
  name: "deploy", path: "/r/deploy.yaml", steps: 2,
  outline: [`pane.split direction="right"`, `pane.last`],
};
// A server too old to send either field, and one too old to send an outline.
const oldFields = { ...plain, name: "midway" };
const noOutline = { name: "legacy", path: "/r/legacy.yaml", steps: 4, expect_steps: [1] };

// --- the notes themselves ----------------------------------------------------

eq(runbookJudgement(plain), [],
  "a runbook using neither field gets no notes at all");
eq(runbookJudgement(oldFields), [],
  "and neither does a listing from a server that sends neither field");

const notes = runbookJudgement(guarded);
eq(notes.length, 2, "a runbook using both fields gets one note each");

// Each note leads with the YAML key, so a reader who wants the check itself
// knows the word to look for once the file is open.
has(notes[0], "expect:", "the check note names the field as the file spells it");
has(notes[0], "steps 2, 4", "and says which steps carry it");
has(notes[1], "continue_on_error:", "the tolerance note names its field too");
has(notes[1], "steps 3, 4", "and says which steps carry it");

// Order: what is CHECKED before what is tolerated. Both are true of the same
// run, but "step 4 may fail" only means something once "step 4 is checked" has
// been read — the check is the thing that can produce the failure.
ok(notes[0].startsWith("expect:") && notes[1].startsWith("continue_on_error:"),
  "the check note comes before the tolerance note");

// A step carrying BOTH appears in both notes. They are two separate claims
// about the same step, not alternatives, and a reader who saw only one would
// draw the wrong conclusion about what a failure there does.
ok(notes[0].includes("4") && notes[1].includes("4"),
  "a step with both fields is named by both notes");

// The clause after the dash is for a reader meeting the field for the first
// time: the key alone is a grep target, not an explanation.
has(notes[0], "—", "the check note explains the key, not just names it");
has(notes[1], "does not stop the run",
  "the tolerance note says what tolerating a failure actually means");

// --- stepNumbers -------------------------------------------------------------

eq(stepNumbers([3]), "step 3", "one position is singular");
eq(stepNumbers([2, 5]), "steps 2, 5", "several are plural and comma-joined");

// The cap. Past a dozen the note has stopped saying WHICH steps and become a
// second copy of the list above it; the count carries the rest, the way the
// outline's own tail carries the steps it did not print.
const many = Array.from({ length: maxNoteSteps + 7 }, (_, i) => i + 1);
const capped = stepNumbers(many);
has(capped, "…and 7 more", "past the cap the note counts the rest instead");
eq(capped.split(",").length, maxNoteSteps + 1,
  `only ${maxNoteSteps} positions are printed, plus the tail`);
lacks(capped, String(maxNoteSteps + 7),
  "and the positions past the cap are not spelled out");

// The numbering is the server's 1-based one, which is what the dialog's <ol>
// renders and what a failed run reports. A note counting from zero would be
// readable and wrong in the one place a reader acts on it.
lacks(stepNumbers([1, 2]), "step 0", "positions are 1-based, never 0-based");

// --- the notice previewRunbook actually opens --------------------------------

notices.length = 0;
previewRunbook(guarded);
eq(notices.length, 1, "previewRunbook opens exactly one notice");
const n = notices[0];
eq(n.title, "release", "the header names the runbook");
eq(n.lines, guarded.outline, "the outline is the list, unchanged");

// The notes ride in linesNote, under the list — dialogLines takes a string or
// an array there, and drops the empty truncation tail this fixture has.
const under = [].concat(n.linesNote || []).filter(Boolean);
eq(under, notes, "the judgement notes sit under the list, in order");

// The LINES are untouched. This is the whole reason the positions are a
// separate field: marking the lines would crowd the one thing a line is for,
// and would silently apply only to the 24 the server had room to send.
for (const line of n.lines) {
  lacks(line, "continue_on_error", "an outline line is not marked up with judgement");
}

// A truncated outline keeps its tail AND gains the notes, in that order: "how
// much of this list am I seeing" is read before anything said about it.
notices.length = 0;
previewRunbook({ ...guarded, steps: 200 });
const tailed = [].concat(notices[0].linesNote).filter(Boolean);
has(tailed[0], "…and 196 more steps", "the truncation tail still comes first");
eq(tailed.length, 3, "and the two notes follow it");

// A runbook using neither field previews exactly as it did before this existed.
notices.length = 0;
previewRunbook(plain);
eq([].concat(notices[0].linesNote || []).filter(Boolean), [],
  "nothing is said about judgement when there is nothing to say");

// No outline, no list — and therefore no notes, however many positions arrived.
// A note naming step 1 with no numbered list to resolve it against is worse
// than silence, and dialogLines drops the whole block anyway.
notices.length = 0;
previewRunbook(noOutline);
eq(notices[0].lines, undefined,
  "a listing without an outline still opens the notice it always did");

// The running bit is unchanged by any of this: the preview is where a running
// row's click lands, and it still says where the run has got to.
notices.length = 0;
running = { step: 2, steps: 4, source: "control", local: true };
previewRunbook(guarded);
has(notices[0].message, "running step 2", "a running runbook still reports its position");
has(notices[0].message, "4 steps", "beside the total it always showed");
running = null;

report("runbook-preview");
