// The bundle compiles, and compiles as ONE scope.
//
// assets.go concatenates js/*.js in list order and head.go wraps the result in
// `(() => { … })()`. That is the only place the parts meet, so it is the only
// place two of them can collide: a `const` or `function` declared in two files
// is a SyntaxError the browser reports at load, with a blank page behind it,
// and nothing in the Go tests would have caught it — they assert the files are
// listed, not that the text they produce parses.
//
// `new Function` compiles the source without running it, which is exactly the
// half wanted here: no DOM, no WebSocket, no `document` — just the parser, and
// with it every duplicate-declaration and syntax error in the shipped page.

import { readPart, parts, ok, eq, report } from "./testutil.mjs";

const files = parts();
ok(files.length > 40, `the js/ directory still holds the front-end (found ${files.length} parts)`);

const bundle = files.map(readPart).join("");

let err = null;
try {
  // The wrapper head.go actually emits, so a stray `return` or a redeclaration
  // is judged in the scope it will really live in.
  new Function(`(() => {\n${bundle}})();`);
} catch (e) {
  err = e;
}
eq(err && String(err), null, "the concatenated bundle compiles");

// The functions this session added are declared once. A grep is not a parser,
// but a duplicate `function runbookLead` in a second part file would pass the
// compile above (function declarations may repeat in sloppy mode) while
// silently shadowing the one the tests exercise.
for (const name of ["runbookLead", "runbookOutlineText", "runbookOutline", "paletteCommands"]) {
  const n = bundle.split(new RegExp("\\bfunction " + name + "\\s*\\(")).length - 1;
  eq(n, 1, `${name} is declared exactly once across the bundle`);
}

report("bundle");
