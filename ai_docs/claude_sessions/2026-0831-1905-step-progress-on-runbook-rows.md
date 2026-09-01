# Session: Step Progress on Runbook Rows

- **Session ID:** `a7c8f4ce-ee86-4391-a005-5ae60b61f48a`
- **Date:** 2026-08-31
- **Branch:** main
- **Repo:** `cats`
- **Follows:** `2026-0831-1845-runbook-runs-marked-anywhere.md`, in the same
  session. That one closed the "a row only marks runs this window started" gap;
  this one takes the next lever its own doc listed.

## Request

> now show step progress on the row

A row said "running". It did not say *where* the run had got to.

## The objection this had to answer

The previous doc's next-levers entry read: "No step-level progress. A row says
'running', not 'step 3 of 7'. That is still the per-step message the original
objection was about, and it would need a real answer for the event-loop question
before it could exist."

Re-read, the original objection is to a control-API **event**: events feed
`fireRunbookTriggers`, so an event per step hands a runbook an event its own
steps produce. A browser broadcast has no such reach, and the previous session
already settled that — `runbook_runs` is a broadcast, not an event.

So the event-loop question was already answered. **The live problem is RATE**,
and it is a different problem with a different fix.

## The design decisions

### Progress is coalesced to the loop turn; the edges are not

A run of inline commands (`pane.last`, `pane.focus`) executes **every step
inside a single turn of the orchestrator loop**. Broadcasting per step would put
a burst of messages on every socket describing positions that existed for
microseconds and were never painted — an 8-step runbook would be 9 messages for
one visible state.

So `noteRunbookStep` only sets a dirty flag, and `flushRunbookRuns` sends once
per turn — from `run()`, beside `flushClients`, which is there for the same
reason ("the one place… no broadcast is in progress", and "a closure that drops
three connections sends one census, not three").

The **edges stay immediate**. Start and finish are transitions, there are
exactly two per run, and a window that learned them a turn late would flash a
mark on and off for a run that had already ended. Progress is a number only ever
read as "the latest one", so the only position worth sending is the one still
true when the turn ends.

`broadcastRunbookRuns` clears the dirty flag, because any broadcast is the whole
set and therefore already answers whatever the flag was going to ask for. Without
that, a run finishing inside one turn is followed by a redundant flush of a
position it no longer has.

Deliberately **not** also flushed inside `startReservedRunbooks`' drain, where
`flushClients` is called: a census must be right before the next run starts, but
progress has no such requirement, and flushing there would turn a burst of eight
reserved runs into eight messages instead of one.

### `step` is the step now EXECUTING, not the count completed

Noted at the *top* of `advanceRunbook`'s iteration rather than when a step
finishes. When a run sits still for a minute, the step doing the sitting is the
one a reader needs — and it is the same 1-based numbering
`RunbookStepResult.Index` uses, so a failure reported as "step 4" names the step
the row was showing.

`step: 0` is a real state: a triggered run takes its slot when the trigger fires
and reaches its first step on the next loop turn.

### The count column becomes the progress column

The row's numeric column already answered "how big is this?". During a run the
honest answer is "this big, and here is where it has got to", so it shows `3/7`
in the same place, in accent. Two numbers side by side would make the row a
readout.

Plus a 1px `::after` bar at the row's own fraction, width from a `--rbprog`
custom property so the stylesheet owns the look and the JS owns only the number.
Drawn as `::after` rather than an element because a full-width bar inside a flex
row would have to be excluded from the layout it is not part of. It earns its
place on the same argument the recorder's step counter does: a step may
legitimately block for minutes on a build, and *running* and *running and
getting somewhere* look identical until something moves.

`prefers-reduced-motion` keeps the bar (it is information) and drops only its
width transition.

### The run's own total wins over the listing's

`runbookCount(rb, run)` prefers `run.steps` to `rb.steps`. They are normally the
same, but a file edited while it runs makes the listing describe a document this
run is not executing — and the count beside a moving position has to be the one
that position counts towards, or the row shows `4/3`.

`step: 0` shows the plain total rather than `0/5`, which reads as a run that is
stuck rather than one that is a millisecond old. The pulsing dot has already
said it is running.

## What shipped

### 1. The wire (`internal/browserproto/down.go`)

`RunbookRun` gained `Step` / `Steps`. The type comment's "per RUN and not per
STEP" paragraph was replaced by the coalescing argument — it carries per-step
progress but is not a per-step message.

### 2. The server

- `runbookRunInfo{step, total}` and `runbookTriggers.dirty`
  (`runbooktrigger.go`).
- `claimRunbookSlot(name, total)` — the total is known at claim time because
  `RunbookRun` loads the document before claiming; `reserveRunbook` takes it
  from `len(rb.Steps)`.
- `noteRunbookStep` (flag only, ignores an unknown name, ignores a re-note of
  the same step) and `flushRunbookRuns`.
- `advanceRunbook` notes `run.i+1` at the top of each iteration (`runbook.go`).
- `run()` calls `o.flushRunbookRuns()` after `startReservedRunbooks()`, because
  a run that just started there has already advanced its cursor.

### 3. The front end

`renderRunbooks` sets `--rbprog` and the `.prog` class; `runbookCount` is the
new helper; the title leads with `running step 3 of 7` and puts the origin on
its own line. `29-runbooks.css` gained `.rsteps.prog` and the `li.running::after`
bar.

### 4. Docs

Both protocol docs (`browser-protocol.md`'s table row, `control-api.md`'s runs
paragraph) gained the position fields and the coalescing rule. The Dart golden
regenerated.

## Checks

- Full `make check` sequence piecewise — clean.
- `go test -race -tags ghostty ./cmd/catway/... ./internal/browserproto/...` — ok.
- Three new Go tests:
  - `TestRunbookProgressIsCoalescedToTheLoopTurn` — a five-step inline run is
    still exactly two messages, and the start message already knows the length.
  - `TestRunbookProgressFlush` — the flush contract: three moves in one turn are
    one message carrying the LAST position; an unmoved turn is silent; re-noting
    the same step is silent; an edge clears the flag; a released run cannot be
    advanced back into existence.
  - `TestTriggeredRunCarriesItsStepCount` — the total comes from the document
    when there is no caller to supply it.
- `scratchpad/rbtest.mjs` grew to **34 assertions**, twelve of them new: the
  idle count, `step: 0` showing the plain total and no bar, `3/7` with `.prog`
  and a 60% bar, the title's step line, the run-total-wins case (`7/9`, 77.8%),
  and the return to the plain length when the run ends.
- Bundle: `node --check` plus a strict-mode `new Function` parse; `runbookCount`
  declared once.

### Verified live, over the browser's own socket

Isolated instance on port 8512 (throwaway `XDG_CONFIG_HOME`, own cathost
socket), same ~60-line RFC6455 client as the previous session.

| case | result |
|---|---|
| `paced` — four steps of `wait_for_output timeout_ms: 1000` | `step 1 of 4` … `step 4 of 4`, one message per second, then the empty set |
| `burst` — eight steps, all inline | **2** messages, not 9 — the coalescing holds end to end |

Processes stopped by pid, sockets removed.

**Not verified on screen.** Chrome still renders an error page for localhost —
third session running, and almost certainly the extension's site permissions.
The `3/7` and the bar are asserted through the render test, not looked at.

## Known limits / next levers

- **No per-step detail.** The row says where, not what: "step 3 of 7" and not
  "step 3 — pane.wait_for_output". The command name is not on the wire, and
  putting it there is a genuinely different message (it changes on every step
  and would defeat the "same number, no message" suppression).
- **A step that fails but is tolerated looks like any other.** The position
  moves on and nothing marks that step 2 errored; only the end-of-run toast
  says so, and only for a run this window started.
- **`inFlight` is still a counter beside the map** — unchanged from last
  session, same reasoning.
- **The bar is unverified on screen.** Its width, colour and 1px height are a
  judgement call made against the stylesheet, not against a rendering.

## Notes for next time

- The distinction that matters when reading the old objections: **event vs
  broadcast** is a correctness question (trigger loops), **per-step vs coalesced**
  is a rate question. They were tangled in one sentence in the previous doc and
  came apart cleanly.
- `run()` in `catway.go:681` is the whole loop and the place to hang anything
  that must be coalesced per turn. Tests do not run it — they call the flushes
  themselves, and the existing idiom is the comment `// what run() does between
  mailbox closures`.
- **No command reachable in the runbook tests resolves asynchronously without a
  daemon** — `pane.wait_for_output` fails immediately with no daemon, so a run
  can never be caught between two steps there. Probed and confirmed. Anything
  needing a mid-run observation has to go to a live instance.
- A live pacing fixture: four `pane.wait_for_output` steps with
  `timeout_ms: 1000` and `continue_on_error: true` gives a clean one-second
  metronome to watch a progress indicator against.
- Adding a param to `claimRunbookSlot` touched three test call sites; `grep -n`
  the helper name in `*_test.go` before building.
