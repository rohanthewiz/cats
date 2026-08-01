# Session: Locking a Workspace Against Plugins and Agents

- **Session ID:** `e39f3666-db60-4390-b27e-1c05f67c1048`
- **Date:** 2026-07-31
- **Branch:** main
- **Repo:** `cats`

## Request

> I would like the ability to kind of lock a workspace so we don't run any
> plugins or agents on it. A dimmed row in the workspace with a gray lock icon
> next to the workspace name should do the trick

## The one scoping question

"Don't run plugins or agents on it" does not name an enforcement point, and the
codebase offers three candidates. Exploration turned up the full list of ways
something automated can act on a workspace:

| Vector | What it is |
|---|---|
| `tab.create` carrying a `command` | how the plugins dialog runs an action (`pluginRunAction`), how `catctl plugin run` works, how an agent gets launched into a fresh pane |
| `pane.send_input` | `catctl send` / `run` — typing into a pane that already exists, i.e. driving an agent that already lives there |
| `pane.split`, bare `tab.create` | plain shells; blocking these stops the *user* working in the workspace |

There is no server-side "run a plugin" command at all — plugin actions are
`tab.create` with an argv, which is why that one line covers both plugins and
agent launches.

The first question offered was rejected in favour of a clarification round; the
answer was:

> block spawns and input injection, keep manual work allowed

So: vectors 1 and 2 refuse, vector 3 is untouched.

## What shipped

### 1. Model + persistence (`internal/workspace/`)

`Workspace.Locked bool` with `SetLocked`, carried through `Snapshot` /
`Restore` as `locked,omitempty`. Durability is the load-bearing part — a
workspace set aside from plugins and agents that quietly reopens on the next
catway restart is a guard that lapses at the one moment nobody is watching for
it. `buildWorkspace` in `persist_test.go` now sets the flag so the round-trip
test covers it.

### 2. Session mutation (`internal/app/session.go`)

```go
func (s *Session) SetWorkspaceLock(id string, locked bool) (bool, error)
```

`id == ""` means the active workspace (the `workspace.close` default). Returns
whether the flag actually *changed*, so a no-op toggle skips the broadcast.

### 3. The command (`internal/app/`)

`CmdWorkspaceLock = "workspace.lock"`, `LockWorkspaceParams{ID, Locked}`, added
to `CommandNames()` — which is what `TestSubcommandRegistryIntegrity` checks, so
forgetting it fails the catctl tests rather than shipping a verb the server
rejects. Routes through `BroadcastLayout()`, the rename/move path, which also
arms the debounced save.

`WorkspaceInfo.Locked` added to both the query result (`workspace.list`) and the
browser layout message (`browserproto`).

### 4. Enforcement (`internal/app/commands.go`)

Two guards, both refusing before any session mutation or backend effect:

```go
case CmdTabCreate:
    if len(p.Command) > 0 {
        if ws := d.session.ActiveWorkspace(); ws != nil && ws.Locked { … }
    }

case CmdPaneSendInput:
    if ws := d.session.PaneWorkspace(layout.PaneID(p.Pane)); ws != nil && ws.Locked { … }
```

`tab.create` targets the *active* workspace; `pane.send_input` resolves the
target pane's owner, so it holds for a locked workspace the caller is not
looking at. Messages name the attempted verb —
`workspace w1 is locked: cannot run a command in it (unlock it first)` — because
the caller is often a script that never saw the sidebar's lock mark.

### 5. CLI (`cmd/catctl/subcommands.go`)

`lock-ws [id]` / `unlock-ws [id]`: two verbs over one command, the way
`send`/`run` both reach `pane.send_input`. `unlock-ws w2` is what someone goes
looking for; `--locked=false` on a lock verb is not.

### 6. Front end (`cmd/catway/web/index.html`)

- `lockMark()` — a 12px padlock SVG (shackle stroke + filled body, so it reads
  as *shut* in a row that is otherwise dimmed out), built the same way
  `todoMark()` is.
- The row takes `.locked`; `#ws-list li.ws.locked > :not(.lock-mark)` dims to
  50%, which deliberately **exempts the lock** — it is the one thing in the row
  that just became more true.
- The lock sits closest to the name, ahead of the todo mark: it qualifies the
  workspace (what may be started in it) where the todo mark reports on the
  project inside.
- `toggleWorkspaceLock()` wired into the workspace context menu and the command
  palette. No confirm either way — nothing is destroyed, and locking is a thing
  you do *in order to work*, so it costs one click.

The lock takes `--muted` rather than a literal gray, so it tracks the theme the
way the rest of the sidebar chrome does. In the default theme that resolves to
`rgb(157,176,162)` — the same tone `+ workspace` wears.

## Verification

Ran the real stack, not just the suite. A throwaway `cathost` + `catway` on
`127.0.0.1:8499` with `--auth none`, its own sockets and its own `--state-dir`,
so the user's running MacApp instance was never touched.

**Unix socket path length matters:** the scratchpad path is over the 104-char
`sun_path` limit and cathost failed with `connect: invalid argument`. Short
`/tmp/ctsv-*.sock` paths fixed it. Worth remembering for any future local run.

Driven with `catctl` against the control socket:

| Attempt against locked `w1` | Result |
|---|---|
| `run 1 "echo hi"` | refused — `cannot send input to a pane in it` |
| `tab.create --params '{"command":[…]}'` | refused — `cannot run a command in it` |
| `new-tab` | ok |
| `split h` | ok |
| same launch in unlocked `w2` | ok |
| `unlock-ws w1` then `run` | ok |

Restarted catway against the same state dir: `"locked":true` was in
`session.json` and both workspaces came back locked.

**Screenshot** via a zero-dep CDP driver (`shot.js`) — node 22 has a global
`WebSocket`, and Chrome is installed, so headless Chrome + `Page.navigate` +
`Page.captureScreenshot` needs no Playwright. It also evaluates the rendered
rows, so the assertion is not purely visual:

```json
[{"text":"● cats","locked":true,"lock":true,
  "lockTitle":"locked — plugins and agents cannot be launched here",
  "nameOpacity":"0.5","lockColor":"rgb(157, 176, 162)"},
 {"text":"○ api rewrite","locked":false,"lock":false,"nameOpacity":"1"}]
```

`make fmt-check`, `go vet ./...`, `go test ./...`, `make test-ghostty` all
clean. JS syntax checked with `node --check` on the extracted script block.

## Known limits (stated, not hidden)

- **Guardrail, not a permission boundary.** `workspace.lock` is an ordinary
  control-API command, so an agent holding `catctl` can unlock and then act.
  It stops accidents, not a determined caller. Documented as such in
  `docs/protocols/control-api.md`.
- **A human typing `claude` into a shell in a locked workspace still starts an
  agent.** Nothing server-side sees that, and blocking it would mean blocking
  the keyboard. The lock covers what arrives over the control API.

## Open nit

When the locked workspace is also the *active* one, dimming makes it read as
less prominent than an inactive unlocked row — the `●` bullet stays the only
active marker. It is exactly the "dimmed row" that was asked for, but the
inversion is real. One number to turn: the `opacity:.5` on
`#ws-list li.ws.locked > :not(.lock-mark)`.

## Notes for next time

- `CommandNames()` in `command_vocab.go` is a hand-maintained list. Any new
  `Cmd*` constant must be added there or catctl's registry-integrity test fails
  — which is the intended tripwire, not an obstacle.
- Plugin actions have no dedicated command. If a future guard needs to catch
  "a plugin is being run", `tab.create` + `Command` is still the only signal;
  `CATS_PLUGIN_ID` reaches the process env but never the pane record.
- The CDP screenshot script is a genuinely cheap way to see this front end.
  Chrome + node's global `WebSocket` is the whole dependency list.
