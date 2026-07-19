# pane.wait_for_output: raw-byte-stream matching (β protocol v2)

**Session id:** `aad4e50e-50cb-41f5-a517-5f9873f75184`
**Date:** 2026-0715-1752 · **Branch:** `roh/phase-b` (herdr-web)
**Continues:** `2026-0715-1606-events-structural-pane-added-removed-focus.md`, whose
"still deferred" note named this as **the last substantive WS4 streaming item**:
raw-byte-stream matching for `pane.wait_for_output` (a `pane_output` orchestration
event → protocol bump + termhost readPump changes).

> `pane.wait_for_output` now matches against the pane's **live raw output stream**
> instead of polling captured screen text. This closes the two edges the
> capture-based approach left open: the child's **final pre-exit output** (a
> post-exit capture can't reach a torn-down pane) and **fast-scrolling transient
> output** (a line that flashes past between 60 Hz captures). The daemon streams
> raw PTY bytes only while a waiter is active; the orchestrator strips VT escapes
> to plain text and matches. Unit-tested at every layer (untagged + ghostty,
> race-clean) and live-verified end-to-end against a real gateway2 + persistent
> termhost + herdrctl.

---

## The mechanism (three layers)

**1. β protocol v2 (`internal/orchestration/protocol.go`).** Bumped
`ProtocolVersion` 1→2 (a v1 daemon would silently ignore the new command and
never stream, so the handshake now *rejects* the mismatch rather than degrading).
Two new messages:
- `set_output_stream {pane_id, enabled}` (Rust→Go command): toggles a pane's raw
  stream.
- `pane_output {pane_id, data}` (Go→Rust event): a chunk of raw PTY bytes, base64
  on the wire, VT escapes and all. Not browser-facing.

**2. termhost streams raw bytes when subscribed (`host.go`).** New per-pane
`streamOutput atomic.Bool`; `set_output_stream` sets it. `readPump` emits
`NewPaneOutput(copy(buf[:n]))` on each read **while enabled** — in the read loop,
*before* the EOF break emits `pane_exited`, so the child's final output is
delivered for matching ahead of the exit. Off by default: a pane with no waiter
never pays the raw-stream cost (the diffed-frame architecture is untouched). The
copy is mandatory — `buf` is reused and `emit` is async.

**3. gateway2 matches the cleaned stream (`gateway.go` + `daemon.go` + new
`outscan.go`).**
- `outputScanner` (untagged `outscan.go`, so it's testable without libghostty): an
  **incremental ANSI stripper** — a state machine over CSI / OSC / DCS-SOS-PM-APC /
  charset / two-byte escapes, carrying state across chunk boundaries so an escape
  split over two reads is still consumed whole. Keeps printable text + `\n`/`\t`,
  drops `\r` and other C0. Feeds a **bounded rolling buffer** (64 KiB tail, matched
  before trim, so any pattern completing within one ≤32 KiB daemon read is seen
  intact). It is a *stripper*, not an emulator — cheap, and the daemon already owns
  the real emulator.
- `StartWaitForOutput` now, on the **first** waiter for a pane, creates the
  accumulator and sends `set_output_stream(true)` **before** the one-shot seed
  capture (enable-then-seed ordering closes the enable-latency gap — output in that
  window is streamed, at worst duplicated with the seed, never missed).
- `onPaneOutput` feeds each chunk through the scanner and matches the result;
  `onWaiterText` (the seed capture reply) and `onPaneOutput` share `matchWaiters`.
- `removeWaiter` on the **last** waiter drops the accumulator and sends
  `set_output_stream(false)`; `flushWaiters` (daemon drop) drops accumulators
  without a send. `sendStreamSub` is nil-daemon-guarded (bare-orch tests).
- `daemon.go`: new `MsgPaneOutput` dispatch case → `onPaneOutput`. **Removed** the
  frame-triggered `triggerWaiterCheck` from `MsgPaneFrame` — the stream drives
  matching now, so frames are purely for browser rendering again. `triggerWaiterCheck`
  survives as the one-shot seed (its sole caller is now `StartWaitForOutput`).

**Seed preserved.** The initial capture-check still seeds the match with output
**already on screen** when the wait begins (so a pattern already visible resolves
at once). Live output rides the stream. `Lines` now bounds only the seed's
recent-rows scan; the live stream is matched in full (documented on
`WaitForOutputParams`). No ctlproto / herdrctl changes — it still rides the unary
await envelope.

## Verification

- **Unit** (all green, untagged + `-tags ghostty`, race-clean):
  `orchestration/protocol_test.go` (set_output_stream + pane_output round-trip,
  base64 byte-fidelity, v≥2 guard); `orchestration/host_test.go`
  (`TestHostStreamsPaneOutput` — real PTY: enable stream, drive input, assert
  pane_output carries the bytes, **and FINALMARK streams before pane_exited**);
  `gateway2/outscan_test.go` (CSI/OSC strip, escape split across feeds, `\r` drop,
  UTF-8 passthrough, buffer-bound-keeps-tail); `gateway2/waiter_test.go`
  (`TestWaiterMatchesStream` colour-wrapped pattern → clean line + accumulator
  torn down, cross-chunk match, post-resolve chunk ignored).
- **Live** (real gateway2 `--auth none` + persistent termhost + herdrctl +
  wsprobe2 `type:` input injection; **β protocol v2 negotiated cleanly** in the
  logs):
  - **transient fast-scroll:** `wait 1 4242` registered, then `seq 1 9000` flooded
    the pane → `{"matched":true,"text":"4242"}` (a line buried deep in a 9000-line
    flood, emitted long after registration — stream-only, no screen capture would
    catch it).
  - **final-frame-at-exit:** `wait 3 GOODBYE`, then `printf GOOD''BYE` (marker
    contiguous **only** in the printf *output*, never in the echoed input) followed
    by `exit` → `{"matched":true,"text":"GOODBYE"}`. The marker existed only in the
    pane's final pre-exit output.
  - **seed (already on screen):** `echo SEED''MARK` rendered first, *then*
    `wait 1 SEEDMARK` → matched in **0.028s** via the seed capture.
  - **no-match:** `wait 1 ZZZ_NEVER 2` → `{"matched":false}` at 2.01s.
  - `server.stop` stopped gateway2; the persistent termhost survived.

## Files

- **New:** `cmd/gateway2/outscan.go` (+`outscan_test.go`).
- **Modified:** `internal/orchestration/protocol.go` (+`protocol_test.go`),
  `internal/orchestration/host.go` (+`host_test.go`), `cmd/gateway2/gateway.go`,
  `cmd/gateway2/daemon.go`, `cmd/gateway2/waiter_test.go`,
  `internal/app/command_vocab.go`, `internal/app/commands.go`. ~564 insertions.

## Notes / leftovers

- **WS4 streaming is now complete** — `events.subscribe` (all 7 events) and
  `pane.wait_for_output` (unary await, now raw-stream matched) both land.
- **Semantic note (documented in code):** the gateway strips ANSI but does **not**
  emulate cursor motion, so a bare-`\r` progress redraw (`50%\r100%`) accumulates
  rather than overwriting — fine for substring/regex matching (newest bytes always
  present), and far cheaper than a second gateway-side emulator. A regex anchored
  with `^`/`$` across such a redraw won't see line boundaries; substring is the
  common case.
- **Pre-existing cosmetic (not this session):** the `serving at http://localhost<addr>`
  log line double-prefixes host when config supplies a full `host:port` addr.
- **Repeatable browser-driven check** (carried): `cmd/wsprobe2`'s `type:` op is the
  reusable headless input injector — no bespoke WS injector needed this session.
  Still worth capturing the launch harness (persistent termhost + gateway2
  `--auth none` + short `/tmp` sockets) as a project `/run` skill.
