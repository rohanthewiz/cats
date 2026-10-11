// The agent state marker (07-workspaces.js): how a rollup item's state and
// flags become the one display state every dot, badge and tab mark is drawn
// from.
//
//   state   seen   blank       → marker
//   idle    true   ""          → idle     green
//   idle    false  ""          → done     blue (finished off-screen)
//   idle    any    "cleared"   → blank    light brown (nothing asked yet)
//   working any    "new"       → working  (blank only qualifies a resting agent)
//
// Blank beats done on purpose: a conversation with nothing in it has finished
// nothing, so an unseen flag left over from before a /clear is stale. And blank
// is a resting state — it ranks with idle, and earns no tab mark.

import { loadFns, eq, ok, report } from "./testutil.mjs";

const fns = loadFns({
  files: ["07-workspaces.js"],
  names: ["stClass", "markerState", "attentionRank", "stateCountLabel", "tabMarker"],
  env: { document: { createElement: () => ({ className: "", textContent: "", title: "" }) } },
});

const it = (state, seen, blank) => ({ state, seen, blank });

eq(fns.markerState(it("idle", true, "")), "idle", "settled idle");
eq(fns.markerState(it("idle", false, "")), "done", "unseen completion");
eq(fns.markerState(it("idle", true, "cleared")), "blank", "cleared conversation");
eq(fns.markerState(it("idle", true, "new")), "blank", "fresh conversation");
eq(fns.markerState(it("idle", false, "cleared")), "blank", "blank beats a stale unseen flag");
eq(fns.markerState(it("working", true, "new")), "working", "blank never masks a working agent");
eq(fns.markerState(it("blocked", true, "new")), "blocked", "blank never masks a blocked agent");
eq(fns.markerState({ state: "idle", seen: true }), "idle", "an older server sends no blank");

eq(fns.stClass("blank"), "st-blank", "blank has its own class");
eq(fns.stClass("idle"), "st-idle", "idle keeps its class");

eq(fns.attentionRank("blank"), fns.attentionRank("idle"), "blank ranks with idle");
ok(fns.attentionRank("blank") < fns.attentionRank("working"), "blank ranks below working");

eq(fns.stateCountLabel("blank"), "blank (new or cleared)", "badge tooltip wording");
eq(fns.stateCountLabel("done"), "done (unseen)", "done wording unchanged");

eq(fns.tabMarker("blank"), null, "no tab mark for a blank agent");
eq(fns.tabMarker("working").className, "tmark st-working", "working still marks its tab");

report("state marker");
