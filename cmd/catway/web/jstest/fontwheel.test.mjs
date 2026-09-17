// Ctrl+wheel font zoom (01-bootstrap.js: fontWheelStep).
//
// What is covered is the accumulator: one mouse notch is one step, a trackpad's
// trickle of small deltas adds up to a step only once it has travelled a step's
// worth, a reversal does not first have to pay back the other direction, and
// wheel UP is the direction that enlarges.

import { loadFns, eq, report } from "./testutil.mjs";

const { fontWheelStep } = loadFns({
  files: ["01-bootstrap.js"],
  names: ["fontWheelStep"],
  consts: ["FONT_WHEEL_PX"],
});

// One mouse notch (~100px) is exactly one step, and up enlarges.
{
  const p = {};
  eq(fontWheelStep(p, -100, 1), 1, "one notch up is one step larger");
  eq(fontWheelStep(p, 100, 1), -1, "one notch down is one step smaller");
}

// A trackpad's small deltas accumulate to a single step at the threshold; the
// sum then starts over, so the next step needs a full step's travel again.
{
  const p = {};
  eq(fontWheelStep(p, -15, 1), 0, "15px: not yet");
  eq(fontWheelStep(p, -15, 1), 0, "30px: not yet");
  eq(fontWheelStep(p, -15, 1), 1, "45px: one step");
  eq(fontWheelStep(p, -35, 1), 0, "35px after a step: not yet");
  eq(fontWheelStep(p, -10, 1), 1, "45px again: the next step");
}

// A big single delta (a fast notch, a flick) is still ONE step, never two.
{
  const p = {};
  eq(fontWheelStep(p, -400, 1), 1, "a 400px flick is one step, not ten");
  eq(fontWheelStep(p, -1, 1), 0, "and it banks nothing for the next touch");
}

// Reversing direction resets the sum: after 30px of up-travel, 40px down is a
// full step down at once, not 10px net.
{
  const p = {};
  fontWheelStep(p, -30, 1);
  eq(fontWheelStep(p, 40, 1), -1, "a reversal is not charged for the other direction's travel");
}

// Line-mode deltas are scaled by the cell height, so one line at 19px is under
// a step and three lines are over one.
{
  const p = {};
  eq(fontWheelStep(p, -1, 19), 0, "one line of 19px is under a step");
  eq(fontWheelStep(p, -2, 19), 1, "three lines of 19px make one step");
}

// A zero delta is a no-op and leaves the accumulator alone.
{
  const p = { zoomAcc: -30 };
  eq(fontWheelStep(p, 0, 1), 0, "zero delta steps nothing");
  eq(p.zoomAcc, -30, "zero delta keeps the banked travel");
}

report("fontwheel");
