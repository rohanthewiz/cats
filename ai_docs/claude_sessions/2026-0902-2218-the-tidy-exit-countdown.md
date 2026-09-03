# Session: The Tidy-Exit Countdown

- **Session ID:** `5e96b48a-1943-4fee-9295-0d0ed9445bf4`
- **Date:** 2026-09-02
- **Branch:** `spike/wire-leaf`
- **Repo:** `cats`
- **Follows:** `c0a250f wire: carve the browser protocol out of internal into a
  leaf package`. First feature built on the freshly carved `wire/` leaf, and it
  exercised the seam immediately: a new down-message field and a new command
  name both landed there rather than in `internal/browserproto`.

## Request

> When a pane is exited. Instead of just leaving it static with "exited (0)" in
> the header auto-close the pane after a countdown of 10s with the opportunity
> to close the timer itself so the pane remains.
> This could look like: `"exited (0) - (close in 10s [x])"`

Followed later by:

> bump autoclose_exited to 20s so plugin output stays readable

## The two forks, asked before building

The request is phrased as a UI change, and the naive reading is fifty lines of
browser JS. Two things about this codebase make that reading wrong, and both
change the work materially, so they were put to the user rather than assumed.

**1. Where the timer lives.** `cats` is multi-client by construction — there is
a `clients` census message, and `sendVisible` exists precisely because several
windows watch the same pane. A browser-local timer means every window races to
send `pane.close`, and one window's `[x]` does not stop another window's clock.

*Chosen: server-side, authoritative.* One timer per pane in the orchestrator,
the remaining time pushed to clients, and `[x]` sends a real command that
cancels for everyone.

**2. Which exits qualify.** `reap.go`'s own header comment already argues the
opposite case for keeping corpses:

> the build output or the stack trace that preceded the exit is usually why
> anyone is looking

*Chosen: status 0 only.* A pane that died non-zero keeps today's behaviour
untouched. A clean `exit` has nothing left to say; a stack trace does.

Both answers came back as the recommended option. The second one is what makes
the feature defensible at all — it is the difference between "the terminal
tidies up after itself" and "the terminal throws away your crash".

## What landed

### The clock (`cmd/catway/reap.go`)

The auto-close is the reaper's short-timescale sibling and lives in the same
file, under its own banner. Four functions:

| function | job |
|---|---|
| `armAutoclose(rt, code)` | starts the countdown, returns what to tell clients (0 = none) |
| `cancelAutoclose(rt)` | stops it; reports whether it stopped one |
| `autocloseLeft(rt)` | what remains, for a late joiner's chrome |
| `fireAutoclose(pid)` | the close itself, posted by the timer onto the loop |

Plus `keepPane(pid)`, which cancels and re-broadcasts.

Design notes worth keeping:

- **`rt.autoclose` is the authority, not the timer.** `cancelAutoclose` calls
  `Stop()` and then *clears the field*, and `fireAutoclose` re-reads it. A timer
  that already fired has posted its closure onto the loop and cannot be recalled
  — clearing the field is what makes the fire a no-op. Ignoring `Stop()`'s
  return is deliberate, not sloppy.
- **The last pane never arms.** `ClosePaneIn` refuses it, so a countdown there
  would visibly reach zero and do nothing. Checked at *arm* time rather than
  fire time: the header must never show a promise that cannot be kept. It is not
  re-checked later — a session that grows a second pane mid-countdown leaves this
  corpse to the reaper, the same answer the sweep gives.
- **`autocloseLeft` returns 1ms, never 0, for a due-but-unprocessed timer.** 0
  means "no countdown" on the wire, and a header that silently stops counting a
  moment before the pane vanishes reads as a bug.
- **Arming hangs off the same `first` flag as the reap stamp** in `daemon.go`, so
  a replayed `pane_exited` on reconnect cannot restart a countdown the user has
  already cancelled.
- **Three cancel sites:** respawn (`createPane` — a cathost reconnect inside the
  countdown is exactly when this races), runtime teardown in `syncDaemon` (a
  pending timer must not outlive its pane and name an id a new pane could get),
  and `pane.keep`.

### The protocol (`wire/`)

`PaneExited` gained `autoclose_ms` — **remaining time, not a deadline**. The
browser's clock and the server's need not agree, and a duration is skewless. It
is re-sent rather than computed once, which does double duty:

- the chrome burst to a late joiner carries what is *left*, so a window opened
  partway through continues the countdown instead of restarting it;
- **a cancel is delivered as the same message with the field absent**, which is
  how one person's `[x]` reaches every other window. No new down-message needed.

`NewPaneExitedIn(pane, code, remaining)` sits beside the old constructor; a
non-positive remaining degrades to the plain form.

New command `CmdPaneKeep = "pane.keep"`, `OptPaneParams`, **not `Recorded`** —
cancelling a countdown answers something that happened *while* the recorder was
on, it is not a step of the work being recorded. Three edits that must agree
(constant, spec, dispatch case), per the file's own note; `TestCommandSpecsRouted`
checks all three.

### The seam (`internal/app`)

`Backend.KeepPane(pane uint32) bool`. On the Backend rather than the Session
because the countdown is runtime state — a timer — and the domain model has no
idea a pane's child ever exited. **A pane with no countdown is a no-op success:**
two windows racing the same `[x]` must both be told yes.

`Session.PaneCount()` exported as a thin wrapper over `totalPanes()`, with the
comment explaining why: a caller that closes panes on its own schedule needs to
know *in advance* whether a close would be refused.

### The header (`web/js/06-chrome.js`, `12-main.css`)

```
pane 3 · build · ~/src · exited (0) — close in 7s ✕
```

- `drawAutoclose(p, add)` takes `renderChrome`'s own span-appender so the run
  lands in the same info row as every other field. No separator dot — it is a
  clause of the exit, not another field.
- `Math.ceil` on the remaining seconds, so the number never sits at 0 while the
  pane is still there.
- Muted, not red: the exit is the news, the countdown is what the pane is about
  to do about it. Two alarms would be one too many.
- **One 500ms interval for all counting panes**, stopping itself when none are —
  an idle session runs no timers.
- `keepPane(id)` clears locally *and* sends the command. The optimism is the
  point: the round trip is long enough to show one more tick, and a countdown
  that keeps counting after you told it to stop reads as a click that missed.
- **Entering copy mode keeps the pane.** Copy mode replaces the identity line
  with its key hints, so the countdown would be invisible — and selecting text
  in a corpse is the clearest possible statement that its last screen is still
  wanted. One line in `23-copymode.js`.

### The knob

`panes.autoclose_exited`, alongside `reap_exited` and parsed the same way, with
one deliberate difference: **absent means the default, not off.** A config file
written before this knob existed should still get the behaviour; only the
explicit off-spellings disable it.

Duplicated as `defaultAutocloseTTL` in `reap.go` for the same reason
`defaultExitedPaneTTL` is — an orch built with no config file must still behave,
and a zero field means "never".

## The 20s bump

The follow-up request has a specific cause. `30-plugins.js` spawns
`catctl plugin …` in a fresh tab and its comment relied, explicitly, on the
corpse:

> The pane stays on screen after exit (exited chrome), so the git/build output —
> or the failure — remains readable until the user closes it.

A successful plugin run exits 0. Ten seconds is enough to *notice* a countdown;
it is not enough to *skim git output*. So the default became 20s, and the config
comment now names what sets it: the slowest thing worth reading off a pane that
then exits cleanly.

That plugin comment was stale the moment this feature landed, so it was
corrected in the same pass — a failed run stays put, a clean one tidies itself
after the countdown, `✕` cancels.

The bump touched both copies of the default (config and catway's fallback) and
every "10s"/"ten seconds" in the prose. Two comments that said "a window opened
7 seconds in draws 3s" were rewritten to be total-agnostic rather than
re-arithmetic'd — an example that has to be recalculated on every bump is an
example that will eventually be wrong.

## Tests

**`cmd/catway/autoclose_test.go`** — eight cases, one per judgement:

| test | asserts |
|---|---|
| `ClosesCleanlyExitedPane` | the feature: exit 0 → announced countdown → pane gone |
| `IgnoresFailedExit` | exit 1 arms nothing and closes nothing |
| `HonoursTheOffSwitch` | `autoclose_exited: off` |
| `NeverArmsOnTheLastPane` | no countdown it could not honour |
| `KeepPaneCancelsTheCountdown` | cancel + broadcast + idempotent + unknown-pane failure |
| `CancelledByRespawn` | a reconnect mid-countdown |
| `ChromeCarriesTheRemainingCountdown` | a late joiner gets a *remainder* |
| `DuplicateExitDoesNotReArmAKeptPane` | a replayed event vs. a cancelled timer |
| `ClosingAPaneCancelsItsCountdown` | no timer outlives its runtime |

`dispatchExit` feeds the real `pane_exited` event — the only path that arms —
and `lastExitMsg` takes the *last* message for a pane, because a cancel is
delivered as a second `pane_exited`.

**`web/jstest/autoclose.test.mjs`** — 16 assertions, using the `loadFns` harness
from the runbook sessions. Needed a ~10-line DOM stub (an element is its tag,
text, children and listeners) since no existing jstest touches `document`.

One shipped-code change fell out of the test harness: the module-level
`let autocloseTimer` became `const autocloseTick = { timer: null }`. `loadFns`
can bind a const but not a mutable free variable, and a box is honest about
being shared state. The comment says so.

## Docs

- `config.example.yaml` — the knob, with the status-0 rule spelled out
- `docs/reference/configuration.md` — a new **`autoclose_exited` — the tidy exit**
  subsection under the reaper's
- `docs/subsystems/session-model.md` — the paragraph after the reaper's
- `docs/protocols/browser-protocol.md` — `autoclose_ms` on the `pane_exited` row
- `docs/protocols/control-api.md` — `pane.keep` in the Panes table
- `catctl keep [pane]` in `cmd/catctl/subcommands.go`

## Known limits / next

- **The catgen-dart golden was regenerated twice** (the field, then the 20s
  doc-comment bump). Its own failure message says to copy the output into
  `cats-mobile/packages/catsproto/lib/src/generated` — a different repo, so that
  copy has NOT been made.
- **`cmd/catgen-dart/` is being deleted by work running in parallel with this
  session.** It appeared in the working tree part-way through, uncommitted:
  the whole `cmd/catgen-dart/` tree plus `docs/protocols/dart-client.md` are
  removed on disk, and `mkdocs.yml`, `docs/index.md`, `docs/protocols/index.md`,
  `internal/flags/flags.go`, `wire/vocab_test.go` and
  `internal/app/command_vocab_test.go` are edited to say the phone imports
  `wire` directly instead. **None of that is in this commit** — it is somebody
  else's half-landed change and it is left in the working tree exactly as found.
  If it lands, the regenerated golden above goes with it and this feature needs
  nothing further; if it does not, the golden is committed and correct.
- **The hovercard (`09-hovercard.js`) still says only `Exited — code N`.** It
  could carry the countdown; it does not. Small, and nobody has asked.
- **The palette has no "keep pane" entry.** `✕`, copy mode and `catctl keep`
  cover it, and a palette entry for a ten-second window is a race you would lose.
- **Nothing cancels on scroll.** Copy mode was judged the clear signal; scrolling
  a dead pane arguably is too, and would be one more line if it ever bites.
- **`autocloseAfter` is live-reloadable but only affects countdowns armed after
  the reload** — re-arming a running one would move a deadline the user is
  watching tick down. Documented in the field comment.
- The feature was verified by tests, `go vet`, and `make jstest`. **It has not
  been watched in a real browser.**
