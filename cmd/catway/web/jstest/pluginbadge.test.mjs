// The toolbar's plugins button: two marks, one tooltip (30-plugins.js).
//
//   ⧉ plugins [!] [2]
//             │    └ .n — updates available (a count)
//             └ .w — a default plugin could not be installed (plugin_notice)
//
// The warning mark is how a fresh-install user who never opens the plugins
// dialog learns that cats-todo is missing, so what is pinned here is that it
// comes and goes with the server's notice on its own, independently of the
// update count sharing the button, and that the tooltip names the plugin
// rather than leaving a bare "!" to be guessed at.

import { loadFns, eq, ok, has, lacks, report } from "./testutil.mjs";

// A stand-in for #pluginsbtn with its two server-rendered slots.
function button() {
  const slots = { ".n": { textContent: "" }, ".w": { textContent: "" } };
  const classes = new Set();
  return {
    title: "",
    slots,
    classes,
    querySelector: (sel) => slots[sel] || null,
    classList: { toggle: (c, on) => (on ? classes.add(c) : classes.delete(c)) },
  };
}

function world(updates) {
  const btn = button();
  const f = loadFns({
    files: ["30-plugins.js"],
    names: ["applyPluginNotice", "paintPluginBadge", "pluginUpdateCount", "pluginDisplayName"],
    env: {
      pluginsBtnEl: btn,
      pluginUpdates: new Map(Object.entries(updates || {})),
      pluginFailedDefaults: [],
    },
    lets: ["pluginFailedDefaults"],
  });
  return { f, btn };
}

// ---- no notice, no updates: the idle button -----------------------------------

{
  const { f, btn } = world();
  f.applyPluginNotice({ t: "plugin_notice", failed_defaults: [] });
  eq(btn.slots[".w"].textContent, "", "an empty notice leaves the warning slot empty");
  eq(btn.slots[".n"].textContent, "", "and the count slot too");
  ok(!btn.classes.has("has-failed"), "no has-failed class while nothing failed");
  eq(btn.title, "plugins — install, run, update", "the idle tooltip is the plain one");
}

// ---- a failed default: the mark, named in the tooltip -------------------------

{
  const { f, btn } = world();
  f.applyPluginNotice({ t: "plugin_notice", failed_defaults: ["rohanthewiz.cats-todo"] });
  eq(btn.slots[".w"].textContent, "!", "a failed default fills the warning slot");
  ok(btn.classes.has("has-failed"), "and marks the button");
  has(btn.title, "cats-todo could not be installed", "the tooltip names the plugin by its display name");
  lacks(btn.title, "rohanthewiz.", "not by its full id");
  eq(btn.slots[".n"].textContent, "", "the update count is untouched");

  // The server retracts it (dismissed, or installed since): the mark goes.
  f.applyPluginNotice({ t: "plugin_notice", failed_defaults: [] });
  eq(btn.slots[".w"].textContent, "", "an empty notice clears the mark");
  ok(!btn.classes.has("has-failed"), "and the class");
  eq(btn.title, "plugins — install, run, update", "and the tooltip goes back to idle");

  // A message with no list at all (omitted by a future encoder) is "none".
  f.applyPluginNotice({ t: "plugin_notice" });
  eq(btn.slots[".w"].textContent, "", "a notice with no list reads as nothing failed");
}

// ---- both at once: two marks, both said ---------------------------------------

{
  const { f, btn } = world({
    "rohanthewiz.cats-git": { id: "rohanthewiz.cats-git", status: "available" },
    "rohanthewiz.ced": { id: "rohanthewiz.ced", status: "up_to_date" },
  });
  f.applyPluginNotice({ t: "plugin_notice", failed_defaults: ["rohanthewiz.cats-todo", "acme.notes"] });
  eq(btn.slots[".w"].textContent, "!", "the warning mark");
  eq(btn.slots[".n"].textContent, "1", "beside the update count");
  ok(btn.classes.has("has-updates") && btn.classes.has("has-failed"), "both classes");
  has(btn.title, "cats-todo, notes could not be installed", "every failed default is named");
  has(btn.title, "1 update available", "and the update is still said");
}

report("pluginbadge");
