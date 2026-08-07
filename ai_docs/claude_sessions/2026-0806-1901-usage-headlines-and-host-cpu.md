# Session: a folded group quotes one row, and HOST gains a CPU chart

- **Session ID:** `2f14b4b2-09e1-4e07-b5ab-cca576897105`
- **Date:** 2026-08-06
- **Branch:** main (`832d53d` → `950fec0`, pushed)
- **Repos:** `cats`

Third session on the sidebar's USAGE section in two days, after
`2026-0806-1706-sidebar-clicks-lost-to-rebuilds.md` (press semantics) and
the fold-pair placement in `832d53d`. Those two made the section foldable
without misfiring; this one decides what a fold should *say*.

## Request

> When the CLAUDE USAGE section is folded show Claude's 5hr limit rather
> than the weekly limit. The hover popup should include 5hr, weekly, and
> Fable limits. Also when the HOST section is folded show the host's
> memory usage percent. While the HOST popup should show disk and memory
> usage, [and CPU]. I guess we could add a mini cpu chart in the HOST
> group also

Four asks, and the CPU row was implied by the third: the popup was to
show a resource the section did not yet read.

## What a fold means

The chip on a folded heading used to be the group's **worst** row —
picked by percentage, graded against that row's own scale. The reasoning
was that the one thing worth interrupting a fold with is a window about
to run out.

The request rejects that, and is right to. A fold is a request for **one
number**, not for a digest of the numbers being hidden, and which number
that is does not change with the readings:

- **CLAUDE → `5 hr`.** The week is planned around; the 5-hour window is
  what a long afternoon walks into. Folding CLAUDE is folding *toward*
  the 5-hour figure, not away from it.
- **HOST → `Memory`.** It ends a session soonest and least reversibly —
  a machine that starts swapping makes every pane treacle and nothing
  but a process exiting takes it back. A pegged CPU is usually the work
  itself; a full disk has been full for a week.

The number that is no longer shown is not lost: it moved into the hover
(below), which is what makes a single-row chip affordable at all.

## The mark is the server's to make

`UsageWindow.Headline bool` — new wire field, `omitempty`.

The front-end could have keyed on group id and row name, the way it
already does for the HOST warning scales. It does not, because that
exception is documented as an exception: `UsageGroup.ID` is opaque except
for the literal `"host"`, and `"host"` is the one group *this* server
synthesises. Which of **Claude's** meters answers the group's question is
knowledge about Claude, and a browser that encoded it would be
enumerating providers — the one thing the list-shaped `usage` message
exists to avoid.

```
claudeUsageGroup   5 hr .Headline = true      (both the account read and
                                               the transcript fallback)
hostUsageGroup     Memory .Headline = true
copilot            marks nothing → unchanged, still folds to its worst row
```

`usageHeadline(wins)` returns the marked row, else the old peak. So the
fallback *is* the previous behaviour, and a provider that never learns
about headlines never notices.

The estimate path keeps the mark deliberately: that row carries a token
count and no percentage, so a folded CLAUDE with no credential shows
`128K tok` instead of a percentage. Still the 5-hour answer, still the
one asked for.

## The hover carries what the fold hid

`usageGroupTitle(g, wins, levelsFor, collapsed)` — every row the group
holds, one per line, each with its percentage on its **own** scale, its
absolute pair or its countdown, then the group's note, then the fold
hint. Verified out of the rendered DOM:

```
5 hr: 37.4% of the window used · resetting        Memory: 63.2% of host memory in use (15.2G/24.0G)
Week: 72.1% of the window used · resets in 4d     CPU: 4.0% of the host's CPU busy (load 4.21)
Week · Fable: 12.5% of the window used …          Disk: 88.6% of the disk in use (347G/460G)
click to collapse                                 click to collapse
```

Two decisions worth recording:

- **Expanded gets the same hover**, duplicating the rows below it. A
  heading whose tooltip depended on fold state is a worse thing to learn
  than a few repeated lines.
- **The `.gsum` chip lost its own `title`.** It sits inside the heading,
  so its tooltip shadowed the heading's — which is now the one that
  matters.

The tooltip is built at draw time, so its countdowns age until the next
reading lands: two minutes at the poll's cadence, against a tooltip
nobody holds open that long. The rows themselves are still ticked every
10s by the existing `.ureset[data-at]` sweep.

## The CPU row (`cmd/catway/hostcpu.go`, new)

### Why it could not ride the poll

Memory and disk are two syscalls on the two-minute usage poll. CPU
cannot be read that way and mean anything:

- Utilization is a **rate**. Every source of it is a difference between
  two cumulative readings, or an interval sample somebody else took.
  There is nothing to read "right now".
- Differencing across the poll would yield one two-minute average per
  point — a chart whose every feature has already been smoothed away.

So it keeps its own goroutine at **10s**, and the poll takes whatever the
ring holds when it fires. Short enough that a test run is a visible hump;
long enough that ten minutes of history is 60 float64s.

### The two hosts

```
linux   /proc/stat aggregate jiffies, differenced across the interval.
        Exact, no subprocess. iowait counts as IDLE — a core waiting on a
        disk is a core anything else could have. Fields past the named
        ones are summed into the total without being enumerated, so a
        kernel that adds a state keeps the denominator right.

darwin  one long-lived `iostat -w 10`; every line after the first is a
        true ten-second average.
```

macOS was the whole difficulty. It exports **no cumulative tick counter**
to userspace without cgo — `kern.cp_time` is a BSD sysctl and returns
`unknown oid` here (checked). The alternatives were all worse:

| Option | Why not |
|---|---|
| `top -l 1` | first sample is a **since-boot** percentage, not ticks — two of them cannot be differenced |
| `iostat -c 2 -w 1` every 10s | a process per sample, for a strictly worse figure: a 1s spot check instead of the full interval |
| `ps -A -o %cpu` | per-process lifetime averages; sums past 100% and misses exited processes |

The streaming form only works if iostat flushes per line rather than
block-buffering into the pipe. **Checked before committing to it:** lines
arrive one per interval through `| cat`. The read is supervised —
a stream that ends is restarted after 30s, and three restarts that
produced *nothing* means the host cannot answer, which costs one log line
and then silence rather than a subprocess every half minute forever.

### Silence over staleness

`cpuStaleAfter = 3 × interval`. A sampler whose source died would
otherwise publish its last reading forever, and a pinned "97%" from ten
minutes ago is worse than no row — it is a number that invites action.
Past that, `window()` returns `UsagePctUnknown` and `hostUsageGroup`
drops the row, exactly as an unreadable `hostMemory` or `hostDisk` does.

A nil sampler answers the same way, which is what lets the tests build a
HOST group without starting a goroutine.

### Details

- **Row order: Memory, CPU, Disk.** By how fast each moves against how
  badly it ends. CPU sits in the middle rather than first because it is
  the row most often high for a good reason, and leading with it would
  train the eye to skip the group.
- **Detail is the load average**, not a used/total pair: CPU has no pair
  to give, the percentage already *is* the whole machine. Load answers
  what the percentage cannot — 100% at load 2 on ten cores is one busy
  process, 100% at load 40 is a queue.
- **The ring copies down rather than reslicing.** `s[1:]` keeps the
  backing array alive and walks its head off the front forever, which on
  a server that runs for weeks is an unbounded allocation.
- **`CPU_LEVELS = { high: 90, crit: 98 }`** — the loosest of the four
  scales. Every build touches 100% on the way past; the row should warn
  only where it has stopped describing work and started describing a
  queue. The strip below it carries the story instead.

## The chart (`usageSpark`)

`UsageWindow.Spark []float64` — recent samples, oldest first, same units
as `Pct`, `omitempty`. Sent only for a row whose movement between polls
*is* the information; a weekly window has nothing to plot that its number
does not already say.

```
100% ┤       ╭─╮
     │   ╭───╯ ╰──╮        ← ten minutes, one point per 10s sample
  0% ┼───╯        ╰────
     oldest          now
```

Geometry fixed at 100×20 user units and stretched to the sidebar's width
with `preserveAspectRatio="none"`, so nothing measures the DOM and a
resized sidebar needs no redraw. That stretch is non-uniform and would
smear a stroke, hence `vector-effect="non-scaling-stroke"` to hold the
line at 1px. Baseline at 20, plot top at 1, so a 100% sample shows its
stroke inside the box instead of being clipped in half. The area fill is
what makes the shape legible at 20px — a bare 1px line reads as noise.
It takes the row's warning colours exactly as the meter above it does.

Fewer than two samples draws nothing: one point is a reading, not a line.

## Verification

- **`go vet` + full suite green** under `-tags ghostty`.
- **Rendered, not just read.** A harness built from the *real* drawing
  functions (extracted from `index.html` by name, with the page's own
  `<style>`) rendered both states through headless Chrome. Folded CLAUDE
  shows `37%` (its 5-hour figure, not the 72% week); folded HOST shows
  `63%` (memory, not the 89% disk); the CPU strip draws under its meter.
  Tooltips dumped from the DOM are the two blocks quoted above.
- **The live streaming path was exercised off-tree** — a throwaway test
  ran `streamIostat` for 20s and logged real samples landing every ten
  seconds: `pct=32.0 detail="load 44.24"`, then `spark=[32 29]`. Deleted
  after; the committed `TestCPUSourceLive` runs a **bounded**
  `iostat -c 2 -w 1` instead, because a test must not own a long-lived
  process.
- **`hostcpu_test.go`** — both parsers against captured output (a two-disk
  Mac, so reading the six CPU fields from the *end* of the line is
  load-bearing); headers must not parse, and the us+sy+id≈100 check
  rejects a coincidentally numeric line; ring bounds, ordering and which
  end it drops from; empty, nil and stale samplers all report unknown;
  one sample yields no spark.
- **`hostmem_test.go`** — `hostUsageGroup(nil)` still `[Memory Disk]`
  (the first ten seconds of every run), with `Memory` the sole headline;
  a new case feeds a sampler by hand and asserts `[Memory CPU Disk]`,
  the newest sample as `Pct`, the newest load as `Detail`, and that CPU
  is *not* flagged headline.
- **`wire.g.dart` regenerated** for the two new fields — `headline`
  defaults false, `spark` to an empty list, both omitted from `toJson`
  when empty.

## Pushed

| Commit | Change |
|---|---|
| `950fec0` | `feat(usage): a folded group quotes one chosen row, and HOST gains CPU` |

## Open

- **The Dart copy was not made.** `catgen-dart` prints "copy the same
  output into `cats-mobile/packages/catsproto/lib/src/generated`", and
  that repo is not checked out here. The golden in *this* repo is current;
  the mobile client's generated source is now one field pair behind.
- **Not seen in the MacApp.** `make macapp` still not run — the note
  every session in this thread has carried. Verified in headless Chrome
  against the real functions, which is closer than reading, and still not
  the installed app.
- **One iostat per server on macOS.** It is supervised and restarted, but
  a machine that suspends and resumes has not been watched across the
  transition; the staleness rule is what covers that case, and it has
  been unit-tested rather than observed.
- **The chart is CPU-only by data, not by design.** `Spark` is a
  generic row capability and memory could carry one for the same reason
  (it moves in minutes, the poll fires in twos). Deliberately not done
  here: one strip in the section is a chart, two are a dashboard.
- **No swap row**, still — carried forward from
  `2026-0804-1347-host-disk-usage-bar.md`. On a machine already
  swapping, memory reads ~100% and says nothing about how hard it is
  thrashing. CPU's arrival makes this more visible, not less.
