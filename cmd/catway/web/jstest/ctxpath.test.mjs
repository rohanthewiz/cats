// The workspace path line at the top of every right-click menu (28-ctxmenu.js
// wsPathItems / paneWsFor): which workspace each menu names, and that a menu
// with no path to show opens on its first real row rather than on an empty one.
//
// The case worth pinning hardest is the pane menu reached from the sidebar for
// a pane in ANOTHER workspace: the path has to be that pane's workspace, not the
// one this window shows, or the line would name the wrong project at exactly
// the moment it is there to prevent a mistake.

import { loadFns, eq, report } from "./testutil.mjs";

const layoutMsg = {
  workspaces: [
    { id: "w1", name: "cats", path: "/Users/me/projs/go/cats", active: true },
    { id: "w2", name: "ced", path: "/Users/me/projs/go/ced", active: false },
    { id: "w3", name: "old" }, // no identity cwd: an old snapshot
  ],
};

// Pane 1 is on screen in w1 (the layout gave it a handle); pane 7 is an
// off-screen agent in w2; pane 9 is an off-screen shell known only to the
// pane inventory, also in w2; pane 42 is known to nobody.
const panes = new Map([[1, { pub: "w1:p1", info: { focused: true } }]]);
const agentItems = [{ pane: 7, workspace: "w2" }];
const paneInv = [{ pane: 9, handle: "w2:p4" }];

const fns = loadFns({
  files: ["07-workspaces.js", "28-ctxmenu.js", "36-windows.js"],
  names: ["wsPathItems", "paneWsFor", "wsOf", "activeWorkspaceID", "tabMenuItems",
    "paneMenuItems", "wsMenuItems", "flagRowLabel", "flagMenuItems", "otherWorkspaces"],
  env: {
    layoutMsg, panes, agentItems, paneInv,
    multiHost: () => false,
    windowsOnWorkspace: () => 0,
    branchCopyText: () => "",
    paneFlagFor: () => null,
    paneFlagTarget: () => ({ flag: null }),
    wsFlagTarget: () => ({ flag: null }),
    flagOf: () => null,
    FLAG_DEFS: [],
  },
  stubs: ["pasteText", "sendCmd", "enterCopyMode", "copyScrollback", "renamePane", "renameTab",
    "newWorkspace", "renameWorkspace", "openWindow", "toggleWorkspaceLock",
    "openWorkspaceSettings", "wakeWorkspace", "cleanWorkspace", "sleepWorkspace",
    "confirmCloseWorkspace", "openNewWorktreeDialog", "openWorktreeOpenDialog", "removeWorktreeFor"],
});
const { wsPathItems, paneWsFor, tabMenuItems, paneMenuItems, wsMenuItems } = fns;

eq(wsPathItems("w2"), [{ path: "/Users/me/projs/go/ced" }, "-"], "a path line, then a separator");
eq(wsPathItems("w3"), [], "no identity cwd: no line and no stray separator");
eq(wsPathItems(""), [], "unknown workspace: nothing");

eq(paneWsFor(1), "w1", "on screen: from the layout's handle");
eq(paneWsFor(7), "w2", "off-screen agent: from the rollup");
eq(paneWsFor(9), "w2", "off-screen shell: from the inventory's handle");
eq(paneWsFor(42), "", "unknown pane");

eq(paneMenuItems(1, true)[0], { path: "/Users/me/projs/go/cats" }, "canvas menu opens on the path…");
eq(paneMenuItems(1, true)[2].label, "paste", "…with paste still the first action under it");
eq(paneMenuItems(7, false)[0], { path: "/Users/me/projs/go/ced" },
  "a sidebar pane in another workspace names THAT workspace, not this window's");
eq(paneMenuItems(42, false)[0].label, "reveal pane", "no workspace known: the menu starts on its first row");

eq(tabMenuItems({ num: 1 }, 1)[0], { path: "/Users/me/projs/go/cats" }, "tab menu: this window's workspace");

const ws = wsMenuItems(layoutMsg.workspaces[1]);
eq(ws[0], { path: "/Users/me/projs/go/ced" }, "workspace menu: the row's own workspace");
eq(ws[2], { head: "Workspace" }, "the first heading follows the separator");
eq(wsMenuItems(layoutMsg.workspaces[2])[0], { head: "Workspace" }, "pathless workspace: heading first, as before");

report("ctxpath");
