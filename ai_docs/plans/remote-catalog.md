# cats "Remote dream" — the catalog slices (2+)

## Context

`ai_docs/plans/remote-dream.md` shipped slice 1: one catway attached to N cathosts, with
every question about another machine now asked of that machine (cwd, branch, meters,
directory listing, hooks, control, worktrees). Its Appendix A is the rest of the catalog.
This plan is that rest, in the order Appendix A recommends, with the three "quick wins
needing no foundation" pulled to the front because they are exactly that.

Two of the quick wins (`pane.open_file`, `ui.notify`) are also two thirds of the "ced trio",
so shipping them here empties that slot down to file transfer.

Phases 1-3 are the three quick wins and are shipped. Everything below inherits slice 1's rule and it is worth restating, because it decides most
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

### Phase 3 — `pane.open_file` — **DONE**

As built, and the striking thing is how little of it is editor code. cats works out WHICH
pane should hear the request and emits `pane_open_file` on the control stream that pane's
editor is already subscribed to. Three things follow, and each is a thing not written: no
editor CLI (ced's own remote discovery is ced's, and runs on ced's machine); cross-host for
free, because an editor in a remote pane subscribes through the Phase 7 control relay; and
any editor at all, since the whole contract is one event name plus the agent label that
pane reports over the hook API.

Deltas from the plan above:

* **The resolution lives in the dispatcher, not the backend.** Every question it asks is a
  MODEL question — which tab is this pane in, which panes exist, split this one — and the
  two runtime facts it needs already cross the Backend seam for other commands
  (`PaneMeta`'s agent and host). The backend gained exactly two methods: `EditorConfig`
  (configuration, which the dispatcher deliberately holds none of) and `OpenFileIn`
  (the event stream).
* **`pane_open_file` is the first event that is a REQUEST rather than a fact.** Everything
  else in the vocabulary reports something that happened. It is pane-addressed, so an
  editor subscribes filtered to its own pane and nothing else in the session has to care.
* **Nearest wins, ties by pane id.** Anchor's tab → anchor's workspace → anywhere, which is
  the order a person means by "the editor". Ties deliberately do NOT go to focus recency: a
  stable answer means clicking two paths in a row opens both in the same editor, where
  recency would send the second wherever the first click left the focus.
  `Session.PaneNeighbourhood` is the one new model helper.
* **The editor must be on the file's machine**, and an explicitly named one elsewhere is
  refused by name rather than handed a path from another filesystem — the worktree slice's
  "a path is only half an identity" applied to the third thing that reads a file.
* **A spawned editor gets the path in its ARGV**, not as an event. An editor that has not
  started cannot be subscribed to one, and an event sent into that gap is simply lost. The
  cost is the line number — no editor CLI here accepts one — so a cold open lands at the
  top of the file, which is why `spawned` is in the result rather than inferred. Starting
  an editor is starting a process, so it answers to the workspace lock like `tab.create`.
* **`spawn` is per request as well as per config.** A linter walking twenty findings must
  not open twenty editors, and "reveal it if the editor is open" is a different command
  that would otherwise need its own name.
* **The browser surface is the `file://` OSC 8 hyperlink.** Clicking one used to
  `window.open` it, which in a browser means rendering a source file as text — and in a
  multi-host session, a file on a machine the browser cannot see at all. It now routes to
  `pane.open_file` with the pane it was clicked in; every other scheme opens in a tab as
  before. A `file://` URI with a host component is left to the browser to refuse visibly,
  since naming a third machine is not something this side can honour.

Tests added: `internal/app/commands_test.go` (delivery with the path verbatim, the three
rungs of nearest-wins, the spawn's argv with no event emitted into the gap, and six
refusals — no path, unknown host, an explicit editor on another machine, no editor on the
file's machine, spawn disabled per request, locked workspace), `cmd/catway/openfile_test.go`
(the event reaching the editor's own pane subscription and not another's, and the policy
tracking the live config, case-insensitively).

Verified live (catway, two cathosts, a `catctl probe` client, and a fake editor that is a
shell script reporting `agent: ced` over the hook API): with no editor, `catctl open
internal/app/commands.go 412` spawns one whose pane shows `FAKE-EDITOR-OPENED
internal/app/commands.go`; once that pane reports itself an editor and subscribes,
`catctl open '~/projs/go/cats/main.go' 9` is delivered as
`pane_open_file {pane:2, path:"~/projs/go/cats/main.go", line:9}` — path unexpanded — and
spawns nothing; the unknown-host, missing-path and spawn-disabled refusals each name what
is wrong; and with an editor on `devbox` only, a file anchored at the local pane is refused
by host while one anchored at the devbox pane reaches it.

Docs updated: `docs/protocols/control-api.md` (an "Opening a file in the editor" section,
the event row and its request-not-fact note), `docs/reference/configuration.md` (a new
Editor section), `docs/reference/cli.md` (`catctl open`), `config.example.yaml`.
catgen-dart goldens regenerated (`OpenFileParams`/`OpenFileResult`, the new command);
**cats-mobile has not been regenerated** — still owed from slice 1.

### Phase 4 — shell integration + the command ledger — **DONE**

As built. Four pieces: a scanner in the daemon, a subscription on the seam, a store in
catway, and an installer that puts the marks in a shell. The division of labour is the
multi-host rule again — the daemon owns everything only that machine can answer (where the
marks are in the byte stream, the cwd at that instant, how long it took on that clock) and
catway owns everything only the SESSION can answer (which host, the public handle, and
whether a human or an agent was driving).

**The store is `btypedb`, not `bytdb`.** Asked and answered: the module cache holds two
different products from the same author, and the ledger's shape is ordered range scans over
a time-ordered key, which is the typed KV store's, not the relational engine's.

Deltas from the plan above:

* **A command with no command line is not recorded**, in the daemon, before it reaches the
  seam. One rule, three jobs: it drops the empty Enter that every shell emits a full
  A-B-C-D cycle for; it guarantees a row always has the field the ledger exists for; and it
  makes the precondition one sentence rather than a history quietly full of blank rows.
* **The command line rides `OSC 633;E`** — VSCode's spelling — because OSC 133 has no field
  for it and that choice means an existing VSCode shell integration feeds the ledger with
  nothing installed.
* **`exit` is a pointer all the way through.** "Finished, status unknown" is true and
  "succeeded" is not, and this is the field somebody filters *what failed* on. A command
  whose `D` never arrives is closed at the next prompt with no status rather than left open
  forever.
* **Retention is a count, enforced on write**, which is what keeps a backward scan honest.
  The alternative is a query that walks a million rows plus a scan budget that silently
  truncates the answer — the same bug as no bound, but harder to see.
* **The subscription is per connection, not per pane**, and off by default: a client wants
  all of the history or none, and a hole where one pane was is worse than no history.
* `internal/shellint` is a package of its own rather than an `integration.Target`. Those
  wire an agent to a running server by editing that agent's config tree; this edits the
  user's SHELL, in a file they wrote by hand, for three shells with three hook mechanisms.
  They share a CLI verb and nothing else.
* **Nothing in the installer edits a line it did not write.** One guarded block, replaced
  *in place* on reinstall (a user who put ours before their prompt framework did it for a
  reason), and an unterminated marker is treated as "not found" rather than swallowing the
  rest of the file. The indirection through a sourced script means an update never touches
  the rc file at all.

Three things the **live** run found, none of which the unit tests could:

* **`__cats_precmd` was the first row in the ledger.** bash's DEBUG trap fires before every
  simple command *including each one inside `PROMPT_COMMAND`*, so the prompt hook traced
  itself. Fixed by arming the trap only from `precmd` (never at source time), putting
  `precmd` LAST in `PROMPT_COMMAND` so a prompt framework's own work is never traced, and
  ignoring `__cats_*` by name.
* **`cd /tmp; ls x` was recorded twice, at two different directories.** The first draft
  disarmed the trap from inside its own handler, which does not hold: it is armed again by
  the time the second command in a list runs. A flag cleared at the next prompt does hold.
* **`$BASH_COMMAND` is the first SIMPLE command, not the line.** `make && ./run` was
  recorded as `make`. The line is now read back out of `history 1` with parameter expansion
  (no fork per prompt), falling back to `$BASH_COMMAND` when history is off.

A fourth thing the live run *appeared* to find and did not: every record showed
`cwd=/tmp`, which looked like the cwd being captured at the end. It was a persistent
cathost — the same PTY, already `cd`'d, surviving the catway restart. Re-run cleanly, a
`cd /var; echo` records `/tmp` and the next command records `/var`, which is exactly right.

Tests added: `internal/orchestration/osc133_test.go` (the marks out of a mixed stream, split
one byte at a time, both terminators, five things that must NOT parse, the exit-code table,
VSCode's escaping, the buffer bound, and the four pairing rules), `internal/ledger`
(newest-first, every filter, unknown-exit-is-not-a-failure, retention trimming the right
end, no key collision between two hosts in the same nanosecond, reopen, the nil no-op, and
the default/clamped limits), `internal/shellint` (install/uninstall round trip per shell
leaving the rc file byte-identical, idempotent and in-place reinstall, two ways an
uninstall must not guess, the two half-installed states told apart, `$ZDOTDIR`, and every
asset emitting all four marks with an interactive guard),
`internal/orchestration/ledger_test.go` (the capability, silence without a subscription, a
real shell's full cycle reported through a real daemon, the switch turning off, and a
command with no text skipped), `cmd/catway/ledger_test.go` (the record's two halves,
origin from the agent, origin captured at the START, an unpaired end ignored, in-flight
commands dropped with their host, the subscription following the ledger and the
capability, and the two refusals).

Verified live (catway, a persistent cathost, a bash with the real integration installed
into a throwaway `$HOME`): `catctl integration install shell bash` writes the script and one
block, `integration status` lists `shell/bash: current (v1)` beside the agents;
`echo one && echo two` is recorded whole, `cd /tmp; ls /definitely/not/here` once with
`exit=1`, `sleep 1` with `duration_ms=1026`, an empty Enter not at all, and `exec bash`
with no exit status (the abandoned-command rule); `cd /var; echo at-var` records `/tmp` and
the next command records `/var`; the `failed`, `contains` (case-insensitively) and `cwd`
filters each narrow correctly and an unknown host is refused by name; and
`integration uninstall shell bash` leaves the rc file byte-identical to before.

Docs updated: `docs/protocols/control-api.md` (a Command history section and the field
table), `docs/protocols/orchestration-seam.md` (the capability, both messages, the
subscription's shape and why the daemon does the scanning),
`docs/reference/cli.md` (`catctl history` and a "shell target" section under
`catctl integration`), `docs/reference/configuration.md` (a Ledger section),
`config.example.yaml`.
catgen-dart goldens regenerated (`LedgerListParams`/`LedgerEntry`/`LedgerListResult`);
**cats-mobile has not been regenerated** — still owed from slice 1.

### Phase 5a — blocks: jump to a command, copy its output — **DONE**

As built, and the scoping note above was right about the two hard parts and wrong about
the fix for one of them.

**A block is two MARKS, not two row numbers**, and that is the whole design. libghostty's
`TrackedGridRef` is a pin: it follows its cell as the terminal changes and reports
`HasValue() == false` once the row is discarded. Recording screen-buffer rows — what the
scoping note proposed — is wrong in a way that only shows up later: those rows count from
the top of the scrollback, so every line evicted when the buffer wraps shifts them by one,
and a stored row would quietly address somebody else's output. Nothing about the result
would look wrong. With marks, a block whose rows are gone says so.

`internal/terminal` grew a `Mark` interface and `Emulator.MarkCursor()`; the daemon keeps a
bounded ring of 64 blocks per pane (two C-owned pins each, freed on eviction and on pane
teardown, which the emulator's own `Close` does not do); `request_block` / `block_result`
resolve one on demand, id-correlated because two can be in flight for one pane.

Three things the live run found:

* **Every block's text was empty.** `readPump` fed the whole 32 KiB chunk to the emulator
  and *then* scanned it, so every mark in a chunk pinned the same final cursor position.
  The scanner now reports each mark's byte offset and the ledger scan owns the feed,
  interleaving it: feed up to the mark, pin, continue. This was predicted in the scoping
  note and is the part that would have been easy to get subtly wrong instead of visibly.
* **Every block's text ended in a stray `b`.** `OSC 133;D` comes from the shell's *prompt*
  hook, by which point the cursor has moved to the line after the output — so the block
  ended at column 0 of the next line and picked up the first character of `bash-5.2$`. A
  mark at column 0 now ends the block at the end of the row above, which needed the pane's
  width tracked (`p.cols`).
* **The jump landed the block at the BOTTOM of the viewport**, i.e. scrolled to a command
  whose output was entirely off-screen. `BlockResult` carries `top_row` — the viewport's
  *current* top — rather than a buffer total, so the scroll is one signed subtraction
  (`start_row - top_row`) that works upward, downward, and from an already-scrolled pane.

The sidebar gained a **History** section, pushed rather than polled (`history`
down-message, sent on client init and on each recorded command). A command finishing is a
moment only the server knows about; a client polling would either lag the command it is
watching or ask on a timer for a section most sessions never open. The whole recent list
travels rather than a delta — it is 40 short rows, and one message that is always the
complete answer beats a delta protocol the client could fall out of step with. A click
jumps; the context menu copies the output or the command. The section stays hidden until
there is something in it, so a session with no shell integration sees the sidebar it always
had.

`catctl output` prints raw so it pipes, and exits 1 with a stderr line when the block is
gone — an empty stdout that meant "scrolled away" would be indistinguishable from a command
that printed nothing.

Tests added: `internal/orchestration/ledger_test.go` (a block resolving to its own output,
two commands in one pane giving distinct blocks over distinct text, `text:false` skipping
the extraction, and unknown block / unknown pane both answering "not found" rather than
erroring), `cmd/catway/ledger_test.go` (the round trip and its request shape, a gone block
answering `found:false` from `output` but refusing from `jump`, the scroll computed up and
down and skipped when already at the top, an incapable host refused by name, and the block
id being taken from the END so a half-pinned command is not offered).

Verified live (a 12-row pane, so output genuinely scrolls away): three recorded commands
get blocks 1/2/3; `catctl output 1 1` prints exactly `ALPHA-OUTPUT\nsecond-line\n` — byte
checked, with no stray prompt character — and block 2 prints `1`…`30` in full though none
of it is on screen; an unknown block exits 1 saying its output has scrolled out;
`catctl jump 1 1` scrolls the pane so `ALPHA-OUTPUT` sits at the top of the viewport
(confirmed by dumping the grid from a second viewer client before and after); and a viewer
attached across a command sees two `history` pushes — one on init, one when the command was
recorded.

Docs updated: `docs/protocols/control-api.md` (a Blocks subsection under Command history),
`docs/protocols/orchestration-seam.md` (both messages, why marks rather than rows, and the
two implementation consequences), `docs/protocols/browser-protocol.md` (the `history`
push), `docs/reference/cli.md` (`catctl output` / `catctl jump`).
catgen-dart goldens regenerated; **cats-mobile has not been regenerated** — still owed from
slice 1.

### Phase 5b — collapse — **NOT STARTED, and deliberately deferred**

cats renders the grid server-side and ships frames, so "collapse this block" is not a
client-side fold: it would mean the daemon rendering a viewport with rows elided, inside
libghostty's screen model. Worth doing only with that cost understood rather than assumed
away — and the three verbs people actually reach for (jump, copy, send to chat) are shipped
without it.

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
