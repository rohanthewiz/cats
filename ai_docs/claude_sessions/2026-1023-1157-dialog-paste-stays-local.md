# 2026-10-23 11:57 — ⌘V in the New Workspace dialog stays in the dialog

Session: `e1efa9e2-f8a8-4d07-8ad7-c4ff25882628`

## Request

From the cats-todo backlog: "The New Workspace dialog does not receive CMD+v
paste, rather it goes to the CEd editor below."

## Cause

`cmd/catway/web/js/21-clipboard.js` registered a page-wide `paste` listener
that sent every native paste event to the focused pane. The only exception
was the chat composer. In the Mac app, Cocoa's Edit ▸ Paste menu item owns
the ⌘V key equivalent, so the page never sees a ⌘V keydown, only the `paste`
event. `onKey` (20-keys.js) already stands aside while `uiOpen()`, but the
paste listener had no such check, so a paste aimed at a dialog input was
cancelled and typed into the pane underneath. In a plain browser the effect
is the same: `onKey` returns early for the open dialog, the browser's own ⌘V
fires `paste`, and the listener takes it.

## Fix (cats `09db82b`)

- The listener became a named `onPaste`. It returns without
  `preventDefault`, so the browser inserts the text where the user is
  typing, when:
  - the chat composer holds the target (as before);
  - the target is an editable field (`isEditableField`: text-like
    `<input>`, a `<textarea>`, or contenteditable, not read-only or
    disabled) other than `textSinkEl`. The text sink is a textarea too, but
    it stands in for the terminal, so its pastes still go to the pane;
  - `uiOpen()` is true, the same rule onKey applies to keys. This covers a
    focused dialog button.
- The fix covers every dialog input (rename prompts, the path picker,
  settings), not only New Workspace.
- New test: `cmd/catway/web/jstest/paste.test.mjs` (16 assertions). It
  covers sink → pane, a dialog input staying local, an editable field outside
  a modal staying local, a dialog button with a dialog open, the
  `isEditableField` edge cases, body focus → pane, and an empty clipboard.

## Verification

- `node cmd/catway/web/jstest/paste.test.mjs`: 16 assertions passed.
- `make jstest`: all suites pass.
- `go build ./cmd/catway/...` and `go test ./cmd/catway/` pass.
- Not yet seen in a rebuilt Cats.app (N-069).

## Next

Closed: None. Declined: None. Raised: N-069. Deferred: None.
Promoted: None. Moved: None. Updated: None.
Full list: `ai_docs/todo/next-list.md`.
