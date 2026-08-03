# Session: AGENTS rows show their age while working, too

- **Session ID:** `da8b0f01-7799-4a52-94ff-8103b20ccddb`
- **Date:** 2026-08-03
- **Branch:** main (from `8a1fcd7`)
- **Repo:** `cats`

## Request

> In the AGENTS section in left aside include the "1 hr ago" status also when
> the agent is working, not just stopped states

Loaded on top of the previous session (host memory row on USAGE) via
`/sess-load 1`.

## The change

One condition in `renderAgents` (`cmd/catway/web/index.html`). The age span was
gated on the state:

```js
if (st !== "working" && it.since_ms >= 0) {
```

now only on whether the rollup knows the instant at all:

```js
if (it.since_ms >= 0) {
```

`since_ms < 0` remains the rollup's "no instant known" and still suppresses the
span.

## Why the gate existed, and why it flipped

The original comment argued a working agent is mid-change, so "how long since
it changed" says nothing — and its dot already pulses. The flip is a
re-reading of the same timestamp: `since_ms` is measured from when the state
*last moved*, so on a working row it is precisely the run-time. That makes
`%1.2 · 5m ago · working` the row most worth a glance — a long-running agent is
either deep in something or wedged, and either way the age is the signal. The
comment in the code was rewritten to make the two readings explicit (settled:
"is this still true?"; working: "how long has it been at this?").

## Nothing else had to move

- The section's shared 5-second tick already refreshes **every** `.aage` by
  re-reading its absolute `data-at` stamp — it never looked at state, so
  working rows count up between rollups with no new code.
- Checked all `since_ms` / `.aage` uses: this was the only site with a state
  gate. Workspace summaries and pane rows don't render ages.
- Contrast with last session's tick bug: `.aage` always carries `data-at`, so
  no `[data-at]`-style scoping was needed here.

## Verification

- `make check` (fmt, vet, build, test, `vet-ghostty`, `race-ghostty`): clean.
- Browser-side only — no wire change, no Go change, so no golden regeneration.

## Files

| File | Change |
|---|---|
| `cmd/catway/web/index.html` | `renderAgents`: drop the `working` gate on the age span; comment rewritten |

## Open

- The working dot still pulses *and* now carries an age; if that reads busy,
  the age could dim further via `.aage` opacity (`#agent-list .aage` is at
  `.75`).
- `fmtAge` rounds one unit deep (`Math.round`), so "90s ago" shows as "2m ago"
  — fine for a glance, but a stopwatch it is not.
