// The AGENTS and PLUGINS sections (17-agentlist.js). One rollup feeds both:
//
//   AGENTS
//   ● claude opus 5        cats:p1 · 2m ago · idle
//   PLUGINS
//   ● cats-todo  todo: cats (3)      todos · cats:p4
//   ────────────────────────────────────────────────
//
// What is covered here is the SPLIT (agent-typed plugins stay in AGENTS,
// everything else goes to PLUGINS, which hides when empty) and the ORDER each
// section is built in — one block per plugin, each closed by a hairline — plus
// the two ways a block boundary can be got wrong: a rule between two panes of
// the same plugin, and a leading rule with nothing above it to close.

import { loadFns, eq, ok, report } from "./testutil.mjs";

// A DOM stub the size of what these functions touch: children, classes, the
// dataset, and a text run to read the row back with.
function el(tag) {
  const e = {
    tag, textContent: "", className: "", title: "", dataset: {}, children: [], on: {},
    appendChild(c) { this.children.push(c); return c; },
    removeAttribute(a) { if (a === "title") this.title = ""; },
    addEventListener(ev, fn) { this.on[ev] = fn; },
    classList: {
      add: (c) => { e.className = (e.className + " " + c).trim(); },
      toggle: (c, on) => {
        const has = e.className.split(" ").includes(c);
        if (on && !has) e.className = (e.className + " " + c).trim();
        if (!on && has) e.className = e.className.split(" ").filter((x) => x !== c).join(" ");
      },
    },
    text() { return this.textContent + this.children.map((c) => c.text()).join(""); },
  };
  return e;
}

// world builds the bindings the lifted render closes over. Everything the
// section reads from elsewhere in the bundle — state classes, labels, the flag
// mark, the workspace lock — is stubbed to the simplest honest answer, since
// what is under test is the shape of the list, not those.
// listStub wraps a stub <ul> in the one behaviour the render needs beyond
// el(): assigning innerHTML empties it.
function listStub(listEl) {
  return {
    get innerHTML() { return ""; },
    set innerHTML(_) { listEl.children.length = 0; },
    appendChild: (c) => listEl.appendChild(c),
    get children() { return listEl.children; },
  };
}

function world() {
  const listEl = el("ul");
  const plugEl = el("ul");
  const plugSec = { hidden: true }; // the markup ships it hidden
  const fns = loadFns({
    files: ["17-agentlist.js"],
    names: ["renderAgents", "isAgentPlugin", "appendPluginBlocks", "agentSep", "agentRow",
      "pluginRow", "pluginShort", "pluginTypeLabel", "setAgentLocked"],
    lets: ["agentItems"],
    env: {
      agentItems: [],
      agentListEl: listStub(listEl),
      pluginListEl: listStub(plugEl),
      pluginSecEl: plugSec,
      document: {
        createElement: el,
        // A text node is a child with text and no tag, which is all the row
        // reader below needs of one.
        createTextNode: (t) => { const n = el(""); n.textContent = t; return n; },
      },
      layoutMsg: null,
      focusedPaneId: () => null,
      agentAttentionSweep: () => {},
      refreshPaneList: () => {},
      stClass: (st) => "st-" + st,
      markerState: (it) => it.state,
      modelLabel: (m) => (m || "").replace("claude-", "").replace("-", " "),
      hueClass: () => "ah1",
      paneRef: (pub) => pub,
      flagOf: () => null,
      flagMark: () => null,
      wsLocked: () => false,
      wsName: (id) => id,
      paintAge: () => {},
      pressActivate: () => {},
      openCtx: () => {},
      paneMenuItems: () => [],
      sendCmd: () => {},
      toast: () => {},
    },
  });
  return { fns, rows: () => listEl.children, plugRows: () => plugEl.children, plugSec };
}

const agent = (pane, state) => ({
  pane, pub: "cats:p" + pane, workspace: "w1", tab: 1,
  agent: "claude", model: "claude-opus-5", state, seen: true, since_ms: 1000,
});
const plug = (pane, id, title, type) => ({
  pane, pub: "cats:p" + pane, workspace: "w1", tab: 1, plugin: id, title, type,
});

// The split: detected agents and agent-typed plugins in AGENTS, every other
// plugin — typed, untyped, or of a type this page does not know — in PLUGINS.
{
  const { fns, rows, plugRows, plugSec } = world();
  fns.renderAgents([agent(1, "idle")], [
    plug(2, "a.bot", "bot", "agent"),
    plug(3, "rohanthewiz.ced", "main.go — ced", "editor"),
    plug(4, "rohanthewiz.cats-todo", "todo: cats (3)", "todos_mgr"),
    plug(5, "x.untyped", "u"),
    plug(6, "y.future", "f", "dev_server"),
  ]);
  eq(rows().map((r) => r.dataset.pane), [1, undefined, 2, undefined],
    "AGENTS: the agent, a rule, the agent plugin's block");
  eq(plugRows().filter((r) => r.dataset.pane).map((r) => r.dataset.pane), [3, 4, 5, 6],
    "PLUGINS: every non-agent plugin, in the server's order");
  ok(!plugSec.hidden, "PLUGINS shows when it has rows");
}

// The shape of PLUGINS: each plugin's panes together, a rule under each. Two
// panes of the same plugin are one block — a rule between them would read as
// two plugins with the same name. Nothing above the first block, so no leading
// rule, and AGENTS keeps its rows with no rule at all.
{
  const { fns, rows, plugRows } = world();
  fns.renderAgents([agent(1, "idle"), agent(3, "working")], [
    plug(4, "rohanthewiz.cats-todo", "todo: cats (3)", "todos_mgr"),
    plug(5, "rohanthewiz.cats-todo", "todo: herdr (1)", "todos_mgr"),
    plug(6, "zz.gonotes", "notes", "notes_mgr"),
  ]);
  eq(rows().map((r) => r.className), ["agent", "agent"], "AGENTS: just the agents");
  eq(plugRows().map((r) => r.className),
    ["agent plug", "agent plug", "sep", "agent plug", "sep"],
    "PLUGINS: one block per plugin, each closed by a rule, none leading");
}

// Agent-typed plugins keep the old AGENTS shape: agents, a rule, the block.
// With no agents above them there is nothing to close, so no leading rule.
{
  const { fns, rows } = world();
  fns.renderAgents([agent(1, "idle")], [plug(2, "a.bot", "x", "agent"), plug(3, "b.bot", "y", "agent")]);
  eq(rows().map((r) => r.className), ["agent", "sep", "agent plug", "sep", "agent plug", "sep"],
    "AGENTS: agents, rule, one block per agent plugin");
  const w = world();
  w.fns.renderAgents([], [plug(2, "a.bot", "x", "agent")]);
  eq(w.rows().map((r) => r.className), ["agent plug", "sep"], "no agents: no leading rule");
}

// A plugin row names the plugin (trimmed of its vendor prefix) and, in the dim
// half an agent gives its model, the pane's own title. Its type rides before
// the handle, which stays at the end, as on every other row in the sidebar.
{
  const { fns, plugRows } = world();
  fns.renderAgents([], [plug(4, "rohanthewiz.cats-todo", "todo: cats (3)", "todos_mgr")]);
  eq(plugRows().map((r) => r.text()), ["●cats-todo todo: cats (3)todos · cats:p4", ""],
    "a plugin row reads: dot, plugin, title, type, handle");
  eq(plugRows()[0].dataset.plugin, "rohanthewiz.cats-todo", "the full id stays on the row");
  eq(plugRows()[0].title, "rohanthewiz.cats-todo", "…and in the tooltip, which the trimmed label dropped");
  const w = world();
  w.fns.renderAgents([], [plug(4, "x.untyped", "t")]);
  eq(w.plugRows()[0].text(), "●untyped tcats:p4", "an untyped plugin row shows the handle alone");
}

// Nothing at all: AGENTS says none and PLUGINS hides. Plugin panes alone
// leave AGENTS saying none — they are not agents — and show PLUGINS.
{
  const { fns, rows, plugSec } = world();
  fns.renderAgents([], []);
  eq(rows().map((r) => r.className), ["empty"], "an empty AGENTS says none");
  ok(plugSec.hidden, "an empty PLUGINS hides");
  const w = world();
  w.fns.renderAgents(undefined, [plug(4, "a.one", "x", "editor")]);
  eq(w.rows().map((r) => r.className), ["empty"], "a tool plugin does not fill AGENTS");
  ok(!w.plugSec.hidden, "…it fills PLUGINS");
  // ...and a later rollup with the pane gone hides the section again.
  w.fns.renderAgents([], []);
  ok(w.plugSec.hidden, "PLUGINS hides again once its last pane closes");
  eq(w.plugRows().length, 0, "…with its old rows cleared");
}

eq(world().fns.isAgentPlugin({ type: "agent" }), true, "type agent is an agent plugin");
eq(world().fns.isAgentPlugin({}), false, "an untyped plugin is a tool");
eq(world().fns.isAgentPlugin({ type: "Agent" }), false, "the match is exact, as the wire's is");
eq(world().fns.pluginTypeLabel("todos_mgr"), "todos", "pluginTypeLabel drops _mgr");
eq(world().fns.pluginTypeLabel("http_client"), "http", "…and _client");
eq(world().fns.pluginTypeLabel("client_tool"), "client_tool", "…only as a suffix");
eq(world().fns.pluginTypeLabel("editor"), "editor", "…and leaves other types alone");
eq(world().fns.pluginTypeLabel(undefined), "", "…and says nothing for no type");
eq(world().fns.pluginShort("rohanthewiz.cats-todo"), "cats-todo", "pluginShort drops the vendor prefix");
eq(world().fns.pluginShort("cats-todo"), "cats-todo", "…and leaves a bare id alone");

report("agentlist");
