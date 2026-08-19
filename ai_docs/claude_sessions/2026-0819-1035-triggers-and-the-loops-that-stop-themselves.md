# Session: `on:` triggers, and the loops that stop themselves

- Session id: `bcecb95c-ce6c-4dba-a6dd-80637f920c45`
- Date: 2026-08-19
- Branch: `main`
- Plan/record: `ai_docs/plans/remote-catalog.md` (Phase 6b)
- Predecessor: `2026-0818-1949-runbooks-and-the-generators-blind-spot.md`

The ask was "start phase 6b — `on:` triggers". It is done, verified live, and
recorded in the plan. Phase 6a shipped the format deliberately first so that the
protections this phase needed could not be baked into it wrongly; that turned out
to be the right split, because two of the four protections changed shape once
they met a running session.

## Part 1 — the events that had to exist first

**`host_attached` is the wrong name, not just a missing event.** The plan noted
there was no such event to trigger on. Reading `HostAttach` says why the obvious
fix is wrong: the command edits the roster and answers *before a single packet
has been sent*, because the dial has its own retry loop. An event named after the
command would therefore fire at a moment that has nothing to do with it — and
would fire again on every reconnect, when nothing was attached at all.

So the pair is `host_connected` / `host_disconnected`, named for the **link**:
the handshake completing and the pump returning, which is when the machine became
usable and when it stopped being. They **strictly alternate** (`orch.hostLinkUp`),
which is not a nicety: a box that is switched off produces a failed session every
few seconds, and without the gate "the devbox went away" would be an event per
retry rather than an event — and a runbook triggered on it would fire until its
rate limit stopped it. `error` carries the cause when the link broke by itself and
is empty for a deliberate detach.

**`runbook_finished` is the answer to "what about a half-failed triggered run".**
A triggered run has no responder, so without it a run that failed at step 2 would
leave no trace outside the log. Manual runs emit it too — a stream client should
not have to know which runs it will be told about — carrying a summary rather than
the step list, because it answers *did the thing I set up work*, not *what
happened*; whoever asked for the run already has the second answer.

**The vocabulary had to become machine-readable.** `app.EventNames()` was a list
of strings with no user in the tree. Triggers need `app.EventPayload(name)`,
because `where: {exit_cod: 0}` must be a **refusal** rather than a filter that
silently never matches — the exact failure `encoding/json`'s dropped keys cause
for command params, one layer out, fixed the same way one layer out.

## Part 2 — the format

`on:` accepts an event name, a clause, or a list of either, because all three
read naturally and the difference is punctuation. A clause is
`{event, where, min_interval}`, and the firing payload binds under the reserved
root `event`.

**`omitempty` would have broken the common case.** Marshalling
`PaneExitedEvent{Pane: 3}` drops `exit_code` entirely, so a filter on
`exit_code: 0` — *the ordinary successful exit* — would have been the one filter
that could never match, and `{{ event.exit_code }}` a run-time "no such field" on
exactly the runs it was written for. `runbook.EventMap` therefore builds the map
from the struct's fields by reflection and puts only the **values** through
encoding/json. Every field the load check validated against is present.

**Numbers cross two decoders.** The filter comes from YAML (`0` is an `int`), the
payload from JSON (`0` is a `float64`). `==` would make every numeric filter
silently false — and silently is the whole problem, since the symptom is a
trigger that simply never fires.

**There is no `{{ }}` for *which* event fired.** `theme_changed`'s payload already
has a `name` field, so injecting the event name would clobber it. And a runbook
has no branching, so "react differently depending on the event" is two runbooks,
which is also how it reads. One reserved root, no magic.

**Checked at load, like everything else here:** the event name, every `where:`
key against the payload struct, an unknown *clause* key (`wehre:` would otherwise
leave a clause with no filter, which fires on everything), non-scalar filter
values, the duration, a runbook triggering on `runbook_finished` (a loop with
nothing between its turns — the one loop decidable from the document alone), two
unfiltered clauses on one event, `event` as a step id, and `{{ event.x }}` in a
runbook with no `on:` at all, which gets its own message rather than the generic
"no earlier step binds it".

`{{ event.field }}` is checked against the **union** of the declared events'
payloads, not the intersection: a runbook started by either event is written once,
so a field only one of them carries is a run-time miss with a precise message, not
a typo.

## Part 3 — what stops a runaway

Four rules, and the live runs reordered which one matters.

1. **One run per runbook**, whatever started it, dropped never queued. A queue
   would be a backlog of stale side effects: the event that queued a run described
   a session the run in front of it is still changing.
2. **A global cap** of four runs in flight, manual ones counted.
3. **Ten trigger starts per minute per runbook**, then a five-minute suspension.
4. **Reserve at fire time, start on the loop's next turn** — otherwise a run's
   steps execute nested inside the `emitEvent` that started them, and a step that
   emits an event recurses into a fan-out still in progress.

**The tight self-loop is cut by (1), not by the rate limit.** A runbook triggering
on `pane_added` whose step is `pane.split` — a genuine self-feeding runaway —
stops after **one** iteration, because the pane it makes appears while the run is
still holding its own slot. That is a much better outcome than being rate-limited
after ten, and it was not obvious until it ran.

**Only (3) can stop a *mutual* loop**, since A and B taking turns are never
running at the same time. Ping (`on: pane_added` → `pane.close`) and pong
(`on: pane_removed` → `pane.split`) ran exactly 20 times, suspended both, and left
the session intact.

**Suspension never blocks a manual run.** The one thing it must not do is stop
somebody debugging the runbook that got suspended.

## What the live runs found that the tests could not

**A stranded reservation, and it would have been permanent.** The ping/pong run
needs twenty drain rounds and the budget is eight, so the remainder rides to the
next loop turn. In a session that then went completely silent that turn never
comes — and a reservation holds its slot from the moment it is made, so its
runbook would sit at "a run is already in flight" for the rest of the session with
nothing to wake it. The drain now posts its own next turn (from a goroutine: it
runs *on* the loop, and `post` blocks when the mailbox is full).

Everything else the live stack confirmed rather than corrected: one
`host_disconnected` across twelve seconds of failed re-dials and one
`host_connected` when cathost came back; `min_interval` throttling a second
`pane_cwd`; `runbooks.triggers: false` taking effect on `catctl reload` with the
listing explaining itself; a bad event name reported by `runbook.list` with the
whole vocabulary in the message; and a manual run of a triggered runbook failing
honestly at its own step with `event has no field "pane"` and exiting 1.

Two things decided by reading rather than running:

- **The index is the one place 6a's "re-scan per call" rule bends.** `runbook.list`
  and `runbook.run` still re-read the directory every call, so "edit, run" cannot
  execute the previous version. The trigger index cannot afford that: it is
  consulted on every emitted event, and `pane_title` alone fires several times a
  second. One-second TTL over a name/size/mtime fingerprint, so a scan that
  changed nothing skips the parse entirely. A trigger is not typed and immediately
  awaited, so the staleness that would be a bug there is unnoticeable here.
- **`runbooks.triggers` is file-only and deliberately absent from `config.set`.**
  A runbook's steps are §7 commands, so it could otherwise turn its own triggers
  back on.

The isolated-catway habit was kept: own `XDG_CONFIG_HOME`, own state dir, short
socket paths under `/tmp/rb6b` (the macOS `sun_path` limit is 104 bytes). The
MacApp session was never touched, and only the two test processes were stopped, by
matching their socket path.

## Shape of the work

- `internal/app/events.go` — `eventSpecs` table, `EventPayload`, `EventNames`
  derived from it; `host_connected` / `host_disconnected` / `runbook_finished`
  with `HostLinkEvent` and `RunbookFinishedEvent`
- `internal/runbook/trigger.go` — `Trigger`, the three `on:` spellings, load
  validation, `Match`, `EventMap`
- `internal/runbook/runbook.go` — `Triggers` on the document, `loadCtx`, the
  `event` root and its field check
- `cmd/catway/runbooktrigger.go` — the index, the four protections, the reserve
  queue and its drain, `runbookTriggerStatus`
- `cmd/catway/runbook.go` — `beginRunbook` (manual and triggered share it), slot
  accounting, the finish event
- `cmd/catway/hosts.go` / `daemon.go` — the link events and their alternation gate
- `internal/config` — `runbooks.triggers`, default true
- `app.RunbookInfo` — `triggers` and `trigger_status`
- Docs: `control-api.md` (four event rows, the naming rationale, a Triggers
  section), `cli.md`, `configuration.md`, `config.example.yaml`

## Still owed

- **cats-mobile regen** — `RunbookInfo` gained two fields; catgen-dart goldens are
  regenerated but the sibling repo is not (push cats first; see memory).
- **Phase 6c — record-a-macro**, still behind a §7 command journal that does not
  exist, and still carrying the privacy dimension the ledger does not.
- **Phase 7 — file transfer through cathost** is next in the recommended order.
- **`make fmt-check` is red on `internal/push/push_test.go`** — pre-existing on
  main, a gofmt-version disagreement, untouched by this work. Every other gate
  (`test`, `test-ghostty`, `vet`, `vet-ghostty`) is green.
- Phase 5b (collapse) still deliberately deferred.
