# Session: the countdown learns to warn, and the window learns to say when

- **Session ID:** `29fd14fb-fdf6-49b0-bc12-4663c8ab4447`
- **Date:** 2026-08-09
- **Branch:** main (`cbdb092` → this commit)
- **Repos:** `cats`

Fifth session on the sidebar's USAGE section, and a direct continuation of
`2026-0808-2158-usage-poll-pacing-and-fold-horizon.md`, which put a horizon on
the folded chip. That change made the countdown *visible*; this one makes it
*speak* — first as a flat threshold, then, on a follow-up, as a per-row one that
had to be carried on the wire.

Two asks, the second reopening the first's central decision.

## Ask 1 — amber in the last 30 minutes

> On the CLAUDE usage if the remaining time in the 5hr window is 30 mins or less
> change the time value to an amber warning color

### Two colour axes, deliberately kept apart

The section already had a warning colour, and it means something else. Last
session's note — *the countdown stays `--muted` through `.high` and `.crit`,
because a clock that turned red would read as a second alarm* — is still the
governing rule. So the new colour is amber and **only ever amber**:

```
.uval / .gsum   →  --warn, then --err     "how much is spent"
.ureset/.gleft  →  --muted, then --warn   "how long is left"
```

Red stays the exclusive property of the first axis. The useful consequence is a
row that shows an **amber clock beside a calm percentage** — *barely used,
nearly over* — which is precisely the state a single spent-percentage cannot
express, and the reason this was worth a colour at all.

### The class has to be re-decided every time the text is

The countdown is rewritten by a 10s tick, not by the push. A row drawn with 35
minutes left crosses its threshold with no reading behind it, so a class taken
only at draw time would sit muted through the whole stretch it was meant to
mark. Text and class therefore move together, in one `setUsageLeft(el, ms,
soonMs, fmt)` shared by both call sites (row and folded chip) and by both paths
(draw and tick).

`<=` rather than `<`: a window at or past its instant — `fmtUntil`'s
`"resetting"` — is the extreme of the state, not a return to calm.

### Keyed off `resets_at`, inherited from last session

Nothing enumerates row names. The HOST rows have no rollover instant and fall
out of the feature for free, exactly as they did for the chip.

**Flagged at the time:** keying it that way meant CLAUDE's *Week* row also went
amber in its last 30 minutes. That was called out as a consequence rather than
left implicit — and it is what the second ask picked up.

## Ask 2 — two hours for the week

> Would it be possible to go amber in the last 2hrs of the week?

Yes, but not as a second front-end constant. To apply 30m to one row and 2h to
another, the browser would have to know *which row is which* — i.e. enumerate
names — and that is the one thing this section is built not to do. The
front-end's own rule pointed at the answer: `Headline` lives on the server
because which meter matters is knowledge about the provider. **A window's
horizon is the same kind of knowledge.**

### The wire carries it

`UsageWindow.SoonSecs` (`soon_secs`, omitempty). `0` means "no opinion" and
leaves the front-end its default.

| row | `SoonSecs` |
|---|---|
| `5 hr` | 30m |
| `Week` | 2h |
| `Week · <model>` | 2h — a week is a week whichever allowance it meters |

### Not the same fraction, on purpose

The tempting generalisation is *warn in the last tenth*. It gives 30m on the
five-hour window and then **16.8 hours** on a week, which would leave the row
shouting through every Friday. Both numbers are instead "one working stretch
left" measured against very different spans — the span in which the answer
changes a decision, because whatever is left either gets spent now or waits for
the reset, and a long task started inside it straddles the boundary.

### The fallback is the tighter one

An unmarked row with a `resets_at` still warns, at 30m. A row whose length is
unknown is likelier to be short, and warning *late* on a long window is a
smaller error than warning through the last day of one.

### The threshold rides on the element

`data-soon`, stamped at draw beside the existing `data-at`. The tick walks the
DOM and has no window object to ask a second time, so this keeps it free of any
knowledge of which row it is rewriting — the same trick `data-at` already plays
for the instant.

The folded chip takes the **quoted** row's horizon, not the group's: folded,
CLAUDE *is* its 5-hour window and warns on the half hour, never on the week's
two.

### Small cleanup

`fmtLeftParen` — the chip's `" (…)"` wrapper was being built separately by the
draw and by the tick, two places that could have drifted into rendering the same
label two different ways. Named once.

## Verification

- **`go build ./...`**, **`go vet -tags ghostty`**, and the suite under
  `-tags ghostty` green for `cmd/catway` and `internal/browserproto`.
  `gofmt` clean.
- **The page script parses** (`node --check` over the extracted `<script>`,
  ~4.5k lines).
- **Wire change caught the Dart generator.** `TestGoldenIsUpToDate` failed on the
  new field; regenerated with
  `go run ./cmd/catgen-dart -out cmd/catgen-dart/testdata/golden` and green.
- **No new Go tests.** The `SoonSecs` assignments sit in `claudeUsageGroup`,
  which performs a live HTTP read; the existing tests cover `fetchAccountUsage`
  below it. The `Name` and `Headline` assignments on the adjacent lines are
  untested for the same reason — this did not make that worse, and did not fix
  it either.

## Open

- **`cats-mobile` is not updated.** The generator's own instruction is to copy
  the output into `cats-mobile/packages/catsproto/lib/src/generated`. That repo
  is at `~/projs/go/cats-mobile`, outside this session's working directories, and
  was left alone. It was already a field pair behind from the previous session;
  it is now `soon_secs` behind as well. The Dart side reads unknown fields
  tolerantly, so this is drift rather than breakage.
- **Not seen in the MacApp.** `make macapp` not run. The colour was reasoned
  about and the code checked; nobody has watched a five-hour window actually
  cross 30 minutes and go amber. This thread keeps carrying this one.
- **The amber has not been checked against the sidebar's other yellows** —
  `--warn` is shared with `.high`, and the two now appear one line apart on the
  same row (clock left, percentage right). The reasoning says they read as
  different axes; that is an argument, not an observation.
- **`Week` still warns for two full hours a week.** Intended, but it is the
  longest-lived amber in the sidebar, and the first candidate to shorten if it
  turns out to read as noise.
