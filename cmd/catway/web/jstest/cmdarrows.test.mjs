// ⌘↑ / ⌘↓ (and ⌘L) in onKey (20-keys.js): handed to a kitty-protocol pane
// as super chords, left to the browser over a legacy pane.
//
// The editor on the other side reads super+Up / super+Down as top / end of
// file, so the chord must arrive VERBATIM — mods carrying the super bit, and
// Shift with it for ⌘⇧↑ — not translated the way ⌘← / ⌘→ are to Home / End.
// A legacy pane has nothing to receive (the encoder emits no bytes for a super
// chord there), so it must not be preventDefaulted either. The real
// CMD_TO_PANE and cmdGoesToPane are lifted; only the pane's kitty mode is
// faked.

import { loadFns, eq, ok, report } from "./testutil.mjs";

function world(kitty) {
  const sent = [];
  const f = loadFns({
    files: ["20-keys.js"],
    names: ["onKey", "zoomChord", "cmdGoesToPane", "mods"],
    consts: ["CMD_TO_PANE"],
    stubs: ["zoomStep"],
    env: {
      chatEl: { contains: () => false },
      document: { activeElement: null },
      composedCodes: new Set(),
      uiOpen: () => false,
      copyModePane: null,
      focusedPaneKitty: () => kitty,
      clearStaleSelections: () => {},
      sendMsg: (m) => sent.push(m),
    },
  });
  return { f, sent };
}

function key(type, code, extra) {
  const e = Object.assign({ type, code, key: code, keyCode: 0, isComposing: false,
    repeat: false, metaKey: true, ctrlKey: false, altKey: false, shiftKey: false,
    prevented: false }, extra || {});
  e.preventDefault = () => { e.prevented = true; };
  return e;
}

// ---- kitty pane: both arrows go down as super, Shift riding along -----------

{
  const { f, sent } = world(true);
  for (const code of ["ArrowUp", "ArrowDown"]) {
    const down = key("keydown", code);
    f.onKey(down);
    ok(down.prevented, `⌘${code} over a kitty pane is taken from the browser`);
  }
  eq(sent.map((m) => m.code + ":" + m.mods).join(","), "ArrowUp:8,ArrowDown:8",
    "and each is sent as the arrow itself with the super bit");

  sent.length = 0;
  f.onKey(key("keydown", "ArrowUp", { shiftKey: true }));
  eq(sent.map((m) => m.mods), [9], "⌘⇧↑ keeps Shift, so the editor selects to the top");
}

// ---- legacy pane: the browser keeps the chord --------------------------------

{
  const { f, sent } = world(false);
  const down = key("keydown", "ArrowUp");
  f.onKey(down);
  ok(!down.prevented, "⌘↑ over a legacy pane is left to the browser");
  eq(sent.length, 0, "and nothing is sent for it");
}

// ---- ⌘L: handed to a kitty pane, kept by the browser over a legacy one ------
// Not an arrow, but it rides the same CMD_TO_PANE gate, and it is the one entry
// that takes something from the browser (focus the address bar), so both halves
// of the gate are pinned.

{
  const { f, sent } = world(true);
  const down = key("keydown", "KeyL", { key: "l" });
  f.onKey(down);
  ok(down.prevented, "⌘L over a kitty pane is taken from the browser");
  eq(sent.map((m) => m.code + ":" + m.mods), ["KeyL:8"], "and sent as super+l");
}
{
  const { f, sent } = world(false);
  const down = key("keydown", "KeyL", { key: "l" });
  f.onKey(down);
  ok(!down.prevented, "⌘L over a legacy pane stays the browser's");
  eq(sent.length, 0, "and nothing is sent for it");
}

// ---- ⌘← is still translated, not swept into the verbatim set ----------------

{
  const { f, sent } = world(true);
  f.onKey(key("keydown", "ArrowLeft"));
  eq(sent.map((m) => m.code + ":" + m.mods), ["Home:0"], "⌘← still arrives as a bare Home");
}

report("cmd arrows");
