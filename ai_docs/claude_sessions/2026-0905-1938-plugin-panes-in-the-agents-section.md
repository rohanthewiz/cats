# Plugin panes in the AGENTS section

Session: https://claude.ai/code/session_01AaoUtxuk5QmaSW7YqYSxJj
Date: 2026-09-05
Repo: ~/projs/go/cats (branch `main`)

## Request

> Let's organize the Agents section. Put model agents first followed by a thin
> horizontal separator then the different types of plugin agents grouped by type
> and a thin horizontal separator after each group

The two groups did not exist in the code, so the request was ambiguous in a way
that changed the work. Two questions, answered:

- **which Agents section** — the sidebar AGENTS list (`sec-agents`), not a menu
  or Claude Code's own agent roster.
- **what separates the groups** — *model agents* are the panes `internal/detect`
  identified as a coding agent; *plugin agents* are the panes a cats plugin
  action was launched into, grouped by plugin id.

## What existed

`renderAgents` (17-agentlist.js) rendered one flat list, in the server's walk
order, from the `agents` rollup. The rollup itself (`orch.agentsMsg`) skipped
every pane whose `effectiveAgent()` was empty — so a plugin pane was never in it
at all. Nothing anywhere recorded *which* plugin a pane belonged to: as
07-workspaces.js already noted, "the plugin host's `CATS_PLUGIN_ID` reaches the
process but not the pane record", which is why the cats-todo paw print on a
workspace row is read out of the pane's OSC title instead.

So the section could not be organized without first giving cats the notion the
grouping is cut on.

## The change

### 1. A pane remembers which plugin launched it

`PaneState.PluginID` (`internal/workspace/tab.go`), durable like `CustomName`
and `Flag`, and carried through `PaneSnapshot` (`internal/workspace/persist.go`,
`plugin_id,omitempty` — an older session.json restores byte-identically).

Durable because catway restarts while cathost keeps the PTY: the pane comes back
adopted, its plugin process still running, with nothing left in memory to say
whose it is.

Written in `createPane` (`cmd/catway/catway.go`), where the spawn plan is
consumed — "what this pane runs is decided here and nowhere else". It reads
`cp.Env[plugin.IDEnvVar]`, the value the launcher already put in the child's
environment (browser `pluginRunAction`, `catctl plugin run`), so no manifest is
parsed and catway stays plugin-agnostic.

Written on **every** spawn, not only a plugin one, so the field is cleared as
well as set. The case that forces it: a cathost restart respawns a plugin pane
as a plain shell (nothing resumes it), and a stale id would leave the sidebar
naming a program that is no longer running.

New session accessors mirroring the flag pair: `SetPanePlugin` (reports whether
it changed, so the common no-op skips the save) and `PanePlugin`
(`internal/app/session.go`).

### 2. The rollup carries two lists

```go
type Agents struct {
    T       Type         `json:"t"`
    Items   []AgentItem  `json:"items"`
    Plugins []PluginPane `json:"plugins,omitempty"`
}
```

Separate lists, not one list with a kind field, because everything downstream of
`items` — the workspace badges, the tab-bar activity markers, the attention
sweep that reopens a folded sidebar, the palette's pane meta — reads it as "the
panes with an agent state". Keeping plugin panes out of it is structural rather
than a filter every one of those consumers has to remember.

`PluginPane` carries no state and no age: a plugin is a program, not an agent
taking turns, so there is nothing the server could send that would mean what
"idle 5m ago" means on an agent row. It carries `Title` instead — the pane's
live terminal title, which is the channel a plugin actually speaks on
(cats-todo's "todo: cats (3)").

`agentsMsg` builds both in the one walk. A pane can be in only one: a plugin that
runs an agent is reported as the agent, since that row says what the pane is
doing now while a plugin row could only say who started it. Exited panes are in
neither. The plugin list is sorted by (plugin id, pane id) so groups keep their
places between rollups and the client can cut them by walking once.

### 3. The section draws in blocks

```
● claude opus 5                cats:p1 · 2m ago · idle
● codex                        cats:p3 · 9s ago · working
──────────────────────────────────────────────────────────
● cats-todo  todo: cats (3)                      cats:p4
● cats-todo  todo: herdr (1)                     w2:p1
──────────────────────────────────────────────────────────
● gonotes    notes                               w2:p2
──────────────────────────────────────────────────────────
```

`renderAgents(items, plugins)` now renders agent rows, then one block per plugin,
each closed by a rule — the last one included, as asked. The rule above a block
is the one that closes the block before it, so with no agents there is no leading
rule, and with no plugin panes the section is exactly what it always was.

The loop body became `agentRow(it, foc)` (moved verbatim, comments and all) so
the render reads as the three-line ordering it now is. New siblings: `agentSep`,
`pluginRow`, `pluginShort`.

A plugin row parallels an agent row field for field: the plugin name where the
agent's name goes (hued from the same six identity slots — one colour per tool
is the property being read), the pane title where the model goes (it changes
under a stable identity, which is what that half is for), the pane handle at the
right. The dot stays for alignment but reports nothing (`st-unknown`, muted).
Click reveals, right-click opens the pane menu, a locked workspace dims and
refuses — all identical to an agent row, since the row's job is the same.

`setAgentLocked` now falls back to `dataset.plugin` for its tooltip, the same
bargain the model tooltip strikes: the row shows the half that tells it apart,
the tooltip holds the whole string.

CSS (`11-agentlist.css`): `#agent-list li.sep` is a zero-height `<li>` with a
`--line` top border, inset by the rows' own 8px. A row, not a border on the
neighbour, because rows here take hover and focus backgrounds that would paint
over such a border. Plugin rows also get `min-width:0` on `.aname` and
`flex:none` on `.ameta`, since a plugin's title is arbitrary text a program chose
and must not push the handle off the edge.

## Files

| File | Change |
|------|--------|
| `internal/workspace/tab.go` | `PaneState.PluginID` |
| `internal/workspace/persist.go` | snapshot + restore of it |
| `internal/app/session.go` | `SetPanePlugin` / `PanePlugin` |
| `wire/down.go` | `PluginPane`, `Agents.Plugins`, `NewAgents(items, plugins)` |
| `internal/browserproto/wire_aliases.go` | `PluginPane` alias |
| `cmd/catway/catway.go` | record/clear at `createPane`; two rosters in `agentsMsg`; `panePlugin` helper |
| `cmd/catway/web/js/17-agentlist.js` | blocks; `agentRow` / `agentSep` / `pluginRow` / `pluginShort` |
| `cmd/catway/web/js/19-messages.js` | pass `msg.plugins` |
| `cmd/catway/web/css/11-agentlist.css` | the rule; plugin-row ellipsis |
| `cmd/catway/web/jstest/testutil.mjs` | `lets:` option — env bindings the lifted code assigns to (`agentItems`) |
| `docs/subsystems/plugins.md` | "Where a running action shows up" |
| `docs/protocols/browser-protocol.md` | the rollup's two lists |

## Tests

- `cmd/catway/pluginpane_test.go` — grouping and ordering by plugin id, the
  agent-wins rule for a pane that is both, the title/handle/flag on a row, a
  corpse leaving the rollup; and `createPane` recording the id then **clearing**
  it on a respawn with no plan.
- `internal/workspace/persist_test.go` — `PluginID` survives the snapshot round
  trip.
- `cmd/catway/web/jstest/agentlist.test.mjs` (new) — the block order, the two
  boundary mistakes (a rule between two panes of one plugin; a leading rule with
  nothing above it), the plugin row's text, and "plugin panes alone are not an
  empty section".

`make check` is clean apart from the two pre-existing `internal/inputenc`
failures (untracked `cmd/catgen-dart/testdata/golden/keys.g.dart`).

## Notes for next time

- A catway rebuild + restart is needed to see it: the front end is embedded at
  build time.
- A cats-todo started **by hand** from a shell has no `CATS_PLUGIN_ID` and so no
  row in a plugin block — same limitation the workspace paw print was written
  around.
- `paneRuntime.execCmd` is still only set in `createPane` and never restored for
  an adopted pane, so `clean.go`'s busy test under-reports after a catway
  restart. Untouched here, but it is the same class of gap the durable
  `PluginID` was added to avoid.
- The trailing rule under the last plugin block is deliberate (the request said
  "after each group"); it is one line in `renderAgents` if it ever reads as
  unfinished.
