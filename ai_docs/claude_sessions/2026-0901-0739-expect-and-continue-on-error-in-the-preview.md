# Session: `expect` and `continue_on_error` in the Preview

- **Session ID:** `c10565cf-5f8d-47cd-91cd-7406deee512d`
- **Date:** 2026-09-01
- **Branch:** main
- **Repo:** `cats`
- **Follows:** `2026-0901-0045-running-runbooks-dead-click.md`. Seventh session
  running. Closes the item that had been sitting on *Known limits* for five of
  them — longer than anything else on the list.

## Request

> make `expect` and `continue_on_error` visible in the preview notice

The wording matters: not "in the outline", not "in the gate". Five sessions of
notes had already narrowed the question to that surface ("the preview notice is
still the likelier home than the gate"), and the request took the narrowing.

## The problem, precisely

`runbook.Step` carries two fields that no surface had ever mentioned:

| field | what it changes |
|---|---|
| `expect:` | a `{{ ... }}` reference that must be truthy after the step, or the step FAILED — the answer to `pane.wait_for_output` reporting a timeout as a successful call returning `matched: false` |
| `continue_on_error: true` | a failure there does not stop the run |

Neither changes what a step DOES. Both change what a run MEANS. So a runbook
whose step 3 is allowed to fail previewed identically to one where a failure
aborts everything after it — the more consequential of the two facts, and the
one entirely absent from the screen.

The exclusion was deliberate and `cmd/catway/runbook.go` said why, in a comment
that has stood since the outline was built:

> a line already truncated to fit a dialog has room for what the step does or
> for how it is judged, not both

That reasoning is still right, and this session did not overturn it. What it
overturned is the unstated corollary the code had been living by: **keeping
them out of the LINE is not the same as keeping them off the WIRE.** For five
sessions the two had been treated as one decision.

## The design decisions

### Positions, not prose

`runbook.list` gains two fields:

```go
ExpectSteps          []int `json:"expect_steps,omitempty"`
ContinueOnErrorSteps []int `json:"continue_on_error_steps,omitempty"`
```

1-based positions. An outline line is prose the server had to render and clip;
a position is three bytes that the client already has a numbered `<ol>` to
resolve against. The line budget is therefore untouched, and the surfaces with
the LEAST room — the palette's one line, the row's hover — are unaffected
because they never read the new fields.

### Uncapped, where the outline is capped

`Outline` stops at `maxOutlineSteps` (24). These do not.

The asymmetry is the point. A document is bounded at 200 steps, so the worst
case here is two short int arrays — but more importantly the two truncations
fail differently: **not printing an outline line is a shorter list, while not
reporting an `expect:` is a wrong claim about the run.** The dialog already
says how many lines it left out; there is no equivalent apology for a missing
position.

The consequence is accepted on purpose: the notes may name a step the outline
never printed (`steps 2, 90` above a list of 24). The step is in the file
whether or not the list got that far, and `…and 176 more steps` is sitting
right there telling the reader those steps exist.

### One predicate, two fields

```go
func stepJudgement(rb *runbook.Runbook, pred func(runbook.Step) bool) []int
```

Rather than two near-identical loops. The two fields differ only in which bit
of a `Step` they read; the part that is easy to get wrong — the off-by-one
between a slice index and the number the `<ol>` and a failure report ("step 4")
both use — is then written once. Returns `nil` for no matches, so `omitempty`
drops the key and a client's `if (list)` and `if (list.length)` agree.

### Notes under the list, not markers on the lines

Markers on the lines were the obvious alternative and are wrong twice over:
they would crowd the one thing a line is for, AND — since the positions arrive
uncapped while the lines are capped — a marker could only ever apply to the
visible 24, under-reporting in precisely the long documents where it matters
most. A note under the list has no such ceiling.

### The wording leads with the YAML key

```
expect: steps 2, 4 — each fails unless its check holds once it has run
continue_on_error: step 3 — a failure there does not stop the run
```

The key rather than a paraphrase ("checked", "tolerant"), so a reader who wants
the check ITSELF knows the word to look for once the file is open — which is
where the expression lives, and the reason the expression is not on the wire.
The clause after the dash is for a reader meeting the field for the first time.

Order is check-then-tolerance: both are true of the same run, but "step 4 may
fail" only means something once "step 4 is checked" has been read, because the
check is the thing that can produce the failure.

### `linesNote` widened from a string to string-or-array

`dialogLines` had one slot under the `<ol>`, holding the truncation tail. It
now takes a string or an array of them, dropping empties, each its own hint
line. Chosen over a second option (`linesNotes` beside `linesNote` — two names
one letter apart) and over joining into one sentence in the caller (the tail
and the judgement are different claims and a `\n` in a `div`'s textContent does
not wrap anyway).

The empty-drop is what lets the caller pass the slot unconditionally:

```js
linesNote: [outline.linesNote].concat(runbookJudgement(rb)),
```

A runbook that was not truncated passes a leading `""` and gets two hint lines,
not three.

### Preview only, and that is asserted

The two gates (`dialogConfirm`, `dialogFields`) do NOT get the notes. A gate is
a decision — "run this?" — and what answers it is what the steps do. This
dialog exists only to be read, so it is the one surface with room to also say
how they are judged. The suite asserts the separation rather than leaving it to
the comment.

### No outline, no notes

A listing without an `outline` opens the notice it always did. `dialogLines`
returns early with no `lines`, so the notes never render — which is right: a
note naming step 1 with no numbered list to resolve it against is worse than
silence. Same shape as the row's residual case, for the same reason.

## What shipped

- `internal/app/command_vocab.go` — `ExpectSteps`, `ContinueOnErrorSteps` on
  `RunbookInfo`, with the why-positions / why-uncapped reasoning.
- `cmd/catway/runbook.go` — `stepJudgement`; both fields wired into
  `RunbookList`; the outline's own note amended to say where they went instead
  of only that they are absent.
- `cmd/catway/web/js/41-runbooks.js` — `maxNoteSteps`, `stepNumbers`,
  `runbookJudgement`; `previewRunbook` passes `linesNote` as an array.
- `cmd/catway/web/js/26-picker.js` — `dialogLines` takes a string or an array
  in `linesNote`.
- `cmd/catway/web/jstest/runbook-preview.test.mjs` — **new suite**, 31
  assertions.
- `cmd/catway/web/jstest/testutil.mjs` — `sliceConst` and `loadFns({consts})`.
- `cmd/catway/web/jstest/bundle.test.mjs` — the two new functions added to the
  declared-once list.
- `cmd/catway/runbook_test.go` — three new tests.
- `cmd/catgen-dart/testdata/golden/commands.g.dart` — regenerated for the two
  new fields.
- `docs/protocols/control-api.md` — the preview's judgement notes.

No CSS (`hint steplist-more` already existed and now simply repeats).

## The harness grew a third capability

`loadFns` took `consts: ["maxNoteSteps"]`, lifting a top-level single-line
`const` with its VALUE alongside the functions.

Written because the alternative was a `12` in the test beside the shipped `12`
— which keeps passing after the shipped one moves, asserting nothing about the
number the browser uses. Budgets are exactly the kind of thing a test should
pin and exactly the kind of thing that drifts. Single-line only, deliberately:
every budget in this code base is a literal, and a multi-line const would want
the brace scanner `slice` already has.

Third session in a row the harness absorbed a new suite; first time it grew an
API to do it.

## Checks

- `make check` — exit 0.
- **jstest: 9 bundle + 41 palette + 31 preview + 30 row = 111.**

Go assertions worth naming:

- A step carrying BOTH fields appears in both lists — two separate claims about
  the same step, not alternatives.
- `nil` (not `[]`) when unused, which is what `omitempty` needs.
- The uncapped case asserts the LAST reported position is past
  `maxOutlineSteps`, so a future refactor that quietly reused the outline's cap
  fails here rather than under-reporting in silence.

JS assertions worth naming:

- The outline lines are asserted to still `lack` "continue_on_error" — the
  markers-on-lines design would pass every other assertion in the suite.
- The truncated case pins the ORDER: tail first, then the two notes. "How much
  of this list am I seeing" is read before anything said about it.
- `lacks(capped, "19")` — the positions past the cap are not merely counted,
  they are absent.

Fixture note: `expect:` requires an `id:` on its own step (it asserts on that
step's own result), which the first draft of the Go fixture did not have. The
loader rejected it with exactly that sentence, which is the validator behaving.

## Notes for next time

- **`cats-mobile` was NOT synced.** The golden regenerated in this repo, but
  `../cats-mobile/packages/catsproto/lib/src/generated` is already several cats
  commits behind and moves in its own deliberate commits ("regenerate the wire
  layer against cats `<sha>`"). Syncing it mid-feature would have been a
  surprise in a second repo.
- Still unverified on screen — **seven sessions**. Chrome will not reach
  localhost from the extension; the demo-instance recipe (port 8520,
  `/tmp/cats-demo.sock`, fixtures under the scratchpad) still stands, still
  unrun. This session added two new hint lines to a dialog nobody has looked at.

## Known limits / next levers

- **`stepNumbers`' cap has no "every step" case.** A 200-step runbook where
  every step is lenient reads `steps 1, 2, … 12, …and 188 more`, where *every
  step continues on error* would be both shorter and more useful. Deferred as a
  branch for a case nobody has hit; worth doing the first time somebody does.
- **The `expect:` expression is still only in the file.** The notes say WHICH
  steps are checked, not what the check is. That was the right call for the
  wire (a ref per step, on a listing that re-reads after every run) but it is
  the obvious next ask, and `paramDigest`'s clipping is the precedent if it
  comes.
- **No surface re-renders while the palette is open.** Unchanged, and now by
  some distance the oldest item here.
- **The outline's shaping is still spread** across `runbookLead`,
  `runbookOutlineText` and the callers. Third session to note it. This one
  added a reader that pointedly does NOT go through `runbookOutline` (the notes
  are the caller's, not the outline's), which is either the answer or the
  reason the question keeps not mattering.
