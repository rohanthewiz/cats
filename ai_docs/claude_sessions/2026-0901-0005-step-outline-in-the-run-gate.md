# Session: The Run Gate Shows the Steps

- **Session ID:** `a7c8f4ce-ee86-4391-a005-5ae60b61f48a`
- **Date:** 2026-09-01 (work began 2026-08-31)
- **Branch:** main
- **Repo:** `cats`
- **Follows:** `2026-0831-1905-step-progress-on-runbook-rows.md`, in the same
  session. Asked for while a live instance was open in Chrome for review.

## Request

> When I click on the runbook, show me it's steps (one per line truncated) in
> the popup

## The reversal

Two sessions back, the runbooks doc closed with this under *Known limits*:

> **The section does not show what a runbook will do** before it runs — the vars
> dialog names the count, not the steps. `runbook.list` does not carry them and
> **should not**; a preview wants the file, which "open in editor" already gives.

That was my call and the user has now overruled it, which settles it. Worth
recording *why the old reasoning was not wrong so much as incomplete*: the
objection was to putting **params** on the wire, and params really cannot go on
the wire — a `file.put` step carries a whole file, and `runbook.list` re-reads
after every run finish. What the objection missed is that a **pre-rendered,
clipped summary** is not the params. Once the rendering happens server-side the
whole cost argument dissolves, and "open it in the editor" stops being the only
honest answer.

The gate was already asking the user to agree to "4 steps against this session".
That is a number to consent to without knowing what it buys.

## The design decisions

### Rendered and truncated on the SERVER

`RunbookInfo.Outline []string` — one line per step, already clipped, built in
`cmd/catway/runbook.go`.

Client-side rendering would mean shipping `Params`, which is exactly the thing
that cannot ship. Server-side rendering also lets composite values collapse to
their **shape** (`{…}`, `[…]`) rather than their content — and that is not only
about length: the values that are big are precisely the ones nobody wants in a
preview.

Two caps, for two different failures:

| cap | value | stops |
|---|---|---|
| `outlineLineBudget` | 72 chars | a line that has stopped being scannable and become a document |
| `maxOutlineSteps` | 24 lines | a 200-step document riding along on every refresh |

`Steps` still reports the TRUE count, which is what lets the dialog say "…and
180 more steps". Silence there would be the worse failure: a preview that
quietly stops at 24 of 200 does not look truncated, it looks like the runbook.

### The line format

`id: command k=v k=v`, params sorted by key.

- **Sorted** because `Params` is a map and its iteration order is not stable — an
  outline that reordered itself between two identical calls would look like the
  file had changed.
- **The id leads**, because it is what later steps call this one by; a reader
  scanning for `{{ build.pane }}` finds it at the left margin.
- **Strings keep quotes and are escaped**, so a `text:` step carrying a newline
  stays ONE line and an empty string is visible as `""` rather than as nothing.
- **Clipped before quoting**, so a megabyte of base64 is never allocated a
  second time just to be thrown away.
- **References stay unresolved.** `text="echo '{{ vars.message }}'"` in the vars
  dialog is the point: you can see where what you are about to type ends up.

### What is deliberately NOT in a line

`continue_on_error` and `expect`. Both are real and both change what a run
*means*, but a line already clipped to fit a modal has room for what the step
**does** or for how it is **judged**, not both — and "what will this do to my
session" is the question a gate is answering. The file answers the other one and
is one right-click away.

### A generic dialog option, not a runbook feature

`opts.lines` + `opts.linesNote` on **both** `dialogFields` and `dialogConfirm`,
rendered by one shared `dialogLines`. "Confirm this" and "fill these in, then do
it" are the same question about the same list, and anything else that grows a
"here is what I am about to do" gate gets it for free.

An `<ol>`, so the numbering is the browser's and matches how the previewed thing
numbers itself — a runbook failure reported as "step 4" is the fourth line here.
`linesNote` sits **outside** the list: it is not one of the items, and numbering
it would claim there is a step that says "…and 180 more".

### Placed LAST in both dialogs

Below the message, below the fields, above the buttons. The fields are what the
user came to do and where focus lands; a ten-line preview above them would push
the input they are typing into off the top of a small window. Consistency
between the two dialogs mattered more than either individual placement.

Styled monospace and `nowrap` — these are command names and `k=v` pairs, code
rather than prose, and a wrapped line would read as two steps. Capped at `15em`
with `overflow:auto`: past a dozen rows the list has stopped being something you
glance at before clicking *run*, and the buttons have left the screen.

## What shipped

- `internal/app/command_vocab.go` — `RunbookInfo.Outline`.
- `cmd/catway/runbook.go` — an `--- the outline ---` section: `stepOutline`,
  `stepLine`, `paramDigest`, `clip`, and the two caps.
- `cmd/catway/web/js/26-picker.js` — `dialogLines`, wired into both dialogs.
- `cmd/catway/web/js/41-runbooks.js` — `runbookOutline(rb)` spread into both
  gates.
- `cmd/catway/web/css/17-modal.css` — `.steplist` / `.steplist-more`.
- `docs/protocols/control-api.md` — the browser block's click paragraph.
- The Dart golden regenerated (`commands.g.dart` this time, since `RunbookInfo`
  lives in the command vocabulary rather than in browserproto).

## Checks

- Full `make check` sequence — clean. `go test -race -tags ghostty
  ./cmd/catway/...` — ok.
- Five new Go tests: the line format (id leading, sorted params, an escaped
  newline), a 4000-byte param staying off the wire, the 24-line cap with `Steps`
  still true, `paramDigest` over every value kind, and rune-aware `clip`.
- `scratchpad/rbtest.mjs` grew to **45 assertions**: which dialog opens, the
  lines reaching it, the singular/plural tail, the unresolved reference
  surviving, and — the compatibility case — an outline-less listing producing a
  dialog byte-identical to the one before this existed.
- Bundle: `node --check` + strict-mode parse; `dialogLines` and `runbookOutline`
  each declared once; modal CSS braces balanced.
- The live instance on `127.0.0.1:8520` was rebuilt and restarted mid-session so
  the user could look at it in Chrome. `catctl runbooks` confirmed the outlines
  as the browser receives them, including the `timeout_…` clip on `on-split`.

## Two things the validator taught me while writing tests

Both cost a build cycle and are worth remembering:

1. **`pane.split` takes `direction`, not `dir`.** The loader refuses an unknown
   param by name and lists the valid ones, which is how the fixture failed.
2. **An `id` is only legal on a step whose command RETURNS data.** `id: build`
   on a `pane.send_input` is refused with "returns no data, so binding it to id
   would bind nothing". The test fixture moved its id onto the `pane.split`.

Both failures surfaced as a runbook that "did not parse" and therefore had an
empty outline — the test asserted the outline and said nothing about why. Adding
an explicit `if res.Runbooks[0].Error != ""` fatal to the test is what turned two
blind failures into two readable ones; a listing test should always assert the
fixture parsed before asserting anything about its content.

## Known limits / next levers

- **No preview anywhere but the click gate.** The row's tooltip still names the
  count, and the palette entry says nothing. Both have the data now.
- **The right-click menu has no "preview" entry** — the only way to see the
  outline without committing to a dialog you then cancel.
- **`expect` and `continue_on_error` remain invisible** in the UI entirely. The
  outline was the natural place and it was deliberately declined; if they ever
  want a surface, a second line under a step is likelier than a longer one.
- **Still unverified on screen.** Chrome would not reach localhost from the
  extension for a fourth session; the user was reviewing it in their own browser
  when this was written.

## Notes for next time

- `RunbookInfo` lives in `internal/app/command_vocab.go`, not in
  `browserproto` — so adding a field there regenerates `commands.g.dart` rather
  than `wire.g.dart`. Either way: `go run ./cmd/catgen-dart -out
  cmd/catgen-dart/testdata/golden`.
- The dialog helpers are `dialogFields` / `dialogConfirm` / `dialogInput` in
  `26-picker.js`; they all build a `.modal > .body` and now share `dialogLines`.
- Spreading an options object into a dialog call (`...preview`) is a clean way
  to make a feature optional at the call site AND absent from the object when it
  does not apply — `{}` spreads to nothing, so the old dialog is genuinely
  unchanged rather than passed an empty array.
- The demo instance for browser review: port 8520, `/tmp/cats-demo.sock` and
  `/tmp/cats-demo-ctl.sock`, fixtures under the scratchpad's `cfg/cats/runbooks`
  covering every row state (vars, triggers, a broken file, a paced run, a long
  run).
