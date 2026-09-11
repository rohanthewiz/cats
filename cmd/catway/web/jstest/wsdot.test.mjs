// The workspace row's dot: one glyph carrying two facts (07-workspaces.js).
//
// The dot used to say one thing — ● here, ○ not here — and now says two, on
// two channels of the same mark:
//
//   shape    ● / ○      which workspace the keyboard is in
//   colour   class      whether this checkout is level with its remote
//
// That split is the whole design, and it is exactly the kind of thing that
// silently regresses: a future edit that colours the dot by rebuilding its
// textContent, or that drops the class on the focused row because the row's own
// accent colour "already covers it", loses one of the two channels without
// breaking anything a screenshot would catch. So both are pinned here
// independently — every combination of focus and sync state.
//
// gitSyncText is pinned alongside because it is the only place the sync state
// is turned into words, and it is read twice: by the dot's tooltip and by the
// hover card's Git row. One wording, two call sites.

import { loadFns, ok, eq, has, lacks, report } from "./testutil.mjs";

// A DOM stub the size of what gitDot actually touches: a span with a class, a
// title and text. Nothing here needs a real document.
function element() {
  return { className: "", textContent: "", title: "" };
}

function world(rows) {
  const wsGit = new Map(rows.map((r) => [r.ws, r]));
  return loadFns({
    files: ["07-workspaces.js"],
    names: ["gitDot", "gitSyncText"],
    env: {
      wsGit,
      document: { createElement: () => element() },
    },
  });
}

const NONE = world([]);

// ---- the shape channel: focus, unchanged --------------------------------------

{
  const f = world([]);
  eq(f.gitDot({ id: "w1", active: true }).textContent, "●", "the focused row keeps its filled dot");
  eq(f.gitDot({ id: "w1", active: false }).textContent, "○", "an unfocused row keeps its hollow dot");
}

// ---- the colour channel: sync state -------------------------------------------

{
  const f = world([
    { ws: "w1", sync: "synced", branch: "main", remote: "origin" },
    { ws: "w2", sync: "ahead", branch: "main", remote: "origin", ahead: 3 },
    { ws: "w3", sync: "behind", branch: "master", remote: "upstream" },
  ]);
  eq(f.gitDot({ id: "w1", active: false }).className, "ws-dot git-synced", "in sync takes the synced class");
  eq(f.gitDot({ id: "w2", active: false }).className, "ws-dot git-ahead", "unpushed commits take the ahead class");
  eq(f.gitDot({ id: "w3", active: false }).className, "ws-dot git-behind", "a remote ahead of us takes the behind class");
}

// The two channels are independent: the FOCUSED row must still be coloured.
// This is the regression the CSS specificity note exists for — the focused
// row's accent colour would otherwise win, and the focused workspace is the one
// the user is most likely to be looking at.
{
  const f = world([{ ws: "w1", sync: "behind", branch: "main", remote: "origin" }]);
  const dot = f.gitDot({ id: "w1", active: true });
  eq(dot.textContent, "●", "the focused row is still marked as focused");
  eq(dot.className, "ws-dot git-behind", "...and still carries its sync colour");
}

// A workspace with no entry in the rollup gets no colour class at all, and so
// inherits the row's colour exactly as it did before any of this existed. This
// is the majority state, not an error case: not a git checkout, no remote, on
// another host, or simply not swept yet.
{
  const dot = NONE.gitDot({ id: "w9", active: false });
  eq(dot.className, "ws-dot", "an unknown workspace takes no colour class");
  eq(dot.textContent, "○", "...and still says whether it is focused");
}

// An entry that somehow arrives with no sync value is treated as unknown rather
// than producing a "git-undefined" class that matches no rule.
{
  const f = world([{ ws: "w1", branch: "main" }]);
  eq(f.gitDot({ id: "w1", active: false }).className, "ws-dot", "a sync-less entry takes no colour class");
}

// ---- the tooltip --------------------------------------------------------------

// Both meanings are said, because a user who has just noticed the dot has no
// way to know which of the two changed.
{
  const f = world([{ ws: "w1", sync: "synced", branch: "main", remote: "origin" }]);
  const t = f.gitDot({ id: "w1", active: true }).title;
  has(t, "current workspace", "the tooltip says the focus half");
  has(t, "in sync", "the tooltip says the sync half");
}
{
  const t = NONE.gitDot({ id: "w9", active: false }).title;
  eq(t, "not the current workspace", "with nothing to say about git, the tooltip says only the focus half");
}

// ---- gitSyncText: the wording -------------------------------------------------

// The pair that was actually compared is named out loud. Neither half is safe
// to assume: a tree still on master, or one whose main pushes to a fork rather
// than to origin, would otherwise report a state the user cannot account for.
{
  const f = NONE;
  has(f.gitSyncText({ sync: "behind", branch: "master", remote: "upstream" }), "master vs upstream",
    "behind names the branch and remote compared");
  has(f.gitSyncText({ sync: "behind", branch: "main", remote: "origin" }), "pull",
    "behind says what to do about it");
  has(f.gitSyncText({ sync: "ahead", branch: "main", remote: "origin", ahead: 1 }), "1 commit to push",
    "one unpushed commit is singular");
  has(f.gitSyncText({ sync: "ahead", branch: "main", remote: "origin", ahead: 4 }), "4 commits to push",
    "several unpushed commits are plural and counted");
  // Behind deliberately carries no number — the poll never fetches, so the
  // commits on the other side are not here to be counted.
  lacks(f.gitSyncText({ sync: "behind", branch: "main", remote: "origin" }), "commits to push",
    "behind does not claim a push");
  eq(f.gitSyncText(undefined), "", "no entry produces no text");
  eq(f.gitSyncText({}), "", "an entry with no sync produces no text");
  // An ahead entry with no count still says something useful rather than "0".
  has(f.gitSyncText({ sync: "ahead", branch: "main", remote: "origin" }), "commits to push",
    "an uncounted ahead still reads as work to push");
}

// The defaults keep a malformed entry readable rather than printing "undefined"
// into a tooltip.
{
  const s = NONE.gitSyncText({ sync: "synced" });
  has(s, "main vs origin", "a branch-less entry falls back to the usual pair");
  ok(!s.includes("undefined"), "no undefined leaks into the wording");
}

report("workspace sync dot");
