# Session: USAGE gets a subsection per model provider, and a copilot group

- **Session ID:** `f0fd86ba-1611-4ea8-898e-f2af5583e957`
- **Date:** 2026-08-03
- **Branch:** main (`54100ad` → `16fd757`, pushed)
- **Repos:** `cats`, and `cats-mobile` (`48baf53` → `298a3a1`, pushed)

Closes the "usage display" item deferred in
`2026-0803-1026-copilot-cli-support-in-cats.md` — but **not the way that
doc planned**. See "The measurement that changed the design" below.

## Request

> In the left aside the USAGE section should now include a subsection for
> each model provider similar to how PANES includes subsections for each
> workspace. Also keys there should not be bolded - all the CLAUDE keys
> are.

Decisions taken up front (asked, answered):

1. Regroup **and** add a real copilot collector — not presentation only.
2. `Memory` gets its own `HOST` heading; no orphan rows.
3. Headings **only** — no caret, no click-to-collapse, no persisted
   state. Reuse `.wsgrp`'s look, not its interactivity.

## The measurement that changed the design

The prior session recorded the intended copilot meter:

> `session.usage_checkpoint.totalPremiumRequests` in `events.jsonl` is a
> local premium-request counter — no network, no second credential read.

Counted across all 23 sessions in `~/.copilot/session-state`:

| signal | coverage |
|---|---|
| `session.usage_checkpoint` | **11 of 23 sessions** — absent on every 1.0.60 session, on 1.0.78, and on 6 of 16 1.0.77 sessions that completed turns. No version/model correlate. |
| `totalPremiumRequests` | max **0.33** machine-wide — mini models bill at 0×, so it rounds to `0 reqs` and separates nothing |
| `assistant.message.outputTokens` | **36 of 36 assistant messages, every version** |

So the planned row would have read a flat `0 reqs`, and on a 1.0.6x
machine would have had no data at all. **`outputTokens` is the primary
signal instead** — same id + timestamp + count shape as a claude
transcript record, so it reuses the bucket/offset/prune machinery
verbatim and lets COPILOT carry the same `5 hr` / `Week` rows.
`totalNanoAiu` survives as a third row, drawn only when a checkpoint
actually landed inside the week. Premium requests dropped entirely.

Nothing on disk records the plan's allowance, so every copilot row is
`Pct: UsagePctUnknown` — a figure, no bar.

## The shape

```
runUsage (own goroutine, 2 min / nudge)
  └─ readUsage(claudeEst, copilotEst)
       ├─ claudeUsageGroup   account read → 5 hr / Week / Week · <model>
       │                     else transcript estimate + Note
       ├─ copilotEst.group() 5 hr / Week (tok out) [+ Week · AIU]   ← if state dir
       └─ hostUsageGroup()   Memory                                 ← if readable
                         │
                         ▼
   Usage{ Groups: []UsageGroup{ {ID,Name,Note,Windows[]} } }
                         │
                         ▼
   renderUsage()  li.ugrp heading → rows → li.unote,  per group
```

**Wire (breaking).** `internal/browserproto/down.go` — `Usage` drops
`Source`/`Err`/`FiveHour`/`Weekly`/`WeeklyModel`/`WeeklyModelName`/`Memory`
for `Groups []UsageGroup`; `UsageWindow` gains `Name`. Four builders
collapse to `NewUsage(groups)` + `WithReadAt`.

- **Row labels and the caption moved server-side.** The set of providers
  and the meters each one reports is open-ended and server-known; the
  browser cannot enumerate them. `WithWeeklyModel`/`WithMemory` existed
  to say "only some sources have this row" — grouping says it
  structurally.
- **`UsageGroup.ID` is a `detect.IdentifyAgent` label, plus the literal
  `"host"`.** That one value is documented as CLOSED: it is the group
  cats synthesises rather than reads, and the only id a client may branch
  on — to pick `MEMORY_LEVELS` over `USAGE_LEVELS`, because 70% of RAM
  and 70% of a week are not the same trouble. Every other id is opaque.
- **A group with neither `Windows` nor `Note` is never sent**, and the
  front-end skips one anyway. An empty heading reads as a broken section.

## The copilot estimator (`cmd/catway/copilotusage.go`, new)

A close sibling of `usageEstimator`, with one addition and one deliberate
inversion.

**Addition — `base map[string]int64`.** `totalNanoAiu` is cumulative
*within a session*, not a per-event amount, so a window wants the delta
between consecutive checkpoints. The baseline rides alongside the byte
offset.

**Inversion — the first read starts at byte 0, never the tail.**
`usageEstimator` caps its first read and takes the tail, which is right
for per-event amounts and *wrong* for a cumulative counter: a tail lands
at an unknown baseline, so the first checkpoint it meets is credited the
entire session total as a delta — a whole session's AIU dumped into the
five-hour window on every cats restart. A file over 32 MB is therefore
**skipped outright and records no offset**; recording one would leave the
baseline unestablished and every later append would fold the running
total.

Other rules that earned their comment:

- **Fork replay.** `copilot --resume` can fork into a new directory
  carrying the earlier events verbatim. A checkpoint whose `id` is
  already in `seen` **sets the baseline and folds nothing**.
- **The baseline advances before any reason to stop does** — including an
  unparseable timestamp. It records where the counter reached, which is
  true whether or not the event can be dated; leaving it behind would
  make the next checkpoint fold this one's spend twice.
- **Backwards counter floors at 0** and still advances the baseline.
- **`bytes.Contains` for the line gate**, not `strings.Contains(string(line), …)`
  — copilot's assistant records carry `content`, `encryptedContent` and
  `reasoningOpaque`, so a string copy per line is real allocation.
- **`sawAIU` is derived, not sticky.** A zero delta still writes its
  bucket key, so "does this copilot report AIU?" is "is there a bucket in
  the week?" — and the row disappears on its own once the last checkpoint
  ages out.

Attribution caveat, commented and accepted: a session's first checkpoint
covers everything since `session.start`, so a long session's AIU lands in
one minute bucket. Tolerable for a figure with no bar.

## Front-end (`cmd/catway/web/index.html`)

- **`li.ugrp`** in `.wsgrp`'s register (uppercase, `--ws-heading`, 10px,
  `letter-spacing:.5px`, `font-weight:600`), `.sep` hairline for every
  group after the first. Stays `display:block` — there is no `.gsum` or
  `.car` to push right, and the rows' `3px/5px` padding is tuned around a
  meter this has not got.
- **The bold keys were never `font-weight`.** There is no such rule
  anywhere under `#usage-list`; `.uname` was `--fg-strong`, brighter than
  the body. With a heading above them that genuinely is bold, the lift is
  redundant → `--fg`.
- **`renderUsage` walks `msg.groups`** and names nothing but `"host"`.
  The `.sep` class keys on *what was emitted*, not the index — a skipped
  first group would otherwise leave the next one drawing a hairline as
  the section's first line.
- Nothing drawable at all falls through to the `li.empty "…"` pending
  row rather than leaving a blank section.
- `usageRow`, the levels tables, `fmtUntil` and the 10s tick untouched —
  the tick's `.ureset[data-at]` selector only matches inside `.urow`.

## Verification

- **Go, `make check` green** (fmt, vet both tags, build, test,
  `-race` under `ghostty`).
- **`copilotusage_test.go`**, the regression tests worth naming:
  - *cumulative counter* — write checkpoints 1→3 AIU, sweep, append 7;
    the week must hold **7.0**, not 10.0. This is what catches a
    reintroduced tail read.
  - *fork replay* — file B repeats A's checkpoints verbatim then adds
    one; only the fork's own delta lands.
  - *undatable checkpoint* — baseline still advances past it.
  - *oversize file* — skipped, **and no offset recorded**; a later
    readable version still folds correctly.
  - *no checkpoints* (the 1.0.60 machine) — exactly two rows, no AIU row.
- **`hostUsageGroup`** — present with one `Memory` row on darwin/linux,
  absent elsewhere; `ID == "host"` asserted because the sidebar's memory
  scale keys on it.
- **The real `renderUsage` run headless.** A scratch node script slices
  the function out of `index.html` and runs it against a minimal DOM
  shim: verified group order, the `.sep` rule when group 0 is skipped,
  `MEMORY_LEVELS` on host (79% → `.high`) vs `USAGE_LEVELS` on a scoped
  week (91% → `.crit`), and both empty states.
- **Live read** against the real `~/.copilot` tree:
  `5 hr 415 tok out` · `Week 8K tok out` · `Week · AIU 5.0 AIU`.
- **cats-mobile**: 72 Dart tests pass, `dart analyze` clean, and
  `wire.g.dart` verified byte-identical to cats's golden.

## Pushed

| Repo | Commit | Change |
|---|---|---|
| cats | `16fd757` | `feat(usage): a sidebar subsection per model provider, and a copilot group` |
| cats-mobile | `298a3a1` | `chore(catsproto): regenerate the wire layer against cats 16fd757` |

The mobile side landed **with** the server, not after it: this is a
breaking wire change with no negotiation behind it, and the generated
`asDouble(j['pct'])` returns **0** for a missing key — an un-regenerated
client would have drawn 0% meters where the truth is "no allowance to
measure against". `tool/regen.sh` used, so `CATS_REV` re-pinned.
`session.dart` needed no change: it stores the whole message and reads no
field, so the test was the only Dart source touching the removed ones.

## Open

- **`make macapp` still not run** — fourth session carrying this. The
  installed app predates copilot support, the chat panel, the model
  labels, and now this.
- **`weeklyModelLimit` still collapses N scoped weeklies to one.** Its
  comment says the only reason was "the sidebar has room for a single
  extra row"; subsections do not create room, so it was left alone
  deliberately. A Max plan with two scoped models still shows one.
- **No provider registry.** `readUsage` names claude and copilot in
  literal appends. A third would want the `detect.IdentifyAgent` /
  `modelResolvers` / `acpchat.BackendDef.ModelAgent` vocabulary the rest
  of the repo already keys on rather than a fourth list — codex and
  gemini both have state trees here and neither is read.
- **Copilot's windows are 5h/week because claude's are**, not because
  copilot meters that way — its real allowance is monthly, on a billing
  anniversary that appears nowhere on disk. Consistent windows across
  groups was judged worth more than a `Month` label claiming a reset
  boundary we cannot compute.
- **No CI enforces the cats-mobile mirror.** Still a ritual.
- The `.ugrp` heading has no accessible role — it is a styled `<li>` in a
  plain `<ul>`, same as `.wsgrp`. Consistent, and consistently untyped.
