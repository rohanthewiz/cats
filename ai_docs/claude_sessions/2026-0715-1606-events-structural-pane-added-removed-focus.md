# events.subscribe: structural events (pane_added / pane_removed / focus_changed)

**Session id:** `ad19a7b0-bb99-4903-be3c-3148a93199c8`
**Date:** 2026-0715-1606 · **Branch:** `roh/phase-b` (herdr-web)
**Continues:** `2026-0715-1554-ws4-streaming-events-subscribe-wait-for-output.md`, whose
"deferred (as scoped)" note listed the structural events. Same session id as that doc — this is
the immediate follow-on increment (committed separately).

> The three model-structure events deferred from the streaming-methods pass, added to
> `events.subscribe`. **Derived by diffing the session**, not by new commands or daemon changes:
> every model mutation already funnels through `applyModel()` (pane-set + focus) or
> `BroadcastLayout()` (pure focus/rename), so a snapshot-and-diff at those two hooks catches every
> add/remove/focus change. Unit-tested (ghostty) and live-verified via plain herdrctl commands
> (`split`/`focus`/`close`) — no WS injection needed since these are model mutations.

`events.subscribe` now emits the full set: `pane_exited`, `pane_agent`, `pane_title`, `pane_cwd`
(terminal-backend sourced) + `pane_added`, `pane_removed`, `focus_changed` (model-derived).

---

## Design

| Event | Fires when | Payload |
|---|---|---|
| `pane_added` | a pane enters the session (split / new tab / new workspace) | `{pane, handle}` |
| `pane_removed` | a pane leaves (close pane / tab / workspace) | `{pane, handle}` — the handle it last had |
| `focus_changed` | the globally-focused pane changes | `{pane, handle}` — newly-focused (0 if none) |

All three share **one** payload struct, `app.PaneRefEvent{Pane, Handle}` — they only name a pane
(the other four events have genuinely different fields, so they keep their own structs). `Pane` is
the internal id every §7 command addresses by; `Handle` is the public label ("w1:p3").

**Mechanism (`cmd/gateway2/gateway.go`):**
- New orch snapshot: `structPanes map[uint32]string` (id → handle) + `structFocus uint32`.
- `seedStructure()` primes the snapshot in `newOrch` (after `refreshViewport`), so a subscriber
  that connects later **never gets a retroactive `pane_added`** for pre-existing panes.
- `emitStructuralEvents()` rebuilds the current pane set, emits `pane_added` for ids not in the
  snapshot and `pane_removed` for ids gone from it, then `focus_changed` if `focusedPaneID()`
  moved — and updates the snapshot. Hooked at the **end of `applyModel()`** (all pane-set +
  most focus changes) **and the `BroadcastLayout()` Backend method** (the focus-only commands —
  `pane.focus`/`focus_direction`/`cycle`/`last` — that route there; rename is a no-op diff).
- Kept current **even with zero subscribers** (no early-return on empty `subs`), so a late
  subscriber always diffs from a live base. Resize / daemon-reconcile / rename pass through as
  no-op diffs. `emitEvent` already filters per-subscriber and drops slow sinks.
- `focusedPaneID()` = `session.FocusedPane()` as uint32 (0 if none).

**Clean separations preserved:** `pane_removed` is model-only (close commands); a pane whose child
**exits** stays in the model with exited state and fires `pane_exited`, not `pane_removed`. No
herdrctl/ctlproto changes — the `events` verb already streams every event and the filter accepts
any name (only the doc comment was updated to list the structural events).

## Verification

- **Unit** (`gateway2/waiter_test.go`, ghostty): `recSub` extended to capture payloads; new
  `TestStructuralEvents` drives a **real session over a bare orch** (via `newOrch`; the daemon
  socket never connects, sends drop — fine): split → `pane_added(new)` with the pre-existing pane
  **not** re-announced; explicit focus of the other pane → `focus_changed`; close → `pane_removed`
  with handle. All green, untagged + `-tags ghostty`, gofmt clean.
- **Live** (real gateway2 `-auth none` + persistent termhost + herdrctl, structural events driven
  purely by control commands):
  - `split h` → `pane_added{2,w1:p2}` then `focus_changed{2}` (split auto-focuses the new pane)
  - `focus 1` → `focus_changed{1}`
  - `close 2` → `pane_removed{2,w1:p2}` (no spurious `focus_changed` — pane 1 was already focused)
  - pane 1 **not** re-announced when the subscriber connected (seeding confirmed)
  - raw-path filter `events.subscribe --params '{"events":["focus_changed"]}'` + `split v` →
    only the `focus_changed{3,w1:p3}` line delivered; `pane_added` correctly dropped

## Files

- **Modified:** `internal/app/events.go` (3 event consts + `EventNames` + `PaneRefEvent`),
  `cmd/gateway2/gateway.go` (snapshot fields, `seedStructure`, `focusedPaneID`,
  `emitStructuralEvents`, hooks in `applyModel` + `BroadcastLayout`),
  `cmd/gateway2/waiter_test.go` (`recSub` payload capture + `findRef` + `TestStructuralEvents`),
  `cmd/herdrctl/main.go` (doc-comment mention).

## Notes / leftovers (WS4)

- **Still deferred:** raw-byte-stream matching for `pane.wait_for_output` (a `pane_output`
  orchestration event → protocol bump + termhost readPump changes) — would close the
  final-frame-at-exit edge and never miss fast-scrolling transient output. This is the last
  substantive WS4 streaming item.
- **Pre-existing cosmetic (not this session):** the `serving at http://localhost127.0.0.1:8477`
  log line double-prefixes host when config supplies a full `host:port` addr.
- **Repeatable browser-driven check** (carried): the scratchpad stdlib WS injector (init v1 + Raw
  keystrokes) from the prior session remains a reusable seed for a headless input helper / `/run`
  skill; structural events, being command-driven, needed none of it.
