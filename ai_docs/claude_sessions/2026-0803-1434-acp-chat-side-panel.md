# Session: An ACP chat side panel, driven by copilot --acp

- **Session ID:** `486e8637-6eaa-41ba-94f0-5e0661fe79f3`
- **Date:** 2026-08-03
- **Branch:** main (`c42abf4` → `063eda6`, pushed)
- **Repos:** `cats` (all changes); `ced` read for the port source

Follows `2026-0803-1026-copilot-cli-support-in-cats.md`, which left the door
open: "the CLI itself also accepts `--acp`, so that door is still open for an
in-app chat surface later."

## Request

> The copilot CLI is pathetic compared to Claude Code. Could we build
> something via the ACP

Plan mode. Three explore agents ran in parallel (ced's ACP code, cats's
seams, the ACP spec + copilot's ACP support), then a Plan agent designed it.
User decisions: **side panel** (not a pane, not a modal), **backend registry
with only copilot wired**, **port ced's transport** (no SDK dependency).

## What already existed to build on

- **ced is a full ACP client.** `ced/internal/lsp` is a protocol-generic
  JSON-RPC transport with an ndjson mode (`NewClientACP`/`StartNDJSON`), and
  its chat panel drives `copilot-language-server --acp`, `claude-code-acp`,
  and `gemini --experimental-acp` through one `{binary, args}` registry. The
  load-bearing detail worth porting exactly: **each inbound agent→client
  request runs on its own goroutine**, so a blocked permission prompt can't
  deadlock against the blocked `session/prompt` call.
- **cats had clean seams**: unknown browserproto `t` is ignored (new messages
  need no version bump); commands are the three-edit vocabulary rule enforced
  by `TestCommandSpecsRouted`; catgen-dart goldens gate drift in `go test`.
- **ACP v1 facts** (researched + live-probed): ndjson JSON-RPC 2.0 over
  stdio, client spawns agent; `initialize` → `session/new {cwd, mcpServers}`
  → `session/prompt` blocks for the whole turn while `session/update`
  notifications stream; declaring `fs:{false,false}, terminal:false` means
  those client methods never arrive — only `session/request_permission`
  needs serving. Copilot 1.0.77: protocolVersion 1, `loadSession:true`,
  auth = "run `copilot login`" via `_meta.terminal-auth`.

## The shape

```
browser chat panel ──chat.send/cancel/permission/clear──► orch loop (Backend)
        ▲                                                     │
        │ chat_state/chat_snapshot/chat_row/chat_delta/       ▼
        │ chat_perm (broadcast, coalesced)          internal/acpchat.Manager
        └────────────────────────────────────                 │ o.post
                                                              ▼
                                                internal/acp.Client (ndjson)
                                                              │ stdio
                                                              ▼
                                                      `copilot --acp`
```

- `internal/acp` — the ported transport (ndjson only; ced's Content-Length
  dialect dropped as dead code here) + typed ACP v1 subset. Additions over
  ced: `dropEnv` (strip `GH_TOKEN`/`GITHUB_TOKEN` so the EMU credential
  store can't be silently shadowed — the exact failure mode the earlier
  session documented) and a stderr→log drain (catway has a real log; agent
  stderr is the only diagnostic when a handshake dies).
- `internal/acpchat` — registry + loop-owned state machine. Untagged on
  purpose so it tests in plain `go test ./...`; bound to catway with just
  `post`/`emit` callbacks. One global session; lazy start on first
  `chat.send`; no auto-restart — a send while dead is the retry gesture;
  generation counter (`connSeq`) drops every stale off-loop callback.
  Owning a subprocess breaks the `plugins.go` "server stays out of the
  subprocess business" rule deliberately — chat is a JSON-RPC peer with
  agent→client requests, which a pane cannot mediate (comment at the top of
  manager.go).
- Protocol: five down types + `CapChat` + four commands. Dart goldens
  regenerated. Skipped `browserproto/cmd.go` aliases, following the
  `usage.refresh` precedent (newest command, no alias).
- `cmd/catway/chat.go` — thin glue; snapshot replay in `registerConn` (the
  agents-rollup slot); teardown in `Shutdown`; cwd = `anchorPaneCwd(nil)`.
- UI: fourth grid column (`--chat-w`, 0px closed so pane math is untouched),
  `✦ chat` statusbar chip gated on the welcome cap, pinned-to-bottom
  autoscroll, `chat_delta` = O(1) text-node append, permission rows with
  inline buttons that collapse into verdicts on every client, sign-in remedy
  rows running `["copilot","login"]` via `tab.create`'s Command seam. Two
  input dams: an early return in `onKey` while focus is inside `#chat`, and
  a guard in the document paste handler (composed/pasted text must never
  reach a PTY).

## Design choices worth remembering

- **Delta coalescing is a correctness feature, not polish**: text appends to
  the loop-owned transcript immediately, but broadcasts flush at 100ms/8KB —
  catway drops clients whose 512-slot out buffer fills, so an uncoalesced
  token stream could disconnect slow browsers.
- **Permissions are transcript rows, not modals**: several can be open at
  once, any client may answer, first answer wins, the loser gets a failed
  cmd_result; turn end / cancel / teardown flush the rest as cancelled (the
  spec requires it — an agent may be unable to end the turn otherwise).
- **`chat_snapshot` replaces everything** on connect and on clear — joining
  mid-conversation (or mid-permission-prompt) needs no request choreography.
- **ChatRow.Role is an open enum** — thoughts-collapsed and friends can ship
  later as new row kinds with zero wire change. Thoughts/plans/usage/mode
  updates are dropped in v1, matching ced.

## Two copilot quirks, found only by running the real thing

The unit fleet (transport pipes, fake ndjson agent) was green before the
live pass; both bugs below were invisible to it and cost one commit
(`063eda6`) after e2e:

1. **`initialize` hard-fails without `clientInfo.version`** (-32602,
   "expected string"). The spec marks it optional; copilot validates it.
   `ClientInfo.Version` is now non-omitempty and always sent.
2. **A cancelled turn answers `stopReason: end_turn`**, not the spec's
   `cancelled` — cancel *works* (turn ends ~20ms after the notification) but
   the agent misreports why. The "— stopped" row now keys off our own
   `cancelSent`, with a unit test pinning the non-compliant shape.

Also probed: copilot's ACP `session/new` returns **no model roster** (the
`models` field ced reads from copilot-language-server is absent), so the
panel's model line stays empty in v1.

## Verification

- Unit, all `-race`: transport (deadlock regression — inbound request during
  a pending call; >1MB single lines; EOF fails pendings; out-of-order id
  correlation), manager (fake ndjson agent over pipes: queued-prompt flush,
  coalescing, tool upserts, permission round trip, cancel, death+restart,
  clear, trim, mid-turn snapshot). One test-harness deadlock along the way:
  the fake agent ran prompt scripts on its read loop, so a script waiting
  for `session/cancel` blocked the loop that would deliver it — scripts now
  run on their own goroutine, same rule as the real client.
- `make check` green (vocab routing + Dart golden gates included), ghostty
  build + `cmd/catway` tests green.
- **Live e2e** against real `copilot --acp`: isolated cathost+catway on
  `127.0.0.1:8799` (`-auth none`, own sockets, `-persist=false`), driven by
  two node WS clients speaking browserproto. 11/11 + 13/13 checks:
  streamed answers, late-joiner snapshot, cancel → "— stopped" on both
  clients, real permission allow (command ran, output reached the answer)
  and reject (it didn't), kill the copilot child → dead + working "Sign in"
  action row → typing restarts, `ps eww` shows no `GH_TOKEN` in the child.
  e2e script gotchas for next time: `init` must carry `v:1` or the welcome
  is a rejection; waits must *consume* the stream through a cursor (matching
  against full history produced phantom passes); after a restart the first
  `ready` is the handshake, not the turn.

## Pushed

`c42abf4..063eda6` on main:

| Commit | Change |
|---|---|
| `2ccb5a9` | `feat(acp): port ced's ndjson json-rpc client for acp agents` |
| `356d92e` | `feat(proto): chat surface messages and chat.* commands` |
| `c6fe406` | `feat(acpchat): the acp chat engine — session manager and backend registry` |
| `8c59668` | `feat(catway): wire the chat engine into the orchestrator` |
| `b99be8f` | `feat(web): a chat side panel beside the pane grid` |
| `063eda6` | `fix(acpchat): survive two copilot quirks found against the live agent` |

## Open

- **`make macapp` still not run** — carried over twice now; the installed
  app predates copilot support *and* the chat panel.
- **cats-mobile is behind again**: the chat messages/commands regenerate
  into `wire.g.dart`/`commands.g.dart`; run its `tool/regen.sh` ritual
  (re-pins `CATS_REV`). The mirror has no CI enforcing it — third session
  in a row this note appears.
- **Model line empty** in the panel (no roster over ACP). Natural fix: the
  chat session writes `~/.copilot/session-state` like any copilot session,
  so the existing `copilotModel` reader could resolve it once the chat's
  session id is known.
- **Deferred stack** (from the plan): claude-code-acp/gemini entries +
  backend picker; `/model` passthrough works today as plain prompt text;
  `chat:` config section; thoughts rendering; slash-command autocomplete;
  usage display; panel drag-resize; `session/load` restore across catway
  upgrade; per-workspace sessions; fs/terminal caps; MCP declaration;
  cats-mobile chat UI. ACP modes ignored permanently (broken upstream,
  copilot-cli#2942).
- The auth-failure path (credential store missing) was exercised only by
  unit test + child-kill, not by moving `~/.config/github-copilot` on this
  machine — deliberately, since the store is shared with the user's IDEs.
