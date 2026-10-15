// The side columns zoom independently (01-bootstrap.js zoomTarget / zoomStep /
// setAsideZoom, 20-keys.js zoomChord).
//
// What is pinned: a zoom chord goes to the column under the pointer, else to a
// focused chat, else to the terminal font; a hover left on a column that has
// since folded or closed is not trusted; a column step is 10% and clamped to
// 50–250; a column zoom writes only its own CSS ratio, is remembered, and
// re-derives the pane grid (the columns are grid tracks); and the chord parser
// steps once per keydown and never on a keyup.

import { loadFns, eq, ok, report } from "./testutil.mjs";

function world(o = {}) {
  const props = {};
  const stored = {};
  const calls = { font: [], persisted: [], resized: 0, toasts: [] };
  const activeInChat = !!o.focusInChat;
  const asideZooms = {
    sidebar: { pct: o.sbPct || 100, key: "cats.sidebar_zoom", pref: "sidebar_zoom", cssVar: "--sb-zoom" },
    chat: { pct: o.chatPct || 100, key: "cats.chat_zoom", pref: "chat_zoom", cssVar: "--chat-zoom" },
  };
  const env = {
    FONT_PX: 14,
    zoomHover: o.hover || "",
    asideZooms,
    document: {
      activeElement: {},
      documentElement: { style: { setProperty: (k, v) => { props[k] = v; } } },
    },
    chatEl: { contains: () => activeInChat },
    localStorage: { setItem: (k, v) => { stored[k] = v; } },
    sidebarHidden: () => !!o.sbHidden,
    chatIsOpen: () => o.chatOpen !== false,
    setFontSize: (px) => calls.font.push(px),
    persistUIPref: (k, v) => calls.persisted.push([k, v]),
    sidebarResized: () => { calls.resized++; },
    toast: (t) => calls.toasts.push(t),
  };
  const f = loadFns({
    files: ["01-bootstrap.js", "20-keys.js"],
    names: ["zoomTarget", "zoomStep", "setAsideZoom", "applyAsideZoom", "zoomChord"],
    consts: ["FONT_DEFAULT", "ASIDE_ZOOM_DEFAULT"],
    env,
    lets: ["zoomHover"],
  });
  return { f, props, stored, calls, asideZooms };
}

// ---- who a chord zooms -------------------------------------------------------------

eq(world().f.zoomTarget(), "", "pointer on no column, focus in a pane: the terminal font");
eq(world({ hover: "sidebar" }).f.zoomTarget(), "sidebar", "pointer over the sidebar: the sidebar");
eq(world({ hover: "chat" }).f.zoomTarget(), "chat", "pointer over the chat: the chat");
eq(world({ focusInChat: true }).f.zoomTarget(), "chat", "typing in the chat with the pointer elsewhere: the chat");
eq(world({ hover: "sidebar", focusInChat: true }).f.zoomTarget(), "sidebar",
  "the pointer beats a lingering focus");
eq(world({ hover: "sidebar", sbHidden: true }).f.zoomTarget(), "",
  "a hover left on a folded sidebar is not trusted");
eq(world({ hover: "chat", chatOpen: false, focusInChat: true }).f.zoomTarget(), "",
  "nor one on a closed chat, nor its stale focus");

// ---- zoomStep routes, and a column step is 10% ------------------------------------

{
  const { f, calls } = world();
  f.zoomStep(1);
  eq(calls.font, [15], "no column under the pointer: ⌘+ is one font px, as before");
  f.zoomStep(0);
  eq(calls.font, [15, 14], "and ⌘0 is the default font");
}
{
  const { f, calls, asideZooms, props, stored } = world({ hover: "chat" });
  f.zoomStep(1);
  eq(asideZooms.chat.pct, 110, "⌘+ over the chat is +10%");
  eq(asideZooms.sidebar.pct, 100, "and leaves the sidebar alone");
  eq(calls.font, [], "and the terminal font too");
  eq(props["--chat-zoom"], "1.1", "only the chat's own ratio is written");
  ok(!("--sb-zoom" in props), "the sidebar's is not touched");
  eq(stored["cats.chat_zoom"], "110", "remembered in this browser");
  eq(calls.persisted, [["chat_zoom", 110]], "and written back to config.json");
  eq(calls.resized, 1, "the pane grid is re-derived: the chat column changed width");
}
{
  const { f, asideZooms, calls } = world({ hover: "sidebar", sbPct: 130 });
  f.zoomStep(-1);
  eq(asideZooms.sidebar.pct, 120, "⌘- over the sidebar is -10%");
  f.zoomStep(0);
  eq(asideZooms.sidebar.pct, 100, "⌘0 over it resets just the sidebar");
  eq(calls.persisted[1], ["sidebar_zoom", 100], "and a reset is saved as 100, so other windows follow");
}

// ---- clamps and no-ops ----------------------------------------------------------------

{
  const { f, asideZooms, calls } = world({ sbPct: 250 });
  f.setAsideZoom("sidebar", 300);
  eq(asideZooms.sidebar.pct, 250, "never past 250%");
  eq(calls.resized, 0, "and a step that changes nothing re-derives nothing");
  f.setAsideZoom("sidebar", 10);
  eq(asideZooms.sidebar.pct, 50, "never under 50%");
}

// ---- the chord parser --------------------------------------------------------------------

{
  const { f } = world();
  const k = (code, extra) => Object.assign({ type: "keydown", metaKey: true, ctrlKey: false, code }, extra);
  eq(f.zoomChord(k("Equal")), 1, "⌘= / ⌘+ is bigger");
  eq(f.zoomChord(k("NumpadAdd")), 1, "numpad + too");
  eq(f.zoomChord(k("Minus")), -1, "⌘- is smaller");
  eq(f.zoomChord(k("Digit0")), 0, "⌘0 is reset");
  eq(f.zoomChord(k("Equal", { type: "keyup" })), null, "a keyup is no step");
  eq(f.zoomChord(k("Equal", { ctrlKey: true })), null, "⌃⌘= is not a zoom");
  eq(f.zoomChord(k("KeyK")), null, "other ⌘ chords are not a zoom");
}

report("aside zoom");
