# Session: a host memory row on USAGE

- **Session ID:** `2ff2bee0-f6d7-4aa0-8e72-5418b8b1d413`
- **Date:** 2026-08-03
- **Branch:** main (from `d0de4c7`)
- **Repo:** `cats`

## Request

> In the USAGE section add a row for host / system memory if the host's memory
> usage is greater than 60% (yellow) go red at 85%

Read as a colour scale rather than a visibility condition: the row is always
drawn when the host reports a figure, and it changes colour at 60 and 85. The
alternative reading — a row that only materialises above 60% — is one `if` away
and was flagged back to the user rather than guessed at silently.

---

## Why it belongs in USAGE at all

The section already answers one question — *is anything about to stop?* — for
the account's rate-limit windows. A laptop running six agents, each with a
language server and a test run behind it, exhausts RAM long before it exhausts
the week's tokens, and that failure looks nothing like a rate limit: the machine
starts swapping and every pane goes treacle. Same question, same glance, same
two-minute poll. No new timer, no new goroutine — `readUsage` takes the reading
on the goroutine that is already awake for it, and the refresh control that
already exists takes a fresh one on demand.

It is attached to **both** branches of `readUsage`, account and fallback alike:

```go
func readUsage(est *usageEstimator) browserproto.Usage {
	mem := hostMemory()
	report, err := readAccountUsage()
	if err == nil {
		return browserproto.NewUsage("account", ...).WithWeeklyModel(...).WithMemory(mem)
	}
	...
	return browserproto.NewUsage("local", fiveHour, weekly, err.Error()).WithMemory(mem)
}
```

A machine with no claude login still has a memory ceiling worth watching. The
row must not vanish because a token expired — which is also why `WithMemory` is
chained rather than a `NewUsage` parameter: it is orthogonal to both sources.

## What counts as "used" — the whole design decision

macOS reports no free-memory figure that means what a reader expects, and the
three available answers disagree wildly. On the machine this was written on
(24 GiB, ordinarily busy):

| Source | Reads | Counts |
|---|---|---|
| `top` "PhysMem … unused" | **96% used** | inactive as used — red from boot, useless |
| `kern.memorystatus_level` / `memory_pressure` | **~44% used** | clean file cache as free — the kernel's question (when do I start killing processes?), not ours |
| this | **72.3% used** | `active + wired + compressor` |

So `available = free + inactive + speculative`, everything else used —
*including* "occupied by compressor", because a compressed page still occupies
physical RAM; it merely holds more of a process's address space per byte. A
large compressor is itself evidence the machine is already fighting for room.
Purgeable is deliberately **not** added: those pages are already counted inside
active/inactive, and adding them credits the same pages twice.

Linux is the same question with a straight answer: `MemTotal - MemAvailable`,
falling back to `MemFree + Buffers + Cached` for kernels before 3.14. `MemFree`
alone would read catastrophically low on any host with a warm page cache.

The percentage and the absolute pair derive from one formula, so the row cannot
say 44% beside "17.2G/24.0G". That consistency is why `kern.memorystatus_level`
was rejected despite being a single cheap sysctl.

### hw.memsize is a subprocess on purpose

`syscall.Sysctl` returns the value as a raw byte string with the terminating NUL
trimmed — which silently eats the high byte of any size ending in `0x00`, i.e.
most of them. `sysctl -n hw.memsize` once, cached in a package var (only
`runUsage`'s goroutine reaches it, so no guard), is the cheaper mistake.
`vm_stat` runs per poll.

## Thresholds live in the browser, and there are two sets

```js
const USAGE_LEVELS  = { high: 75, crit: 90, of: "of the window used" };
const MEMORY_LEVELS = { high: 60, crit: 85, of: "of host memory in use" };
```

`usageRow(name, w, levels)` takes the pair; colour is a display concern and
never crosses the wire. The two kinds of row do not share a scale because the
same percentage does not mean the same thing — a week 70% spent is on track, a
machine 70% into its RAM is a couple of test runs from swapping. `of` also fixes
the tooltip, which would otherwise have said "Memory: 69.5% of the window used".

## The tick bug

`.ureset` is the small muted slot after the row name. For rate-limit rows it
holds a countdown; host memory has no reset, so it holds the absolute pair
instead. But the section's 10-second tick rewrote **every** `.ureset`:

```js
for (const el of usageListEl.querySelectorAll(".ureset")) {
  el.textContent = fmtUntil(Number(el.dataset.at) - Date.now());
}
```

`Number(undefined)` → `NaN` → `fmtUntil` falls through every branch and returns
`"resets in NaNd"`. Ten seconds after the first render, "16.7G/24.0G" would have
become that. Now scoped to `.ureset[data-at]` — the stamp is what separates a
countdown from a fact.

Caught by the DOM-stub harness below, not by reading; it only appears on the
second tick.

## Verification

- **Parser tests** (`hostmem_test.go`) run against captured `vm_stat` and
  `/proc/meminfo` text, so the macOS parser is exercised on a Linux runner and
  vice versa — the one arrangement that catches a parser that only works on the
  machine it was written on. Junk input must **error**, never return zero
  available: "unreadable" must not render as "100% full".
- **`TestHostMemoryLive`** smoke-tests the real host on darwin/linux and asserts
  `UsagePctUnknown` elsewhere.
- **DOM stub in node** (scratchpad, not committed): the real `usageRow` source
  extracted from `index.html` and run against a fake `document`, across the
  threshold boundaries.

```
Memory  cls=(none) cells=[uname=Memory, ureset=9.9G/24.0G,  uval=41%] 41.2% of host memory in use
Memory  cls=high   cells=[uname=Memory, ureset=16.7G/24.0G, uval=70%] 69.5% …
Memory  cls=crit   cells=[uname=Memory, ureset=21.1G/24.0G, uval=88%] 88.0% …
Week    cls=(none) cells=[uname=Week,   ureset=resets in 63h, uval=70%]   ← unchanged at 75/90
Week    cls=unknown cells=[uname=Week,  uval=14.8M tok]                   ← fallback preserved
```

- `make check` (fmt, vet, build, test, `vet-ghostty`, `race-ghostty`): clean.

## Files

| File | Change |
|---|---|
| `cmd/catway/hostmem.go` | new — darwin/linux readers, `hostMemory`, `formatBytes` |
| `cmd/catway/hostmem_test.go` | new — parser tests on captured output, live smoke test |
| `cmd/catway/usage.go` | `readUsage` attaches the host window to both branches |
| `internal/browserproto/down.go` | `Usage.Memory` + `WithMemory`; `NewUsage` seeds it unknown |
| `internal/browserproto/proto_test.go` | memory window on the `usage` round-trip case |
| `cmd/catway/web/index.html` | Memory row, `USAGE_LEVELS`/`MEMORY_LEVELS`, detail-in-`.ureset`, `[data-at]` tick fix |
| `cmd/catgen-dart/testdata/golden/wire.g.dart` | regenerated |

## Open

- **The row reads yellow on this machine at rest** (72.3%). If that turns out to
  be alarm fatigue rather than signal, the knob is `MEMORY_LEVELS.high` — both
  constants sit together in `index.html`.
- **`memory` is a required field in the generated Dart constructor**, same as
  `weeklyModel`. `cats-mobile`'s `packages/catsproto` needs the regenerated
  goldens copied across, and an old server omitting `memory` decodes to
  `pct: 0` there — a phone that draws the row would show a 0% machine. Guard on
  the server version or on `pct > 0` when that client grows the row.
- `cats-todo` keeps a copy of the wire vocabulary; this change is additive (one
  message field, no command), so nothing there breaks.
