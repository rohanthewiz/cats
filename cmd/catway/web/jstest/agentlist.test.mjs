// The AGENTS section's blocks (17-agentlist.js).
//
//   ● claude opus 5        cats:p1 · 2m ago · idle
//   ────────────────────────────────────────────────
//   ● cats-todo  todo: cats (3)                 cats:p4
//   ────────────────────────────────────────────────
//
// What is covered here is the ORDER the section is built in — agents first,
// then one block per plugin, each closed by a hairline — plus the two ways a
// block boundary can be got wrong: a rule between two panes of the same plugin,
// and a leading rule with no agent rows above it to close.

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
function world() {
  const listEl = el("ul");
  listEl.innerHTML = ""; // the render clears it this way; the stub just holds the value
  const fns = loadFns({
    files: ["17-agentlist.js"],
    names: ["renderAgents", "agentSep", "agentRow", "pluginRow", "pluginShort", "setAgentLocked"],
    lets: ["agentItems"],
    env: {
      agentItems: [],
      agentListEl: {
        get innerHTML() { return ""; },
        set innerHTML(_) { listEl.children.length = 0; },
        appendChild: (c) => listEl.appendChild(c),
        get children() { return listEl.children; },
      },
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
  return { fns, rows: () => listEl.children };
}

const agent = (pane, state) => ({
  pane, pub: "cats:p" + pane, workspace: "w1", tab: 1,
  agent: "claude", model: "claude-opus-5", state, seen: true, since_ms: 1000,
});
const plug = (pane, id, title) => ({
  pane, pub: "cats:p" + pane, workspace: "w1", tab: 1, plugin: id, title,
});

// The shape of one full render: agents, a rule, each plugin's panes together,
// a rule under each. Two panes of the same plugin are one block — a rule
// between them would read as two plugins with the same name.
{
  const { fns, rows } = world();
  fns.renderAgents([agent(1, "idle"), agent(3, "working")], [
    plug(4, "rohanthewiz.cats-todo", "todo: cats (3)"),
    plug(5, "rohanthewiz.cats-todo", "todo: herdr (1)"),
    plug(6, "zz.gonotes", "notes"),
  ]);
  eq(rows().map((r) => r.className),
    ["agent", "agent", "sep", "agent plug", "agent plug", "sep", "agent plug", "sep"],
    "blocks: agents, rule, one block per plugin, each closed by a rule");
}

// A plugin row names the plugin (trimmed of its vendor prefix) and, in the dim
// half an agent gives its model, the pane's own title. The handle stays at the
// end, as on every other row in the sidebar.
{
  const { fns, rows } = world();
  fns.renderAgents([], [plug(4, "rohanthewiz.cats-todo", "todo: cats (3)")]);
  eq(rows().map((r) => r.text()), ["●cats-todo todo: cats (3)cats:p4", ""],
    "a plugin row reads: dot, plugin, title, handle");
  eq(rows()[0].dataset.plugin, "rohanthewiz.cats-todo", "the full id stays on the row");
  eq(rows()[0].title, "rohanthewiz.cats-todo", "…and in the tooltip, which the trimmed label dropped");
}

// With no agents, the first plugin block has nothing above it to close, so it
// draws no leading rule — only the one under it.
{
  const { fns, rows } = world();
  fns.renderAgents([], [plug(4, "a.one", "x"), plug(5, "b.two", "y")]);
  eq(rows().map((r) => r.className), ["agent plug", "sep", "agent plug", "sep"],
    "no agents: no leading rule");
}

// With no plugins the section is exactly what it always was: rows, no rules.
{
  const { fns, rows } = world();
  fns.renderAgents([agent(1, "idle")], []);
  eq(rows().map((r) => r.className), ["agent"], "no plugins: no rules at all");
}

// Nothing at all is still "none" — and a rollup with plugin panes in it is not
// nothing, which is the case an `items.length` check alone would get wrong.
{
  const { fns, rows } = world();
  fns.renderAgents([], []);
  eq(rows().map((r) => r.className), ["empty"], "an empty section says none");
  const w = world();
  w.fns.renderAgents(undefined, [plug(4, "a.one", "x")]);
  ok(w.rows()[0].className !== "empty", "plugin panes alone are not an empty section");
}

eq(world().fns.pluginShort("rohanthewiz.cats-todo"), "cats-todo", "pluginShort drops the vendor prefix");
eq(world().fns.pluginShort("cats-todo"), "cats-todo", "…and leaves a bare id alone");

report("agentlist");
