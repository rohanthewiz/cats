# Session: "no such pane" traced to exited panes (N-036)

Session ID: ef61385d-fdfa-4f88-a92f-696318e142a1
Date: 2026-09-24

## 1. The asks

1. Next-list item N-036: `catway: daemon error (pane N): no such pane` is
   still a WARN (3 in 8 days of the installed app's `daemons.log`). Find where
   it comes from before demoting it, since it might be a real race between a
   pane closing and a command addressed to it.
2. "commit and push" (already done as part of 1).
3. `/sw`.

## 2. Where it comes from

The installed app's `~/Library/Application Support/cats/daemons.log` had all
three hits in one burst for a single pane, about 90s after a launch
(2026-09-17 10:57:08.778, then two at 10:57:09.617).

The daemon (`internal/orchestration/host.go`) sends "no such pane" from the
`input` dispatch and from `resizePane`, `scrollPane`, `requestSelection` and
`requestText`. Resync and set_output_stream ignore a missing pane without an
error, and `close_pane` is a no-op for one.

The pane goes missing because the read pump calls `h.removePane` at PTY EOF
(`host.go:1205`), then `cmd.Wait()`, and only then emits `pane_exited`.
catway keeps the exited pane in `o.panes` with `rt.exited` set, on screen for
the reaper's tidy-exit countdown. So it is not a close race: any catway send
without an `rt.exited` check reaches the daemon for a PTY that is gone.

Send sites checked in `cmd/catway`:

| Send | Guarded before? |
|---|---|
| Key / Mouse / Paste (`inputTarget`) | yes |
| focus reports, `pane.send_input` | yes |
| reconcile resize (`syncDaemon`) | **no**: every layout change hit it |
| `ScrollPane` (catctl scroll) | **no** |
| `StartRead` / `StartCapture` | **no** |
| `triggerWaiterCheck` | **no** |
| `Raw` input | **no** |
| history sweep (`persist.go`) | yes (skips exited) |
| ledger block requests | not affected: id-keyed, the daemon answers `Found:false` |

Two costs besides the WARN:

- Every error was also broadcast to the browser, which toasts
  `"error: " + msg` (`web/js/19-messages.js:151`), so the user saw a bare
  "error: no such pane".
- Read/capture of an exited pane never resolved. `orchestration.Error` names
  no request kind, so the pending entry could not be matched, and the caller
  waited the full 5s `reqTimeout` and got "timed out".

`exited` is only cleared in `createPane` (respawn / cold restore / host move),
so gating sends on it cannot strand a live pane.

## 3. The fix (`1f1a960`)

- `orchestration.ErrNoSuchPane` (`protocol.go`) is the wire string, used by
  every site in `host.go` that sends it, so catway can match on it.
- Reconcile: `case changed && rt.exited == nil`. The new grid is still stored
  on the runtime (`rt.cols/rows`, `enc.SetGrid`), so a respawn spawns at the
  right size.
- `ScrollPane` returns "pane N has exited", in the words `send_input` uses.
- `StartRead` / `StartCapture` go through a new `failExited` helper that fails
  the responder at once.
- `triggerWaiterCheck` and the `Raw` case skip exited panes.
- `daemon.go` MsgError: `ErrNoSuchPane` logs via `log.Printf` (informational,
  as N-006 did) and is not broadcast. What is left is the gap between EOF and
  `pane_exited` reaching the loop, which nothing can close, and a command sent
  in it is moot. Every other daemon error stays WARN plus toast.

`dlog` has only WARN/ERROR/FATAL, so "demote" means `log.Printf`, which is how
N-006 did it.

Tests: `cmd/catway/nosuchpane_test.go` (ghostty tag).

- `TestResizeSkipsExitedPane` uses a pipe daemon: after a window resize the
  live pane gets a resize and the exited one doesn't, but the exited pane
  still records its new grid.
- `TestReadAndCaptureOfExitedPaneFailFast`: capture, read and scroll fail at
  once with "has exited", and nothing is left pending.

Both tests failed with the `catway.go` gates stashed and pass with them.
`go test -tags ghostty -race` passes for `./cmd/catway`,
`./internal/orchestration` and `./internal/app`.

Reminder: `cmd/catway` builds only with `-tags ghostty`. A plain
`go build ./...` passes without compiling it.

The installed Cats.app has to be rebuilt and reinstalled to pick this up.

## Next

Closed: N-036. Declined: None. Raised: N-039.
Deferred: None. Promoted: None.
Updated: None. Full list: `ai_docs/todo/next-list.md`.
