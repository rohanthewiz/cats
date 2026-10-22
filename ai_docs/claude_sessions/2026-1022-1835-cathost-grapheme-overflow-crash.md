# 2026-10-22 18:35 — cathost crash: libghostty-vt grapheme overflow

Session: `4a1ddbdb-b81a-40dc-81e9-ffa92cb715fd`

## Request

Cats.app was running but showed "cathost daemon not connected — retrying".

## Diagnosis

- `ps`: catapp and catway were up, with no cathost process.
- `~/Library/Application Support/cats/daemons.log` at 2026-10-22 16:00:35 had
  this panic:

  ```
  cathost panic: runtime error: slice bounds out of range [:18] with capacity 8
    libghostty.(*RenderStateRowCells).AppendGraphemes   render_state_cell.go:428
    terminal.(*ghosttyEmulator).Snapshot                ghostty.go:442
    orchestration.(*Host).paneScreenAndTitle            host.go:1569
    orchestration.(*Host).detectPump                    host.go:1523
  ```

  catapp logged `cathost (pid 66875) exited while the app was running: exit
  status 2` and did nothing more. catway kept retrying the dead socket
  (`cats-th-<pid>.sock`, connection refused).
- The panic is the SUCCESS branch of `AppendGraphemes`, which returns
  `dst[:oldLen+len]`. The C getter had reported success with `len=18` into
  an 8-byte buffer (Snapshot's `buf := make([]byte, 0, 8)`).
- First suspect was a data race, so every `p.emu` call in `orchestration`
  (host.go, ledger.go) was checked. All of them hold `emuMu`, which ruled
  that out.
- The real cause is in the vendored libghostty-vt,
  `src/terminal/c/render.zig` → `rowCellsGetGraphemesUtf8`:

  ```zig
  var needed = std.unicode.utf8CodepointSequenceLength(cell.codepoint()) catch …
  ```

  `utf8CodepointSequenceLength` returns `u3`, so `needed` is inferred as
  `u3` and wraps at 8. An 18-byte cluster (a ZWJ family emoji such as
  👨‍👩‍👧 is exactly 18) counts as 2, passes `out.cap < needed`, and the
  encode loop writes 18 bytes into the Go buffer. The build is ReleaseFast,
  which drops the overflow and bounds checks, so this was a heap overrun into
  Go memory before the panic. Any cluster longer than 8 bytes can trigger it:
  emoji with skin tones, ZWJ sequences, stacked combining marks.
- The installed app (built 2026-10-17) and the vendored source match. The
  bug has been there since libghostty-vt was vendored (`7a6393db`).

## Changes

- `third_party/libghostty-vt/src/terminal/c/render.zig`:
  `var needed: usize = …`, with a comment on why.
- New Zig test, `render: row cells get graphemes utf8 longer than 8 bytes`.
  It uses a 17-byte cluster of `a` plus 8×U+0301, since combining marks
  attach without mode 2027. An 8-byte buffer must get `out_of_space` with
  the true length and be left untouched, and a 32-byte buffer must get the
  full cluster. Run against the old line, it fails with `panic: integer
  overflow`; with the fix it passes.
- `internal/terminal/ghostty_test.go`: `TestLongGraphemeCluster` checks that
  the same cluster comes back whole from `Snapshot`, with the next cell
  intact. `strings` was added to the imports.
- Rebuilt libghostty-vt (`scripts/build-libghostty-vt.sh`).
  `go test -race -tags ghostty ./internal/terminal/ ./internal/orchestration/`
  passes.
- Not done: `make macapp`. It replaces `/Applications/Cats.app`, and the
  user restarts the app. A relaunch starts a fresh cathost; the pane layout
  restores from `session.json`, but the processes in the panes died with the
  old cathost.

The pre-existing uncommitted ⌘X change in `cmd/catway/web/js/20-keys.js`
(cut sent to kitty-protocol panes) was committed separately as part of the
wrap. It was not authored or tested in this session.

## Finding: a cathost-only crash has no recovery path

N-025 (Non-goals) declined an automatic cathost restart because "the overlay
tells the user to quit and reopen". That overlay (`ui.catwayDown` in
`cmd/catapp/restart.go`) only goes up when *catway* uses up its restart
budget. Nothing watches cathost's `daemonProc.done` except
`cathostExited()`, which only the catway restart path calls. So a lone
cathost exit leaves the window stuck on a retry toast. This was raised as
N-068, with the restart-vs-overlay choice left to the user.

## Next

Closed: None. Declined: None. Raised: N-068. Deferred: None.
Promoted: None. Moved: None. Updated: None.
Full list: `ai_docs/todo/next-list.md`.
