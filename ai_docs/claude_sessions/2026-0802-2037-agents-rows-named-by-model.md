# Session: the AGENTS rows name their model, not "claude"

- **Session ID:** `6912e0e4-cba3-466d-8c0a-8f9a17fc433e`
- **Date:** 2026-08-02
- **Branch:** main (from `52764ff`)
- **Repo:** `cats`

## Request

One message, with a screenshot of the sidebar:

> Per previous session work, we want to name the agent by the model type. It is
> already done in the PANES section but not in the AGENTS section.

The screenshot showed it plainly — PANES rows ending in `opus` / `fable`, and
three AGENTS rows below them all reading `claude`.

---

## Why one section already had it and the other didn't

The two sections are fed by different messages, and only one of them carried a
model.

`modelLabel()` — the trimmer that turns `"claude-opus-5 · high"` into `opus`,
and `"claude-sonnet-4-5-20250929[1m]"` into `sonnet [1M]` — was already in the
page and already used in two places:

```js
// pane header (renderChrome)
if (p.agent) add("agent", "  " + (modelLabel(p.agentModel) || p.agent) + ":" + p.agentState);
// PANES row (paneRowEl)
ag.textContent = modelLabel(row.model) || row.agent;
```

Both of those read a **pane's** model, which reaches the browser two ways: the
`pane_agent` push (`Model` field) for on-screen panes, and the `pane.list`
snapshot (`agent_model`) for the rest. `renderPaneList` merges them per row.

The AGENTS section is built from the global `agents` rollup, and `AgentItem`
simply had no model on it:

```go
type AgentItem struct {
    Pane      uint32 `json:"pane"`
    Pub       string `json:"pub"`
    Workspace string `json:"workspace"`
    Tab       int    `json:"tab"`
    Agent     string `json:"agent"`
    State     string `json:"state"`
    Seen      bool   `json:"seen"`
    SinceMs   int64  `json:"since_ms"`
}
```

Note what the row-merge in `renderPaneList` does with the rollup — it overrides
agent identity and state from it, but **not** the model:

```js
const a = byPane.get(pi.pane);
if (a) { row.agent = a.agent; row.state = markerState(a); }
```

That is why PANES kept working: its model never came from the rollup in the
first place. The rollup was the one path with a hole in it.

## The subtle half: the resolution lands *after* the rollup that wanted it

Adding the field was the easy part. The interesting bug was in the timing.

Model resolution is a **disk read of the claude transcript**, deliberately kept
off the loop goroutine (`agentmodel.go`). The sequence on a state change is:

```
onPaneAgent
  └─ publishAgent
       ├─ refreshAgentModel(rt, agent)   ← spawns the read, returns immediately
       ├─ broadcast(PaneAgent{... rt.agentModel ...})   ← the OLD model
       └─ broadcast(agentsMsg())                        ← the OLD model too

   ...later, off-loop...
  └─ setAgentModel(pid, model)   posted back onto the loop
       └─ broadcast(PaneAgent{...})   ← visible panes only. And nothing else.
```

`setAgentModel` published a `pane_agent` and stopped. That was sufficient while
the model only ever appeared in pane chrome, since both consumers of it are
pane-scoped. With the rollup naming rows by model, it left two failures:

1. **Every row one turn behind.** The rollup that shipped alongside the state
   change carried the pre-read model, and no rollup followed the read.
2. **A quiet pane never correcting itself.** The 30s background sweep
   (`runAgentModels`) exists precisely to catch a `/model` switch on a pane that
   then sits idle — no state transition, so no rollup, so the row keeps the old
   name indefinitely.

The fix is one broadcast at the end of `setAgentModel`, guarded on the pane
still having an agent:

```go
rt.agentModel = model
if agent == "" {
    return
}
if o.visible[pid] {
    o.broadcast(browserproto.NewPaneAgent(pid, agent, state, model, !rt.unseen))
}
o.broadcast(o.agentsMsg())
```

`setAgentModel` already returns early when the model is unchanged, so this is a
session-wide rebuild only on a model that actually moved — first resolution
after an agent appears, or a `/model` switch. Rare by construction.

The `agent == ""` guard matters for a second reason: `setAgentModel` blanks the
model when the arbitrated agent isn't claude, and a pane that switched from
claude to another agent needs the rollup to *drop* the stale model, not keep
painting it.

## The fallback chain, stated once

```
modelLabel(it.model)   →  "opus" / "fable" / "sonnet [1M]"
      ‖ (empty)
it.agent               →  "claude", "codex", …
```

Empty covers three real cases: a non-claude agent (nothing resolves one today),
claude before its first answer, and a transcript whose id doesn't parse into a
family word. All three fall through to the agent's own name rather than printing
a mangled id — `modelLabel` returns `""` rather than guessing, which is what
makes the `||` safe.

## Decisions worth keeping

**The row's tooltip carries the untrimmed string.** `modelLabel` deliberately
drops the effort suffix (`· high`) and the exact id, because the sidebar has no
room and the PANES hover card shows the whole thing. AGENTS rows have no hover
card, so the full model became the row's `title` — stored on `dataset.model` so
`markLockedAgents` can restore it when a lock lifts:

```js
function setAgentLocked(li, locked) {
  li.classList.toggle("wslocked", locked);
  if (locked) li.title = "workspace locked — clicking will not reveal this agent";
  else if (li.dataset.model) li.title = li.dataset.model;
  else li.removeAttribute("title");
}
```

The lock message **displaces** the model tooltip rather than merging with it —
while a click is being refused, saying so is worth more than naming the model.
(That handler is from the previous session; this is the first thing to contend
with it for the same attribute.)

**No layout risk from the wider label.** `#agent-list .aname` is left-aligned
with `.ameta { margin-left:auto }` pushing the meta right, so `sonnet [1M]`
eats slack rather than truncating anything.

**The generated Dart client was regenerated, not hand-edited.**
`cmd/catgen-dart/testdata/golden/wire.g.dart` is the exact bytes cats-mobile's
`packages/catsproto` ships, and `golden_test.go` fails if the generator and the
golden disagree:

```
go run ./cmd/catgen-dart -out cmd/catgen-dart/testdata/golden
```

One wire field addition, four files re-emitted, nine lines of diff — `Model`
flows into `AgentItem.fromJson` / `toJson` for free. This is the first time the
generator has been exercised by an ordinary feature change since Phase 1 built
it, and it behaved: the golden test is what caught that the mobile client had a
stake in this at all.

## Files

| file | change |
|---|---|
| `internal/browserproto/down.go` | `AgentItem.Model` (`model,omitempty`) |
| `cmd/catway/catway.go` | `agentsMsg()` fills `Model` from `rt.agentModel` |
| `cmd/catway/agentmodel.go` | `setAgentModel` broadcasts the rollup on a real change |
| `cmd/catway/web/index.html` | `renderAgents` names by model; `dataset.model`; model tooltip in `setAgentLocked` |
| `cmd/catway/agentmodel_test.go` | `TestAgentsRollupCarriesModel` |
| `cmd/catgen-dart/testdata/golden/wire.g.dart` | regenerated |

## Verification

- `go build ./...` — clean.
- `go test -count=1 ./...` and `go test -tags ghostty -count=1 ./...` — all pass.
  **The tag matters:** `cmd/catway`'s tests are behind `//go:build ghostty`, and
  the untagged run reports `ok` for the package while silently running none of
  them. A new test there passes vacuously until it is run with the tag.
- `TestAgentsRollupCarriesModel` covers both halves: the resolution that lands
  after the state change reaching the rollup, and a non-claude agent leaving the
  rollup's `Model` empty.
- Page script extracted and `node --check`ed (3501 lines, non-empty) — clean.
- **Not** verified visually, same reason as the previous session: the running
  catway is the installed `Cats.app` serving the old embedded page. Next person
  in front of a rebuilt binary should confirm the three AGENTS rows in the
  screenshot now read `opus` / `fable` / `fable`, and that hovering one shows
  the full `claude-opus-5 · high`.

## For next time

`setAgentModel` is now the only place that broadcasts a rollup outside
`publishAgent`. If a third consumer of the model appears, check whether it also
needs that broadcast — the pattern to look for is "chrome that is not
pane-scoped reading a value resolved asynchronously".

One stale-ish comment was left alone deliberately: `renderAgents` still says the
age stamp is absolute because "the rollup won't come again until the state
moves". A model change can now bring one early, but the stamp being absolute is
exactly what makes that harmless, so the reasoning still holds.
