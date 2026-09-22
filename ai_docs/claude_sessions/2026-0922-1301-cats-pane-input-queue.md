# Session: per-pane input queue — one stuck pane can't freeze the workspace

Session ID: 3d72d213-f69c-4448-8c33-db07c4e60594
Date: 2026-09-22
Driven from: the ced repo (follow-up to ced's session
`2026-0922-1235-wheel-burst-frame-coalescing`)

## 1. The problem

An MX Master free-spinning into ced at the end of a 4000-line file froze the
WHOLE cats workspace; closing ced's pane freed it. ced's side was fixed in
ced (one frame per wheel burst). The cats side, found by that session:

- cathost wrote every pane's input with a blocking `ptmx.Write` from the
  session's ONE dispatch goroutine (`pane.writePTY`, `internal/orchestration/
  host.go`). A child that stops reading stdin fills the kernel PTY buffer, the
  write parks, and every later message for every pane (keys, resizes, closes)
  waits behind it.
- The emulator's query-reply callback (`terminal.WithWritePTY`) wrote the
  same way from inside `emu.Write`, i.e. holding `emuMu` — which the flusher's
  `takeFrame` also takes, so frames stopped too.
- Wheel reports arrive one SGR report per notch with no coalescing
  (`inputenc/encoder.go` `wheel`).

## 2. The fix

New `internal/orchestration/paneinput.go` — `paneInput`, the input-side twin
of `Outbox`:

- **Non-blocking `push`** (copies the bytes — the emulator callback reuses its
  buffer), drained by a per-pane writer goroutine, `Host.inputPump`, which
  takes every queued chunk as ONE coalesced write.
- **Backpressure policy.** `queued` counts waiting bytes PLUS the in-flight
  batch (a write the child isn't draining is exactly the case that must
  count). Past `droppableBacklog` (4KB), a chunk made ENTIRELY of SGR wheel
  reports or buttonless-motion reports (`isDroppable` / `parseSGRMouse`) is
  discarded whole. Everything else — keys, pastes, presses, releases, drags,
  query replies, legacy X10, alternate-scroll arrows — is LOSSLESS up to
  `defaultPaneInputBudget` (2 × MaxFrameSize = 16MB), past which the caller
  gets `errPaneInputFull`.
- `host.go`: `writePTY` now pushes; `input` is created BEFORE the emulator
  (the history seed can provoke a query reply); `inputPump` starts beside
  readPump/detectPump; `closePane` closes the queue, then the PTY (which
  fails a blocked write); a failed write is reported once unless the pane is
  already closing. `ptyMu` no longer guards writes — only resizes — so a
  resize can't wait behind a stuck child (TIOCSWINSZ is safe alongside a
  concurrent write).

## 3. Tests

- `paneinput_test.go` (untagged): the droppable classifier table, in-order
  coalescing, copy-on-push, drop-under-backpressure with keys surviving,
  in-flight batch counts as backlog, lossless budget refusal, close releases
  a parked writer.
- `host_test.go` `TestHostStuckPaneDoesNotStallOthers` (ghostty): pane 1 runs
  `stty raw -echo; printf RAWREADY; exec sleep 30`, gets flooded with ~200KB
  of keys and wheel reports, and pane 2 (`/bin/cat`) must still echo.
  Verified to FAIL against the old host.go ("dispatch stalled…") and pass
  with the fix (×3 under -race).
- `go test -race -tags ghostty ./...` and untagged `go test ./...` green.

**Gotcha found along the way:** on macOS a CANONICAL-mode tty discards input
past its line limit rather than blocking the master's writer — a probe wrote
1MB into a cooked `sleep` without blocking, and blocked at once in raw mode.
So the stall only reproduces with a raw-mode child (ced, vim, any TUI), and
the test must wait for the child's marker before flooding, or the flood races
the `stty` and lands in the cooked tty.

## 4. Follow-ups (not done)

- `errPaneInputFull` emits an error event per refused message; a flood into a
  dead pane could spam them — rate-limit if it ever shows up.
- Legacy X10 / urxvt mouse encodings are never droppable (conservative); fine
  while the browser encoder emits SGR.
