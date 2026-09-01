// Test harness for the front-end parts under cmd/catway/web/js/.
//
// Why this exists at all. The browser code is ONE closure: assets.go stitches
// js/*.js together and wraps the result in `(() => { … })()`, so nothing is
// exported and nothing can be imported — see the long note at the top of
// assets.go for why that is deliberate. A unit test therefore cannot `import`
// a function; it has to lift the function's source out of the part file and
// evaluate it in a scope it controls.
//
// That is what `loadFns` does:
//
//   part file(s)  ──slice()──▶  just the named functions' source
//                                   │
//        env + stubs ─────────▶  new Function("__env", `const a = __env.a; … `)
//                                   │
//                              ◀────┴──  { name: fn, … }
//
// The functions come back closed over exactly the bindings the test provides,
// so a test can hand `startRunbookRun` a spy and see that the palette entry
// points at it, without a DOM, a bundler, or a server.
//
// Two sessions rebuilt this trick from scratch in a scratchpad before it was
// worth filing; a third would have been careless. Run it with `make jstest`.

import { readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const jsDir = join(dirname(fileURLToPath(import.meta.url)), "..", "js");

/** readPart returns one file from web/js/ by name ("41-runbooks.js"). */
export function readPart(name) {
  return readFileSync(join(jsDir, name), "utf8");
}

/** parts lists the shipped part files, in load order (the numeric prefixes). */
export function parts() {
  return readdirSync(jsDir).filter((f) => f.endsWith(".js")).sort();
}

// slice pulls one top-level `function NAME(…) { … }` out of a part file.
//
// Brace counting rather than a parser, with a scanner that skips the three
// places a brace can appear without nesting: line comments, block comments and
// string literals. That is enough for this code base — the part files use
// template literals nowhere outside comments, and the sliced functions carry no
// regex literals, whose `/` would otherwise be indistinguishable from division.
// If either ever changes, this throws a mismatched-brace error rather than
// silently returning half a function.
export function slice(src, name) {
  const at = src.search(new RegExp("^\\s*function " + name + "\\s*\\(", "m"));
  if (at < 0) throw new Error(`slice: no function ${name}(`);
  const open = src.indexOf("{", src.indexOf("(", at));
  let depth = 0, i = open;
  for (; i < src.length; i++) {
    const c = src[i], next = src[i + 1];
    if (c === "/" && next === "/") { i = src.indexOf("\n", i); if (i < 0) break; continue; }
    if (c === "/" && next === "*") { i = src.indexOf("*/", i) + 1; continue; }
    if (c === '"' || c === "'" || c === "`") { i = endOfString(src, i); continue; }
    if (c === "{") depth++;
    else if (c === "}" && --depth === 0) return src.slice(at, i + 1);
  }
  throw new Error(`slice: unbalanced braces in ${name}`);
}

// endOfString returns the index of the quote that closes the one at `i`,
// honouring backslash escapes (the outline's `text="make all\n"` lines are
// exactly this case).
function endOfString(src, i) {
  const q = src[i];
  for (let j = i + 1; j < src.length; j++) {
    if (src[j] === "\\") { j++; continue; }
    if (src[j] === q) return j;
  }
  throw new Error("unterminated string literal");
}

/**
 * loadFns evaluates the named functions from the named part files.
 *
 * @param {object}   o
 * @param {string[]} o.files  part file names, e.g. ["41-runbooks.js"]
 * @param {string[]} o.names  functions to lift; they may call each other
 * @param {object}   o.env    bindings the code closes over (values or spies)
 * @param {string[]} o.stubs  free names to bind to a no-op function — for the
 *                            references that are evaluated eagerly (`fn: foo`)
 *                            but never called by the test
 * @returns {object} the functions, keyed by name
 */
export function loadFns({ files, names, env = {}, stubs = [] }) {
  const src = files.map(readPart).join("\n");
  const picked = names.map((n) => slice(src, n)).join("\n\n");
  const bindings = { ...Object.fromEntries(stubs.map((s) => [s, () => {}])), ...env };
  const body = [
    '"use strict";',
    ...Object.keys(bindings).map((k) => `const ${k} = __env[${JSON.stringify(k)}];`),
    picked,
    `return { ${names.join(", ")} };`,
  ].join("\n");
  return new Function("__env", body)(bindings);
}

// ---- the world's smallest test runner ---------------------------------------
//
// Node's own `node:test` would do, but its output is a TAP stream and this is
// run by hand as often as by make; a failure should be one line naming the
// assertion, and the exit code should be all CI needs.

let passed = 0;
const failures = [];

export function ok(cond, what) {
  if (cond) passed++;
  else failures.push(what);
}

export function eq(got, want, what) {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g === w) passed++;
  else failures.push(`${what}\n    got:  ${g}\n    want: ${w}`);
}

/** has asserts a substring, which is most of what string-building code needs. */
export function has(hay, needle, what) {
  ok(String(hay).includes(needle), `${what}\n    ${JSON.stringify(hay)} does not contain ${JSON.stringify(needle)}`);
}

export function lacks(hay, needle, what) {
  ok(!String(hay).includes(needle), `${what}\n    ${JSON.stringify(hay)} unexpectedly contains ${JSON.stringify(needle)}`);
}

/** report prints the tally and exits non-zero if anything failed. */
export function report(suite) {
  if (failures.length) {
    console.error(`${suite}: ${failures.length} failed, ${passed} passed`);
    for (const f of failures) console.error("  ✗ " + f);
    process.exit(1);
  }
  console.log(`${suite}: ${passed} assertions passed`);
}
