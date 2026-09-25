// How a pane's resolved model string is condensed for the sidebar and pane
// header (05-labels.js modelLabel).
//
// The string is built server-side (agentmodel.go) as " · "-joined segments —
// model, then optionally effort, then optionally context usage — and only the
// model and the context survive into the label. These cases pin the parts that
// are easy to get wrong: the context segment is found by shape wherever it sits,
// it replaces the [1M] marker rather than repeating it, and a string without it
// renders exactly as it did before the segment existed.

import { loadFns, eq, report } from "./testutil.mjs";

const { modelLabel } = loadFns({
  files: ["05-labels.js"],
  names: ["modelLabel", "modelVersion"],
  consts: ["MODEL_SIZES", "MODEL_VER", "MODEL_CTX"],
});

eq(modelLabel("claude-opus-5-5 · high · 44k/1M"), "opus 5.5 (44k/1M)", "effort then context");
eq(modelLabel("claude-haiku-4-5 · 800/200k"), "haiku 4.5 (800/200k)", "context with no effort");
eq(modelLabel("claude-sonnet-4-5-20250929[1m] · 1.2M/1M"), "sonnet 4.5 (1.2M/1M)", "context replaces [1M]");

// Unchanged without a context segment.
eq(modelLabel("claude-opus-5 · high"), "opus 5", "effort alone is dropped");
eq(modelLabel("claude-sonnet-4-5-20250929[1m]"), "sonnet 4.5 [1M]", "1M marker kept");
eq(modelLabel("gpt-5.4-mini · medium"), "gpt 5.4 mini", "copilot id");
eq(modelLabel(""), "", "empty");

report("modellabel");
