# Session: Runbook Runs, Marked Wherever They Started

- **Session ID:** `a7c8f4ce-ee86-4391-a005-5ae60b61f48a`
- **Date:** 2026-08-31
- **Branch:** main
- **Repo:** `cats`
- **Follows:** `2026-0831-1828-runbooks-sidebar-section.md`, whose "Known limits"
  named this as the first one.

## Request

> close the gap: mark rows for runs started anywhere

The gap, in the previous session's own words: a runbook row only lit for a run
**this window** was awaiting. A run from `catctl`, a plugin, an `on:` trigger or
a second browser window left every row idle while panes appeared on the desktop.

## Why the previous session left it open, and why that reasoning does not block this

The old note said closing it "means a per-step down-message, which would feed
`emitEvent` → `fireRunbookTriggers` and hand a runbook an event that its own
steps produce."

That objection is to a **control-API event**, and it is correct: events feed the
trigger index, so an event per run start would let a runbook trigger on the fact
that a runbook started. But `broadcastRecord` had already established the other
route one phase earlier — a **browser broadcast**, which is not an event, lights
an indicator and stops. Its own comment says exactly that ("The browser message
has no such reach"). So the shape was already in the codebase; this session
applied it a phase later.

`runbook_finished` remains the event-side report, once per run, at the end.
Nothing about it changed.

## The design decisions

### The state was already there — it just did not know who asked

`runbookTriggers.running` is the concurrency accounting, and it is the complete
set of run transitions **by construction**:

| path | takes the slot | released by |
|---|---|---|
| `runbook.run` — browser, `catctl`, plugin, relayed command | `claimRunbookSlot` | `releaseRunbookSlot` |
| an `on:` clause firing | `reserveRunbook` | `releaseRunbookSlot` |

A run that did not take a slot does not exist; a slot never released wedges its
runbook at "already in flight" whether or not anyone is watching. So the
broadcast hangs off those three functions rather than off the executor — the
same rule `macroRecorder.changed` follows. Announcing from the caller instead
would be one `catctl` path away from being forgotten.

`running map[string]bool` became `map[string]runbookRunInfo{source, trigger,
started}`. A struct rather than a parallel map because this map is the only
record of what the session is running — `runbookRun` itself is private to the
executor's chain — and a second map could disagree with it about which runs
exist. Four read sites moved to the comma-ok form.

### The whole set, not start/stop deltas

A set is idempotent: a window that reconnects converges on the next message, and
a dropped or coalesced delta cannot leave a row lit for a run that ended — the
one failure a progress mark must not have. It is also what lets the connect
burst be the whole answer for a window that joins mid-run.

Sorted by name in `runbookRunsMsg`, because map iteration is not: two identical
sets that serialise differently are two different messages on the wire, which
turns a listing a client could compare against its last one into one it cannot.

### A triggered run is marked from RESERVE, not from its first step

`reserveRunbook` takes the slot a loop turn before `startReservedRunbooks` runs
the steps (the file's protection #4). The mark goes on at reserve: the session
has already committed to the run and is already refusing every other start of
it, so a window told anything else would be told something untrue.

### `source` and `trigger` are on the wire; "which socket asked" is not

"It is running" and "it started **itself**" are different facts to somebody
watching panes appear that they did not ask for, and the second is the one that
sends them to the file — so the trigger's event name travels too, since it is
the word to grep the YAML for.

What is *not* on the wire is the requester. The server has no business tracking
which socket asked for what, and the browser already knows: `runbookPending` is
this window's own claim, which also covers the click→broadcast round trip and
stops a double-click starting a run the server would only refuse. Three titles
result: `started here` / `started by its pane_added trigger` / `started outside
this window` — the last one deliberately merges another window, `catctl` and a
plugin, because the session genuinely cannot tell them apart and inventing a
distinction it does not have is worse than the honest answer.

### The listing refresh moved to the falling edge

It used to live in `runRunbook`'s own callback. Now `applyRunbookRuns` refreshes
when a name **leaves** the set — for every run, not just the ones with a
callback attached. That is what makes `trigger_status` ("a run is in flight")
recover after a *trigger's* own run, the case where nobody was there to press
`⟳`. Only the falling edge: refreshing on a start would ask the server to
re-read the directory at its busiest moment, to learn what the message just
said.

## What shipped

### 1. The wire (`internal/browserproto/`)

`MsgRunbookRuns = "runbook_runs"` + a `DecodeDown` case (`proto.go`), and
`RunbookRuns{Runs []RunbookRun{Name, Source, Trigger, StartedAt}}` with
`NewRunbookRuns` normalising nil → `[]` (`down.go`). The type's doc comment
carries the whole argument above — broadcast not reply, set not delta, browser
message not event, per run not per step.

### 2. The server (`cmd/catway/runbooktrigger.go`, `catway.go`)

- `runbookRunInfo`, the map type change, and the four comma-ok reads.
- `initRunbookAccounting()` — the map allocation the two slot-takers duplicated,
  lifted into one place so a map allocated in only one of them cannot nil-panic
  in the other.
- `runbookRunsMsg()` / `broadcastRunbookRuns()` under a new
  `--- what the browser is told ---` section.
- The connect burst gained `o.send(c, o.runbookRunsMsg())`, right after
  `recordMsg`, always — including empty.

### 3. The front end (`web/js/41-runbooks.js`, `19-messages.js`)

- `runbookRunning` (Set) → `runbookRuns` (Map, from the server) +
  `runbookPending` (Set, this window's claims).
- `applyRunbookRuns` (replace wholesale, refresh on the falling edge),
  `runbookRunOf` (the run plus a `local` flag), `runOrigin` (the three titles).
- `renderRunbooks` / `runbookTitle` take the run object rather than a bool;
  `startRunbookRun`'s guard names the origin; `runRunbook` lost its refresh.
- `case "runbook_runs"` in the dispatcher.

### 4. CSS + docs

`29-runbooks.css`: the `●` comment now says "a run in flight, whoever started
it". `docs/protocols/control-api.md`: the Runbooks browser block split into "the
files are a query, the runs are pushed". `docs/protocols/browser-protocol.md`:
one table row for `runbook_runs`.

### 5. The Dart golden

`cmd/catgen-dart/testdata/golden/wire.g.dart` regenerated (its own test asked);
it now carries `RunbookRun` / `RunbookRuns` and the codec case. **The sibling
`cats-mobile` checkout's copy was already several protocol additions behind
before this change** (it lacks `pane_respawned` among others), so it was left
alone rather than bundling unrelated drift into this commit.

## Checks

- Full `make check` sequence piecewise: `gofmt -l cmd internal`, `go vet ./...`,
  `go build ./...`, `go test ./...`, `go vet -tags ghostty ./...`,
  `go test -tags ghostty ./...` — all clean.
- `go test -race -tags ghostty ./cmd/catway/... ./internal/runbook/...
  ./internal/browserproto/...` — ok.
- Three new tests in `runbooktrigger_test.go`, with a `runbookRunsSeen` helper
  that returns the whole ordered sequence (the sequence is the subject: a run
  that announced its start and not its finish leaves every window marking a
  runbook that is not running):
  - `TestRunbookRunsReachEveryWindow` — empty connect burst, then both edges of a
    dispatched run with `source: control` and a start time.
  - `TestTriggeredRunIsBroadcastWithItsTrigger` — `source: trigger`,
    `trigger: pane_agent`, empty set at the end.
  - `TestConnectBurstCarriesRunsInFlight` — two slots claimed directly (every
    runbook these tests can write finishes inside the call that starts it), the
    burst carries both sorted, and one release retracts only one.
- JS: strict-mode `new Function` parse of the concatenated 42-file bundle;
  declaration sweep over the five new identifiers; zero remaining
  `runbookRunning` references.
- `scratchpad/rbtest.mjs` — `41-runbooks.js` evaluated alone via `new Function`
  with a DOM stub, 22 assertions over glyphs, classes, titles, toasts and the
  command stream. It covers the interleaving that matters: our own broadcast
  landing **before** our own `cmd_result`, which must still read "started here".

### Verified live, over the browser's own socket

An isolated instance (throwaway `XDG_CONFIG_HOME`, its own cathost socket, port
8511) with a ~60-line RFC6455 client speaking `/ws` — handshake, masked text
frames, a frame reader — so the browser path was exercised, not the control
socket. The `/ws` first message is `{t:"init", v:1, cols, rows, dpr, cell_w_px,
cell_h_px}`.

| case | result |
|---|---|
| connect burst, nothing running | one `runbook_runs` carrying `[]` |
| `catctl runbook slow` (a 3s `wait_for_output`) | marked at 2.31s, retracted at 5.31s |
| `catctl split v` firing an `on: pane_added` clause | `source: trigger`, `trigger: pane_added`, held 2s |
| a second window connecting mid-run | its burst carried the in-flight `slow` |

Both processes were stopped by pid afterwards and the test sockets removed.

## Known limits / next levers

- **No step-level progress.** A row says "running", not "step 3 of 7". That is
  still the per-step message the original objection was about, and it would need
  a real answer for the event-loop question before it could exist.
- **A browser-started run still acts on the PRIMARY view**, not the issuing
  window (`RunbookRun` dispatches through `app.NewDispatcher(o.session, o)`).
  Pre-existing, untouched, and now one click further from being obscure.
- **`inFlight` is still a plain counter** beside the map. It could be derived
  from `len(running)` now that the map is the record; it was left alone because
  the reserve path increments it for runs the map also holds and changing that
  is a separate argument.
- **No keybinding**, same as the recorder. The palette is the keyboard route.

## Notes for next time

- The **three slot functions** (`claimRunbookSlot`, `reserveRunbook`,
  `releaseRunbookSlot`) are the complete set of run transitions. Anything that
  needs to know what the session is running hooks there, not in the executor.
- `broadcastRecord`'s comment is the precedent to cite for "browser broadcast,
  not control-API event". It states the trigger-loop hazard in full.
- **Adding a `browserproto` down type breaks `cmd/catgen-dart`'s golden test.**
  Fix: `go run ./cmd/catgen-dart -out cmd/catgen-dart/testdata/golden`. The Dart
  types are generated from the Go structs, so a good doc comment ships to the
  mobile client too.
- `catctl` verb spellings that cost a round trip each: `rename-pane <pane>
  <name>` (not `pane rename`), `split v|h [pane]`. `catctl help` lists them.
- `pane_title` is OSC 0/2 only — `rename-pane` sets a *custom* name and emits no
  `pane_title`, so it is useless as a trigger in a test. `pane_added` via
  `catctl split v` is the cheap one that works.
- The `new Function(...stubNames, src + "; return {…}")` trick from last session
  scales: stubbing `classList: {add,remove,toggle}` on the fake element was the
  only addition needed to drive a whole broadcast→render cycle.
- Ports 8498/8499 were already taken by earlier test instances; 8511 was free.
