// Running a plugin beside a pane (30-plugins.js pluginSplitItems /
// pluginSplitMenu / pluginRunSplit) and the loader submenu that carries it in
// the pane menu (28-ctxmenu.js buildCtx with a function `sub`).
//
// What is pinned:
//   - the placement words map to the right pane.split directions ("to the
//     right" = h, "below" = v), and a launch sends the plugin's argv + env with
//     the target pane and NO cwd (the split inherits the split pane's own);
//   - one runner gets the placements as its rows, several get a heading per
//     placement, none gets a note pointing at the plugins dialog;
//   - a pane in a locked workspace answers with a note and never asks the
//     server for the roster;
//   - a loader submenu opens on "loading…" at once and swaps its rows in when
//     the answer lands — unless the pointer has moved on, in which case a late
//     answer must not resurrect it.

import { loadFns, eq, ok, report } from "./testutil.mjs";

// ---- Placement rows and the launch ----

const sent = [], toasts = [];
let modalClosed = 0;
let listReply = null; // what the stubbed plugin.list answers with
const locked = new Set(["w2"]);
const paneWs = new Map([[1, "w1"], [7, "w2"]]);

const todo = { id: "cats-todo", env: { CATS_PLUGIN_ID: "cats-todo" },
  actions: [{ id: "open", title: "open", argv: ["/p/todo/bin/cats-todo"] }] };
const git = { id: "cats-git", env: { CATS_PLUGIN_ID: "cats-git" },
  actions: [{ id: "ui", title: "ui", argv: ["/p/git/bin/cats-git"] }] };

const fns = loadFns({
  files: ["30-plugins.js", "27-dialogs.js"],
  names: ["pluginSplits", "pluginSplitItems", "pluginSplitMenu", "pluginRunSplit", "pluginRunners"],
  env: {
    closeModal: () => { modalClosed++; },
    toast: (m) => toasts.push(m),
    sendCmdAwait: (name, params, cb) => {
      sent.push({ name, params });
      if (name === "plugin.list") cb(listReply);
      else cb({ ok: true });
    },
    paneWsFor: (id) => paneWs.get(id) || "",
    wsLocked: (id) => locked.has(id),
  },
});
const { pluginSplitItems, pluginSplitMenu, pluginRunners } = fns;

// One runner: the placements are the rows.
const one = pluginSplitItems(pluginRunners([todo]), 1);
eq(one.map((r) => r.label), ["to the right", "below"], "one runner: placements as rows");

one[0].fn();
eq(sent.at(-1), { name: "pane.split", params: {
  direction: "h", command: ["/p/todo/bin/cats-todo"], env: { CATS_PLUGIN_ID: "cats-todo" }, pane: 1,
} }, "to the right = an h split of that pane with the plugin's argv and env, and no cwd");
eq(modalClosed, 1, "the launch closes the plugins dialog it may have come from");

one[1].fn();
eq(sent.at(-1).params.direction, "v", "below = a v split");

// No pane (the dialog with nothing focused): pane.split's own default.
pluginSplitItems(pluginRunners([todo]), null)[0].fn();
ok(!("pane" in sent.at(-1).params), "a null pane leaves the target to the server");

// Several runners: a heading per placement, every runner under each.
const many = pluginSplitItems(pluginRunners([todo, git]), 1);
eq(many.map((r) => r.head || r.label),
  ["to the right", "cats-todo", "cats-git", "below", "cats-todo", "cats-git"],
  "several runners: a heading per placement");
many[5].fn();
eq([sent.at(-1).params.direction, sent.at(-1).params.command[0]], ["v", "/p/git/bin/cats-git"],
  "the row under 'below' launches its own plugin below");

eq(pluginSplitItems([], 1).length, 1, "no runners: one row…");
ok(pluginSplitItems([], 1)[0].note, "…and it is a note, not a dead action");

// Broken plugins never reach the menu (pluginRunners drops them).
eq(pluginSplitItems(pluginRunners([{ id: "bad", broken: "no manifest", actions: [] }, todo]), 1)
  .map((r) => r.label), ["to the right", "below"], "a broken plugin is left out");

// ---- The pane menu's loader ----

let got = null;
sent.length = 0;
pluginSplitMenu(7, (items) => { got = items; });
ok(got.length === 1 && /locked/.test(got[0].note), "a pane in a locked workspace gets a note");
eq(sent.length, 0, "…and the roster is never fetched for it");

listReply = { ok: true, data: { plugins: [todo, git] } };
pluginSplitMenu(1, (items) => { got = items; });
eq(sent.at(-1).name, "plugin.list", "an open workspace fetches the roster");
eq(got[0], { head: "to the right" }, "and lays it out beside the pane");

listReply = { ok: false, error: "disk on fire" };
pluginSplitMenu(1, (items) => { got = items; });
ok(/disk on fire/.test(got[0].note), "a failed list says why");

// ---- buildCtx with a loader submenu ----
//
// Same fake DOM idea as ctxpath.test.mjs, plus isConnected: an element is
// connected once mounted on body and disconnected by remove(), which is what
// the late-answer guard reads.
function fakeEl(tag) {
  const n = {
    tag, children: [], className: "", textContent: "", title: "", style: {}, on: {},
    isConnected: false,
    appendChild(c) { n.children.push(c); return c; },
    addEventListener(ev, fn) { n.on[ev] = fn; },
    getBoundingClientRect: () => ({ width: 100, height: 100, right: 100, top: 0 }),
    remove() { n.isConnected = false; },
  };
  return n;
}
const body = fakeEl("body");
body.appendChild = (c) => { c.isConnected = true; body.children.push(c); return c; };
const { buildCtx } = loadFns({
  files: ["28-ctxmenu.js"],
  names: ["buildCtx"],
  env: {
    document: { createElement: fakeEl, createTextNode: (t) => ({ textContent: t }), body },
    window: { innerWidth: 1000, innerHeight: 1000 },
    closeCtx: () => {},
  },
});
const textOf = (menu) => menu.children.map((c) => c.textContent || (c.children[0] || {}).textContent);

let answer = null;
const menu = buildCtx(0, 0, [
  { label: "run plugin…", sub: (done) => { answer = done; } },
  { label: "close pane", fn: () => {} },
]);
menu.children[0].on.mouseenter();
eq(textOf(menu._sub), ["loading…"], "a loader submenu opens on a loading note at once");
const loading = menu._sub;
answer([{ label: "to the right", fn: () => {} }]);
ok(!loading.isConnected, "the answer removes the loading note…");
eq(textOf(menu._sub), ["to the right"], "…and puts the real rows in its place");

// The pointer moves to a sibling row before the answer lands: the sibling
// closed the submenu, so the late answer is dropped.
menu.children[0].on.mouseenter();
const late = answer;
menu.children[1].on.mouseenter();
eq(menu._sub, null, "hovering a sibling closes the pending submenu");
late([{ label: "to the right", fn: () => {} }]);
eq(menu._sub, null, "a late answer does not resurrect it");

// An empty answer still says something.
menu.children[0].on.mouseenter();
answer([]);
ok(menu._sub.children[0].className === "note", "an empty answer shows a note, not a bare box");

report("pluginsplit");
