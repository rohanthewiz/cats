# Session: a refresh control on USAGE, and how old the numbers are

- **Session ID:** `d80c3d2e-37d9-42d1-aaa2-de8fdd8db75c`
- **Date:** 2026-08-02
- **Branch:** main (from `064681b`)
- **Repo:** `cats`

## Request

> Add a refresh icon to the right of the USAGE section also if it is feasible say
> "n <time-units> ago"

Both halves are UI, and both turned out to need the server: the browser cannot
read the account endpoint, and it cannot know when the reading it was handed was
taken.

---

## The age is a wire field, not a receive time

The obvious implementation — stamp the message when the browser receives it — is
wrong here, and wrong in the way that only shows up later. `serveInit` replays
`o.usage`, the **stored** reading, to a browser that connects mid-interval. A
page opened ninety seconds after a poll would say "0s ago" about numbers that are
already a minute and a half old.

So `browserproto.Usage` gained `read_at` (RFC 3339), stamped by the poller:

```go
func (u Usage) WithReadAt(t time.Time) Usage
```

Chained rather than folded into `NewUsage` so building a `Usage` stays
clock-free: both readers are pure functions over an endpoint reply or a pile of
transcripts, and only `runUsage` — which owns the poll — knows what "now" is.

Verified against the live wire: a client connecting at 16:47:09Z was sent
`read_at=2026-08-02T16:45:37Z`. Ninety-two seconds. The label reads "2m ago",
which is the truth the old code could not have told.

## The dedup had to go

`setUsage` used to suppress an unchanged reading:

```go
if o.usage != nil && *o.usage == m { return }
```

That is exactly wrong once the message carries its own age. The numbers move
slowly by design — a 5-hour window advances 0.3%/min at full tilt — so the common
case is a reading whose percentages are identical and whose *timestamp* is two
minutes newer. Keeping the dedup freezes the label: the section would report an
hour-old age while the server polls every two minutes.

The trade is stated in the code: a few hundred bytes, twice an hour, per client,
against a section that lies about its own freshness. `TestSetUsageDedupes` became
`TestSetUsageStoresLatest` and now asserts the newer stamp wins.

## The button is a §7 command, not an intercept

`controlDispatch` already has one bypass (`ctlproto.MethodPair`), so an intercept
in `handleCmd` was available and cheaper. It was the wrong shape:

- The §7 table **is** the vocabulary. `cmd/catgen-dart` generates the phone's
  typed call sites from it, `catctl commands` prints it, and
  `TestCommandSpecsRouted` reads the dispatcher's AST to keep the three honest.
  A command outside the table is invisible to all of that — by construction.
- Usage is no more "session state" than config, themes, plugins or path listing,
  and all four of those already sit on the `Backend` seam for the same reason:
  only the backend can perform the effect.

So: `CmdUsageRefresh` + spec entry + `Dispatch` case + `Backend.RefreshUsage()`,
and the generator picked it up with no edit — `usageRefresh()` and a
`CommandSpec('usage.refresh')` appeared in the Dart goldens on regeneration.

**It acks the ask, not the answer.** The read is a network round trip on the
poller's goroutine and its product is a broadcast, so the reply carries nothing
and every client sees the fresh numbers, not just the caller. That also makes it
non-reply-gated: a caller that never listens still gets the effect it wanted.

## The nudge is a signal, not a queue

`runUsage` was `for { read; post; sleep }`. Now the sleep is a timer raced against
a one-slot channel:

```go
tick := time.NewTimer(usageInterval)
select {
case <-tick.C:
case <-o.usageNudge:
    tick.Stop()
}
```

Two properties, both deliberate:

- **Non-blocking send.** `RefreshUsage` runs on the loop goroutine — the sole
  owner of session state — and must never block on a poller that is mid-HTTPS.
  A dropped send is correct: two asks arriving before the poller wakes want the
  same single fresh reading.
- **The interval re-bases.** Recreating the timer after every poll means a manual
  refresh is followed by a full quiet period, not a tick that happened to be two
  seconds away.

## Front end

- The heading gets `.hctl` — the same right-edge control strip Panes already
  uses — holding the age label and the button.
- The age reuses `fmtAge` (the Agents section's register), so one vocabulary
  covers "n ago" across the sidebar. Tooltip carries the wall-clock read time.
- The section's existing 30s tick became 10s and now rewrites both moving labels.
  The reset countdowns did not need it; the age does — it starts at zero after
  every push, and a "0s ago" lingering half a minute is the one stale number this
  whole feature exists to prevent.
- Busy state: the mark spins from click until a `usage` message lands. Any
  reading answers a pending refresh, since the poller pushes every reading it
  takes. Two backstops — an explicit `cmd_result` failure (what a server too old
  to know the command returns) and a 20s timer, because a spinner that outlives
  its request is a lie.

### The icon is drawn

First attempt was `⟳` as button text, matching the `⊞`/`⊟` on Panes. It renders
as a speck: the glyph is drawn small for its point size in a monospace face, and
bumping to 13px did not fix it (screenshot-checked, twice). Replaced with an SVG
circular arrow — two paths, arc plus head — following `todoMark`/`lockMark`,
which is already the sidebar's convention for anything that has to read at 12px.

## Verification: the app, not the tests

The extension was not connected, so Chrome went headless.

An isolated instance — own port, own cathost socket, own `--state-dir` — so the
user's running `Cats.app` session was never touched. Socket paths in `/tmp`, not
the scratchpad: macOS caps `sun_path` at 104 bytes and the scratchpad path alone
overruns it (`bind: invalid argument`).

1. **Raw WebSocket client** (hand-rolled RFC 6455 in Python; no ws library on the
   box, none in `go.mod`): connect → stored reading replayed with a 92-second-old
   `read_at` → send `usage.refresh` → fresh push one second later.
2. **Headless screenshot** for the heading's look, cropped and magnified with
   `sips`.
3. **CDP over the same frame codec** to click the real control in a real page:

```
before: {"age":"2m ago","title":"read at 11:52:39 AM","icon":true,"busy":false}
click : clicked
during: {"age":"2m ago", ...,"busy":true}
after : {"age":"1s ago","title":"read at 11:54:24 AM", ...,"busy":false}
```

`gofmt`, `go vet -tags ghostty`, full tagged suite: clean. Every verification
process was killed and its sockets removed.

## Files

| File | Change |
|---|---|
| `internal/browserproto/down.go` | `Usage.ReadAt` + `WithReadAt` |
| `internal/app/command_vocab.go` | `CmdUsageRefresh` + spec |
| `internal/app/commands.go` | `Backend.RefreshUsage` + dispatch case |
| `internal/app/commands_test.go` | fake backend method, `TestDispatchUsageRefresh` |
| `cmd/catway/catway.go` | `orch.usageNudge` |
| `cmd/catway/usage.go` | timer/nudge race, `RefreshUsage`, `setUsage` always pushes |
| `cmd/catway/usage_test.go` | `TestSetUsageStoresLatest`, `TestRefreshUsageNudgeCoalesces` |
| `cmd/catway/web/index.html` | heading controls, `refreshMark`, age label, 10s tick |
| `cmd/catgen-dart/testdata/golden/*.dart` | regenerated |
| `docs/protocols/control-api.md` | `usage.refresh` row + the ack-the-ask note |

## Open

`cats-todo` keeps a copy of the wire vocabulary in lockstep with this repo. The
change is additive — one command, one message field — so nothing there breaks,
but the copy is now a command behind.
