// The session-origin label (07-workspaces.js): how an agent's conversation
// began, kept on screen after its first prompt — the half of the blank word
// ("new" / "cleared") that stays true once the conversation has had a turn.
//
//   origin    → word      pane header / agents-row tooltip
//   startup   → started   "started 2h ago"
//   clear     → cleared   "cleared 5m ago"
//   resume    → resumed   "resumed 1d ago"
//   ""/other  → (none)

import { loadFns, eq, ok, report } from "./testutil.mjs";

const el = () => ({ className: "", textContent: "", title: "", dataset: {} });
const fns = loadFns({
  files: ["13-agents.js", "07-workspaces.js"],
  names: ["fmtAgeNum", "fmtAge", "originWord", "originPhrase", "paintOrigin", "fillOrigin"],
  env: { document: { createElement: el } },
});

eq(fns.originWord("startup"), "started", "a fresh launch reads as started");
eq(fns.originWord("clear"), "cleared", "a /clear reads as cleared");
eq(fns.originWord("resume"), "resumed", "a resume reads as resumed");
eq(fns.originWord(""), "", "an unknown origin draws nothing");
eq(fns.originWord("compact"), "", "a word the page does not know draws nothing");

const fiveMin = Date.now() - 5 * 60 * 1000;
eq(fns.originPhrase("cleared", fiveMin), "cleared 5m ago", "phrase with an age");
eq(fns.originPhrase("resumed", 0), "resumed", "bare verb when the instant is unknown");

// The header label.
{
  const h = fns.fillOrigin(el(), "cleared", fiveMin);
  eq(h.textContent, "cleared 5m ago", "header spells the whole phrase");
  ok(h.title.startsWith("conversation cleared 5m ago ("), "title carries the phrase and the clock time");
  eq(h.dataset.at, String(fiveMin), "the instant rides on the element for the tick");
}
{
  const h = fns.fillOrigin(el(), "resumed", 0);
  eq(h.textContent, "resumed", "no age when the instant is unknown");
  eq(h.dataset.at, undefined, "no instant stored, so the tick skips it");
  eq(h.title, "conversation resumed", "title without a time");
}

// The tick repaints from the element alone.
{
  const h = fns.fillOrigin(el(), "started", Date.now() - 2 * 3600 * 1000);
  h.dataset.at = String(Date.now() - 3 * 3600 * 1000);
  fns.paintOrigin(h);
  eq(h.textContent, "started 3h ago", "repaint reads the stored instant");
}

report("session origin");
