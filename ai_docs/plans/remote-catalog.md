# cats "Remote dream" — the catalog slices (2+)

## Context

`ai_docs/plans/remote-dream.md` shipped slice 1: one catway attached to N cathosts, with
every question about another machine now asked of that machine (cwd, branch, meters,
directory listing, hooks, control, worktrees). Its Appendix A is the rest of the catalog.
This plan is that rest, in the order Appendix A recommends, with the three "quick wins
needing no foundation" pulled to the front because they are exactly that.

Two of the quick wins (`pane.open_file`, `ui.notify`) are also two thirds of the "ced trio",
so shipping them here empties that slot down to file transfer.

Everything below inherits slice 1's rule and it is worth restating, because it decides most
of the arguments in this document: **a question about another machine is answered by that
machine.** A pane's screen, its files, its editor and its shell all live where the pane
lives, so anything this plan reads or runs per pane is a cathost capability with a catway
fallback only for the local host.

## Verified facts that shape the design

- `cmd/catway/notify.go` (build tag `ghostty`) already funnels every notification through
  **`notifyAll(n, agent, msg)`** — browser broadcast, `pane_notify` control event, push
  bridge — and its comment states the invariant this slice relies on: "a second
  notification source added later must be unable to reach browsers without also reaching
  the phone". Today it has exactly one caller, `publishAgent`.
- `internal/push` is untagged, never calls back, and renders ntfy's **header API**
  (`Title`, `Priority`, `Tags`, `Click`). ntfy's action buttons are one more header,
  `Actions`, in the same shape. `push.Config` already carries `ClickURL`, `Kinds`,
  `Priority`, `MinInterval`; `Token` comes from `CATS_PUSH_TOKEN` and never from the config
  file (`config.set` would write it back to disk).
- `browserproto.Notify{Kind,Message,Body,Pane,Pub}` is the toast; `app.PaneNotifyEvent` is
  its control-API twin. Neither carries actions.
- The HTTP surface is three routes on one rweb server (`cmd/catway/main.go:319`) behind
  `authGuard.middleware`, whose public-path list is `/login` and `/favicon.ico`. A bearer
  token or the session cookie is required for everything else, and `/ws` additionally
  requires same-origin.
- Events: `internal/app/events.go` is the whole streaming vocabulary; a subscription may
  filter by pane, and `EventThemeChanged` set the precedent for a **session-scoped** event
  (pane 0, invisible to a pane-filtered subscriber).
- cats has **no notion of an editor** — grep finds `ced` nowhere in the Go tree. ced
  identifies itself the other way round: it reports `agent: "ced"` over the hook API
  (ced plan §5 ask 5, verified), and it already runs as a control-socket client inside its
  pane, which since slice 1 Phase 7 works on a remote host too.
- `ced --remote <file>` hands a file to an instance **rooted at a directory containing it**
  (longest root wins), sockets are per-process under `$XDG_RUNTIME_DIR/ced`, and
  `ErrNoInstance` is a fallback rather than a failure. That discovery is ced's and runs on
  ced's machine; nothing here should reimplement it.
- OSC scanning is a settled pattern in the daemon: `osc.go` (7, cwd), `osc9.go` (progress),
  `osc52.go` (clipboard), `osctitle.go` (0/2) are hand-rolled scanners over the raw PTY
  bytes in `readPump`, because libghostty-vt surfaces only title. OSC 133 belongs beside
  them as `osc133.go`.
- `internal/integration` is the installer subsystem (`catctl integration install <agent>`)
  with embedded assets per agent and `CATS_INTEGRATION_ID` / `CATS_INTEGRATION_VERSION`
  markers that install and status logic key off. A shell integration is a new target in it,
  not a new subsystem.
- Storage rule for anything durable added here: `github.com/rohanthewiz/bytdb` unless
  stated otherwise (user's global preference), not a hand-rolled JSONL file.
- Wire-struct changes ⇒ regen `cmd/catgen-dart/testdata/golden`, then cats-mobile per
  memory (which is **still owed** from slice 1 phases 6 and 8 — cats must be pushed first).

## Key decisions

| # | Decision | Choice |
|---|---|---|
| 1 | What a notification action *is* | A **declared effect plus an announcement**: `{id, label, send?, submit?}`. Catway performs `send` itself (`SendInput`) and always emits a `ui_action` event. Declarative because the interesting caller is a hook script that has already exited by the time anyone taps the button — an action that meant "call me back" would be dead on arrival, which is the whole point of the phone case. |
| 2 | Who may raise a notification | `ui.notify` is an ordinary §7 command, so anything holding the control socket — including a remote pane through the Phase 7 relay. No new privilege: a caller that can `pane.send_input` can already do everything an action does, by a longer route (the `clipboard.read` argument, restated). |
| 3 | Push kinds | `ui.notify` may choose `attention` / `finished` / `info`; `info` is new and is **not** in the default `push.kinds`, so a plugin that narrates itself cannot start vibrating a phone by existing. |
| 4 | Inbound action delivery | One new route, `POST /api/notify-action`, carrying a **single-use, per-action, expiring token** in the path rather than the catway password. The ntfy server relays the request and therefore sees whatever credential rides it; a token that answers exactly one prompt once is the only kind worth showing it. Off unless `push.actions: true`. |
| 5 | Where the prompt's options come from | Captured from the pane's own screen at notify time and parsed (`internal/promptopts`) — the options are on screen because a human is meant to read them, and the alternative (every agent's hook asset learning to report its menu) is a fleet upgrade for a feature that would still have to handle the agents that never send one. Nothing parsed ⇒ no action buttons, never a guess. |
| 6 | `pane.open_file` transport | An **event**, `pane_open_file`, not an exec: catway resolves which editor pane should receive it and emits it on the control stream that editor is already subscribed to. Cross-host is then free (Phase 7 relay), and cats never learns an editor's CLI. Spawn-if-absent is the one case that does exec, and it passes the path as **argv** to the new pane rather than racing the editor's startup to deliver an event it is not yet listening for. |
| 7 | Which pane is "the editor" | A pane whose effective agent is in `editor.agents` (default `["ced"]`), on the target host, preferring the most recently focused, scoped to the anchor pane's workspace before the session at large. cats gets a notion of an editor here for the first time, and it is one config key plus a lookup — not an editor subsystem. |
| 8 | Ledger storage | bytdb, keyed `(host, pane, started_at)`, written by catway on the daemon's OSC 133 report. Not JSONL: the interesting queries ("what did I run in this repo", "re-run that on host X") are ordered range scans, which is what the store is for. |
| 9 | Where OSC 133 is parsed | cathost, beside the other four scanners, reported as a seam event behind a `command_ledger` capability. The prompt marks are in the pane's byte stream and the pane's byte stream is on that machine; catway seeing them at all would mean shipping every byte twice. |

## Phases (each independently shippable, tests green)

### Phase 1 — `ui.notify`: notifications anything can raise — **DONE**

As built. `ui.notify` and `ui.action` are two §7 commands over one registry
(`cmd/catway/uinotify.go`), and the registry is the whole feature: an entry holds a
notification's actions until one is taken or it expires, and **the first take drops it**.
Two clients can be showing the same buttons — a browser toast and (phase 2) a lock screen
— and only one of them may answer.

Deltas from the plan above, each found by writing it down:

* **An action is a declared effect, not a callback.** `{id, label, send?, submit?, pane?}`,
  and catway performs `send` itself through the same `SendInput` `pane.send_input` uses.
  A callback shape would have been dead on arrival in the case the feature exists for: the
  caller is a hook script that reported its agent blocked and exited milliseconds before
  anybody saw the notification. `send` may be empty, and then the action is
  announcement-only for a live subscriber.
* **Order inside a take is load-bearing.** The entry is removed *before* the input is
  sent, so an action whose pane exited between the notification and the tap is still
  **spent** — otherwise a phone retrying over a flaky link could land a "yes" twice
  because the first attempt reported an error. The `ui_action` event is emitted after the
  input, so a subscriber reading it knows the effect has happened rather than that it is
  about to.
* **A paneless `send` is refused rather than dropped.** An action with no pane of its own
  falls back to the notification's, and a notification with neither has a button that
  cannot land anywhere. That is a caller bug worth naming.
* **The dispatcher owns shape, the backend owns existence.** Title, kind, labels and
  unique ids are checked in `internal/app` (which also fills an empty action id in from its
  index, so `"1"`, `"2"` … are addresses while labels stay display text); whether a pane
  exists is checked in catway, which holds the panes.
* **The toast changed two rules.** An answerable toast does not auto-dismiss — a button
  that disappears after four seconds is a button nobody can press — and it is never
  suppressed for a visible pane: suppression exists because a toast is redundant with a
  pane you are looking at, and a *button* is not redundant with anything. The first click
  disables the whole row, since a second could only earn a refusal that would look
  identical to the click that worked.
* **`info` is a new kind and is deliberately not pushed.** `config.PushKindInfo` /
  `push.KindInfo` exist so an operator *can* forward it; the default `push.kinds` stays
  attention-only so a plugin narrating itself cannot start vibrating a phone by existing.
* Ids are random (`crypto/rand`, 12 URL-safe chars) rather than sequential: they travel to
  clients, and a guessable id would let one browser answer a notification for a pane it
  cannot see by counting. The registry is bounded (64 entries, 30-minute TTL) and pruned
  on insert.

Tests added: `cmd/catway/uinotify_test.go` (all three destinations reached at once, the
send arriving at a real pipe daemon before the event, single-use, spent-even-when-the-send-
fails, the two pre-registration refusals leaving no entry behind, announcement-only, and
the bound + expiry), `internal/app/commands_test.go` (the validation table, the kind
default, action-id fill-in, and `ui.action` routing), `internal/config` (`info` accepted
but not defaulted).

Verified live (one cathost, one catway, a `catctl probe` client for the viewport, a
`catctl events` subscription for the stream): `catctl notify deploy finished on devbox`
lands as `pane_notify {pane:0, kind:"info"}`; an actionable one carries its `id` and both
actions with the unnamed one auto-numbered `"2"`; `ui.action` on it prints `ok`, emits
`ui_action {source:"control"}`, and the pane's own capture shows the shell ran
`echo ANSWERED-FROM-NOTIFICATION` and printed it; the second take is refused with
"is no longer answerable"; and the paneless-send, unknown-kind and unknown-pane refusals
each name what is wrong.

Docs updated: `docs/protocols/control-api.md` (a Notifications section, the two new rows
in the event table, `ui_action`'s payload), `browser-protocol.md` (actions on `notify` and
the suppression exception), `docs/reference/cli.md` (`catctl notify` + the raw form),
`docs/reference/configuration.md` and `config.example.yaml` (the third kind).
catgen-dart goldens regenerated (`NotifyAction`, the two commands); **cats-mobile has not
been regenerated** — still owed from slice 1 (see memory: cats-mobile regen flow).

### Phase 2 — reply from the notification — **DONE**

As built. Phase 1 gave a notification buttons and one way to press them; this is the other
way — a tap on a lock screen, which is the case the push bridge exists for at all. The
single-use guarantee needed no new machinery: the notification registry already drops
itself on the first take, so a token is spent whichever route reaches it first.

Deltas from the plan above:

* **The push waits; the browser never does.** Deriving an agent's menu means reading its
  screen, which is a round trip to that pane's cathost — so `notifyAll` grew an outbound
  half, `sendPush`, and it is the only step that can block. The broadcast and the event
  have already gone by then, which is `notifyAll`'s own stated rule ("the browser broadcast
  goes first and unconditionally") applied to a slower push rather than bent for it. Every
  way the read can fail — timeout, host gone, pane closed while in flight, nothing
  parseable — still pushes, without buttons.
* **The internal capture goes through the ordinary pending queue** (`funcResponder`, three
  lines) rather than a private path. It gets the timeout, the host-scoped flush and the
  disconnect handling for free; a private path would have needed its own copies of all
  three, which is how the "capture failure swallows the notification" bug gets written.
* **Scope is `recent`, not `visible`.** A pane whose viewport is scrolled up still has its
  prompt at the bottom of the *buffer*, and that is what the agent is waiting on.
* **`internal/promptopts` refuses more than it accepts, and that is the feature.** A menu
  is the last contiguous run of numbered lines with nothing printed after it, numbered from
  1, ascending by 1, at least two entries. Anchoring at the bottom is not an optimisation:
  numbered lines are everywhere in ordinary output, and "nothing came after it" is the only
  thing that distinguishes a live prompt from a list that finished. A run starting at 4 is
  a menu whose head has scrolled off, and offering "4" as the first button would answer a
  prompt whose other options are invisible. Nothing parsed ⇒ no buttons, never a guess.
* **Derived buttons are phone-only**, and the notification that declared its own is never
  second-guessed. A browser is one click from the pane; a second delayed toast carrying the
  same choices would be noise in the one place the prompt is already reachable.
* **POST only.** Notification clients, link previewers and crawlers fetch URLs they are
  shown; a GET that answered a prompt would be answered by whatever prefetched it. Verified
  live: `GET` on a live token is a 404.
* **The route is public in the middleware and authenticated by the token.** A phone holds
  no session cookie, so it cannot be gated there; the token answers one choice on one
  notification once and expires with it, which is the only credential worth showing the
  notification server that relays the request. With `push.actions` off no token exists, so
  every request to the endpoint is refused — the route is still registered, because a stale
  button deserves a straight 409 rather than the login redirect a phone would render as a
  mysterious success.
* **`action_url` is required by `Validate` when `actions` is set** and cannot be derived:
  catway knows the address it bound, not the one a phone on another network would dial.
  Buttons pointing nowhere are worse than none — they look like they worked.
* Labels are quoted unconditionally in the `Actions` header. Agent menu text is
  "Yes, and don't ask again" far more often than not, so a header correct only for the
  labels somebody remembered to test is the kind that breaks on the notification that
  mattered. `clear=true` dismisses the phone's copy, so a pressed button stops inviting the
  second tap catway will refuse.

Tests added: `internal/promptopts` (the two real prompt shapes, ANSI stripping, trailing
blank rows, and every way an ordinary screen can look like a menu — a finished list, a run
that starts at 4, gaps, bare numbers, one option — plus the cap and the label rules),
`internal/push` (the header's exact text, quotes/semicolons/newlines/empty labels, the
fourth button dropped, and the header set only when there are actions),
`cmd/catway/notifyaction_test.go` (the menu reaching the phone with distinct tokens, a tap
answering once from `source:"push"`, an unparseable screen minting nothing, a failed
capture still pushing, the feature off reading no screen, declared actions skipping the
read, tokens dying with their notification, and only `attention` triggering a read),
`internal/config` (`action_url` required, checked, trimmed, and ignored while off).

Verified live (catway + cathost + a stand-in ntfy topic that prints its headers, a
`catctl probe` client for the viewport, and a claude-shaped prompt drawn into a pane
running `read`): reporting the agent blocked produced a push whose `Actions` header carried
all three of the agent's own choices — `"Yes"`, `"Yes, and do not ask again for rg…"`,
`"No, and tell Claude what to do…"` — each with a distinct token under the configured base;
`GET` on one is a 404 and `POST` is `ok`, after which the pane shows `2` and `PICKED=2`;
the second `POST` is `409 this notification is no longer answerable`. Restarted with
`--password`: `/` redirects to `/login` while the action endpoint still answers without a
credential and refuses an invented token, and a token containing a slash is a 404.

Docs updated: `docs/reference/configuration.md` (a new "Answering from the notification"
section with the four properties, the two new keys in the table),
`docs/protocols/control-api.md` (derived buttons in the Notifications section),
`config.example.yaml`.

### Phase 3 — `pane.open_file`

`pane.open_file {path, line?, column?, pane?, host?}`. Resolution, refusals, the
`pane_open_file` event, `editor.agents` / `editor.command` config, spawn-if-absent as a
split carrying the path in its argv, and the browser side: a path in a toast or in the
sidebar becomes clickable.

Ship gate: with ced running in a pane, `catctl open-file --params '{"path":"…","line":42}'`
puts ced on that line; with no editor pane, one is spawned already showing the file; a path
on a remote pane's host reaches that host's editor.

### Phase 4 — shell integration + the command ledger

`catctl integration install shell` (bash/zsh/fish) emitting OSC 133 A/B/C/D with `cmd`,
`exit`, `duration`, plus OSC 7 which is already handled. cathost scans it
(`osc133.go`), reports `command_start` / `command_end` behind a `command_ledger`
capability; catway records `{host, pane, cwd, cmd, exit, duration, origin}` in bytdb.
`origin` is human vs agent, from the pane's live hook/detection state at command start.
`ledger.query` / `ledger.recent` §7 commands; `catctl history`.

### Phase 5 — semantic scrollback blocks

The ledger's marks give the browser block boundaries: collapse, jump to previous/next,
copy just the output, send a block to chat/agent/note. "Explain this failed block" is
`chat.send` with the block as context.

### Phase 6 — runbooks

YAML steps over the §7 vocabulary with params and `on:` triggers (manual, `host.attach`,
agent state, `pane_exited`, cron via cats-todo). Record-a-macro from live use; guardrail
automations.

### Phase 7 — file transfer through cathost

The last third of the ced trio: drag-drop upload into a pane's cwd, `file.get`,
`catctl cp host:path .`. Chunked over the seam behind a `file_transfer` capability.

### Phase 8 — adjacent stars

Record & replay, port preview, agent migration between hosts, presence, the global palette.
Scoped when the phases above have landed and the shape of the ledger is known.

## Verification

- Every phase: `make test` and `make test-ghostty`; regen catgen-dart goldens whenever
  `internal/app`, `browserproto` or `orchestration` wire structs change and
  `go test ./cmd/catgen-dart`; `TestCommandSpecsRouted` for each new command; then
  cats-mobile per memory.
- Phases 1–2 are the only ones with an outbound and an inbound network surface: both get a
  live run against a real ntfy topic, and Phase 2's refusals (spent token, expired token,
  dead pane, actions disabled) are each exercised by hand as well as in tests.
