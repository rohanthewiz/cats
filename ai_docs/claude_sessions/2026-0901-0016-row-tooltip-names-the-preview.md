# Session: The Row Tooltip Names the Preview

- **Session ID:** `4332df6f-9883-4da8-a605-39e935dd1129`
- **Date:** 2026-09-01
- **Branch:** main
- **Repo:** `cats`
- **Follows:** `2026-0901-0011-preview-entry-in-the-runbook-menu.md`, whose *Known
  limits* named this gap. Third session in a row that closes the previous one's
  first lever.

## Request

> make the row tooltip mention the preview

The previous session had written:

> **The row tooltip still says "click to run · right-click for more"** — it does
> not name the preview, which is the one menu item a first-time reader would
> want pointed at.

## The design decisions

### Name one verb, not four

The menu holds *run…*, *preview steps*, *open in editor*, *copy path* and the
`catctl` spelling. The tooltip names exactly one of them, because a first-time
reader is not asking what else is in the menu — they are asking two things:
**is the click the only way in**, and **can I look before I leap**. "right-click
to preview the steps" answers both; "for more" answered neither. The rest of the
menu introduces itself once the menu is open.

### The running row is the case that actually needed this

An idle row's gain is real but modest — its click already opens a gate that
lists the steps, so the preview is a convenience.

A **running** row is the case with a hole in it. `startRunbookRun` refuses a
second run before any dialog opens, so the click does nothing but toast, and the
tooltip previously ended at `running step 2 of 3` / `started here` — **no verb at
all**. That is precisely the moment somebody wants to see what is coming: panes
are appearing and the next step is the question. So the running branch gains the
hint as its last line, and pointedly does *not* say "click to run" — offering a
click that is refused would be worse than saying nothing.

### One function decides, so the two surfaces cannot disagree

Added `runbookHasPreview(rb, broken)` and made **both** `runbookTitle` and
`runbookMenuItems` ask it. The failure this forecloses is specific and quiet: a
tooltip that says *right-click to preview* over a menu with no preview entry.
That is a promise the UI would break in exactly the situation the gate exists
for — a broken file, or a listing from a server too old to send an `outline`.

Both conditions are load-bearing and stay in the helper's comment: `broken` has
no steps to show (its menu is about the error), and an absent-or-empty `outline`
means the server does not send one.

### The older, vaguer line stays where there is nothing to name

Without an outline the tooltip falls back to `click to run · right-click for
more` verbatim. Pointing at an entry that is not built would be the bug above;
naming nothing is the honest older behaviour, unchanged.

### Broken rows untouched

They keep `click to open it in the editor` and promise no preview — asserted, so
a later edit cannot quietly change it.

## What shipped

- `cmd/catway/web/js/41-runbooks.js` — `runbookHasPreview`, the two tooltip
  branches in `runbookTitle`, and `runbookMenuItems` switched onto the helper
  (its comment trimmed to point at the helper rather than restate the gate).
- `docs/protocols/control-api.md` — the browser block's right-click paragraph now
  says the tooltip names the entry, and that on a running row the hint is the
  whole of what the row offers.

No Go, no CSS. Entirely a second reader of a field that already existed.

## Checks

- `go build ./...`, `go test ./cmd/catway/...`, `gofmt -l` — clean (no Go changed).
- Bundle: concatenated `js/*.js` in a strict-mode closure, `node --check` ok;
  `runbookHasPreview` declared once.
- `scratchpad/tiptest.mjs` — **25 assertions**, all passing. Covers the gate in
  all four states; the idle line naming the preview and dropping "for more"; the
  no-outline and empty-outline fallbacks; the running row keeping position and
  origin, gaining the hint last, and never offering the refused click; the
  positionless (`running…`) and trigger-origin variants; a running row with no
  outline getting no hint at all; the broken row's own line; description, vars
  and trigger lines surviving; and menu-vs-tooltip agreement in all three states
  plus unchanged menu order.

## Notes for next time

- **The slice-by-name harness had to be rebuilt.** Last session's
  `previewtest.mjs` lived in a session scratchpad and is gone; this session wrote
  the same `slice(text, name)` + `new Function` trick again from the session doc's
  description. Two rebuilds is the signal — **lift it into the repo** (something
  like `cmd/catway/web/js/testutil.mjs` plus a `make jstest`) before a fourth
  session pays for it a third time. This one needed no DOM stub at all, since the
  functions under test return strings and arrays.
- Still unverified on screen — Chrome would not reach localhost from the
  extension again, three sessions running. The demo-instance recipe (port 8520,
  `/tmp/cats-demo.sock`, fixtures under the scratchpad) still applies.

## Known limits / next levers

- **The palette still says nothing.** `runbook.list`'s outline is available to it
  and a palette entry could carry the first line or the step count. Untouched for
  three sessions now; it is the last surface where the outline is on the wire and
  unread.
- **`expect` and `continue_on_error` remain invisible.** Still out of the
  outline; the notice dialog remains the likelier home than the run gate.
- **The tooltip is now four-to-six lines.** Adding a seventh should mean removing
  one, not appending.
