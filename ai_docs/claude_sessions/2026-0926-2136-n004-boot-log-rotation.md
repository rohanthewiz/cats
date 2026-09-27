# Session: keep the last five boot transcripts (N-004)

Session ID: 8332afc0-52f2-4a85-902d-e14a281c000d
Date: 2026-09-26

The user pasted N-004 from the next list:

> The boot transcript is one file overwritten per launch (`boot.log`), and the
> splash has no "copy this log" button (it has no bridges, so the path is in
> the failure footer instead). Only worth doing if launches ever need
> comparing.

This session did the first half. Earlier launches are kept now, so a launch
that went wrong can be compared with one that didn't.

## 1. What was there

- `cmd/catapp/bootlog.go`: `writeTranscript()` does an `os.WriteFile` over
  `~/Library/Application Support/cats/boot.log`. Two paths call it:
  `fail()` and `finish()`.
- `finish()` returns early if `done` is set, but nothing stops a launch that
  called `fail()` from writing again later. Any rotation therefore has to
  happen once per process, not once per write. Otherwise a launch's second
  write would push its own first draft into `boot.log.1` and drop the real
  previous launch.
- `daemons.log` (in `daemonlog.go`) already rotates to `.1` by size. That
  scheme doesn't fit here: a boot transcript is a few KB, and the unit worth
  keeping is a launch, not a number of bytes.

## 2. The change (cats `05f21d7`)

`cmd/catapp/bootlog.go`:

- New constant `bootLogKeep = 5`: `boot.log` plus `boot.log.1` … `boot.log.4`,
  newest first.
- New field `bootLog.rotated`. `writeTranscript()` reads and sets it under
  `mu`. Only the first write of a process rotates; later writes just replace
  `boot.log`.
- New function `rotateTranscripts(path, keep)`. It renames the oldest
  generation first (`.3→.4`, `.2→.3`, `.1→.2`, then `boot.log→.1`), so each
  target has already been moved out of the way or is the one being dropped.
  A missing generation (`fs.ErrNotExist`) is normal and skipped. Other
  errors are logged and the shift carries on, so a launch never fails because
  a log couldn't be rotated.
- The file-level comment now mentions the kept generations.

`cmd/catapp/bootlog_test.go`, two new tests:

- `TestTranscriptKeepsEarlierLaunches`: runs six simulated launches, each a
  fresh `bootLog` carrying a marker. It checks that each generation holds the
  right launch and that `boot.log.5` never exists.
- `TestTranscriptRotatesOncePerLaunch`: runs two launches, the second writing
  twice. It checks that `.1` is still the previous launch, `boot.log` holds
  the second write, and there is no `.2`.

Docs: `docs/architecture/standalone-mac.md` and
`docs/reference/troubleshooting.md` now mention `boot.log.1` … `boot.log.4`.
The troubleshooting page says a bad launch can be diffed against one that
worked.

The "copy this log" button was not built. The splash has no JS bridges, and
the failure footer already shows the path. The closing note in
`next-list.md` records this.

## 3. Verification

- `gofmt`, `go vet ./cmd/catapp`, and `go test ./cmd/catapp` all pass.
- Not tried in a rebuilt Cats.app. After the next reinstall, look for
  `boot.log.1` next to `boot.log` after the second launch.

## 4. Commits

- `05f21d7` Keep the last five boot transcripts (N-004)
- `ec337f5` Next list: close N-004 (boot transcripts kept five deep)
- `90973ae` Next list: N-004 goes at the top of Closed (newest first). The
  previous commit had appended it at the bottom, but Closed is newest first.

## Next

Closed: N-004. Declined: None. Raised: None.
Deferred: None. Promoted: None.
Updated: None. Full list: `ai_docs/todo/next-list.md`.
