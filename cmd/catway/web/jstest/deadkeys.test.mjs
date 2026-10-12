// Dead keys and composed keys in onKey (20-keys.js, N-014).
//
// A dead key (⌥e on a US Mac layout) must reach the host's composition, not
// the pane: no preventDefault, nothing sent. A key pressed while a
// composition is running is the composition's too, and so is its keyup, even
// once the composition has ended. An ordinary key is still sent and still
// preventDefaulted, which is what keeps it from ALSO typing into the sink.

import { loadFns, eq, ok, report } from "./testutil.mjs";

function world() {
  const sent = [];
  const f = loadFns({
    files: ["20-keys.js"],
    names: ["onKey"],
    env: {
      chatEl: { contains: () => false },
      document: { activeElement: null },
      composedCodes: new Set(),
      uiOpen: () => false,
      copyModePane: null,
      cmdGoesToPane: () => false,
      clearStaleSelections: () => {},
      sendMsg: (m) => sent.push(m),
      mods: () => 0,
    },
  });
  return { f, sent };
}

function key(type, code, keyName, extra) {
  const e = Object.assign({ type, code, key: keyName, keyCode: 0, isComposing: false,
    repeat: false, metaKey: false, ctrlKey: false, altKey: false, shiftKey: false,
    prevented: false }, extra || {});
  e.preventDefault = () => { e.prevented = true; };
  return e;
}

// ---- ⌥e then e: the accent is composed, nothing reaches the pane --------------

{
  const { f, sent } = world();
  const dead = key("keydown", "KeyE", "Dead", { altKey: true });
  f.onKey(dead);
  ok(!dead.prevented, "a dead key's keydown is not preventDefaulted (the host composes it)");
  eq(sent.length, 0, "and nothing is sent for it");
  f.onKey(key("keyup", "KeyE", "Dead", { altKey: true }));
  eq(sent.length, 0, "nor for its keyup");

  const during = key("keydown", "KeyE", "é", { isComposing: true });
  f.onKey(during);
  ok(!during.prevented, "the key that completes the accent is left to the composition");
  // Chrome ends the composition on the keydown, so the keyup is no longer
  // composing; it must still be swallowed.
  f.onKey(key("keyup", "KeyE", "e"));
  eq(sent.length, 0, "its keyup, after the composition ended, is swallowed too");
}

// ---- an ordinary key still goes to the pane --------------------------------------

{
  const { f, sent } = world();
  const down = key("keydown", "KeyA", "a");
  f.onKey(down);
  ok(down.prevented, "an ordinary keydown is preventDefaulted");
  f.onKey(key("keyup", "KeyA", "a"));
  eq(sent.map((m) => m.kind + m.key).join(","), "da,ua", "and both its press and release are sent");
}

// ---- a swallowed keyup is swallowed once ------------------------------------------

{
  const { f, sent } = world();
  f.onKey(key("keydown", "KeyE", "Dead", { altKey: true }));
  f.onKey(key("keydown", "KeyE", "é", { isComposing: true }));
  f.onKey(key("keyup", "KeyE", "e"));
  f.onKey(key("keydown", "KeyE", "e"));
  f.onKey(key("keyup", "KeyE", "e"));
  eq(sent.map((m) => m.kind + m.key).join(","), "de,ue", "the next plain press of the same key is sent whole");
}

report("dead keys");
