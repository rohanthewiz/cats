// The ui_prefs push (33-settings.js applyUIPrefsPush): config.json's "ui"
// section reaching windows that are already open (N-033).
//
// What is pinned: a push applies only what differs from the page's copy, so the
// window that saved ignores its own echo; a size that went to 0 ("unset") is
// forgotten but left on screen; and show_untitled_panes can be turned back off.

import { loadFns, eq, ok, report } from "./testutil.mjs";

function world(file) {
  const applied = [];
  const uiPrefsFile = Object.assign({}, file || {});
  const f = loadFns({
    files: ["33-settings.js"],
    names: ["applyUIPrefsPush"],
    env: {
      uiPrefsFile,
      // The real applyUIPrefs merges into uiPrefsFile first; the stand-in does
      // the same so a second push can see the first one's effect.
      applyUIPrefs: (ui) => { applied.push(ui); Object.assign(uiPrefsFile, ui); },
    },
  });
  return { f, applied, uiPrefsFile };
}

// ---- the saving window's own echo changes nothing ------------------------------

{
  const { f, applied } = world({ font_px: 15, sidebar_width: 240 });
  f.applyUIPrefsPush({ t: "ui_prefs", font_px: 15, sidebar_width: 240, show_untitled_panes: false });
  eq(applied.length, 0, "a push equal to the page's copy applies nothing");
}

// ---- another window's change lands, and only the part that changed --------------

{
  const { f, applied } = world({ font_px: 15, sidebar_width: 240 });
  f.applyUIPrefsPush({ t: "ui_prefs", font_px: 18, sidebar_width: 240, show_untitled_panes: false });
  eq(applied.length, 1, "a changed font size is applied");
  eq(JSON.stringify(applied[0]), JSON.stringify({ font_px: 18 }), "and only the font size");
  f.applyUIPrefsPush({ t: "ui_prefs", font_px: 18, sidebar_width: 240, show_untitled_panes: false });
  eq(applied.length, 1, "the same push again is a no-op once applied");
}

// ---- a size the file no longer sets is forgotten, not forced ---------------------

{
  const { f, applied, uiPrefsFile } = world({ font_px: 15, sidebar_width: 240 });
  f.applyUIPrefsPush({ t: "ui_prefs", font_px: 15, show_untitled_panes: false });
  eq(applied.length, 0, "an unset width changes nothing on screen");
  ok(!("sidebar_width" in uiPrefsFile), "but the page stops treating 240 as the file's value");
}

// ---- show_untitled_panes travels both ways ----------------------------------------

{
  const { f, applied } = world({});
  f.applyUIPrefsPush({ t: "ui_prefs", show_untitled_panes: true });
  eq(JSON.stringify(applied[0]), JSON.stringify({ show_untitled_panes: true }), "turned on from another window");
  f.applyUIPrefsPush({ t: "ui_prefs", show_untitled_panes: false });
  eq(JSON.stringify(applied[1]), JSON.stringify({ show_untitled_panes: false }), "and back off");
}

// ---- the side columns' zooms travel like the sizes ---------------------------------

{
  const { f, applied } = world({ font_px: 15, sidebar_zoom: 120 });
  f.applyUIPrefsPush({ t: "ui_prefs", font_px: 15, sidebar_zoom: 120, chat_zoom: 140, show_untitled_panes: false });
  eq(JSON.stringify(applied[0]), JSON.stringify({ chat_zoom: 140 }),
    "a chat zoom set in another window lands, and the unchanged sidebar zoom does not re-apply");
}

report("ui prefs push");
