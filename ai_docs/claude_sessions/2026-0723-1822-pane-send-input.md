# pane.send_input: text injection command (herdr-todo integration, phase 1)

- **Session ID:** `be0cac39-7eac-4693-93c5-c836ca26ca48`
- **Date:** 2026-07-23 18:22
- **Branch:** `main`
- **Scope:** `internal/app`, `internal/browserproto`, `cmd/catway`, `cmd/catctl`, docs

## Request

Started as a question: "Do we have plugin support?" — Cats has outbound
integration installers (`internal/integration` drops hook/plugin assets into 12
coding agents) but no plugin *host*. The user then asked how to integrate
[herdr-todo](https://github.com/rohanthewiz/herdr-todo), a prompt-backlog
Bubble Tea TUI originally built as a plugin for the Rust herdr: it fuzzy-picks
a saved prompt and "drops" it into a Claude Code pane (paste-only or run-now).

### Gap analysis (from reading herdr-todo + Cats)

herdr-todo needs from its host: a plugin lifecycle (manifest, install/link,
actions, plugin panes) and a socket API (`pane.send_input`, `tab.create`, pane
focus, agent-tagged panes). Cats already had the ctlproto control socket with
`tab.create`, `pane.focus`, `capture`, `pane.wait_for_output`, and agent
detection (`internal/detect`, `pane_agent` events) — but **no input injection**
(input only flowed through the browser WebSocket as structured key events) and
no plugin host.

Agreed plan: phase 1 = add `pane.send_input` (this session); phase 2 = port
herdr-todo's client to ctlproto as a standalone `cats-todo` binary; a plugin
host is optional later work.

## What changed

### 1. §7 vocabulary (`internal/app/command_vocab.go`)

- `CmdPaneSendInput = "pane.send_input"`, added to `CommandNames()`.
- `SendInputParams{Pane uint32; Text string; Submit bool}` + `Validate()`
  (rejects empty-text + no-submit as "nothing to send"). Submit is separate
  from Text so a caller can stage input for review (paste-only) or fire it;
  empty-Text + Submit = bare Enter, submitting previously staged input.

### 2. Dispatcher + Backend seam (`internal/app/commands.go`)

- New `Backend.SendInput(pane uint32, text string, submit bool) error` —
  synchronous like `ScrollPane` (fire-and-forget daemon write; success means
  "encoded and sent", not "the app consumed it").
- `case CmdPaneSendInput`: decode → `Validate()` → gate on `PaneExists` and
  `DaemonConnected` (like read/capture — the daemon write path drops silently
  when disconnected, and a vanished prompt is worse than an error) → backend →
  `r.OK(nil)`.

### 3. orch implementation (`cmd/catway/catway.go`, ghostty-tagged)

`func (o *orch) SendInput(...)` in the app.Backend adapters block:

- Addressed by pane id (not focus, unlike browser Key/Paste handling) — an
  automation client targets panes the user isn't looking at.
- Guards unknown pane and `rt.exited != nil`.
- Text → `rt.enc.Paste(text)` (per-pane encoder): bracketed-paste wrapping +
  ghostty control-byte sanitizing track the foreground app's live modes, so a
  multi-line prompt lands intact in a TUI input (readline/Claude Code) instead
  of executing line-by-line.
- Submit → synthesizes Enter press+release via `rt.enc.Key(browserproto.Key{
  Code:"Enter", Key:"Enter", Kind:...})` — adapts to kitty/modifyOtherKeys/
  DECCKM like a real browser keystroke; the release usually encodes to nothing
  and is skipped (exists for kitty report-event-types apps).
- Writes via `o.daemon.send(orchestration.NewInput(rt.id, b))`.

Because it routes through the shared §7 command table, the command works from
the control socket *and* the browser WebSocket (`browserproto.Cmd` routes by
name) with no extra wiring. `internal/browserproto/cmd.go` got the usual
re-exports (`CmdPaneSendInput`, `SendInputParams`).

### 4. catctl verbs (`cmd/catctl/subcommands.go`)

- `send <pane> <text...>` — stage only (tmux-send-keys-style; review first).
- `run <pane> [text...]` — text + Enter; bare `run <pane>` = just Enter.

**Gotcha found live:** originally built as `send [-r] <pane> <text...>`, but
catctl's `main.go` re-parses post-verb args through the global FlagSet
(`flag.CommandLine.Parse(rest[1:])`), so a leading `-r` operand dies with
"flag provided but not defined". Two verbs sidestep that (and read better).
Non-leading dash operands (e.g. `send 1 ls -la`) are fine — flag parsing stops
at the first non-flag arg.

### 5. Tests

- `internal/app/commands_test.go`: `fakeBackend.SendInput` (+ `sendErr`/
  `lastSend`), `TestDispatchSendInput` (forwarding + sync ack),
  `TestDispatchSendInputEmpty` (submit-only ok; empty send = bad params before
  the backend), `TestDispatchSendInputGated` (unknown pane / daemon down /
  backend error). `TestCommandNamesAllRouted` + catctl's
  `TestSubcommandRegistryIntegrity` enforce vocabulary drift for free.
- `cmd/catctl/subcommands_test.go`: `TestBuildSendRun` covering both builders.

### 6. Docs

- `ai_docs/phase-c-ws9-protocol.md`: "since added" note under §7 (worktree ops
  + `pane.send_input` shape/semantics).
- `README.md`: `catctl send` / `catctl run` examples.

## Verification

- `make check` clean (fmt, vet, untagged tests, ghostty-tagged race tests).
- Live smoke test: scratch `cathost` + `catway` on private sockets
  (`--auth password`, `-hook-socket none`), then via `catctl`:
  `send 1 echo ...` left the text staged un-executed at the prompt (confirmed
  by `capture` scope 0), and a bare `run 1` submitted it — output appeared,
  `wait` matched.
- Unix-socket lesson: the session scratchpad path exceeded macOS `sun_path`
  (~104 bytes) → `connect: invalid argument`; short `/tmp` socket paths fix it.
- **Persistence caution:** the scratch catway used the default state dir
  (`$XDG_STATE_HOME/cats`) with `-persist` on by default — it restored the real
  saved session, and its debounced saves appended the two test `echo` lines to
  the persisted scrollback (`history.json`). Layout state round-tripped
  unchanged; the smoke processes were SIGKILLed to avoid the shutdown save.
  Cosmetic only, but future smoke tests should pass `-state-dir` to a scratch
  location (or `-persist=false`).

## Follow-ups (phase 2, saved to auto-memory as `herdr-todo-integration`)

- Port herdr-todo → `cats-todo`: swap its `herdr.go` JSON-RPC client for the
  ctlproto envelope (newline-framed `{id, method, params}` → `{ok, error,
  data}`), rename runtime bits (`CATS_PANE_ID`, `.herdr-todo/` dir), run its
  TUI directly in a shell pane.
- Optional: `agent` field on `pane.list`'s `PaneInfo` (agent identity currently
  only via `events.subscribe` `pane_agent` events).
- Optional/larger: a real plugin host (manifest, install/link, actions, plugin
  panes) if first-class `cats plugin install` is wanted.
