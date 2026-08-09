# Session: the folded chip gains a horizon, and the poll learns to sleep

- **Session ID:** `a76005dd-ff86-4df9-b1fd-fb9f07ece942`
- **Date:** 2026-08-08
- **Branch:** main (`df7c142` → this commit)
- **Repos:** `cats`

Fourth session on the sidebar's USAGE section, after
`2026-0806-1901-usage-headlines-and-host-cpu.md` decided what a fold should
*say*. This one finishes that sentence — a percentage of a window is half an
answer without the window's horizon — and then follows a question the first
change provoked into the poll that feeds it.

Two asks, the second arising from the answer to a question about the first.

## Ask 1 — the chip should carry the time left

> Under the USAGE section, when we know the 5 hr window for the agent and the
> agent usage is folded, say along with the percent of the 5hr window, the
> remaining time, example: 3% (2hrs)

`3%` spent means one thing with four hours left in the five and quite another
with ten minutes, because what is left is what the rest of the afternoon has to
fit into. The chip was quoting the numerator and dropping the denominator that
makes it actionable.

### Keyed off `resets_at`, not off the row's name

The obvious implementation tests for the row named `5 hr`. It does not, for the
same reason the headline mark itself lives on the server (last session): a row
that **rolls over** is exactly the row a horizon belongs on, and asking the
question that way means the front-end never enumerates providers.

```
CLAUDE  5 hr   pct + resets_at  →  3% (2hrs)
        Week   pct + resets_at  →  (would carry one if it were the headline)
HOST    Memory pct, no rollover →  63%          ← unchanged, and untouched by
        Disk   pct, no rollover →  89%             this code knowing they exist
```

Nothing resets host memory but a process exiting, so those rows have no
`resets_at` and fall out of the feature for free.

### Details worth keeping

- **`fmtLeft(ms)`** — `fmtUntil` with the sentence stripped off. Hours carry the
  word (`2hrs`, `1hr`) because hours are the unit a 5-hour window is read in and
  `2h` beside `3%` scans as a second measurement rather than as a clock. Below
  the hour (`45m`, `50s`) the terse register the rest of the sidebar uses is
  unambiguous on its own. The mixed register is deliberate, and is the one part
  of this a future reader may want to argue with.
- **The countdown stays `--muted` through `.high` and `.crit`.** The colour in
  that header means *how much is spent*; a clock that turned red would read as a
  second alarm for something that is only the window doing what windows do.
- **It ticks.** A nested `.gleft[data-at]` span joins the existing 10s sweep
  beside `.ureset[data-at]`. Folded, this is the *only* number on screen for
  that provider, which makes it the last one that should be allowed to age.

## The question in between

> How often are we actually hitting the Anthropic API?

One place in the repo talks to Anthropic: `runUsage`, `GET
/api/oauth/usage`, every **2 minutes**. 30/hr, ~720/day per daemon. Four facts
came out of reading it:

| | Before |
|---|---|
| Client gate | none — polled with no browser connected at all |
| Cache | none — every tick a fresh round trip |
| Failure handling | none — a 401/429/offline laptop retried at the same 2m forever |
| What it costs | nothing token-wise: `/api/oauth/usage` reports the windows, it does not consume them |

The one self-limiting case already present: an *expired* token fails in
`claudeOAuthToken` before the request is built, so a stale credential falls to
the transcript estimate and stops reaching out. That distinction turned out to
be load-bearing for the backoff below.

## Ask 2 — gate on focus, slow when nobody is connected, back off on failures

### One tiered cadence

Focus and connectedness are the same question asked twice ("is anyone
looking"), so they became one ladder rather than two mechanisms:

| state | interval | req/hr |
|---|---|---|
| a window in the foreground | 2m | 30 |
| connected, all windows backgrounded | 10m | 6 |
| no browser connected | 30m | 2 |

A daemon left running overnight with no tab open drops from ~360 requests to
~24.

**No front-end work was needed.** The browser has reported window focus since
the pane-focus work (`{t:"focus"}`, `index.html`), and `anyClientFocused()`
already aggregated it. The whole server-side signal was there; it had simply
never been asked this question.

### Slowed, not stopped

The ask said *gate*, and a hard gate was the wrong shape. A backgrounded window
is very often still a **visible** one — a second monitor, a tiled half-screen —
and freezing a number the user can plainly read is worse than reading it
slowly. The dark tier is slower still but also does not stop, because the stored
reading is what the next browser is handed on connect (`serveInit`).

### The nudge is what pays for the gate

```
blur ──► tier drops, current sleep runs out at the OLD cadence
                     (one extra read; a glance back at a just-blurred
                      window should not find stale numbers)

focus ─► tier rises ──► RefreshUsage() ──► the poller's select wakes now
                          │
                          └─ unless the stored reading is younger than
                             usageInterval — alt-tabbing ten times a minute
                             must not become a faster poll under another name
```

Without the nudge the gate would be a straight downgrade: the first thing you
would see on returning is an old number and a long wait. With it, the staleness
the slower tier permits is never actually seen by anybody.

### Where the tier lives

`orch.usageAttention atomic.Int32` — **the one piece of loop state read from
outside the loop.** `runUsage` has to be its own goroutine (the read blocks), and
posting a closure to ask the loop a question would park the poller behind a loop
that may itself be idle. Published on every transition, read once per tick; a
tick that races a transition picks up the previous tier and corrects on the next
one, and the nudge covers the only edge where that lag would be felt.

`noteUsageAttention` hangs off **`syncAppFocus`**, which is already called from
exactly the two places that can change the answer — a `Focus` report, and
`flushClients` when a connection arrives or dies (the last focused window
leaving is a blur no `Focus` message will ever report). A new call site therefore
cannot forget to publish, which would otherwise show up as a poller stuck at the
wrong cadence for the life of the session.

### Backoff paces the read, not the poll

`usageBackoff`: the first failure is forgiven at the normal cadence (endpoints
hiccup, and one dropped read that heals by the next tick should not cost ten
minutes of stale section); from the second it is 5m → 10m → 20m → 30m cap, reset
on success.

Two decisions:

- **The poll keeps its cadence and keeps publishing.** Only the account read is
  skipped. The HOST rows are local syscalls with nothing to do with a failing
  endpoint, and the CLAUDE group falls to the transcript estimate it already
  uses for an unreadable credential. Backing off the whole poll would have let
  one endpoint take the memory meter down with it.
- **Only *remote* failures count** (`usageRemoteError`, wrapped at the one call
  site that has actually reached out). A missing or expired credential never
  built a request, so there is nothing to be gentle with — and that is precisely
  the case where the estimate is the *permanent* answer rather than a stopgap,
  so it must keep refreshing on the normal tick. The wrapper carries the message
  unchanged, because that message is what the sidebar prints.

The caption says the wait is deliberate rather than stuck:

```
estimate · usage endpoint: HTTP 429 · retrying in 8m
```

`claudeEstimateGroup` was split out because there are now three ways to reach
the estimate — a read that failed, a read skipped because a previous one failed,
and a machine with no credential — and they differ only in that caption. The
rows they produce must not differ at all.

## Verification

- **`go build` + `go vet` + full suite green** under `-tags ghostty`. `vet` is
  load-bearing here: `atomic.Int32` on `orch` makes it non-copyable.
- **Tests added** (`usage_test.go`): the tier ladder including the two edges no
  `Focus` message reports (a second background window not diluting the first
  one's focus; the last focused window *leaving*); the nudge on return, its
  suppression at the same tier, and its freshness floor; the backoff curve
  through the cap and its reset; the caption counting down with the clock;
  remote-vs-local classification and that wrapping does not touch the printed
  message.
- **Correction carried from earlier in the session:** the first verification run
  used `go test ./cmd/catway/` with no `-tags ghostty` and reported `ok` having
  compiled essentially nothing. Everything above — including the ask-1 chip
  change from before that was noticed — is verified under the tag.

## Open

- **Not seen in the MacApp.** `make macapp` not run this session; the chip and
  the tiers were read and unit-tested, not watched. The pattern this thread
  keeps carrying.
- **The idle and dark tiers have not been observed over a real night.** Their
  arithmetic is tested; a daemon actually left alone for twelve hours has not
  been checked against the request count it should now produce.
- **The CPU sampler still ticks every 10s regardless of attention.** Left alone
  on purpose: it is a local `/proc/stat` read (one long-lived `iostat` on
  darwin), no API involved, and gating it would lose the sparkline history it
  exists to accumulate. But it does mean "nobody is connected" is not yet a
  fully quiet state.
- **No wire change**, so no `catgen-dart` regeneration and no new drift with the
  mobile client — the Dart copy from the previous session is still one field
  pair behind, unchanged by this one.
- **`fmtLeft`'s mixed register** (`45m` but `2hrs`) is a judgement call about how
  a 5-hour window is spoken about, not a consistency the code enforces.
