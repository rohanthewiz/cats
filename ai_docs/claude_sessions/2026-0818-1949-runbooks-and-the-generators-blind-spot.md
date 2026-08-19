# Session: the mobile regen that would not compile, then runbooks

- Session id: `35c2005f-1bed-4d8d-852d-6580f3f1c0c3`
- Date: 2026-08-18
- Branch: `main` (cats), `main` (cats-mobile)
- Plan/record: `ai_docs/plans/remote-catalog.md`
- Predecessor: `2026-0818-1905-remote-catalog-quick-wins-and-the-ledger.md`

The ask was "do the cats-mobile regen then start phase 6 — runbooks". Both of
the previous session's outstanding items are now closed. Three commits in cats
(`e55b893` `4cb515e` `e16a301`) and two in cats-mobile (`59ccf3f` `e99d4d8`).

## Part 1 — the regen, and the gate that was not one

The regen was owed from five phases of cats. It produced Dart that **did not
compile**: `commands.g.dart` referenced `LedgerEntry` and `NotifyAction` with no
import, and both classes were in `wire.g.dart`.

**A file split resting on a comment.** catgen-dart writes a class "wherever it
was first reached", and the code said in as many words: *"commands.g.dart imports
nothing from wire.g.dart because the two root sets share no types."* That was
true when it was written. Phases 1–5 made `LedgerEntry` reachable from both a
wire message (`history`) and a §7 result (`ledger.list`), and `NotifyAction` from
both `ui_action` and `ui.notify`. Nothing announced the change.

**The golden test was green the whole time, and correctly so.** It compares
generator output against committed golden — it gates *drift*, not
*compilability*. The broken references were regenerated into `testdata/golden`
and the test went on passing. Nothing in cats can run a Dart analyzer, so what
actually found it was `dart test` in a sibling repo, five phases late. Worth
keeping: **a golden test proves the output has not changed, never that it was
ever right.**

**Why the import is safe rather than half a cycle.** `messageRoots` runs to
completion before `commandRoots`, so every type reachable from a wire message is
tagged `wire` before a command is looked at — a shared type is therefore always
on the wire side, and a wire class can never reference a commands one. That
guarantee lives in *the order of two calls in `generate()`*, which is far too
quiet to rely on, so `checkFileDeps` now asserts it before a byte is emitted.

`usedArrayClasses` gained the same first-reached rule. Nothing shares an array
class today (Rect is wire-only, RowCol commands-only), which is exactly why the
rule had to be written down: with the import in place a shared one would be
*declared twice* rather than copied, and that is a duplicate-declaration error
nowhere near its cause.

## Part 2 — scoping phase 6 before building it

Phase 6 was three sentences bundling four features. Reading them against the code
first — the move that made phase 5 land right — found two false premises:

- **`host.attach` is a command, not an event.** No `host_attached` exists in
  `app.EventNames()`. Of the named triggers, `pane_exited` and `pane_agent` are
  real; that one is not.
- **"cron via cats-todo" is not a thing.** cats-todo's schedules are *one-shot*
  fire times run by its own TUI tick loop; a closed manager marks them `Missed`.
  It is a todo app that drops prompts into panes, not a scheduler service. Cron
  is free today by pointing launchd at `catctl runbook`.
- **Record-a-macro has nothing to record from.** The ledger stores *shell*
  commands via OSC 133; no journal of §7 commands exists anywhere. That journal
  is the work, and it carries a privacy dimension the ledger does not —
  `chat.send` params and `config.set` secrets would flow through it.

Raised; the user chose **6a — engine and manual run, no triggers**. Recorded in
the plan as 6a/6b/6c so the reasons outlive the session.

## Part 3 — what shipped

`internal/runbook` (parser, references, param checking), `cmd/catway/runbook.go`
(the executor), `runbook.list` / `runbook.run`, `catctl runbooks` /
`catctl runbook <name> [k=v ...]`, and the docs.

**A step is a §7 command, and that is the whole security story.** No runbook verb
for sleeping, branching or shelling out; no runbook-specific implementation of
anything. `runbook.run` re-enters the *same* `app.Dispatcher` a browser cmd and a
catctl request go through, once per step. So "what can a runbook do?" is
answerable without reading the package: what its caller could already do, in one
round trip instead of five. Waiting for a shell is `pane.wait_for_output`.
Automation that needs real control flow is a program, and a program can hold the
socket itself. The one command a step may not be is `runbook.run` — everything
else is bounded by the table, and that recursion is bounded by nothing.

**Everything checkable is checked at load.** A runbook is a sequence of side
effects on a live desktop with no undo, so a typo found at step 4 has already let
steps 1–3 change the session. Load refuses unknown commands, missing required
params, duplicate or meaningless ids, forward references, undeclared vars, an
unclosed `{{`.

**The reference rule that makes references usable.** A value that is *exactly*
one reference keeps its type — `pane: "{{ ws.pane }}"` sends the number, because
`Pane` is a `uint32` and the string `"3"` there is a decode error. One embedded
in longer text is stringified, because the surrounding characters prove the
author wanted text. Two modes, and the design does not work with either alone.

**Vars are declared so they can be checked.** A free-form var map would make
`{{ vars.brnach }}` unverifiable; declaring defaults in the document makes it a
load error. Passing a var the runbook never declared is *refused*, not ignored —
a silently dropped var makes the run succeed at doing the wrong thing.

**Sync and async without writing the chain twice.** A dispatched step may resolve
inline (`pane.focus`) or milliseconds later (`capture`, anything round-tripping
to a cathost), and the loop goroutine must never block. One `inFlight` flag
separates them: while control is still inside `Dispatch`, a responder call means
the step finished inline and the loop iterates; once `Dispatch` has returned
unresolved, the responder re-enters `advanceRunbook` later. Recursion depth is
bounded by the one async step in flight, never by the step count.

## What the live runs found that the tests could not

Three, all in the same family: **a wrong answer produced confidently.**

1. **`timeout_secs` is an error nowhere.** The struct says `timeout_ms`, and
   `encoding/json` *drops* a key it has no field for — so the wait ran with a
   zero timeout and reported success. In a client that is a bug you see in the
   output; in a runbook it is a step that appears to work and quietly did
   something else, three steps before the one that matters. Params are now
   checked against the command's params struct at load, nested objects included.
   The check is one-directional: unknown keys refused, missing ones not, because
   requiredness is `CommandSpec.ParamsRequired`'s business and a second notion of
   it here would be a quieter contract.

2. **`pane.wait_for_output` reports a TIMEOUT as success** (`matched: false`).
   That is right for a client — it asked a question and got an answer — and wrong
   for a sequence, which has to stop when the build never finished. Without help
   "wait for the build, then deploy" deploys. Hence `expect:`, a reference that
   must resolve truthy after its own step. Teaching the engine about
   `wait_for_output`'s result shape was the alternative and is worse: the engine
   would know one command specially, and the next field meaning "did not happen"
   (`ledger.output`'s `found`) would need the same edit.

3. **A failed run exited 0**, so `catctl runbook deploy && ./ship.sh` shipped. A
   runbook that ran with failing steps is a *successful command with an
   unsuccessful result*, and the shell has to tell them apart. Every other verb
   reports the command's fate because for every other verb they are the same
   fate.

Also caught by reading rather than running: **the ctlproto backstop.**
`runbook.run` is the second method after `pane.wait_for_output` that is *meant* to
be slow, so the per-request backstop is now sized off `app.MaxRunbookRuntime`. A
backstop below the run's own limit would answer "command timed out" while the run
carried on changing the session — neither knowing it failed nor knowing it was
still going.

And a habit that paid off: the live run used an **isolated** catway (own
`XDG_CONFIG_HOME`, own state dir, short socket paths — the macOS `sun_path` limit
is 104 bytes and the scratchpad path alone blows it). The user's MacApp session
was never touched, and only the two test processes were stopped by name.

## Shape of the work

- New package: `internal/runbook` (`runbook.go`, `refs.go`, `params.go`)
- New file: `cmd/catway/runbook.go` — the executor, loop-goroutine only
- New §7: `runbook.list` (reply-gated), `runbook.run` (not — its product is the
  effects). New Backend methods `RunbookList` / `RunbookRun`
- New wire types: `RunbookInfo`, `RunbookListResult`, `RunbookRunParams`,
  `RunbookStepResult`, `RunbookRunResult`; `app.MaxRunbookRuntime`
- catctl: `runbooks`, `runbook <name> [k=v ...]`, `argRunbook` completion, and a
  non-zero exit on a failed run
- Docs: `control-api.md` §Runbooks, `cli.md` verbs
- Runbooks live in `~/.config/cats/runbooks`, re-scanned **per call** — caching
  would make "edit, run" execute the previous version, a staleness bug whose
  symptom is a correct-looking run of the wrong steps

## Still owed

- **Phase 6b — `on:` triggers.** The risky half: a runbook firing on
  `pane_exited` can spawn panes that exit, so loop protection, a concurrency
  rule and a half-failed-run answer are all needed before the first `on:` fires.
  `host_attached` would have to be emitted first.
- **Phase 6c — record-a-macro**, behind a §7 command journal that does not exist.
- **Phase 7 — file transfer through cathost** is next in the recommended order.
- **`make fmt-check` is red on `internal/push/push_test.go`** — pre-existing on
  main, a gofmt-version disagreement, untouched by this work. Every other gate
  (`test`, `test-ghostty`, `vet`, `vet-ghostty`) is green.
- Phase 5b (collapse) still deliberately deferred.
