// The help overlay's "sent to an editor pane, not the browser" row
// (32-help.js) against the chords cats actually hands down (20-keys.js).
//
// The row is the only place a user can learn that ⌘S over an editor pane is
// the editor's save, not the browser's, so a chord missing from it is a
// shortcut that silently changed meaning. It drifted twice — ⌘A and ⌘X were
// forwarded for weeks before the row listed them — because nothing tied the
// two together. This test does, BOTH ways:
//
//   CMD_TO_PANE entry  ──▶ must be in the row   (a forwarded chord is named)
//   row entry          ──▶ must be in the set   (a named chord is forwarded)
//
// with one exception on the second leg: ⌘C and ⌘Z, which are not in the set at
// all — onKey's tail lets them through to EVERY pane, kitty or not, so they
// are named in the row but never in CMD_TO_PANE.
//
// The set is lifted with its value (sliceConst), never re-declared here, so a
// new entry fails this test until the row names it. The row is read from the
// source rather than from a rendered DOM: openHelp builds the overlay with
// document.createElement, and a regex over one literal line is all this needs.

import { readPart, sliceConst, ok, eq, report } from "./testutil.mjs";

// The real set, evaluated from its source line.
const setSrc = sliceConst(readPart("20-keys.js"), "CMD_TO_PANE");
const CMD_TO_PANE = new Function(setSrc + "\nreturn CMD_TO_PANE;")();
ok(CMD_TO_PANE instanceof Set && CMD_TO_PANE.size > 0, "CMD_TO_PANE lifts as a non-empty Set");

// The row, from its one source line.
const help = readPart("32-help.js");
const m = /\["sent to an editor pane, not the browser",\s*(\[[^\]]*\])\]/.exec(help);
ok(!!m, "the help overlay still has its \"sent to an editor pane\" row on one line");
const row = m ? JSON.parse(m[1]) : [];

// glyph spells an e.code the way the row does: ⌘ plus the keycap. The
// footnote marker († = Chrome keeps it) is not part of the chord.
function glyph(code) {
  if (code.startsWith("Key")) return "⌘" + code.slice(3);
  const named = { Slash: "⌘/", ArrowUp: "⌘↑", ArrowDown: "⌘↓" };
  if (!(code in named)) throw new Error(`glyph: no keycap for ${code} — teach this test`);
  return named[code];
}
const named = row.map((k) => k.replace(/†$/, ""));

// Leg one: every forwarded chord is named.
for (const code of CMD_TO_PANE) {
  ok(named.includes(glyph(code)), `CMD_TO_PANE forwards ${code}, so the help row must list ${glyph(code)}`);
}

// Leg two: every named chord is forwarded (or is the ⌘C / ⌘Z fall-through).
const fallThrough = ["⌘C", "⌘Z"];
const forwarded = new Set([...CMD_TO_PANE].map(glyph));
for (const k of named) {
  ok(forwarded.has(k) || fallThrough.includes(k), `the help row lists ${k}, which nothing hands to a pane`);
}

// And no chord is listed twice.
eq(named.length, new Set(named).size, "the help row lists each chord once");

report("cmd help row");
