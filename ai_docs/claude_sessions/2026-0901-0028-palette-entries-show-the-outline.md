# Session: The Palette Entries Show the Outline

- **Session ID:** `583454bf-6d2a-4e67-a32d-cb2fa8adf259`
- **Date:** 2026-09-01
- **Branch:** main
- **Repo:** `cats`
- **Follows:** `2026-0901-0016-row-tooltip-names-the-preview.md`, whose *Known
  limits* named the palette as "the last surface where the outline is on the
  wire and unread". Fourth session in a row closing the previous one's first
  lever — and the one that finally paid the harness debt three of them logged.

## Request

> make the palette entries show the outline

## The design decisions

### One line, three depths

A palette row is one line; an outline is up to 24. So the entry spends what it
has and puts the rest where a row can still reach it:

```
CMD  run runbook: deploy…   built: pane.split direction="right" pane=1   3 steps
```

- **`sub`** — the first step, always visible.
- **`meta`** — the total, so the sub reads as *one of three* rather than as the
  whole runbook. Without it a single step line is indistinguishable from a
  one-step document.
- **`title`** — the outline entire, on hover. Enter already reaches the same
  list in the gate, so this is the mouse's shortcut, not the only route.

### The first step, not the description

The obvious field for a secondary column is `description`, and it is the wrong
one. A description is optional, and where it exists it usually restates the name
in a longer form ("deploy — deploys the app"). Step one is the fact that
separates two runbooks whose *names* have stopped separating them — `deploy`
beside `deploy-staging` — which is exactly the moment a fuzzy list stops helping.

### Searchable because visible

The haystack was `label + meta + kind`: every field the row renders. The `sub`
joins it, which makes a runbook findable by something it *does* (`split` finds
the one whose first step is `pane.split`).

The same rule settles the question the outline raises — why not search all 24
lines? Because only the first is on screen, and **a row that matched for a
reason you cannot see is the worst row in a fuzzy list.** The other steps are
one Enter away in the gate, which is a list you read rather than one you filter.

### The old-server fallback is the entry it always was

No `outline` (a server too old to send the field) → all three fields empty and
the row is byte-for-byte what it was before this existed. Same fallback
`runbookOutline` already takes in the dialogs, asserted on all three fields so a
stray `undefined` cannot start rendering an empty span. Broken files stay
excluded, unchanged — the palette must not offer what the server will refuse.

### The tail wording lives in one place

`runbookOutlineText` is built on `runbookOutline` rather than joining
`rb.outline` itself, so "…and 176 more steps" is worded once. A second copy is
how a dialog and a tooltip start disagreeing about the same number — the same
argument that produced `runbookHasPreview` last session.

### `.sub` moved to where it now belongs

The class was defined in `21-plugins.css` (for the linked-checkout path) with a
`.pal .row .sub` selector. It has two callers now, so the rule moved up to
`20-palette.css` with the rest of the row chrome; `21-plugins.css` keeps a
pointer. Its `flex:0 1 auto; min-width:0` already did the right thing here for
free: the sub gives way before the label, because a clipped step line still
reads and a clipped runbook name does not.

## The harness debt, paid

Three session docs in a row noted that the `slice(text, name)` + `new Function`
trick was being rebuilt in a scratchpad and thrown away. It is now in the repo.

`cmd/catway/web/js/` ships as ONE closure with no exports (see the note atop
`assets.go`), so nothing there can be imported. The harness lifts a function's
source out of its part file and evaluates it in a scope the test supplies:

```
part file(s)  ──slice()──▶  just the named functions' source
                                │
     env + stubs ─────────▶  new Function("__env", `const a = __env.a; …`)
                                │
                           ◀────┴──  { name: fn, … }
```

- `jstest/testutil.mjs` — `readPart`, `parts`, `slice` (brace counting with a
  scanner that skips comments and strings), `loadFns({files, names, env, stubs})`,
  and a runner whose whole output is a tally and an exit code. `stubs` exists for
  the references evaluated *eagerly* (`fn: openSettings`) but never called.
- `jstest/runbook-palette.test.mjs` — 24 assertions.
- `jstest/bundle.test.mjs` — compiles the whole concatenation inside head.go's
  `(() => { … })()`. This is the only check that catches a `const` declared in
  two part files, which reaches the browser as a blank page and which no Go test
  would see: they assert the files are *listed*, not that the text parses.
- `make jstest`, in `make check` between `test` and `vet-ghostty`, and in CI's
  quick job. Skips loudly where there is no `node`.

A sibling `jstest/` directory rather than `js/`: the embed glob is `js/*.js`, so
an `.mjs` there would have been safe — but `js/` should stay exactly what ships.

## What shipped

- `cmd/catway/web/js/41-runbooks.js` — `runbookLead`, `runbookOutlineText`.
- `cmd/catway/web/js/31-palette.js` — the runbook entry's three fields, the
  defaults in the `paletteCommands` map, the `.sub` span and `row.title` in the
  renderer, and the sub in the fuzzy haystack.
- `cmd/catway/web/css/20-palette.css` / `21-plugins.css` — `.sub` moved.
- `cmd/catway/web/jstest/` — new: `testutil.mjs`, two test files.
- `Makefile` — `jstest` target, in `.PHONY` and in `check`.
- `.github/workflows/ci.yml` — `make jstest` in the quick job.
- `docs/protocols/control-api.md` — a paragraph on the palette in the runbook
  block.
- `docs/reference/build-and-packaging.md` — `jstest` in the `make check`
  flowchart, plus what it is and why it can exist at all.

No Go changed.

## Checks

- `make check` — exit 0 end to end (fmt-check, vet, build, test, jstest,
  vet-ghostty, race-ghostty).
- **30 JS assertions**: the lead line and its two empty cases; the outline text
  with and without a tail, singular and plural; the three entry fields; the
  capped hover; the title *not* repeating the name; all three fallback fields on
  an outline-less listing; the broken row still excluded; the entry still opening
  the gate for its own runbook; an ordinary command carrying no columns; a step
  word matching the sub and pointedly *not* the label; the bundle compiling; and
  the four function names declared exactly once across it.

## Notes for next time

- Still unverified on screen — Chrome would not reach localhost from the
  extension, four sessions running. Demo-instance recipe (port 8520,
  `/tmp/cats-demo.sock`, fixtures under the scratchpad) still applies.
- The harness needed no DOM again. The first test that does will want a stub
  factory in `testutil.mjs`, not a second copy in the test file.

## Known limits / next levers

- **A running runbook's palette entry is a dead Enter.** `startRunbookRun`
  refuses it with a toast, which is exactly what the file's own comment says a
  palette must not do ("the palette must not list something it knows the server
  will refuse" — the recorder's rule). The entry could carry `running 2/5` in
  the meta, or route to `previewRunbook` instead of the gate. This is now the
  clearest hole in the surface.
- **The palette is 520px** and the sub gets roughly 38 characters of it. The
  plugins dialog widened to 620px for exactly this reason; the palette could,
  but it is a long-standing surface and truncation is graceful.
- **`expect` and `continue_on_error` remain invisible.** Unchanged for three
  sessions: still out of the outline, and the notice dialog remains the likelier
  home than the run gate.
- The outline now has four readers (both gates, the preview notice, the palette).
  A fifth should ask whether `runbookOutline` wants to own more of the shaping.
