# Session: Reaping the Dead — Exited Panes Get a TTL, and a Respawned Pane Stops Being a Corpse

> Session: https://claude.ai/code/session_01ArbqgLNgYG5X4zaciG8aZ7
> Date: 2026-08-26
> Repo: cats, on main — landed as `0a3a696`
> Follows: `2026-0826-1116-wide-glyph-spacer-clipping.md`

## The prompt

> Reap closed panes after 4 hrs

Then, after the first pass landed:

> Make the TTL configurable in config.yaml, and fix the pre-existing bug

## Reading the ask

"Closed panes" needed pinning down first — nothing in the tree retains a *closed*
pane. `ClosePane` unregisters the pane number, `histSaveNow` prunes the seed,
`nav.go` drains dead entries from the focus trail. Nothing accumulates.

What does accumulate is the **exited** pane. `cmd/catway/daemon.go`'s
`MsgPaneExited` handler sets `rt.exited = &code` and leaves the pane in the
model; `index.html` turns the header red (`.dead`, `exited (N)`) and keeps the
last screen. That is deliberate — `index.html:6150` says so out loud: the pane
"stays on screen after exit (exited chrome), so the git/build output" is still
readable. But nothing ever removed it again except a hand-issued `pane.close`,
so a days-long session silts up with corpses, each holding a slot in its tab's
BSP tree, a `paneRuntime`, and a scrollback seed in `history.json`.

Host-side there is nothing to reclaim: `readPump` already calls `removePane` +
`closePane` before emitting `pane_exited`, so the emulator is freed at exit and
a later `close_pane` for that id is a no-op. The waste is entirely catway's.

## What landed — part 1: the reaper

`cmd/catway/reap.go`, a five-minute ticker in the same shape as
`runHistoryCapture` / `runAgentModels` / `runPaneBranches` (own goroutine, only
ever `o.post`s onto the loop).

```
pane_exited ──▶ rt.exited = &code
                rt.exitedAt = now      (first exit only)
                     │
      every 5 min ───┼──▶ now - exitedAt >= ttl ?
                     │         │
                     │         └─▶ Session.ClosePaneIn(pane)
                     │                  ├─ refused (last pane) → skip, keep the stamp
                     │                  └─ closed → drop capturedHist seed
                     │
   createPane ───────┴──▶ exit state cleared: this pane is alive again
```

Three judgements keep it safe to run unattended:

* **Old enough.** `exitedAt` is a new `paneRuntime` field, separate from
  `exited` because the two answer different questions — `exited` is "is this a
  corpse" (chrome, input refusal, capture skip), `exitedAt` is "how long has it
  been one", and only the latter follows the reaper. First exit wins the stamp,
  so a replayed `pane_exited` cannot push the clock back.
* **Never the last pane.** `Session.ClosePaneIn` already refuses it ("cannot
  close the last pane"); the sweep skips what it is refused *without clearing
  the stamp*, so an idle session cannot reap itself to nothing and the corpse
  still goes the moment a second pane exists.
* **Still dead.** See part 2.

Reaping is an ordinary close from the model's point of view: `ClosePaneIn` +
one `applyModel` for the whole sweep, so the layout broadcast, viewport
recompute, `pane_removed` events and debounced save all happen exactly as for a
hand-issued close. `capturedHist` entries are dropped per reaped pane —
`histSaveNow` prunes to the model, but only when a capture arms a write, and a
reaped pane produces no more captures.

## What landed — part 2: `panes.reap_exited`

```yaml
panes:
  reap_exited: "4h"   # "off" / "0" / "never" keeps exited panes forever
```

* `config.Panes` + `ReapExitedAfter()`, defaulted to `"4h"` in `Default()`,
  checked in `Validate()` alongside `session_ttl` and `push.min_interval` — a
  typo or a negative fails at load, not four hours later.
* Off is a value of the same knob, not a second one: `""`, `"0"`, `"off"`,
  `"never"`, `"none"` all resolve to 0 = never reap.
* The orch carries `reapAfter`, **seeded with the built-in default by the
  constructor**, not by the config layer. A zero means "never", so a code path
  that forgot to fill the field would otherwise switch the feature off in
  silence; config overrides the constructor rather than being its only source.
* Live-reloadable — `ReloadConfig` re-reads it, so `catctl reload` moves the
  line without a restart. That made it the second server-side setting that is no
  longer restart-only (after `hosts:`), so its doc comment and the live-reload
  diagram in `docs/reference/configuration.md` were updated to say so.

## What landed — part 3: the undead respawn

Found while making the reaper safe, and it predates all of this.

`createPane` is reached for a pane the daemon no longer holds — a cold restore,
a host reconnect after a cathost restart (`reconcile` drops `created` for every
pane not in the host's alive set), or a pane moved to another host
(`hosts.go:276`). Every one of those ends with a **fresh child on a fresh PTY**.
`rt.exited` was left set through all of them. The pane was undead:

* `SendInput` refused with `pane N has exited` — typing into a live shell,
  rejected;
* `captureHistory` skipped it, so it never got a cold-restore seed again;
* `syncAppFocus` withheld DEC 1004 focus reports;
* the header stayed red until the page was reloaded;
* and now, a reaper clock ticking on a live shell.

`createPane` clears the exit state instead. That needed a wire message, because
the exit is **remembered by the client**: the chrome a late joiner gets simply
omits `pane_exited` for a live pane, which retracts nothing for a window that
already drew the header. So `pane_respawned` (pane id, no code — the pane is
alive), sent via `sendVisible` and only when the pane was actually dead;
`index.html` clears `p.exited` and re-renders. Additive within protocol v1, so
an old client ignores it and behaves exactly as it did.

## Verification

* `make test-ghostty`, `make test`, `go vet -tags ghostty ./...` — all green.
* **Mutation-checked both halves.** Reverting `createPane`'s clear (leaving only
  the `exitedAt` reset) fails `TestRespawnClearsTheExitState` and
  `TestRespawnTellsTheWatchingWindows`; the sweep tests fail against a stale TTL
  or a missing last-pane guard.
* Nine tests in `cmd/catway/reap_test.go`: TTL boundary both ways, the
  configured-TTL knob (off / 4h / 1h over the same corpse), the constructor
  default, the last-pane refusal *and* the corpse going once a second pane
  exists, respawn-skips-reap, respawn-clears-exit-state, respawn-tells-the-
  window (including: a *live* pane's respawn announces nothing), and the
  first-exit-wins clock via the real `daemon.dispatch` path.
* `internal/config`: `TestParsePanes` (default, explicit, six off-switch
  spellings), two new `TestValidateRejects` cases, and `Panes` added to the
  example-config drift check.

## Gotchas worth remembering

* **`exited` and `exitedAt` are deliberately two fields.** Collapsing them would
  tie "is this a corpse" to "is the reaper counting", and the respawn path needs
  to clear both while the chrome/publish paths only ever read the first.
* **`createPane` is the one choke point for "this pane got a fresh PTY"** — cold
  restore, host reconnect, host move all route through it. Anything that should
  be reset when a pane comes back to life belongs there, not in `reconcile`.
* **A browser remembers a pane's death.** Chrome for late joiners is built by
  *omission* (`sendPaneChrome` sends `pane_exited` only when dead), so any
  state a client latches needs an explicit retraction message. `pane_respawned`
  is the first one of these; expect the same shape for the next latched fact.
* **Adding a `browserproto` message type breaks the Dart golden.**
  `cmd/catgen-dart` generates `cats-mobile/packages/catsproto` from the Go wire
  types and `TestGoldenIsUpToDate` fails in *this* repo — deliberately, so the
  drift fails the commit that causes it. Regenerate with
  `go run ./cmd/catgen-dart -out cmd/catgen-dart/testdata/golden`. The copy in
  `../cats-mobile` still has to be updated by hand — **not done in this session**.
* `MsgPaneRespawned` is longer than every other name in the down-message const
  block, so gofmt re-aligned all 22 lines of it. Real change is one constant.
* `histSaveNow` prunes the history file to panes still in the model, but only
  runs when a capture arms a write. A pane removed by anything *other* than a
  capture-producing path has to drop its `capturedHist` entry itself.
* `Session.ClosePaneIn(wsID, target)` ignores `wsID` entirely when `target` is
  non-nil — the workspace is resolved from the pane. A view-less caller can pass
  `""` safely.

## Still open

* `../cats-mobile/packages/catsproto/lib/src/generated/wire.g.dart` is a version
  behind (missing `pane_respawned`). Harmless — an unknown type is ignored — but
  it is drift.
* The reaper is not surfaced anywhere in the UI: a pane vanishing four hours
  after it died is only explained by the catway log line. If that reads as
  spooky in practice, the sidebar's pane list is where to say it.
