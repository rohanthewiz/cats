// The terminal painter's incremental path (18-render.js).
//
// A pane_diff repaints only the rows it touched — see paint/drawBand. The
// invariant that makes that safe is simple to state and easy to break:
//
//   after any sequence of frames and diffs, the canvas must hold EXACTLY the
//   pixels a from-scratch full draw of the same state would produce.
//
// So this test runs the real painter (and the real pane_diff handler) against
// a fake 2D context that rasterizes into a pixel grid, applies a few hundred
// random diffs, and after each one compares the incrementally-maintained
// canvas with a fresh canvas that got one full draw().
//
// The raster records LAYERING, not just the top color: a translucent fill or a
// glyph composites onto what was underneath, so each pixel's value is the
// stack of paints that reached it. That is what makes the comparison strict
// enough to matter — the two ways a band repaint goes wrong in a real browser
// are a glyph drawn twice (its antialiased edges darken) and a translucent
// overlay stacked twice (the scrollbar track creeps toward white), and both
// show up here as a different stack, not as the same color.
//
// Glyph boxes overhang their cell by a pixel on every side, and background
// rects overshoot by half a cell pixel the way the real ones do, so the
// neighbour-row bookkeeping in drawBand is exercised rather than assumed.

import { loadFns, ok, eq, report } from "./testutil.mjs";

const CELL_W = 8, CELL_H = 16;

// ---- a fake canvas ----------------------------------------------------------

function fakeCanvas(w, h) {
  const canvas = { width: w, height: h };
  const px = new Array(w * h).fill("");
  const stats = { font: 0, fill: 0, text: 0 };
  let st = { fillStyle: "#000", strokeStyle: "#000", font: "", globalAlpha: 1,
    lineWidth: 1, textBaseline: "alphabetic", t: [1, 1, 0, 0], clip: null };
  const stack = [];
  let path = null;

  // Device-space rect from user space: scale + translate only, which is all
  // the painter ever installs.
  const dev = (x, y, w2, h2) => {
    const [a, d, e, f] = st.t;
    return [x * a + e, y * d + f, (x + w2) * a + e, (y + h2) * d + f];
  };
  // A pixel is covered when its centre is inside the rect — deterministic,
  // and identical for the same op whichever path issued it.
  function paint(r, tag) {
    let [x0, y0, x1, y1] = r;
    if (st.clip) {
      x0 = Math.max(x0, st.clip[0]); y0 = Math.max(y0, st.clip[1]);
      x1 = Math.min(x1, st.clip[2]); y1 = Math.min(y1, st.clip[3]);
    }
    const opaque = st.globalAlpha === 1 && !/^rgba/.test(tag);
    for (let y = Math.max(0, Math.ceil(y0 - 0.5)); y < Math.min(h, Math.ceil(y1 - 0.5)); y++) {
      for (let x = Math.max(0, Math.ceil(x0 - 0.5)); x < Math.min(w, Math.ceil(x1 - 0.5)); x++) {
        const i = y * w + x;
        const v = st.globalAlpha === 1 ? tag : `${tag}@${st.globalAlpha}`;
        px[i] = opaque && !tag.startsWith("glyph") ? v : `${px[i]}+${v}`;
      }
    }
  }

  const ctx = {
    get fillStyle() { return st.fillStyle; },
    set fillStyle(v) { stats.fill++; st.fillStyle = v; },
    get font() { return st.font; },
    set font(v) { stats.font++; st.font = v; },
    get strokeStyle() { return st.strokeStyle; }, set strokeStyle(v) { st.strokeStyle = v; },
    get globalAlpha() { return st.globalAlpha; }, set globalAlpha(v) { st.globalAlpha = v; },
    get lineWidth() { return st.lineWidth; }, set lineWidth(v) { st.lineWidth = v; },
    get textBaseline() { return st.textBaseline; }, set textBaseline(v) { st.textBaseline = v; },
    setTransform(a, _b, _c, d, e, f) { st.t = [a, d, e, f]; },
    save() { stack.push({ ...st, t: [...st.t] }); },
    restore() { if (stack.length) st = stack.pop(); },
    beginPath() { path = null; },
    rect(x, y, w2, h2) { path = dev(x, y, w2, h2); },
    clip() {
      if (!path) return;
      st.clip = st.clip ? [Math.max(st.clip[0], path[0]), Math.max(st.clip[1], path[1]),
        Math.min(st.clip[2], path[2]), Math.min(st.clip[3], path[3])] : path;
    },
    fillRect(x, y, w2, h2) { paint(dev(x, y, w2, h2), String(st.fillStyle)); },
    strokeRect(x, y, w2, h2) { paint(dev(x, y, w2, h2), "stroke:" + st.strokeStyle); },
    fillText(ch, x, y) {
      stats.text++;
      const wide = ch.codePointAt(0) > 0x2e80 ? 2 : 1;
      // One pixel of overhang on every side: italics lean, descenders drop.
      paint(dev(x - 1, y - 2, CELL_W * wide + 2, CELL_H + 2), `glyph:${ch}:${st.font}:${st.fillStyle}`);
    },
  };
  canvas.getContext = () => ctx;
  return { canvas, ctx, px, stats };
}

// ---- the painter, lifted ----------------------------------------------------

const frames = [];
const raf = (fn) => { frames.push(fn); };
const flush = () => { while (frames.length) frames.shift()(); };

const env = {
  dpr: 1, cellW: CELL_W, cellH: CELL_H, FONT_PX: 13,
  THEME_FG: 0xd6ddd6, THEME_BG: 0x1f2420,
  SEL_FILL: "rgba(88,204,140,0.30)", CM_CURSOR: "rgba(122,220,170,0.95)",
  SCROLL_THUMB: "rgba(122,220,170,0.6)", SCROLL_THUMB_IDLE: "rgba(255,255,255,0.25)",
  winFocused: true, requestAnimationFrame: raf,
  cellFontsPx: 0, cellFontsTab: null,
  panes: new Map(),
  // Single-line consts with trailing comments, which sliceConst cannot lift.
  SB_W: 6, PAD_X: 6, PAD_Y: 4,
};
let current = null; // the pane onMessage's pane() hands back
env.pane = () => current;

const R = loadFns({
  files: ["01-bootstrap.js", "02-color.js", "03-layout.js", "18-render.js", "19-messages.js"],
  names: ["scheduleDraw", "markRow", "requestPaint", "paint", "cursorRow", "beginFrame", "draw",
    "drawBand", "paintCells", "cellFonts", "drawOverlays", "hasScrollbar", "drawScrollbar",
    "drawCopyCursor", "drawSelection", "rgbOf", "css", "blend", "dimCss", "onMessage",
    "sameScroll", "setInset", "placePane"],
  consts: ["M_BOLD", "COLOR_CACHE_MAX", "cssCache", "MIN_INSET_SCALE"],
  env,
  lets: ["cellFontsPx", "cellFontsTab"],
});

// ---- a deterministic random terminal ----------------------------------------

let seed = 7;
// Math.imul keeps the LCG in 32-bit integer arithmetic; a plain multiply
// overflows a double's 53-bit mantissa and collapses into a short cycle.
const rnd = (n) => { seed = (Math.imul(seed, 1103515245) + 12345) & 0x7fffffff; return seed % n; };
const COLORS = [0, 0, 0, 0x02c04040, 0x0240c040, 0x024040c0, 0x02808080];
const GLYPHS = ["a", "b", "x", " ", " ", "─", "│", "界", "🍏", "_"];
function randCell() {
  const s = GLYPHS[rnd(GLYPHS.length)];
  const c = { s };
  const f = COLORS[rnd(COLORS.length)], b = COLORS[rnd(COLORS.length)];
  if (f) c.f = f;
  if (b) c.b = b;
  const m = [0, 0, 0, 0x1, 0x2, 0x4, 0x5, 0x8, 0x40, 0x80][rnd(10)];
  if (m) c.m = m;
  if (!rnd(15)) c.h = 1;
  return c;
}

function newTestPane(W, H) {
  const { canvas, ctx, px, stats } = fakeCanvas(W * CELL_W, H * CELL_H);
  const p = { id: "p1", canvas, ctx, px, stats, W, H, cells: [], links: [],
    cur: { x: 0, y: 0, vis: true }, info: { focused: true },
    defFg: 0x02d0d0d0, defBg: 0x02101010, scroll: { off: 0, max: 40, rows: H },
    modes: { alt: false }, sel: null, cm: null, dirty: false,
    full: true, rows: new Set(), curRow: -1, curCol: -1 };
  R.setInset(p, W * CELL_W, H * CELL_H);
  for (let i = 0; i < W * H; i++) p.cells.push(randCell());
  return p;
}

// reference draws the pane's CURRENT state from scratch on a fresh canvas.
function reference(p) {
  const { canvas, ctx, px } = fakeCanvas(p.canvas.width, p.canvas.height);
  const q = { ...p, canvas, ctx, rows: new Set() };
  R.draw(q);
  return px;
}

function firstDiff(a, b, w) {
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return `pixel (${i % w},${(i / w) | 0}): ${a[i]} vs ${b[i]}`;
  return null;
}

// ---- incremental == full, over a random walk --------------------------------

{
  const W = 24, H = 12;
  const p = newTestPane(W, H);
  current = p;
  R.scheduleDraw(p); flush();
  eq(firstDiff(p.px, reference(p), p.canvas.width), null, "the first full draw matches the reference");

  let partial = 0, mismatches = 0, firstBad = null;
  for (let step = 0; step < 300; step++) {
    const cells = [];
    const kind = rnd(10);
    // Mostly small diffs on a row or two (the case being optimized), some
    // wider ones that must take the full-draw fallback, some with no cells.
    const nrows = kind < 7 ? 1 + rnd(2) : kind < 9 ? 0 : H;
    const rowsHit = new Set();
    while (rowsHit.size < nrows) rowsHit.add(rnd(H));
    for (const y of [...rowsHit].sort((a, b) => a - b)) {
      const n = 1 + rnd(W);
      const xs = new Set();
      while (xs.size < n) xs.add(rnd(W));
      for (const x of [...xs].sort((a, b) => a - b)) cells.push({ i: y * W + x, ...randCell() });
    }
    const msg = { t: "pane_diff", pane: "p1", cells };
    if (rnd(3) === 0) msg.cur = { x: rnd(W), y: rnd(H), vis: rnd(5) !== 0 };
    // Keep scroll mostly steady so the band path runs; move it sometimes.
    msg.scroll = rnd(12) === 0 ? { off: rnd(5), max: 40, rows: H } : p.scroll;
    // A finished selection and copy-mode come and go through scheduleDraw,
    // exactly like the real mouse/keyboard paths.
    if (rnd(25) === 0) { p.sel = p.sel ? null : { anchor: { x: 2, y: 3 }, cursor: { x: 9, y: 6 }, done: true }; R.scheduleDraw(p); }
    if (rnd(40) === 0) { p.cm = p.cm ? null : { cursor: { x: rnd(W), y: rnd(H) } }; R.scheduleDraw(p); }

    const before = p.stats.text;
    R.onMessage(msg);
    const wasFull = p.full;
    flush();
    if (!wasFull && p.stats.text - before < W * H / 2) partial++;
    const d = firstDiff(p.px, reference(p), p.canvas.width);
    if (d) { mismatches++; if (!firstBad) firstBad = `step ${step}: ${d}`; }
  }
  eq(firstBad, null, "every incremental repaint matches a from-scratch full draw");
  eq(mismatches, 0, "no step diverged");
  ok(partial > 100, `most small diffs took the band path (${partial} of 300)`);
}

// ---- a small diff paints a small amount -------------------------------------

{
  const W = 80, H = 40;
  const p = newTestPane(W, H);
  current = p;
  R.scheduleDraw(p); flush();
  const full = p.stats.text;
  const before = p.stats.text;
  R.onMessage({ t: "pane_diff", pane: "p1", cells: [{ i: 5 * W + 3, s: "z" }], scroll: p.scroll });
  flush();
  const spent = p.stats.text - before;
  ok(full > 1000, `a full draw of a busy 80x40 pane is thousands of glyphs (${full})`);
  ok(spent <= 5 * W, `a one-cell diff repaints at most five rows of glyphs (${spent})`);
}

// ---- canvas state is written only when it changes ---------------------------

{
  const W = 40, H = 10;
  const p = newTestPane(W, H);
  for (let i = 0; i < W * H; i++) p.cells[i] = { s: "q" }; // one font, one color, no bg
  p.cur = null; p.scroll = null;
  current = p;
  const f0 = p.stats.font, c0 = p.stats.fill;
  R.scheduleDraw(p); flush();
  eq(p.stats.font - f0, 1, "a screen of uniform text sets the font once");
  ok(p.stats.fill - c0 <= 2, `…and the fill style at most twice (margin + text): ${p.stats.fill - c0}`);
}

// ---- the color caches hand back the same string ------------------------------

{
  ok(R.css(0x02123456) === R.css(0x02123456), "css() returns the identical string for the same color");
  eq(R.css(0x02123456), "rgb(18,52,86)", "…and it is still the right string");
  eq(R.dimCss(0x02ffffff, 0x02000000), R.blend(0x02ffffff, 0x02000000, 0.5), "dimCss is blend at 0.5");
}

// ---- a relayout that does not resize leaves the backing store alone ----------

{
  let writes = 0;
  const canvas = { style: {}, _w: 0, _h: 0,
    get width() { return this._w; }, set width(v) { writes++; this._w = v; },
    get height() { return this._h; }, set height(v) { writes++; this._h = v; } };
  const p = { el: { style: {} }, chrome: { style: {} }, canvas };
  R.placePane(p, [0, 0, 40, 20], [0, 1, 40, 19]);
  eq(writes, 2, "the first placement sizes the canvas");
  R.placePane(p, [0, 0, 40, 20], [0, 1, 40, 19]);
  eq(writes, 2, "placing it again at the same size does not reallocate it");
  R.placePane(p, [0, 0, 41, 20], [0, 1, 41, 19]);
  eq(writes, 3, "a width change resizes only the width");
}

report("render");
