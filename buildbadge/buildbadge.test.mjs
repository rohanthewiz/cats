// buildbadge.js, run under node against a hand-built DOM stub.
//
// Self-contained on purpose (no import of cats' jstest harness): this file
// travels with the package, so a project that copies buildbadge/ can run it
// with plain `node buildbadge/buildbadge.test.mjs`.
//
// The library is an IIFE that hangs itself on `window`, so the test runs its
// source in a vm context whose global stands in for one.

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import vm from "node:vm";

const src = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "buildbadge.js"), "utf8");

let fails = 0;
function eq(got, want, msg) {
  if (got !== want) { fails++; console.error(`FAIL ${msg}\n  got:  ${JSON.stringify(got)}\n  want: ${JSON.stringify(want)}`); }
}

// el is the least of a DOM element mount and the card touch: attributes, a
// class list, children, listeners that the test can fire.
function el(tag) {
  const attrs = new Map(), cls = new Set(), on = {};
  return {
    tag, children: [], textContent: "", style: {}, on,
    get className() { return [...cls].join(" "); },
    set className(v) { cls.clear(); for (const c of v.split(" ").filter(Boolean)) cls.add(c); },
    classList: { add: (c) => cls.add(c), remove: (c) => cls.delete(c), contains: (c) => cls.has(c) },
    setAttribute: (k, v) => attrs.set(k, v),
    hasAttribute: (k) => attrs.has(k),
    appendChild(c) { this.children.push(c); return c; },
    addEventListener: (ev, fn) => { on[ev] = fn; },
    getBoundingClientRect: () => ({ width: 200, height: 60 }),
  };
}

// load evaluates the library in a fresh window. placeholders are the elements
// querySelectorAll("[data-build-badge]") returns.
function load({ info, placeholders = [] } = {}) {
  const body = el("body");
  const win = {
    __buildInfo: info, innerWidth: 1000, innerHeight: 800,
    addEventListener() {},
    document: {
      readyState: "complete", body,
      createElement: el,
      querySelectorAll: () => placeholders,
      addEventListener() {},
    },
  };
  vm.runInNewContext(src, { window: win, globalThis: win, Date, Math, String, isNaN });
  return win;
}

const HOUR = 3600e3;
const info = { hash: "abc1234", subject: "fix the thing", when: "2026-10-15T14:41:07Z", dirty: true };

{ // Ages: one unit, rounded.
  const { BuildBadge: B } = load();
  eq(B.fmtAge(5e3), "5s ago", "seconds");
  eq(B.fmtAge(90e3), "2m ago", "minutes round");
  eq(B.fmtAge(3 * HOUR), "3h ago", "hours");
  eq(B.fmtAge(50 * HOUR), "2d ago", "days");
  eq(B.fmtAge(-1000), "0s ago", "a clock behind the commit clamps to zero");
}

{ // When: unknown and garbage drop to "", a real time carries its age.
  const { BuildBadge: B } = load();
  eq(B.fmtWhen(""), "", "empty when");
  eq(B.fmtWhen("last tuesday"), "", "unparseable when");
  const now = Date.parse(info.when) + 3 * HOUR;
  eq(B.fmtWhen(info.when, now).endsWith(" · 3h ago"), true, "when carries its age");
}

{ // Rows: the dirty tree is spelled out, a missing subject stays as an empty value.
  const { BuildBadge: B } = load();
  const r = B.rows({ hash: "abc1234", dirty: true });
  eq(r[0][1], "abc1234 · modified tree", "build row names a dirty tree");
  eq(r[1][1], "", "no subject → empty value, for the card to skip");
  eq(r[2][1], "", "no time → empty value");
}

{ // Auto-mount fills each placeholder once, from window.__buildInfo.
  const ph = el("small");
  const win = load({ info, placeholders: [ph] });
  eq(ph.children.length, 1, "placeholder got a badge");
  eq(ph.children[0].textContent, "abc1234*", "badge text is hash + dirty mark");
  eq(ph.children[0].className, "build-badge", "badge class");
  win.BuildBadge.autoMount();
  eq(ph.children.length, 1, "a second autoMount does not double up");
}

{ // No hash, no badge.
  const ph = el("small");
  load({ info: { hash: "" }, placeholders: [ph] });
  eq(ph.children.length, 0, "unknown build mounts nothing");
}

{ // Host hooks replace the built-in card and receive the precomputed rows.
  const { BuildBadge: B, document: doc } = load();
  const target = el("div");
  const seen = [];
  const badge = B.mount(target, info, { tag: "small", className: "hash", show: (e, items) => seen.push(items), hide: () => seen.push("hide") });
  eq(badge.className, "build-badge hash", "extra class rides beside build-badge");
  badge.on.mouseenter({ clientX: 1, clientY: 1 });
  badge.on.mousemove({ clientX: 2, clientY: 2 });
  eq(seen.length, 2, "show called on enter and move");
  eq(seen[0], seen[1], "the same rows object both times — computed once at mount");
  badge.on.mouseleave({});
  eq(seen[2], "hide", "hide on leave");
  eq(doc.body.children.length, 0, "a hooked badge never builds the built-in card");
}

{ // The built-in card: built on hover with only the known rows, hidden on leave.
  const { BuildBadge: B, document: doc } = load();
  const badge = B.mount(el("div"), { hash: "abc1234", subject: "s" });
  badge.on.mouseenter({ clientX: 10, clientY: 10 });
  const card = doc.body.children[0];
  eq(card.classList.contains("show"), true, "card shows on hover");
  eq(card.children.length, 4, "two rows (label + value each); the empty When is skipped");
  eq(card.style.left, "24px", "placed right of the pointer");
  badge.on.mouseleave({});
  eq(card.classList.contains("show"), false, "card hides on leave");
}

if (fails) { console.error(`buildbadge: ${fails} failure(s)`); process.exit(1); }
console.log("buildbadge: ok");
