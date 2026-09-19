# Dictation text sink: giving macOS dictation somewhere to land

*Edit ▸ Start Dictation in the mac app opened the microphone but no text ever
reached the pane. The panes are canvases and focus sat on `<body>`, so the
host had no text-input client to insert into. A hidden `<textarea>` now holds
focus when nothing else wants it and forwards committed text to the focused
pane as a paste. Tests pass; the fix has NOT been tried in the app yet.*

Session: b0234bff-0c84-477d-8a8a-0858267dc435
Date: 2026-09-18
Repos: `~/projs/go/cats` (branch `main`)
Previous doc: `2026-0916-1547-peer-sync.md`

## Request

> Using the Start Dictation menu does seem to start something, but after
> dictation I see no text in the prompt editor

## Diagnosis

- "Start Dictation" is not in `cmd/catapp/menu_darwin.m`. AppKit appends it
  (and Emoji & Symbols) to any menu titled Edit on its own.
- Dictation inserts through the first responder's text-input client.
  WKWebView only has one while an editable element is focused.
- The web front-end never focuses anything editable in the ordinary state:
  panes are `<canvas>`, and `20-keys.js` reads raw `keydown`/`keyup` on
  `window`, so `document.activeElement` is `<body>`
  (`25-modal.js`'s `focusField` note already says as much).
- Result: the mic opens, words are recognised, insertion is a silent no-op.
  IME composition, the emoji palette and text replacement were dropped by the
  same hole.

## Changes

| File | What |
| --- | --- |
| `cmd/catway/web/js/42-textsink.js` (new) | Invisible 1px fixed-position `<textarea id="textsink">`. `parkTextSink` focuses it only when `activeElement` is `<body>`; re-parks on `focusout`, `mouseup` and window `focus` (one frame later). `input`/`compositionend` → `flushTextSink` → `{t:"paste"}`; the value is emptied on flush so the WebKit/Chrome event-order difference cannot double-send. Flushes are held while composing so dictation's revised hypotheses never reach the PTY. Text is dropped while `uiOpen()` or copy-mode. `placeTextSink` moves the sink to the focused pane's cursor at `compositionstart` (approximate: `p.ox/p.oy/p.gs`). Skipped on `(pointer: coarse)` devices so a touch screen does not raise the soft keyboard. |
| `cmd/catway/web/js/20-keys.js` | `onKey` returns early on `e.isComposing || e.keyCode === 229`, so keys steering a composition are neither typed into the pane nor `preventDefault`ed. |
| `cmd/catway/web/assets.go` | Registers `42-textsink.js` in `jsFiles`. |
| `cmd/catway/web/jstest/runbook-palette.test.mjs` | Adds the missing `openPeersDialog` stub. |

### Why `{t:"paste"}`

The server's paste encoder (`catway.go`, `rt.enc.Paste`) already wraps in
bracketed paste when the pane asked for it and scrubs control bytes — so a
dictated newline cannot submit a half-finished prompt. No new wire message.

## Found along the way

`make jstest` was already failing on `main`: the peers commit (`e491984`)
added a "peers / sync…" palette entry that names `openPeersDialog` eagerly in
an object literal, and `runbook-palette.test.mjs` had no binding for it.

## Verification

- `go test ./cmd/catway/...` — ok
- `make jstest` — all nine suites pass (after the stub fix)
- NOT verified: actual dictation in the mac app. Sessions run inside Cats.app
  and cannot drive its GUI, and rebuilding would replace the running app.

## To try

1. `scripts/build-macapp.sh`, reinstall, relaunch.
2. Focus a Claude Code pane, Edit ▸ Start Dictation, speak.
3. Expect text to arrive when a phrase commits; the mic popup should sit near
   the terminal cursor.

## Open points

- IME users compose blind: the marked text lives in the invisible sink until
  it commits. A visible preedit overlay at the cursor would be the follow-up.
- Dead keys (⌥e) are still `preventDefault`ed by `onKey` unless the engine
  reports them as `keyCode 229`; unchanged from before.
- If focus-parking misbehaves with some overlay, `parkTextSink` is the single
  place to gate it.
