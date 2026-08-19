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

### Phase 6a — runbooks: the format, the engine, manual run — **DONE**

YAML steps over the §7 vocabulary, in `~/.config/cats/runbooks`, run by
`runbook.list` / `runbook.run` and `catctl runbooks` / `catctl runbook <name> [k=v]`.
Vars with declared defaults, `{{ ref }}` into earlier steps' results, `expect:`,
`continue_on_error:`. Every step is a §7 command; the engine re-enters the same
dispatcher per step and holds no privileged path.

### Phase 6b — `on:` triggers — **DONE**

As built. `on:` accepts an event name, a clause, or a list of either; a clause is
`{event, where, min_interval}`; the firing payload is bound under the reserved
root `event`. Two new session-scoped events had to exist first, plus one to
report the runs nobody is waiting on.

Deltas from the plan above, each found by writing it down or by running it:

* **The event is `host_connected`, not `host_attached`.** `host.attach` edits the
  roster and answers *before a packet has been sent* — the dial has its own retry
  loop — so an event named after the command would fire at a moment that has
  nothing to do with it, and would fire again on every reconnect when nothing was
  attached. Named for the link, emitted on the completed handshake, with
  `host_disconnected` for the other half. They **strictly alternate**
  (`orch.hostLinkUp`): verified live, a killed cathost produces one
  `host_disconnected` across twelve seconds of failed re-dials, not one per
  attempt, and one `host_connected` when it comes back.
* **`app.EventPayload` / a payload table is the enabling change.** The event
  vocabulary was a list of names; triggers need it machine-readable, because
  `where: {exit_cod: 0}` must be a *refusal* rather than a filter that silently
  never matches. Same failure encoding/json's dropped keys cause for params, one
  layer out — and the same fix, one layer out.
* **`runbook.EventMap` fills in what `omitempty` drops.** Marshalling
  `PaneExitedEvent{Pane: 3}` loses `exit_code` entirely, so a filter on
  `exit_code: 0` — the *ordinary successful exit* — would have been the one filter
  that could never match. Fields come from the struct, values through
  encoding/json, so a trigger sees exactly the shape the load check validated.
* **Numbers cross two decoders.** The filter is YAML (`0` is an int), the payload
  is JSON (`0` is a float64). `==` would make every numeric filter silently false.
* **No `{{ }}` for *which* event fired.** `theme_changed`'s payload already has a
  `name` field, so injecting the event name would clobber it; and a runbook has no
  branching, so "react differently per event" is two runbooks. One reserved root,
  no magic.
* **Four protections, and the one that matters is not the obvious one.**
  (1) one run per runbook, dropped never queued; (2) a global cap of 4;
  (3) 10 trigger starts per minute per runbook, then a 5-minute suspension;
  (4) reserve at fire time, start on the loop's next turn. The tight self-loop is
  cut by (1) — verified live: a runbook triggering on `pane_added` whose step is
  `pane.split` stops after **one** iteration, because the pane it made appears
  while the run still holds the slot. Only (3) can stop a *mutual* loop, since A
  and B taking turns are never running at the same time — verified live with
  ping/pong on `pane_added`/`pane_removed`: exactly 20 runs, both suspended, the
  session intact.
* **`runbook_finished` is the answer to the half-failed triggered run.** A
  triggered run has no responder, so without it a run that failed at step 2 would
  leave no trace outside the log. Manual runs emit it too — a stream client should
  not have to know which runs it will be told about — carrying a summary rather
  than the step list, because it answers "did the thing I set up work", not "what
  happened".
* **A manual run of a triggered runbook binds `event` to `{}`**, so
  `{{ event.pane }}` fails at its own step with `event has no field "pane"` and
  exits 1. True, and it stops; the alternative was inventing a value.
* **The listing had to grow `trigger_status`.** Suspended, a run in flight, the
  feature switched off — all invisible daemon state, all producing the identical
  symptom of nothing happening.
* **The index is the one place the "re-scan per call" rule bends.** `runbook.list`
  and `runbook.run` still re-read the directory every call so "edit, run" cannot
  run the previous version; the trigger index cannot, because it is consulted on
  every emitted event and `pane_title` alone fires several times a second. One
  second TTL over a name/size/mtime fingerprint, so a scan that changed nothing
  skips the parse.
* **`runbooks.triggers` is file-only and deliberately not in `config.set`** — a
  runbook's steps are §7 commands, so it could otherwise turn its own triggers
  back on. Read per event, so `catctl reload` takes effect immediately.
* **A drain that hits its round budget posts its own next turn.** Found by the
  live ping/pong: twenty runs need twenty rounds and the budget is eight, so the
  remainder rides to the next loop turn — which in a session that then went
  silent would never come, stranding a reservation whose slot is already held and
  wedging that runbook at "already in flight" for good.

Tests added: `internal/runbook/trigger_test.go` (the three spellings, every
refusal — unknown event, unknown `where` key, unknown clause key, non-scalar
filter, bad duration, self-triggering on `runbook_finished`, a duplicate
unfiltered clause, `event` as a step id, a ref with no `on:`, a ref to a field no
declared event carries — plus any-of matching, the cross-decoder numeric compare,
`EventMap`'s zero fill, and a table check that every event in the vocabulary has
a payload); `cmd/catway/runbooktrigger_test.go` (a trigger running a runbook off
its payload, `where` filtering both ways, the zero exit code, the config switch
both ways with its listing text, the listing's `triggers`, each of the four
protections, the suspension not blocking a manual run, `min_interval`, the
finish event's contents, and that `emitEvent` only *reserves*).

Docs: `control-api.md` (four new event rows, the session-scoped note, the
naming rationale, and a Triggers section), `cli.md`, `configuration.md`
(a Runbooks section), `config.example.yaml`. catgen-dart goldens regenerated
(`RunbookInfo` gained `triggers` / `trigger_status`); **cats-mobile regen owed**.

### Phase 6c — record-a-macro — **NOT STARTED, scoped**

Turn a stretch of live use into a runbook file: do the thing once by hand, then
ask for it back as YAML. 6a and 6b already built everything downstream of that
sentence — the document format, `params:`, step refs, `on:` — so the whole of
6c is upstream of it: there is nothing to record from.

The ledger records *shell* commands via OSC 133 (`internal/ledger`); no journal
of §7 commands exists anywhere. That journal is the work.

#### Where the journal hooks

`app.Dispatcher.Dispatch` and nowhere else. It is the one choke point every
caller already funnels through — browser `cmd`, catctl, the control relay,
plugin binaries, a runbook's own steps, a trigger's run — so a recorder placed
there records the vocabulary rather than one client's use of it. Anywhere else
is a second vocabulary that starts drifting the day it is written.

That it also catches runbook steps is a feature, not an accident: recording
while a runbook runs should produce a runbook that does the same thing.

#### What is recorded, and the flag that decides

Effects, not queries. A macro containing `pane.list` is noise, and the caller
that ran it was looking at something, not doing something.

"Has an effect" is not derivable from the table as it stands — `pane.split`
returns a result and is very much an effect — so it is a **third dispatch
property beside `ReplyRequired` and `ParamsRequired`**, declared per command in
`commandSpecs`. Declared, not inferred, for the reason the other two are: the
next command added must have to answer the question, and a default that guesses
means it never gets asked.

#### The privacy dimension, and why it needs a build-breaking test

`chat.send` carries whatever was typed to an agent, `config.set` carries
whatever value was set, and `pane.send_input` carries keystrokes — which is
simultaneously the most private field in the vocabulary and the one a macro
exists to replay. So redaction is per FIELD, not per command, and it cannot
live in the recorder: it is a property of the params struct, declared beside the
field as a struct tag (`cats:"secret"`), walked by the same reflection
`runbook.EventMap` already uses on event payloads.

The default for an untagged field has to be "recorded" — a recorder that
dropped fields it did not recognise would silently emit runbooks that do not
reproduce what was done, which is 6b's `omitempty` bug wearing a different hat.
Which means the safety has to come from somewhere else: **a table test that
fails the build when a params struct carries a field no classification covers**,
in the manner of `TestCommandSpecsRouted`. The omission fails a test rather
than leaking a secret into a file on disk.

#### Always-on journal, or armed recording

The decision the phase turns on, and the recommendation is the smaller one.

* **Armed** — `runbook.record start` / `stop` / `cancel`, held in memory, never
  written until a name is given. Nothing exists unless somebody asked for it, so
  the privacy question above shrinks to "what goes in the file the user just
  asked for", and the ledger's whole retention/eviction apparatus is not needed.
* **Always-on** — a durable journal with retention, from which a time range is
  sliced ("make a runbook out of the last ten minutes"). Strictly more powerful,
  and strictly worse to own: a durable store of every parameter of every command
  including every chat message, kept by default, on a machine somebody else may
  administer.

Take armed. It is the feature as named — "I did this once, do it again" — and
the always-on journal can be built later on top of the same recorder, at which
point its retention and its redaction have a working implementation to inherit
rather than a design to invent.

#### The hard part: pane ids do not survive the recording

This is why 6c is not an afternoon. A recorded `pane.send_input {pane: 7}`
replays into whatever pane 7 happens to be tomorrow, which is somebody else's
terminal. So the emitter has to **rewrite pane references into step refs**:
a pane produced by an earlier recorded `pane.split` becomes
`{{ steps.<id>.pane }}`, exactly as a hand-written runbook would spell it.

A pane reference the recording did not create has no honest rewrite. The
options are to refuse the recording (naming the step), or to emit the literal id
with a comment saying it will not survive. Refusing is more in keeping with
everything else here — a runbook that loads and then does the wrong thing is the
failure mode the load checks exist to prevent — but the common case is a
recording that starts in an existing pane, so a refusal has to come with the fix
in the same sentence: split first, or record from a fresh tab.

Consecutive `pane.send_input` calls to one pane coalesce into one step. A
keystroke-per-step recording is technically faithful and unreadable, and
readability is the point of emitting YAML rather than a blob.

#### Shape of the work

* `internal/app` — the record flag on `CommandSpec`, the `cats:"secret"` tag on
  the params structs, the classification test, and the recorder hook in
  `Dispatch`
* `internal/runbook` — an emitter (steps → the document the loader accepts) plus
  the pane-ref rewrite, tested by round-tripping through `runbook.Load`
* `cmd/catway` — the armed recorder's state, `runbook.record`, and the write
* Docs: `control-api.md`, `cli.md`, a `runbook.record` verb in catctl

### Phase 7 — file transfer through cathost — **DONE**

The last third of the ced trio. `file.stat` / `file.get` / `file.put` are §7
commands over a `file_transfer` seam capability, `catctl cp` is the loop over
them, and dropping a file on a pane in the browser uploads it into that pane's
cwd on that pane's machine.

Deltas from the plan line above, each found by writing it down:

* **Ranged, not chunked-on-the-seam.** The plan said "chunked over the seam",
  which would have put a streaming protocol — open, pump, close — inside the
  seam. The transports forbid it being that simple anyway: a seam frame caps at
  8 MiB, the control relay caps one client line at 4 MiB, and JSON renders bytes
  as base64 at 4/3 the size. A streaming API would have had to invent its own
  chunking to fit inside those, and would then have owned half-open transfers
  and file descriptors held for a client that went away. So the primitive is
  **stateless and positional** (offset + length), the chunking is the CALLER's
  loop, and the seam carries one request and one answer per chunk. One message
  pair, one capability, nothing held open — and the one-shot case ("read this
  40-line config") needs no loop at all.
* **A whole-file read of a large file is REFUSED, not truncated.** `file.get`
  with neither offset nor length means "the whole file" and answers with an
  error naming the size when it will not fit in one chunk. Handing back the
  first megabyte with `eof:false` would be indistinguishable from the whole file
  to a caller that did not check the flag — and a caller asking for a whole file
  without ranging it is exactly the caller who would not check. The same family
  of bug as 6b's `omitempty`: the failure is silent and looks like success.
* **`more` is inverted on purpose.** A write marks the chunks that are NOT last,
  so the default — no flag — is "this put is the whole file", and the naive
  one-shot caller gets an atomic write for free. The obvious spelling (`final`
  on the last chunk) would have made every flagless call write a part file that
  never lands.
* **Writes go through a part file and rename.** `.name.cats-part`, renamed by
  the chunk that is not `more`. An interrupted transfer therefore leaves a
  visible fragment rather than a truncated file under the name a script is about
  to read — which matters here precisely because the client doing the chunking
  is on the far side of a network that can drop. The overwrite refusal runs on
  every chunk, not only the first, and a refusal removes its own now-dead
  fragment rather than leaving litter.
* **`MaxChunk` is a constant, not a config key.** 1 MiB, in `internal/filexfer`,
  because it is not a preference — it is what the transports allow. A knob would
  let an operator pick a value that makes every transfer through a relayed pane
  fail, with a symptom ("connection closed") a long way from the setting.
* **No config gate at all, deliberately.** The considered `files.enabled` was
  dropped: it would sit on catway while the disk being exposed is the HOST's,
  and it would gate a capability that grants no new privilege — a control-socket
  caller can already `pane.send_input` a `cat` and read the pane back, and a
  peer holding the seam can already spawn arbitrary processes there
  (`create_pane` takes a command and an argv). `path.list` and the worktree
  commands read and write another machine's filesystem with no switch either.
* **Local files take the in-process path**, like `path.list` and unlike the
  worktree commands. The "one implementation" argument that makes a worktree op
  go to the daemon even locally does not apply: a file operation IS
  `internal/filexfer`, so calling it directly is the same code, and it means
  transfer works against a local cathost too old to advertise the capability.
* **The browser never learns the cwd.** A drop sends a bare filename and lets
  `file.put` resolve it against the anchor pane's live cwd on that pane's
  machine. Reading a cwd client-side would have used the value as of the last
  `pane_cwd` event, so a file dropped just after somebody `cd`'d would land in
  the previous directory with nothing to indicate it.
* **`catctl cp` owns its runner** rather than joining the ergonomic verb table,
  because every entry there is one request and this is a loop; and it dispatches
  before the global flag re-parse, because `-f` is its own flag and its operands
  are paths.

Verified live against two cathosts and an isolated catway (own `XDG_CONFIG_HOME`,
own state dir, sockets under `/tmp/f7`): a 3 MB binary copied host→local
byte-identical over three chunks; local→host; host→host; `.` and a trailing
slash as destinations; the overwrite refusal and `-f`; a missing file, a missing
destination directory and an unknown host each refused by name; `~` resolved on
the ANSWERING machine; a relative path resolved against the anchor pane's cwd,
and that anchor correctly DROPPED when the addressed host was not the anchor's;
mode carried, so a copied script stayed executable; a deliberately abandoned put
leaving only `.half.bin.cats-part`, and the next chunk renaming it into place.
The browser half was driven headlessly by a WebSocket client sending the page's
own `file.put` sequence: 2.5 MB into a pane on the remote host in three chunks,
checksum-identical, landing in that pane's cwd — and a second drop of the same
name refused.

Tests added: `internal/filexfer/filexfer_test.go` (stat, path resolution, every
read range including a read at and past EOF, the whole-file refusal and the
clamp, the part-file discipline, both overwrite refusals, the retry truncation,
mode, and a chunked round trip over binary data); `internal/app/commands_test.go`
(params passed through unaltered and unexpanded, the path requirement, negative
ranges, `file.put` running without a reply channel while its two siblings do
not, and the two flag defaults); `cmd/catway/files_test.go` (the round trip to a
remote host, the capability refusal, an unknown host, the anchor-cwd rule in both
directions, a filesystem error becoming the command's error, id-matched replies
answered out of order, a host dropping mid-transfer, and the local in-process
path never touching the seam); `cmd/catctl/cp_test.go` (the scp notation and the
four ways it is ambiguous).

Docs: `orchestration-seam.md` (the message pair, the capability row, a File
transfer section), `control-api.md` (the command rows and a File transfer
section), `cli.md` (a Files block under the verbs). catgen-dart goldens
regenerated — `[]byte` lands as `Uint8List`, so the mobile client gets typed
`file.get` / `file.put` — **cats-mobile regen owed**.

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
