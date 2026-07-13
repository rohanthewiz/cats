# WS2 — hoist the §7 command dispatch behind an app.Backend seam (WS4 prereq)

**Session id:** `c017d5e8-f786-4291-92be-1d4990e402fb`
**Date:** 2026-0713-1731 · **Branch:** `roh/phase-b` (herdr-web)
**Continues:** `2026-0713-1538-ws2-capture-command-browser-selection.md`, whose top leftover was
"hoist `orch` behind a `PaneBackend`/`Sink` interface into `internal/app`". Did exactly that.

> The §7 command table moved out of gateway2 (package main, ghostty-tagged) into `internal/app`
> (untagged, libghostty-free, unit-testable). The dispatcher mutates `app.Session` and drives
> runtime effects through three neutral interfaces `orch` implements, so a future WS4 CLI/control-API
> reuses the exact same table by implementing the same seam. Done as a planned 3-phase migration,
> each phase an independently-building, green, committed slice.

Planned first (EnterPlanMode + a Plan subagent), scope confirmed with the user as **full seam
(all 3 phases)**. Plan file: `~/.claude/plans/swift-tickling-treehouse.md`.

---

## Design — three interfaces + a dispatcher (all in internal/app)

- **`Backend`** — the orch runtime-effect seam. `Area() layout.Rect`, `ApplyModel()`,
  `BroadcastLayout()`, `BroadcastPaneTitle(pane)`, `ScrollPane(pane, delta) error`,
  `PaneExists(pane) bool`, `DaemonConnected() bool`, `StartRead(r Responder, p ReadParams)`,
  `StartCapture(r Responder, p CaptureParams)`, `ReloadConfig() error`, `Shutdown()`.
  Walked all 24 §7 cases — every effect reduces to one of these. Message construction
  (`viewportLayout`/`NewPaneTitle`/`NewShutdown`) stays behind the seam in orch, so the neutral
  dispatcher never imports browserproto.
- **`Responder`** — per-caller `cmd_result`: `WantsReply() bool`, `OK(data any)`, `Fail(errMsg)`.
  Storable in a `pending` for the async read/capture round-trips.
- **`ParamDecoder`** — `Decode(v any) error`, with sentinel `ErrNoParams`. Browser supplies a json
  decoder; a CLI would bind flags. One sentinel replaces the old `unmarshalParams`/`optUnmarshalParams`
  split: required commands surface `ErrNoParams` as `bad params: missing params`; optional commands
  ignore it via `decodeOptional`.
- **`Dispatcher{session, backend}`** with `Dispatch(name, dec, r)` — the full 24-case switch.

**Ownership:** orch keeps constructing/owning `*app.Session` (still needs it directly for
`viewportLayout`/`agentsMsg`/`effectiveTitle`/`syncDaemon`/`handleUp`); the dispatcher borrows the
same pointer. Everything runs on orch's single actor-loop goroutine, so no new locking — the
single-writer invariant is unchanged. `handleCmd` builds the dispatcher inline
(`app.NewDispatcher(o.session, o)`) rather than storing a field, which keeps the bare test harness
(`newPendingHarness`, no `newOrch`) working.

**Import direction after:** `browserproto → app → {layout, workspace}`; `gateway2(main) →
{app, browserproto}`. No cycle; `app` stays libghostty-free.

## Phase 1 — vocabulary relocation (`bec348c`)

New `internal/app/command_vocab.go`: the `Cmd*` names, `SplitH/V` + `Dir*` values,
`SplitDirection`/`NavDirection`/`BorderPath`, all param+result structs (with their json tags —
inert unless a json decoder consults them), and `optPaneID`. `browserproto/cmd.go` became thin
re-exports (const/type aliases + `var SplitDirection = app.SplitDirection`), so `wsprobe2`,
`gateway2/commands_test.go`, and browser code keep spelling `browserproto.*` unchanged.
`BorderID` (the wire encoder) stays in `browserproto/layout.go` with `BuildLayout`; `BorderPath`
(its inverse, needed by the dispatcher) moved to app and is re-exported. Zero behavior change.

## Phase 2 — the seam, whole switch moved atomically (`7c4e2bc`)

- New `internal/app/commands.go`: `Backend`/`Responder`/`ParamDecoder`/`ErrNoParams`/`Dispatcher`
  + all 24 cases (authored complete before the old switch was deleted — never a half-migrated table).
- `cmd/gateway2/gateway.go`: `pending` now holds `resp app.Responder` (was `{c, id}`); `startRead`/
  `startCapture` → Backend `StartRead`/`StartCapture` storing the responder; `replyPending` collapses
  to `resp.Fail`/`resp.OK`; added the Backend adapter methods (one-liners over
  `applyModel`/`broadcast`/`viewportLayout`/`effectiveTitle`/`daemon.connected`/`daemon.send`).
- `cmd/gateway2/commands.go`: `handleCmd` → 2-line delegate; new `browserResponder`
  (`WantsReply`=`id!=""`) + `jsonParamDecoder` (empty ⇒ `ErrNoParams`). Old switch + `optPaneID` deleted.
- `cmd/gateway2/daemon.go`: deleted `unmarshalParams`/`optUnmarshalParams`.
- `cmd/gateway2/pending_test.go`: `pending{c,id}` → `pend(o,c,id)` helper wrapping `browserResponder`.

**Behavior-parity landmines held exactly:** read/capture short-circuit on `!WantsReply()` *before*
`StartRead` (no orphan pending); `workspace.close` ignores *all* decode errors; `server.stop` calls
`OK` *before* `Shutdown`; missing-params reproduces `bad params: missing params`.

## Phase 3 — untagged dispatcher tests (`fbff016`)

`internal/app/commands_test.go` — 15 tests, first coverage below the ghostty-tagged/daemon-backed
integration tests. `fakeBackend` + `fakeResponder` share an event log (asserts effect/reply
ordering) over a **real `app.Session`** (reuses `newTestSession`/`fakeSpawner` from
`session_test.go`); a `jsonDec` mirrors the browser decoder. Asserts: focus rebroadcasts+acks
without touching the pane set; bad split direction fails without mutation or reconcile; valid split
reconciles once; read with no reply channel is a no-op; read/capture on unknown pane (and down
daemon) fail before any round-trip; valid read starts the round-trip carrying the caller's responder;
scroll surfaces the backend error; an all-optional command decodes empty params to the zero value;
`workspace.close` ignores malformed params; `server.stop` replies before Shutdown; reload_config and
the unknown-command default reply as expected.

## Files

- `internal/app/command_vocab.go` (new) · `internal/app/commands.go` (new) ·
  `internal/app/commands_test.go` (new)
- `internal/browserproto/cmd.go` (defs → aliases) · `internal/browserproto/layout.go` (`BorderPath` re-export)
- `cmd/gateway2/commands.go` (shrink `handleCmd`; `browserResponder`+`jsonParamDecoder`) ·
  `cmd/gateway2/gateway.go` (Backend adapters; `pending` holds Responder) ·
  `cmd/gateway2/daemon.go` (drop param helpers) · `cmd/gateway2/pending_test.go` (`pend` helper)

## Verification (macOS)

- Build: `PKG_CONFIG_PATH=~/projs/rust/herdr/vendor/libghostty-vt/zig-out/share/pkgconfig`.
  `go build ./...` (untagged) + `go build -tags ghostty ./...` clean. `go vet` both clean.
  gofmt clean on all touched/new files (go1.26.1 toolchain; folded the two gofmt nits into the
  Phase 2 commit before it was pushed). Pre-existing gofmt-dirty files
  (`session_test.go`/`down.go`/`proto_test.go`) predate this work and were left alone.
- Tests: `go test ./...` green (incl. the new untagged `internal/app` dispatcher tests, 15/15);
  `go test -tags ghostty ./cmd/gateway2` green (drives `handleCmd`→dispatcher unchanged);
  **`-race` clean** on unit tests and a live race-built gateway2.
- **Live** (real ghostty daemon, one wsprobe2 script through the refactored dispatch): a model
  mutation (`split`→panes=2, `close`→panes=1), focus/last, `tabnew`/`tabfocus`, `read`+`readeq`,
  `capture`+`capturehas`, `scroll`, `reloadconfig`, `serverstop` — all PASS, 0 races, and the
  persistent termhost daemon **survived `server.stop`** (the key requirement). Binaries to `$scratch`.

## Notes / leftovers

- **WS4 CLI/control-API** can now reuse the whole §7 table by implementing `Backend` + `Responder`
  + a `ParamDecoder` (e.g. flag/map-backed). No gateway2 code needed below the seam.
- `optPaneID` now lives in `internal/app` (used by the dispatcher); the gateway2 copy is gone.
- The repo carries a **tracked** `termhost` binary at root (committed Jun 24, pre-dates this work) —
  left untouched. Worth a future `.gitignore`/`git rm` cleanup, out of scope here.
- Browser-side leftovers from the prior session remain (selection-wash polish; a "copy scrollback"
  chrome button for `capture`; copy-mode keyboard motions).
