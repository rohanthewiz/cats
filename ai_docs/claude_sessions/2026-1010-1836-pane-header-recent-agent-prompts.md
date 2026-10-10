# Session: recent agent prompts in the pane header

Session ID: 93af5b34-1694-4b84-8c3d-19ba018c4868
Date: 2026-10-10
Driven from: cats

The request came from the user's cats-todo backlog:

> Somewhere in the pane heading, somehow allow me to see the last few commands
> sent to the agent

## 1. Where the prompts come from

The command ledger (HISTORY) can't answer this. It records what a shell ran,
through OSC 133 marks, and a prompt typed into an agent's TUI never passes
through a shell.

Two sources were possible:

- **Keystrokes** catway forwards to the pane. They hold every edit, every
  ↑-recall and every line abandoned with Esc. They hold nothing for a prompt
  that arrived by paste, by `pane.send_input` (cats-todo), or before catway
  started.
- **The agent's own on-disk history.** cats already reads it to name the
  pane's model (`cmd/catway/agentmodel.go`), and it records what the agent
  actually *received*, from whoever sent it.

The history won. It is the same file as the model, located the same way: the
hook-reported session id, then the host-detected one, then the newest history
under the pane's cwd. So the agents whose prompts can be read are exactly the
ones whose model shows, claude and copilot.

It is a **pull, not a push**. The model rides the agents rollup because every
sidebar row shows it all the time. Prompts are looked at occasionally, on one
pane, so they are read when the menu opens and are never stale.

## 2. The change

```
… · claude opus 5.5 (42k/1M) · idle · prompts ▾
                                      ┌───────────────────────────────┐
                                      │ LAST 5 PROMPTS                │
                                      │ commit and push           5m  │
                                      │ ! git status             15m  │
                                      │ Add a regression test fo…45m  │
                                      │ /model opus               1h  │
                                      └───────────────────────────────┘
```

- **New command `pane.prompts`** (`wire/vocab.go`). It takes an optional pane
  (default: the focused one) and a limit (default 5, clamped to 50). It
  returns `{pane, agent, prompts:[{text, at}], note}`, newest first, and is
  reply-gated like `ledger.list`. The wire change is additive; cats-mobile and
  cats-todo pick it up when they bump their pin.
  - Every "can't answer" case is an OK with a `note`, not a failure: no agent
    in the pane, an agent with no reader, a remote pane (its history is on
    that machine), or no prompts yet. This is the stance `ledger.output` takes
    on a scrolled-away block. Only an unknown pane fails, in the dispatcher.
- **Dispatcher** (`internal/app/commands.go`): new `Backend.PanePrompts`, a
  reply gate first, the pane resolved through the session, and
  `clampPromptLimit`.
- **Readers** (`cmd/catway/agentprompts.go`). `modelResolver` gained a
  `prompts` field, so one table entry per agent covers both its model and its
  prompts. `historySession` was pulled out of `refreshAgentModel` so both
  reads pick the session the same way. `tailLines` became a wrapper over
  `tailLinesN`. The read runs in a goroutine and answers through `o.post`, as
  `path.list` does.
  - The prompt tail is **8 MiB** (the model reads 256 KiB). A prompt is
    followed by a whole turn of tool results, so 256 KiB would often hold only
    the newest prompt.
  - What claude stores as a "user" turn is much more than prompts. These are
    left out: `isSidechain`, `isMeta`, `origin.kind` other than `human`
    (task-notification, peer), `tool_result` blocks, `<local-command-…>`,
    `<bash-stdout>`/`<bash-stderr>`, `<task-notification>`,
    `<system-reminder>`, and `[Request interrupted`. These were surveyed from
    120 real transcripts on this machine.
  - Respelled as typed: `<command-name>/model</command-name>…<command-args>opus`
    becomes `/model opus`, and `<bash-input>git status` becomes
    `! git status`. An image block shows as `[image]`.
  - Copilot: `user.message` events, `data.content` (not the transformed
    copy).
  - Each prompt is capped at 4000 runes, with a `…` marking the cut.
- **Browser.** `js/06-chrome.js` draws a `prompts ▾` segment after the agent
  state on every agent pane; the server's note covers agents it can't read.
  `openPromptsMenu` sends `pane.prompts` and opens the existing context menu
  under the chip. `promptsMenuItems` / `promptRowLabel` shape the rows: one
  line, 72 characters, the full text as tooltip, the bare age figure, and a
  click copies through `clipWrite`.
  - The context menu was reused, not a new popover, because it already does
    viewport clamping and outside-press, Esc and blur dismissal.
  - `js/28-ctxmenu.js` gained three optional item fields, `title`, `hint` (a
    muted right-aligned figure) and `{note}` (an inert sentence for an empty
    list), styled in `css/18-ctxmenu.css`. The chip is muted and brightens on
    hover (`css/12-main.css`).
- **CLI:** `catctl prompts [pane]`. Longer lists go through
  `catctl pane.prompts --params '{"limit":20}'`.
- **Docs:** `docs/protocols/control-api.md` (§ Agent prompt history, plus the
  verb table) and `docs/reference/cli.md`.

## 3. A bug the live run caught

The first version gated lines on the exact bytes `"type":"user"` before
decoding. The fixture, written by Python, has `"type": "user"` with a space,
and the list came back empty with no sign of why. Claude writes compact JSON
today, so real use would have worked, but the gate now looks for the quoted
value `"user"` alone and the decode decides.
`TestLastClaudePromptsIgnoresJSONSpacing` covers it.

## 4. Verification

- Go tests: `TestDispatchPanePromptsResolvesAndClamps` (app);
  `TestLastClaudePromptsFiltersAndRespells`,
  `…ReachesPastTheModelTail`, `…IgnoresJSONSpacing`, `TestClipPrompt`,
  `TestLastCopilotPrompts`, `TestPanePromptsReadsThePaneAgentsHistory` and
  `TestPanePromptsSkipsRemotePanes` (catway). The wire census tests
  (`TestCommandSpecs*`) picked up the new command on their own.
- `jstest/prompts.test.mjs`: 16 assertions.
- `go test -tags ghostty ./...`, `go vet` both ways, gofmt and
  `make jstest` all pass.
- Run against this repo's real transcripts, the reader returned
  `/sw`, the typed prompt, `/sl` and `/clear`, with tool traffic and caveats
  dropped.
- **Live**, in a scratch catway and cathost on 127.0.0.1:8531 with their own
  sockets, `--persist=false`, and `CLAUDE_CONFIG_DIR` pointed at a fixture
  history. The user's own catway on 8499, and the cats this session ran
  inside, were left alone.
  - A copied `/bin/cat` named `claude` was killed by macOS (code signature),
    so the stand-in is a tiny Go binary.
  - `pane.send_input` needs `submit:true`; a `\r` in the text arrives as part
    of a bracketed paste.
  - The header showed `… · claude opus 5.5 (42k/1M) · idle · prompts ▾`. The
    menu opened 2px below the chip, clamped to the viewport, with five rows,
    ages and tooltips.
  - A prompt appended to the history afterwards was at the top a second
    later.
- The claude-in-chrome capture covers only the left ~1154px of a 1536px
  viewport, so the chip at x≈1310 was out of frame. A temporary page zoom of
  0.72 brought it into the screenshot.
- Not checked: a real claude pane, Cats.app's clipboard bridge, and narrow
  panes (N-054).

## Next

Closed: None. Declined: None. Raised: N-054, N-055.
Deferred: None. Promoted: None. Moved: None.
Updated: None. Full list: `ai_docs/todo/next-list.md`.
