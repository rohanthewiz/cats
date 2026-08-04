# Session: a HOST usage bar for disk space

- **Session ID:** `bcdf5f71-018e-4add-9a31-bcf79407ca0d`
- **Date:** 2026-08-04
- **Branch:** main (`6c628b8` → `0545933`, pushed)
- **Repos:** `cats`

Second row for the `HOST` subsection introduced in
`2026-0803-2159-usage-provider-subsections.md`, whose thresholds were
retuned in `6c628b8` the same morning.

## Request

> Add a HOST usage bar for Disk space

Taken at face value: one more row beside `Memory`, on the same poll, in
the same shape. No new timer, no new message, no wire change.

## Why the row is worth having

Memory earned its row because a laptop runs out of RAM before an account
runs out of week. Disk earns one for the opposite reason — it moves in
weeks, not minutes, which is exactly why nobody notices it. Transcripts,
worktrees, module and build caches, container images and a language
server per pane all land on the same volume, and a full disk does not
degrade the way memory does: it fails outright, mid-write, in whichever
pane happened to be writing. A row creeping up over a week is the only
warning available before that.

## The shape

```
hostUsageGroup()                         ← unchanged caller, on the 2-min usage poll
  ├─ hostMemory()  → "Memory"   ┐ each row gathered independently;
  └─ hostDisk()    → "Disk"     ┘ a failed reader drops its own row only
       │
       └─ diskBytes(hostDiskPath())
            ├─ ghostty && (darwin || linux)   statfs(2)
            └─ ghostty && !darwin && !linux   error → no row
```

### `statfs(2)`, not `df(1)`

It is the call `df` itself makes, it costs a syscall instead of a fork,
and it hands back the numbers already parsed. It also forces build tags
where `hostmem.go` got away with a `runtime.GOOS` switch: `syscall`
does not *declare* `Statfs_t` off darwin/linux, so the choice has to be
made before the compiler sees it. Hence
`hostdisk_statfs.go` / `hostdisk_unsupported.go` rather than one file.
`Bsize` is `uint32` on darwin and `int64` on linux — widened once, and
the rest of the arithmetic is identical.

### `Bavail`, and used = total − available

`Bfree` includes the blocks reserved for root; `Bavail` is what an
unprivileged process could actually write. Used is then `total - avail`,
which counts the reserved blocks as spent — deliberate, and what `df`'s
own `Capacity` column does. Those blocks are not room this program's
work can grow into, so crediting them would be exactly the kind of lie
the row exists to prevent.

### Which volume: `$HOME`, falling back to `/`

Home rather than root because home is where *this program's* growth
happens — sessions, transcripts, worktrees, `~/.cache`, `~/go/pkg`. On
macOS the two are the same APFS container, so the choice costs nothing
there; on a Linux host with a separate `/home` partition, the root
figure would answer a question nobody asked.

Worth recording because it is counter-intuitive on APFS: statfs on `/`
reports the **container's** shared free space, so the root volume's
`Size`/`Avail` already describe the Data volume's situation, while its
`Used` (≈12 Gi of read-only system) does not. `total - avail` is
therefore right on both systems and `Used` would have been wrong on one.

### An unreadable resource drops one row, not the section

`hostUsageGroup` used to return `(zero, false)` the moment memory came
back unknown. It now appends whatever each reader produced and only
withholds the group when **both** came up empty. The pair have no host
in common where exactly one is expected to fail, but a permission or a
synthetic mount can silence either on its own, and the surviving number
is still worth showing.

## Front-end (`cmd/catway/web/index.html`)

**The two HOST rows do not share a scale with each other.** The group
branch used to be one ternary picking `MEMORY_LEVELS` for `id === "host"`;
it now looks levels up per row **by name**:

```js
const USAGE_LEVELS  = { high: 75, crit: 90, of: "of the window used" };
const MEMORY_LEVELS = { high: 80, crit: 90, of: "of host memory in use" };
const DISK_LEVELS   = { high: 85, crit: 95, of: "of the disk in use" };
const HOST_LEVELS   = { Memory: MEMORY_LEVELS, Disk: DISK_LEVELS };
```

- **85/95 for disk**, against memory's 80/90: a volume 80% full is
  unremarkable and stays that way for weeks, and painting it yellow
  would repeat the mistake `6c628b8` had just finished fixing for
  memory. It is the last few percent that end a build mid-write.
- **Keying on the row name** is a documented exception, not a new
  pattern. `UsageGroup.ID` is opaque except for the literal `"host"`,
  and `"host"` is the one group *this* server synthesises — so this file
  is entitled to know its row names and no others. An unknown host row
  falls back to `MEMORY_LEVELS`: a new one here is far likelier to be
  another "nothing resets this" resource than a rate window.
- **`formatBytes` drops the tenth of a gigabyte above 100 G.** The disk
  pair would otherwise read `358.0G/460.4G`, two characters wider than
  the slot wants, and at that scale a tenth of a gigabyte is below the
  resolution of any decision the row informs. Memory figures are
  untouched — a 24 G machine never reaches the branch.
- `usageRow`, the meter, the 10s tick and the `.ureset[data-at]`
  selector unchanged: the disk row's detail is a fact, not a countdown,
  so it lands in the same slot memory's does and is skipped by the tick.

## Verification

- **`make check` green** (fmt, vet both tags, build, test, `-race`
  under `ghostty`).
- **Live read matches `df` exactly.** A scratch program running the same
  statfs against `$HOME` returned `total=460.4G avail=102.4G used=358.0G
  pct=77.8`; `df -h ~` reports `460Gi / 102Gi / 77%`. The row renders
  `Disk … 358G/460G … 78%`.
- **`hostdisk_test.go`** — live smoke test (a plausible share, a
  `used/total` detail, `UsagePctUnknown` off the two supported systems);
  an unstattable path must **error**, because zeroes would reach the
  sidebar as a 0% row, i.e. a volume with infinite room, which is the one
  answer worse than no row; and the path choice, with `HOME` emptied to
  force the `/` fallback and the fallback then proved stattable.
- **`hostmem_test.go`** — `formatBytes` boundary cases either side of
  100 G (`(100<<30)-1` renders `100.0G`, the wider form, by rounding);
  `hostUsageGroup` now asserts the rows are exactly `[Memory Disk]` **in
  order**, because the sidebar picks a scale per row from those names.
- **`proto_test.go`** — the canonical `usage` fixture carries both host
  windows through the round trip.

## Pushed

| Commit | Change |
|---|---|
| `0545933` | `feat(usage): a HOST disk row beside memory` |

## Open

- **Not seen in the MacApp.** `make macapp` still not run — the fifth
  session carrying that note. The installed app predates copilot
  support, the chat panel, the model labels, the usage subsections, and
  now this. The row was verified against `df` and in the Go tests, not
  on screen.
- **`renderUsage` was not re-run headless this time.** The prior session
  built a node DOM shim to exercise it; this change touches the same
  function (the level lookup moved inside the row loop) and was reviewed
  by reading rather than by running.
- **One volume only.** A worktree on an external disk, or a Linux `/var`
  that fills while `/home` is roomy, is invisible. Measuring every mount
  would need a row cap and a naming scheme; measuring the one where this
  program's own growth happens is the useful 90%.
- **No swap row.** On a machine that is already swapping, the memory row
  reads ~100% and says nothing about how hard it is thrashing. Related,
  and deliberately not attempted here.
- **`hostDiskPath` reads `$HOME` via `os.UserHomeDir`,** so a cats
  launched from a daemon context with no `HOME` silently measures `/`
  instead. Correct behaviour, but it is a fallback nothing surfaces.
