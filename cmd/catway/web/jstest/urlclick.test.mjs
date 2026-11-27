// ⌘-click on plain-text URLs (19-messages.js plainUrlAt / trimUrlTail /
// linkAt / linkModHeld).
//
// What is pinned here is the part a user can't see go wrong until they click
// and land somewhere else: that the URL found under the pointer is the whole
// URL (including the half on the next row when it wraps), and nothing more
// (no trailing period, no closing paren from the surrounding prose, no TUI
// box border). The mouse wiring and the underline are DOM and canvas; they
// are exercised in the app.

import { loadFns, eq, ok, report } from "./testutil.mjs";

const fns = loadFns({
  files: ["19-messages.js"],
  names: ["plainUrlAt", "trimUrlTail", "linkAt", "linkModHeld"],
  consts: ["URL_MAX_ROWS"],
});
const { plainUrlAt, trimUrlTail, linkAt, linkModHeld } = fns;

// pane builds the client-side shape of a pane's grid from text rows, padded
// to width W with blank cells. A row longer than W is an error in the test.
function pane(W, rows, extra = {}) {
  const cells = [];
  for (const r of rows) {
    if (r.length > W) throw new Error("row wider than pane: " + r);
    for (const ch of r.padEnd(W, " ")) cells.push({ s: ch });
  }
  return { W, H: rows.length, cells, links: [], ...extra };
}

// urlAt is plainUrlAt reduced to just the URL, for the common assertions.
const urlAt = (p, x, y) => (plainUrlAt(p, x, y) || {}).url || null;

// --- one row, the screenshot's case ---
{
  const p = pane(60, [
    "rosync is serving at http://127.0.0.1:7420",
    "Ctrl-C to stop.",
  ]);
  const col = "rosync is serving at ".length;
  eq(urlAt(p, col, 0), "http://127.0.0.1:7420", "click on the scheme finds the URL");
  eq(urlAt(p, col + 15, 0), "http://127.0.0.1:7420", "click mid-URL finds the URL");
  eq(urlAt(p, 3, 0), null, "click on the prose before it finds nothing");
  eq(urlAt(p, 50, 0), null, "click on the blank after it finds nothing");
  eq(urlAt(p, 2, 1), null, "a row with no URL finds nothing");
  const hit = plainUrlAt(p, col + 3, 0);
  eq([hit.a, hit.b], [col, col + "http://127.0.0.1:7420".length - 1], "span is the URL's cells");
}

// --- a URL soft-wrapped across rows: the token half must come along ---
{
  const W = 30;
  const url = "http://127.0.0.1:7420/?token=12db11a9d965ca2449b996";
  const line = "use:  " + url;
  const p = pane(W, [line.slice(0, W), line.slice(W), "next line"]);
  eq(urlAt(p, 10, 0), url, "click on the first row gets the wrapped tail");
  eq(urlAt(p, 3, 1), url, "click on the continuation row gets the head");
  const hit = plainUrlAt(p, 3, 1);
  eq([hit.a, hit.b], [6, W + line.length - W - 1], "span runs across the wrap");
  eq(urlAt(p, 2, 2), null, "the row after the wrap is not part of it");
}

// --- trailing prose punctuation and brackets ---
eq(trimUrlTail("http://x.io/a."), "http://x.io/a", "trailing period dropped");
eq(trimUrlTail("http://x.io/a),"), "http://x.io/a", "unbalanced paren + comma dropped");
eq(trimUrlTail("https://en.wikipedia.org/wiki/Go_(programming_language)"),
  "https://en.wikipedia.org/wiki/Go_(programming_language)", "balanced paren kept");
eq(trimUrlTail("http://x.io/?q=1'"), "http://x.io/?q=1", "trailing quote dropped");
{
  const p = pane(50, ["(docs at https://example.com/guide)."]);
  eq(urlAt(p, 12, 0), "https://example.com/guide", "parenthesised URL in a sentence");
  eq(urlAt(p, 34, 0), null, "the stripped ')' itself is not part of the link");
}

// --- delimiters: quotes, angle brackets, TUI box borders ---
{
  const p = pane(40, ['href="http://a.io/x" <http://b.io/y>', "│ https://c.io/z │"]);
  eq(urlAt(p, 8, 0), "http://a.io/x", "stops at a closing quote");
  eq(urlAt(p, 24, 0), "http://b.io/y", "stops at an angle bracket");
  eq(urlAt(p, 5, 1), "https://c.io/z", "stops at a box-drawing border");
}

// --- only real schemes count ---
{
  const p = pane(40, ["see Cargo.toml://nope and ftp://f.io/a"]);
  eq(urlAt(p, 12, 0), null, "an arbitrary word:// is not a link");
  eq(urlAt(p, 30, 0), "ftp://f.io/a", "ftp:// is");
  eq(urlAt(pane(20, ["http://"]), 2, 0), null, "a bare scheme with no body is not a link");
}

// --- two URLs on one row: the one under the pointer wins ---
{
  const p = pane(60, ["a http://one.io b http://two.io"]);
  eq(urlAt(p, 4, 0), "http://one.io", "first URL");
  eq(urlAt(p, 20, 0), "http://two.io", "second URL");
}

// --- a wide glyph's empty spacer cell does not shift the mapping ---
{
  const p = pane(40, []);
  p.H = 1;
  p.cells = [{ s: "🍏" }, { s: "" }, { s: " " }];
  for (const ch of "http://w.io".padEnd(37, " ")) p.cells.push({ s: ch });
  eq(urlAt(p, 3, 0), "http://w.io", "URL after an emoji is found at its own columns");
  eq(plainUrlAt(p, 3, 0).a, 3, "and its span starts at its own first cell");
}

// --- linkAt: an OSC 8 link on the cell wins over text scanning ---
{
  const p = pane(40, ["click here: http://plain.io"]);
  p.links = ["https://osc8.example/target"];
  p.cells[0].h = 1;
  eq(linkAt(p, 0, 0), { url: "https://osc8.example/target", a: 0, b: 0 }, "OSC 8 cell");
  eq(linkAt(p, 14, 0).url, "http://plain.io", "plain URL elsewhere on the row");
  eq(linkAt(p, 5, 0), null, "neither");
}

// --- the modifier: ⌘ on a Mac, Ctrl elsewhere ---
{
  const realNav = globalThis.navigator;
  const setPlatform = (platform) =>
    Object.defineProperty(globalThis, "navigator", { value: { platform }, configurable: true });
  setPlatform("MacIntel");
  ok(linkModHeld({ metaKey: true }), "Mac: ⌘ is the link modifier");
  ok(!linkModHeld({ ctrlKey: true }), "Mac: Ctrl is right-click, not the link modifier");
  setPlatform("Linux x86_64");
  ok(linkModHeld({ ctrlKey: true }), "elsewhere: Ctrl is the link modifier");
  ok(!linkModHeld({ metaKey: true }), "elsewhere: Meta is not");
  Object.defineProperty(globalThis, "navigator", { value: realNav, configurable: true });
}

report("urlclick");
