# Session: The chat panel names its model

- **Session ID:** `e2fcdb25-4841-45c4-9a61-5d8d8f29c1c3`
- **Date:** 2026-08-03
- **Branch:** main (`c1435c8` → `7e474ea`, pushed)
- **Repo:** `cats`

Picks up the "Model line empty" open item from
`2026-0803-1434-acp-chat-side-panel.md`, which shipped the panel and noted
that copilot's ACP `session/new` returns no model roster.

## Request

> Address Chat panel model line empty.

## The fact that made it easy (probed live, before writing anything)

A throwaway node client spoke ndjson ACP to a real `copilot --acp` and
answered the one question the design hinged on:

- **copilot's ACP `sessionId` *is* its `~/.copilot/session-state/<id>`
  directory name**, and the directory exists the moment `session/new`
  returns — not lazily at first prompt.
- That session writes the ordinary events: `session.model_change`,
  `session.auto_mode_resolved` (`chosenModel` + `reasoningBucket`), and
  `assistant.message` (`model`). So `copilotModel` — the reader the
  sidebar's pane hover card already uses — answers for chat unchanged,
  effort suffix included.
- **`assistant.message` lands *after* `session/prompt` responds.** The
  probe read the directory the instant the turn ended and found the two
  session events but not the message. This is the whole reason the poll
  below is a schedule and not a single read.
- Confirmed again: `session/new` carries `modes` and `configOptions` but
  **no `models` field** — so `sess.Models.CurrentModelID` is empty for
  copilot and always would have been.

## The shape

```
turn ends ─► Manager.refreshModel()          (loop)
               │ modelSeq++ , captures connSeq
               ▼
             pollModel(seq, gen, attempt)    0 / 400ms / 1.5s / 4s
               │ go read(sessionID)          (off-loop, filesystem)
               ▼
             o.chatModelResolver(def)  ──►  modelResolvers["copilot"].read(root, "", sessionID)
               │ non-empty?                                            ▲
               ▼                                        cwd empty ─────┘ on purpose
             setModel ─► chat_state (only on a real change)
```

- **`BackendDef.ModelAgent`** names the agent whose on-disk history
  describes this backend — the key catway's `modelResolvers` table is
  already keyed by. `""` means "only what ACP tells us". Data, not code,
  same as the rest of the registry.
- **`cmd/catway/chat.go:chatModelResolver`** builds the reader from that
  table plus `o.modelRoots`, returning nil when the agent's home is not on
  this machine (the manager then never polls at all).
- **`Manager.SetModelResolver`** is the injection seam — a setter rather
  than a `New` parameter so `acpchat` stays untagged and its existing
  tests keep their three-arg construction. Reading an agent's history is
  catway's job; the engine only knows there may be a function.

## Design choices worth remembering

- **cwd is passed empty, deliberately.** `copilotModel`/`claudeModel` fall
  back to "newest session started in this directory" when the id misses.
  That fallback is right for a *pane* (whose agent may have no hook
  installed) and wrong for chat, which always knows its own session id —
  taking it would put some pane's model on the chat panel. Exact id or no
  answer. A test pins this.
- **Re-read every turn, not once.** copilot's auto mode routes per turn, so
  turn two may have been answered by a different model than turn one.
- **A retry schedule (0 / 400ms / 1.5s / 4s), because the history is
  written on the agent's clock.** Measured live twice: turn end → model
  broadcast at +400ms both times, i.e. the immediate read misses and the
  second attempt catches it, leaving ~5.5s of unused margin. Giving up
  after the last attempt is fine — the line just keeps its previous value.
- **Two generation counters.** `connSeq` (existing) drops a read belonging
  to a torn-down agent; new `modelSeq` drops one belonging to a turn that
  has since been overtaken. Without the second, a slow read from turn 1
  could overwrite turn 2's answer.
- **An empty read never clobbers.** Whatever ACP *did* report survives a
  silent history — the on-disk reader only ever upgrades the answer.
- **`teardown` now clears `modelID`.** A dead agent runs no model, and the
  next one may run a different one; leaving the old name on the status
  line reads as live.
- **No UI change was needed** — `chatApplyState` already rendered
  `st.model` through `modelLabel`.
- Also polled after the handshake. Useless for a fresh session (nothing
  has answered yet), but it is the hook `session/load` will want.

## Verification

- **Unit, `-race`**: `acpchat` — resolution + per-turn re-read + the
  broadcast actually carrying it + the reader only ever seeing the session
  id; silence keeps the ACP model and stops polling after the schedule; a
  read in flight across a teardown is dropped. `cmd/catway` — the resolver
  reads by exact id, never borrows a pane's session in the same cwd, and
  is nil when the agent's home is absent.
- `make check` green (vet both tags, vocab routing, Dart goldens).
- **Live e2e, kept**: `cmd/catway/live_chat_model_test.go` drives the real
  manager + real `copilot --acp` + real reader, skipped unless
  `CATS_LIVE_COPILOT=1`. Its doc comment records the measured timeline:

  ```
  +0.00s  starting
  +2.22s  ready   (handshake)
  +6.53s  ready   (turn done — a read taken now finds nothing)
  +6.93s  ready   model="gpt-5-mini · medium"   (the 400ms retry)
  ```

## Pushed

| Commit | Change |
|---|---|
| `7e474ea` | `feat(acpchat): name the chat panel's model from the agent's history` |

## Open

Unchanged from the previous session except the model line, now closed:

- **`make macapp` still not run** — third session carrying this; the
  installed app predates copilot support, the chat panel, and this.
- **cats-mobile is behind** — the chat messages/commands still need its
  `tool/regen.sh` ritual. No CI enforces the mirror.
- **Deferred stack**: claude-code-acp/gemini entries + backend picker
  (each would want its own `ModelAgent` — "claude" already resolves);
  `chat:` config section; thoughts rendering; slash-command autocomplete;
  usage display; panel drag-resize; `session/load` restore; per-workspace
  sessions; fs/terminal caps; MCP declaration; cats-mobile chat UI. ACP
  modes ignored permanently (broken upstream, copilot-cli#2942).
- The model is only re-read at turn boundaries, so during the first turn
  the line is empty. `session.auto_mode_resolved` lands early in the turn
  and names `chosenModel`, so a mid-turn resolution is possible later —
  it would mean teaching the reader to accept that event as a model
  source, not just as an effort source.
