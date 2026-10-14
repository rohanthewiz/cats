// The left sidebar zooms with the panes (01-bootstrap.js uiZoom, 38-sidebar.js
// setSidebarWidth / setSidebarBase / sidebarWidthCSS).
//
// What is pinned: a dragged width is stored as its BASE — the width at the
// default font — so the same drag at a bigger font banks a smaller number; the
// width CSS multiplies that base by --ui-zoom and carries the 150px / 60vw
// clamp; the drag's 60% cap is taken in screen px before the division; and the
// base never drops under the 150 that config.go accepts for ui.sidebar_width.

import { loadFns, eq, has, ok, report } from "./testutil.mjs";

function world(fontPx, innerWidth) {
  const props = {};
  const stored = {};
  const env = {
    FONT_PX: fontPx,
    sbwBase: 0,
    window: { innerWidth: innerWidth || 2000 },
    document: { documentElement: { style: { setProperty: (k, v) => { props[k] = v; } } } },
    localStorage: { setItem: (k, v) => { stored[k] = v; } },
  };
  const f = loadFns({
    files: ["01-bootstrap.js", "38-sidebar.js"],
    names: ["uiZoom", "sidebarWidthCSS", "setSidebarBase", "setSidebarWidth"],
    consts: ["FONT_DEFAULT", "SBW_KEY", "sbwMax"],
    env,
    lets: ["sbwBase"],
  });
  return { f, props, stored };
}

// ---- the zoom ratio --------------------------------------------------------------

eq(world(14).f.uiZoom(), 1, "the default font is zoom 1");
eq(world(28).f.uiZoom(), 2, "twice the font is zoom 2");

// ---- a drag stores the base, not the screen width ----------------------------------

{
  const { f, stored, props } = world(14);
  f.setSidebarWidth(300);
  eq(stored["cats.sidebar_w"], "300", "at the default font, base = screen width");
  has(props["--sidebar-w"], "300px * var(--ui-zoom)", "and the CSS multiplies it by the zoom");
}
{
  const { f, stored } = world(21);
  f.setSidebarWidth(300);
  eq(stored["cats.sidebar_w"], "200", "a 300px drag at 1.5× zoom banks 200 — what it is at 1×");
}

// ---- the clamps ------------------------------------------------------------------------

{
  const { f, props } = world(14);
  f.setSidebarBase(240);
  has(props["--sidebar-w"], "max(150px", "the floor rides in the CSS");
  has(props["--sidebar-w"], "60vw", "and so does the window cap, re-evaluated on every zoom");
}
{
  // 1000px window: the drag cap is 600 screen px, so at 2× the base is 300 —
  // never a base the window could not show at the zoom it was dragged at.
  const { f, stored } = world(28, 1000);
  f.setSidebarWidth(900);
  eq(stored["cats.sidebar_w"], "300", "the 60% cap is taken in screen px before the division");
}
{
  const { f, stored } = world(28);
  f.setSidebarWidth(150);
  eq(stored["cats.sidebar_w"], "150", "the base never goes under 150 (config.go's ui.sidebar_width floor)");
  ok(+stored["cats.sidebar_w"] >= 150, "so a drag at any zoom saves a width the file accepts");
}

report("sidebar zoom");
