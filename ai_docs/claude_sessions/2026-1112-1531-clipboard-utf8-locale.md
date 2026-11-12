# 2026-11-12 15:31 — ⌘V "clipboard has no text" was a missing UTF-8 locale

Session: `7c4d69c0-f180-4b27-8cd5-00d147f9dbce`

## Request

"In Cats sometimes CMD+v doesn't allow paste - it says clipboard has no text,
however when I do CTRL+v inside of Cats-Todo (../cats-todo), the clipboard
text is pasted."

## Cause

The toast comes from `pasteText()` (`cmd/catway/web/js/21-clipboard.js`). In
the mac app it reads through `window.catsClipRead`, which leads to
`catappClipRead`, `clipboardRead` and `/usr/bin/pbpaste`. The ObjC handler
turns the result into an `NSString` with `[NSString stringWithUTF8String:]`.

- launchd starts Cats.app with no `LANG`. `ps -E` on the running app, catway
  and every cats-todo pane showed no `LANG`/`LC_*`. `hydratePATH` adopts only
  the login shell's PATH.
- With no UTF-8 locale, pbpaste transcodes through MacRoman. Measured on the
  `ruler` pasteboard: `“` → `d2`, `é` → `8e`, `🐱` → `??`.
- `stringWithUTF8String:` returns nil for invalid UTF-8. The reply is null,
  `if (t)` fails, and the page toasts "clipboard has no text". So it was
  "sometimes" because it happened whenever the clipboard held a non-ASCII
  character, which is most text copied from a browser, Slack or a doc.
- cats-todo uses the same pbpaste with the same missing locale. Its Ctrl+V
  only *looked* fine: Go accepts the bytes, and the editor turns each bad
  one into `�`. ASCII text pastes correctly.
- The write side was broken too. pbcopy read the UTF-8 input as MacRoman, so
  `a “b” é` copied from a pane reached other apps as `a ‚Äúb‚Äù √©`.
- Probing which variables count: `LANG=C LC_ALL=en_US.UTF-8` and
  `LANG=en_US.UTF-8 LC_ALL=C` both still give MacRoman. Any one non-UTF-8
  value among LANG, LC_ALL and LC_CTYPE is enough, so all three are pinned.

## Fix (cats `c727a48`, cats-todo `cfce052`)

- `internal/clipboard.UTF8Env()` returns `os.Environ()` with LANG, LC_ALL and
  LC_CTYPE appended as `en_US.UTF-8`. os/exec keeps the last value of a
  duplicated key. The darwin `pbpaste` reader is marked `utf8Locale`, so
  catway's `catctl clipboard` read gets it too.
- `cmd/catapp/clipboard.go`: `clipboardWrite` and `clipboardRead` run under
  `UTF8Env`. A new `validUTF8` guard replaces invalid sequences with U+FFFD
  and logs a line, so an encoding slip can no longer turn into a wrong "no
  text" diagnosis.
- cats-todo `clipboard.go`: a local `pasteboardEnv()` with the same three
  overrides for its pbcopy and pbpaste. It is kept local rather than imported
  from cats to avoid a cats re-pin.
- Docs: `docs/reference/troubleshooting.md` ("clipboard has no text" said the
  pasteboard must have been emptied, which was wrong for older builds) and
  `docs/architecture/standalone-mac.md` (Clipboard bridge).

## Tests

- `TestPasteboardRoundTripsUnicodeWithoutLocale` (internal/clipboard) unsets
  the locale the way launchd leaves it, then runs the real pbcopy and pbpaste
  on the `ruler` pasteboard, so the user's clipboard is untouched. It checks
  both directions against an explicitly UTF-8 reference side.
- The first version paired `UTF8Env` with itself and passed even with the pin
  removed (a mutation check caught it). With no locale, both tools use
  MacRoman and the two mistranslations cancel out. The bug only shows when
  one side is a Unicode-native app. The rewritten test fails without the fix
  in both directions and passes with it.
- `TestUTF8EnvOverridesInheritedLocale`, cats-todo
  `TestPasteboardEnvPinsUTF8`: the override beats an inherited `LC_ALL=C`.
- `TestValidUTF8*` (cmd/catapp): MacRoman bytes are repaired, and valid text
  passes through unchanged.
- These pass: `go test ./internal/clipboard ./cmd/catapp ./cmd/catctl`, the
  catway clipboard tests (`-tags ghostty`, with libghostty's
  `PKG_CONFIG_PATH`) and the full cats-todo suite.
- Not yet seen in a rebuilt, reinstalled Cats.app (N-070).

## Next

Closed: None. Declined: None. Raised: N-070, N-071. Deferred: None.
Promoted: None. Moved: None. Updated: N-001.
Full list: `ai_docs/todo/next-list.md`.
