# Session: N-032 and N-007, zoom steps in recordings and blank windows after an outage

Session ID: 8739cd4d-8ba0-45d1-bd78-16ba30826a86
Date: 2026-09-26
Driven from: cats

The user pasted two next-list items in turn. Each was fixed, tested and
committed on its own.

## 1. N-032: the recorder captured ⌘+/⌘- and sidebar drags

> ⌘+/⌘- and sidebar drags now write `ui` prefs via config.set, which is a
> Recorded command, so zooming while a macro records captures a config.set
> step.

The page's `persistUIPref` (`cmd/catway/web/js/33-settings.js`) debounces a
zoom or a gutter drag. When the value settles it sends
`config.set {options: {ui: {font_px|sidebar_width: n}}}`. The command table
marks `config.set` as `Recorded: true` (`wire/vocab.go`). So an armed recorder
reserved a slot at `Begin` and filled it at `Commit` like any other step.

### Choice: skip it in the recorder, keep persisting

The item offered two options. We took the first one: skip a ui-only config.set
in the recorder.
- **Not in the command table.** `Recorded` applies to the whole command.
  Theme switches and panes changes through config.set are real steps and must
  stay recorded. The difference is in the params, and they only exist at
  `Commit`.
- **Not "don't persist while recording".** That would lose any zoom made
  during a recording. It would also only cover the page's own writes, so the
  same ui-only call from catctl or another browser would still be captured.

### Change

`cmd/catway/record.go`: a new function, `viewerPrefsOnly(cmd, params)`, returns
true for a `config.set` with no theme, no copy_mode, and exactly one options
section, `ui`. `macroRecorder.Commit` checks it first and `drop`s the slot. No
`changed()` fires, because the visible step count is `doneCount` and does not
move. The pref still saves: the command itself runs unchanged.

A config.set carrying ui **plus** another section is recorded whole. The
recorder keeps params exactly as sent (see `recordDecoder`), so removing one
section would make the step something the user never called.

Test: `TestRecordSkipsUIOnlyConfigSet` in `cmd/catway/record_test.go`. A
ui-only set saves (`o.cfg.UI.FontPx == 18`) and records nothing. A ui+panes
set records one `config.set`. We confirmed the test fails with the check
disabled. `go test -tags ghostty ./cmd/catway ./internal/app` passes.

Commit `00214ba`.

## 2. N-007: windows opened during an outage stayed blank

> A window opened or reloaded while catway is down stays blank after
> recovery. `catwayBack` only evaluates JS in existing pages.

`catwayBack` (`cmd/catapp/backenddown.go`) ran `backendClearJS` in every window
via `catsEvalAll`. A window whose load failed has no document, so there was
nothing to evaluate in and nothing to reconnect. `relaunch`
(`cmd/catapp/restart.go`) calls `ui.catwayBack()` after every successful
relaunch, automatic ones included, so it is the right hook.

### Change

`cmd/catapp/window_darwin.m`:
- `CatsWindowController` gets `@property(copy) NSURL *failedURL`. It uses
  `copy` because the file is compiled without ARC, so the synthesized setter
  does the retain and release. It is cleared in `windowWillClose` (there is no
  dealloc).
- A new method, `noteFailedLoad:`, is called from both
  `didFailProvisionalNavigation` and `didFailNavigation`. It records only
  `NSURLErrorDomain` failures other than `NSURLErrorCancelled`. A cancelled load
  was superseded by a newer one, and retrying it would drag the window back.
  WebKit-domain failures would fail the same way on a retry. The URL comes
  from `NSURLErrorFailingURLErrorKey`, because after a failed provisional load
  `webView.URL` is the previously committed page, which is nil for a new
  window.
- `didFinishNavigation` clears `failedURL`. That includes the connect form's
  `loadHTMLString`, so a window moved to the form is never retried.
- A new function, `catsReloadFailedWindows()`, calls `loadRequest` on
  `failedURL` for each window that has one. Windows that loaded fine are left
  alone, because their page reconnects its own WebSocket.
- `catsWindowsJSON` now uses `web.URL ?: failedURL`. A window that never
  loaded keeps its `?ws=` in the restore list instead of being saved as the
  primary view.

`cmd/catapp/window_darwin.go` adds a wrapper, `winManager.reloadFailed()`.
`catwayBack` now clears the overlay and reloads the failed windows in one
main-thread hop.

It builds, and `go vet ./cmd/catapp` and `go test ./cmd/catapp` pass. **It was
not exercised in the app**, because that means killing the live catway. Manual
check:
1. Kill catway until the automatic restarts are spent and the overlay shows.
2. Press ⌘N; the new window should be blank.
3. Click Restart catway. The new window should load, and the other windows
   should drop the overlay.

Commit `0d427c1`.

## Notes

- Another session committed N-031 (`85861b1`, the config reference as JSON) on
  main while this one ran. Its Closed entry sits above this session's two.
- An open question from this session: whether a failed *reload* of a live page
  leaves the old document running (WebKit keeps the committed page on a
  provisional failure) or blanks it. The fix covers both: the old page, if
  still there, just gets the reload the user asked for.

## Next

Closed: N-032, N-007. Declined: None. Raised: None.
Deferred: None. Promoted: None.
Updated: None. Full list: `ai_docs/todo/next-list.md`.
