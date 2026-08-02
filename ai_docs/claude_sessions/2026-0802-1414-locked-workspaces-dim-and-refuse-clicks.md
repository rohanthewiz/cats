# Session: a locked workspace dims its agents, and stops taking clicks

- **Session ID:** `f62bdaa8-ad7a-4b4b-a986-6a661d89936d`
- **Date:** 2026-08-02
- **Branch:** main (from `1af3358`)
- **Repo:** `cats`

## Request

Three messages, each widening the last:

> When a workspace is locked dim any corresponding agents in the AGENTS section

> For locked workspaces go ahead and also disallow switching to the workspace
> with click under WORKSPACES

> Also block clicking dimmed agent rows in locked workspaces

The third one closed a hole the second one opened, and it was already on the
table — see "the gap that was flagged" below.

---

## Where the lock lives, and why AGENTS has to ask

The lock is a field on the **layout** message (`browserproto.Workspace.Locked`,
`down.go:103`), and WORKSPACES rows are built straight from that message, so
`renderWorkspaces` already had `w.locked` in hand.

AGENTS is built from a different message. The `agents` rollup is its own
broadcast — and notably the *only* chrome message that is not gated on pane
visibility (see the table in the 2026-0729 sidebar session) — whose items carry
`workspace` as an id, not a lock flag:

```go
type AgentItem struct {
    Pane      uint32 `json:"pane"`
    Pub       string `json:"pub"`
    Workspace string `json:"workspace"`
    ...
}
```

So an AGENTS row cannot read the lock off its own data. It looks the id up
against the cached layout, via a new sibling to the existing `wsName`:

```js
function wsLocked(id) {
  const w = layoutMsg && layoutMsg.workspaces.find((x) => x.id === id);
  return !!(w && w.locked);
}
```

Unknown workspace (no layout yet) reads as unlocked. That is safe because of the
next part.

## The two-message split, and the re-mark it forces

A lock flip goes out as a **layout** rebroadcast, not as an agents rollup:

```go
case CmdWorkspaceLock:
    changed, err := d.session.SetWorkspaceLock(p.ID, p.Locked)
    ...
    if changed {
        d.backend.BroadcastLayout()   // pane set untouched — the rename/move path
    }
```

Which means: toggle a lock and the AGENTS list receives nothing. Dimming applied
only at row-build time would sit stale until the next unrelated state change.

This is the *same* split that already forced `markFocusedAgent` to exist — focus
moves also arrive in the layout, not the rollup — so the fix copies that shape
rather than inventing one. Each row remembers its workspace id, and `applyLayout`
re-marks in place:

```js
li.dataset.ws = it.workspace;         // alongside the existing dataset.pane
...
function markLockedAgents() {
  for (const li of agentListEl.children) {
    if (li.dataset.ws) setAgentLocked(li, wsLocked(li.dataset.ws));
  }
}
```

Re-running `renderAgents` would have rebuilt the list *and* re-queried the pane
inventory for what is a class change — precisely the cost the `markFocusedAgent`
comment warns about.

```
workspace.lock ──► BroadcastLayout ──► applyLayout ──► markFocusedAgent()
                                                   ├─► markLockedAgents()   ← new
                                                   ├─► refreshPaneList()
                                                   └─► renderTabbar()

agents rollup  ──► renderAgents ──► rows built, each stamped via wsLocked()
```

## The gap that was flagged, then closed

After the WORKSPACES click was blocked, the summary called out a remaining route
in — and the third message asked for it. The reason it matters is in the comment
that was already sitting above that handler:

> `agent.focus`, not `pane.focus`: the agents list is global, so the target may
> sit in another workspace/tab and **has to be revealed into the viewport**.

Revealing a pane *is* a workspace switch. Leaving the agent row clickable would
have made the dimmed row the way around the refusal one section above it. The two
refusals are one refusal.

The check is asked **at click time**, not read off the row's `wslocked` class:

```js
li.addEventListener("click", () => {
  if (wsLocked(it.workspace)) {
    toast(wsName(it.workspace) + " is locked — unlock it to reach this agent");
    return;
  }
  sendCmd("agent.focus", { pane: it.pane });
});
```

A lock lifted a moment ago is then honoured without waiting for the row to be
rebuilt — the class is for painting, the closure is for deciding.

## Decisions worth keeping

**A silent no-op reads as a broken row.** Both refusals toast, and both name the
way out ("unlock it to switch" / "unlock it to reach this agent") rather than
just reporting the state. The row's own context menu holds the unlock.

**`cursor:default`, not `not-allowed`.** A locked workspace row still renames,
still reorders, still opens its menu. Only one gesture is off, and the plain
arrow says "not a button" without overclaiming.

**Dimming lifts on hover.** `#agent-list li.agent.wslocked:hover { opacity:1; }`
— whatever the row is dimmed *for*, the user is reading it now, and `.ameta` is
the smallest type in the sidebar.

**Scoped to clicks; deliberate routes stay open.** The context menu, the command
palette and the keyboard all still switch into a locked workspace, and the server
still accepts both commands. This narrows the accident, not the workspace — the
lock is a guardrail, and `workspace.lock` is itself an ordinary command that
anything holding the control API can lift.

**Tooltips lead with the consequence.** The agent row says "clicking will not
reveal this agent", not "workspace locked — no plugins or agents". The padlock
that carries the definition is a section away; the row states what the user is
about to run into.

## Files

| file | change |
|---|---|
| `cmd/catway/web/index.html` | `wsLocked()`, `setAgentLocked()`, `markLockedAgents()`; both click handlers; `.wslocked` + locked-row cursor CSS |
| `docs/reference/cli.md` | `lock-ws` prose — dimmed agents, both click refusals |
| `docs/protocols/control-api.md` | same, plus the explicit "presentation only" note |

Both docs previously claimed "anything you do in the browser [is] untouched" by
the lock. That is no longer true, and the correction says plainly that the new
behaviour is front-end only: `workspace.focus` and `agent.focus` on a locked
workspace remain ordinary commands that succeed.

## Verification

- `go build ./...`; `go test -count=1 ./cmd/catway/ ./internal/app/` — pass.
- Page script extracted from the `<script>` block and `node --check`ed — clean.
  (Worth confirming the extraction is non-empty: `--check` passes on an empty
  file too.)
- **Not** verified visually. The running catway is the installed `Cats.app`
  serving the *old* embedded page, and a fresh instance needs its own cathost
  daemon — not worth starting against a live session unasked. The next person in
  front of a rebuilt binary should confirm: lock a workspace with a running agent
  in it, watch both rows dim in the same breath, click each, and unlock to see
  the dimming lift without a rollup arriving.

## CSS specificity note

`#agent-list li.agent.wslocked` (1 id, 2 classes, 1 type) beats the shared
`#pane-list li, #agent-list li { cursor:pointer; }` — which is what lets the
locked row drop its pointer. The `:hover` variant adds a pseudo-class and so
wins the opacity back. Worth remembering if these rules ever move.
