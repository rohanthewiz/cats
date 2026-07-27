# Session: macapp Font Sizing + Clipboard via Native Menus/Bridge; r-ed Esc-z Undo Alias

- **Session ID:** `2d61e74d-af64-43a7-8c71-9c685b008a6b`
- **Date:** 2026-07-27
- **Branch:** main (cats); r-ed changes uncommitted in `~/projs/go/r-ed`

## Requests

1. Running as a mac app, ⌘+/⌘-/⌘0 font sizing (added last session) doesn't work.
2. With the `r-ed` editor (`~/bin/rd`) in a pane inside the macapp: ⌘C doesn't
   copy, and ⌘V toasts that the clipboard is empty.
3. Add an undo gesture to r-ed, "perhaps ESC-Z".

## Root causes

### 1. Font sizing dead in the app (fixed — commit `6fd877b`)

The page handles ⌘+/⌘-/⌘0 in a JS `keydown` handler, but Cocoa resolves
Cmd-key presses as **menu key equivalents before the WKWebView's page sees a
keydown**. With no View menu, the keys matched nothing and died — the same
WKWebView quirk that originally forced the Edit menu into `menu_darwin.m` for
⌘C/⌘V.

**Fix** (mirrors the Quit → `catappCleanup` pattern):

- `cmd/catapp/menu_darwin.m` — View menu: Bigger Text (⌘+, plus a **hidden ⌘=
  twin** with `allowsKeyEquivalentWhenHidden` so the unshifted key works),
  Smaller Text (⌘-), Default Text Size (⌘0); actions target `gMenuTarget`.
- `cmd/catapp/menu_darwin.go` — cgo export `catappZoom(delta)`; +1/-1 step, 0 reset.
- `cmd/catapp/main.go` — package-level `uiWindow`; `zoomFont` dispatches
  `w.Eval("window.catsAdjustFont && window.catsAdjustFont(N)")`.
- `cmd/catway/web/index.html` — exposes `window.catsAdjustFont(delta)` beside
  `setFontSize`, reusing clamping/localStorage/resize/toast logic.

### 2. Copy/paste with rd in a pane (fixed — commit `3fe0133`)

Two independent breaks:

- **⌘C never reached rd anywhere.** rd binds Cmd+C/Cmd+V via the kitty
  keyboard protocol (tcell v2.13 pushes `ESC[>1u`; delivers super+c as
  `ModMeta`). But cats' web client dropped **every** Cmd key
  (`if (e.metaKey && !e.ctrlKey) return`), so no frontend ever forwarded it.
  The server side was already complete: `internal/inputenc/encoder.go:270`
  maps the client's meta bit → libghostty `ModSuper`, honoring the pane's
  kitty flags.
- **WKWebView cripples `navigator.clipboard`.** Reads resolve empty (hence the
  "clipboard has no text" toast on ⌘V); writes demand a user activation that
  WebSocket-driven copies (OSC 52 from rd, §7 read results) never have — so
  even rd's menu-driven OSC 52 copy silently failed in the app.

**Fix:**

- `cmd/catway/web/index.html` — ⌘C now falls through to the pane as a
  structured key with the meta bit; kitty-protocol apps get super+c, legacy
  panes encode nothing (matches Ghostty). New `clipWrite`/`clipRead` helpers
  prefer a bridge when injected, else `navigator.clipboard`; all four clipboard
  sites converted (OSC 52 "clipboard" msg, `pasteText`, `readAndCopy`,
  `copyScrollback`).
- `cmd/catapp/clipboard.go` (new) + `main.go` — app binds
  `catsClipWrite`/`catsClipRead` into every page, backed by
  `/usr/bin/pbcopy` / `/usr/bin/pbpaste` (cgo-free). Remote mode gets the
  bridge too; browsers unaffected.

Flow now: ⌘C → super+c → rd copies selection → OSC 52 → forwarded up →
native pasteboard; ⌘V → pbpaste → bracketed paste into the pane.

Watch item: Edit ▸ Copy also claims ⌘C; evidence (⌘V toast) says the page gets
first crack and `preventDefault` wins. If ⌘C ever goes dead in-app, fallback is
routing the menu Copy item through Go like the zoom items.

### 3. r-ed undo (already existed; alias added — uncommitted in r-ed repo)

r-ed already has full undo: `internal/editor/undo.go` (per-tab snapshot stack,
500 entries, typing coalesced in a 500ms window), bound as **Esc-u / Esc-r**.
Added the Cmd+Z-muscle-memory aliases, same pattern as Esc-k → palette:

- `internal/app/leader.go` — `{'z', menuUndo}`, `{'Z', menuRedo}` (shifted pair
  mirrors h/H, o/O).
- `internal/app/leader_test.go` — new `TestHandleKey_LeaderUndoRedoAliases`;
  the tests using `'z'` as the canonical *unbound* probe now use `'y'`.
- `internal/app/app_test.go` — same probe swap in the Alt-folded leader test
  (tmux delivers Esc-z as Alt+z).
- `website/content/docs/hotkeys.md` — added `Esc z` / `Esc Z` rows.

Full `go test ./...` passes in r-ed. Esc leaders are plain keystrokes, so this
works identically in macapp/browser/tmux — no Cmd plumbing.

## Key learnings

- **WKWebView Cmd-key model:** menu key equivalents are the reliable channel;
  the page sees a Cmd keydown only if it `preventDefault`s before the menu
  claims it. Any new app-level ⌘ shortcut needs a menu item routed through a
  cgo export + `Eval` of a page hook.
- **WKWebView clipboard:** `navigator.clipboard` is effectively unusable
  (empty reads, activation-gated writes). The `catsClipRead`/`catsClipWrite`
  bridge is the pattern; page helpers `clipRead`/`clipWrite` are the single
  choke points.
- **Key forwarding chain is fully wired:** web `mods` bit 8 (meta) →
  `browserproto.ModMeta` → libghostty `ModSuper` → kitty CSI-u, gated per-pane
  by the app's kitty flags — safe to forward more ⌘ keys later if needed.
- `index.html` is `go:embed`ed in catway — UI changes need `make macapp`, not
  just a catapp rebuild.

## Commits (cats, main)

- `6fd877b` fix(macapp): route ⌘+/⌘-/⌘0 font sizing through a native View menu
- `3fe0133` fix(macapp): native clipboard bridge + forward ⌘C to kitty-protocol panes

## Pending

- r-ed changes (Esc-z/Esc-Z + tests + hotkeys doc) are **uncommitted**; owner
  to rebuild `~/bin/rd` to pick them up.
