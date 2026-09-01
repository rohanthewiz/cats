# Session: A Preview Entry in the Runbook Menu

- **Session ID:** `4a1a9bd4-e5ef-428e-ae54-38498ef9c2a0`
- **Date:** 2026-09-01
- **Branch:** main
- **Repo:** `cats`
- **Follows:** `2026-0901-0005-step-outline-in-the-run-gate.md`, whose *Known
  limits* named this exact gap. Loaded as context, then closed in one request.

## Request

> add a preview entry to the right-click menu

The previous session had written:

> **The right-click menu has no "preview" entry** — the only way to see the
> outline without committing to a dialog you then cancel.

So the shape was already argued for; this session only had to decide what a
preview *is* when it is not a gate.

## The design decisions

### A notice, not a confirm with a "run" button

The tempting version is a preview whose primary button runs the thing. It was
declined for two reasons, and the second is the decisive one:

1. A preview that can start what it previews **is the gate again**, and the gate
   is one right-click away in the same menu.
2. **The vars case breaks the button.** A runbook with declared vars cannot run
   from a confirm — it needs `dialogFields`. So the button would sometimes
   commit and sometimes open a *second* panel showing *the same list*, which is
   worse than not having it.

### What the preview is FOR — two cases the gate cannot serve

- **Reading a runbook you are not about to run.** Getting there through the run
  dialog means opening a panel that says "run 4 steps against this session" and
  then pressing cancel — it works, and it asks the user to decline something
  they never proposed.
- **A row that is already running.** `startRunbookRun` refuses a second run
  *before any dialog opens* (the server's slot is per name), so on a running row
  there was no way to see the steps at all — and that is precisely the moment
  somebody wants to: panes are appearing and they want to know what is next.

That second case is why the preview reports the run state in its own message
line (`3 steps · running step 2`): a preview opened from a running row is being
read against what is happening on screen, and the `<ol>`'s numbering lines up
with the position the row is showing.

### `dialogNotice` = `dialogConfirm` minus the choice

Added `opts.noCancel` to `dialogConfirm` (a one-line guard) and a thin
`dialogNotice` wrapper over it, rather than a second modal builder. Everything
that makes the two look alike — header, body, `dialogLines`, Enter/Esc — is
worth having **identical**: a preview and the gate it previews are the same
panel showing the same list, and a reader should recognise the second from the
first. Esc still reaches `closeModal` directly, so the single button is a
convenience rather than the only way out.

`onClose` exists and is normally absent. A notice that has to *run* something on
close is a confirm wearing a disguise.

### Headed by the name, not by the word "preview"

The list below is self-evidently a preview; the name is what the reader opened
the menu to be sure about.

### Gated on the OUTLINE, not on the file having parsed

`if (!broken && rb.outline && rb.outline.length)`. A server too old to send the
field would otherwise offer a preview that opens onto a step count and no steps,
which is worse than no entry. An outline that arrived empty is the same case.

### Placed between *run…* and *open in editor*

`run…` stays first because it mirrors the click. Preview sits beside *open in
editor* because those two are **the same verb at two magnifications** — a glance
at what it does, or the file that says why.

## What shipped

- `cmd/catway/web/js/26-picker.js` — `dialogNotice`, and `opts.noCancel` on
  `dialogConfirm`.
- `cmd/catway/web/js/41-runbooks.js` — `previewRunbook(rb)`, reusing the existing
  `runbookOutline(rb)` (so the "…and 197 more steps" tail comes along unchanged),
  and the menu entry in `runbookMenuItems`.
- `docs/protocols/control-api.md` — the browser block's right-click sentence,
  including the older-server behaviour.

No Go changed: the outline was already on the wire from the previous session, so
this is entirely a second consumer of a field that already existed. No CSS
either — `.steplist` was already generic.

## Checks

- `go build ./...`, `go test ./cmd/catway/...`, `gofmt -l` — clean.
- Bundle: concatenated `js/*.js` inside a strict-mode closure, `node --check` ok;
  `dialogNotice` and `previewRunbook` each declared once.
- `scratchpad/previewtest.mjs` — **28 assertions**, all passing. It slices the
  functions out of their files by name (they are declared at two-space indent
  and closed by a line that is exactly `  }`) and evaluates them against a small
  DOM stub, rather than booting a front-end that resolves DOM ids at load. It
  covers: the menu gate in all four states (outline, no outline, empty outline,
  broken); the notice's header, message, `<ol>`, per-item `title`, and single
  *close* button; description-present vs. absent and the vars list; the running
  and running-with-position lines; the truncation tail's plural, singular and
  silent cases; and — the compatibility case — that `dialogConfirm` still builds
  cancel + primary exactly as before.

## Notes for next time

- **The slice-by-name test harness is the reusable part here.** `cmd/catway/web/js`
  is one closure over the DOM, so unit-testing any of it used to mean booting all
  of it. `slice(text, name)` + a ~20-line `El` stub + `new Function(...)` over the
  extracted sources gives a real test of a handful of functions in a few seconds.
  Worth lifting into the repo if a third session wants it.
- The dialog family is now `dialogFields` / `dialogInput` / `dialogConfirm` /
  `dialogNotice`, all in `26-picker.js`, sharing `dialogLines`.
- Still unverified on screen — Chrome would not reach localhost from the
  extension again. The demo instance recipe from the previous session (port 8520,
  `/tmp/cats-demo.sock`, fixtures under the scratchpad) still applies.

## Known limits / next levers

- **The palette still says nothing.** `runbook.list`'s outline is available to it
  and a palette entry could carry the first line or the step count.
- **The row tooltip still says "click to run · right-click for more"** — it does
  not name the preview, which is the one menu item a first-time reader would
  want pointed at.
- **`expect` and `continue_on_error` remain invisible.** Still deliberately out
  of the outline; a notice has more room than a gate, so if they ever get a
  surface, this dialog is now the likelier home for it than the run gate.
