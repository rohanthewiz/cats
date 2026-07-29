# Session: Sidebar PANES Spans All Workspaces (+ model on pane.list)

- **Session ID:** `ce4c9bb5-4d65-4c1a-a3eb-83216d06ed9a`
- **Date:** 2026-07-29
- **Branch:** main

## Request

> The Panes section only ever shows one Pane. Is that correct?

(with a screenshot: workspace `cdx` active with a single pane, PANES showing one
row, while AGENTS listed two — `cats:pM` and `cdx:p1`)

Then: **"Source it from pane.list so it spans all workspaces"**, and after the
follow-up note about the hover card: **"yes, add Model to PaneMeta too"**.

## The answer to the question

Not a bug. `renderPaneList` iterated `msg.panes` from the layout message, and
`BuildLayout` fills `Panes` for **the active tab of the active workspace** only —
plus two narrowings that fall out of that: other tabs in the same workspace
contribute nothing, and a zoomed tab reports only its focused pane. AGENTS is
deliberately different (the rollup is global), which is why the screenshot was
self-consistent rather than broken.

The section read like a session-wide list while being "panes in view", so the
user chose to make it actually session-wide.

## The pivotal find

`pane.list` **already** carries everything a full row needs. `PaneInfo` embeds
`PaneMeta` (agent, agent_state, title, cwd), merged in by the dispatcher from
`Backend.PaneMeta` — added originally for cats-todo's drop-target picker. So the
front end needed no new protocol, just a different source.

## The chrome-filtering correction (the reason a query is needed at all)

`docs/protocols/browser-protocol.md` claimed chrome is "always sent, for every
pane". Reading the code says otherwise — only the `agents` rollup is global:

| message | gated on visibility? |
|---|---|
| `pane_title` | yes (`daemon.go:235`) |
| `pane_cwd` | yes (`daemon.go:254`) |
| `pane_agent` | yes (`notify.go:86`) |
| `pane_modes` | yes (`daemon.go:219`) |
| `pane_exited` | yes (`daemon.go:288`) |
| `agents` | **no — always broadcast** |

What makes visibility-gating safe is `broadcastPaneChrome`: `applyModel` resends
a pane's cached chrome when it *enters* the viewport. So an off-screen title
exists **nowhere in the browser** — hence the query. Doc corrected.

## Design: two sources per row, on purpose

- **Viewport state (visible / focused) from the layout push.** Focus moves land
  without waiting on a round trip, and the layout is what actually decides what's
  on screen (a zoomed tab hides its siblings — snapshot `focused` is per-tab, so
  several panes are "focused" at once; only `focused && visible` gets the ▸).
- **Title / cwd / agent / model** from local pane state when on screen (live
  pushes beat any snapshot), from the snapshot otherwise. Custom name overrides
  title off-screen, mirroring `effectiveTitle` server-side.
- **Agent state from the rollup when it knows the pane** — it carries the `seen`
  flag, which is what renders a run that finished off-screen as "done".

### Refresh policy — push-driven, no polling

`refreshPaneList()` renders immediately from what the browser knows, then
re-queries `pane.list` **debounced 120ms + single-flight** (`paneInvBusy` /
`paneInvAgain` / `paneInvWait`). Callers are the pushes that can change the
inventory: layout, the agents rollup, `pane_title`, `pane_agent`, `pane_exited`.
A timer-based poll was considered for off-screen title drift and rejected — the
rollup fires on every agent state change, which is the churn that matters, and
polling clashes with the codebase's push ethos.

`ws.onopen` resets the guard: a query in flight when the socket dies never gets
its callback, which would latch `paneInvBusy` and freeze the section forever.

### Off-screen rows needed different affordances

Checked each command's actual scope before wiring the row:

- `pane.focus` only moves the focus flag inside the current viewport → off-screen
  rows send **`agent.focus`** (`RevealPane`), same as the agents list.
- `Session.SwapPanes` is **active-tab only** → the swap drag is armed only for
  on-screen rows (an off-screen row has no slot to trade).
- `ToggleZoom` errors for a pane outside the active tab; `enterCopyMode` needs a
  rendered pane → both dropped from the menu when off-screen, which gains a
  "reveal pane" item instead.
- `pane.split` / `pane.close` / `pane.rename` / `capture` are workspace-resolved
  → kept for every pane.
- `renamePane` falls back to the snapshot for its handle and prefilled name.

## AgentModel on PaneMeta (the follow-up)

- `internal/app/command_vocab.go`: `PaneMeta.AgentModel` → `agent_model,omitempty`.
- `cmd/catway/catway.go` `orch.PaneMeta`: filled from `rt.agentModel` **inside the
  `effectiveAgent()` branch** — the model is resolved from that agent's
  transcript, so reporting it for a pane with no agent reports a leftover.
- `runAgentModels` already sweeps *every* pane, not just visible ones, so
  off-screen models are current server-side with no extra work.
- Hover card reads `row.model` (local `agentModel` on screen, `agent_model`
  otherwise). Model was previously blank for off-screen panes.
- `docs/protocols/control-api.md`: the queries section said these are answered
  "straight from the Session with no backend effects" — skipped the PaneMeta
  merge entirely. Now lists the merged fields.

## Verification — ran the real app, twice

Built ghostty-tagged binaries into the scratchpad and drove an **isolated**
instance so the user's live MacApp session was never touched:

```
cathost -socket /tmp/cats-dev-h.sock -persistent
catway  -addr 127.0.0.1:8499 -socket /tmp/cats-dev-h.sock \
        -control-socket /tmp/cats-dev-c.sock -auth none -persist=false
```

Five panes / two workspaces / three tabs, titles set via OSC (`printf
'\033]0;build watch\007'`) and one `rename-pane`, rendered:

```
cats:p1 build watch     ← off-screen title, only pane.list knows it
cats:p2
cats:p3 notes           ← off-screen custom name
──────────────────────  ← workspace break
todo:p1 server logs
▸ todo:p2               ← focused in the viewport
```

`catctl agent 1` then flipped the state correctly (cats:p1 focused+visible,
todo:* off). For the model, a **real claude pane** pushed into another workspace:

```
w1:p1 vis False | agent claude | state idle | model claude-opus-5 | title '✳ Claude Code'
```

and its hover card, read by dispatching `mouseenter` over CDP:

```
Pane | cats:p1 | Title | ✳ Claude Code | Dir | ~/projs/go/cats |
Agent | claude · idle | Model | claude-opus-5 | Focus | off screen | Window | 112×35 cells | Link | connected
```

Size correctly absent — no local frame for a pane in another workspace.
`gofmt`, `go vet -tags ghostty ./...` and the full `go test -tags ghostty ./...`
are clean.

## Gotchas / notes for next time

- **macOS unix sockets cap at ~104 chars.** The scratchpad path blew past it and
  every dial failed with `connect: invalid argument` — not "socket missing".
  Dev sockets have to live at short paths (`/tmp/cats-dev-*.sock`).
- **`catctl -socket /path` must be two argv entries.** Shoving `C="catctl
  -socket …"` through a shell variable passes it as one flag and prints usage.
  `CATS_CONTROL_SOCKET` is the cleaner handle for scripted runs.
- **Chrome `--dump-dom --virtual-time-budget` races real WebSocket I/O.** A 6s
  budget dumped *before* the `pane.list` reply landed and showed only the
  viewport rows — which looks exactly like the bug you're testing for. 20s was
  reliable; a false alarm cost a round of debugging. Chrome also never exits
  with a live socket, so background it and `pkill` the `--user-data-dir`.
- **Node 22 has a global `WebSocket`** — enough to drive CDP (`/json/list` →
  `Runtime.evaluate`) with no npm install. That's how the hover card was read;
  `--dump-dom` can't trigger hover.
- **`panes.get(id)` is not a visibility test.** `pane()` auto-creates an entry
  from a `pane_title`/`pane_agent` message for a pane that isn't in the layout;
  those have `info: null`. On-screen means **`p && p.info`**. (Note
  `index.html:~2641` still uses bare `panes.has` for notify suppression.)
- **`cats-todo/internal/app/command_vocab.go` is a hand-kept copy** whose header
  asks for lockstep with cats' original. Added `AgentModel` there too (builds +
  tests pass, uncommitted — separate repo, user's call).
- The MacApp needs a rebuilt bundle (`make macapp`, or `make local`) to pick any
  of this up — the recurring stale-bundle trap.
- gopls still flags `cmd/catway/*.go` as excluded (needs `-tags ghostty`), and
  now also flags the cats-todo file as outside the workspace module. Harmless.
