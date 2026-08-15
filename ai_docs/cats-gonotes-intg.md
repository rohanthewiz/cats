# gonotes × cats: bytdb merge, TUI revamp, native integration

*Third run of the cats-native pattern (`../ced/ai_docs/cats-native-plan.md`,
`ai_docs/dbc-intg-plan.md`), applied to gonotes (`../gonotes`). Written
2026-08-14. Like the dbc doc, this is both the plan and the running status
record — phases get checkboxes as they land.*

## Context

gonotes is a notes app: `models/` data layer, `web/` rweb server + JSON API
(JWT, `{success,data,error}` envelope), and a Bubble Tea TUI (`tui/`, ~1,590
LOC, screen-stack architecture). It becomes the third cats-native client:
stdlib-only transport mirrors, hook reporter (sidebar badge / toast / phone
push), host identity, host theme sync, agent collaboration, ⌘ accelerators —
behind the capability ladder where **Tier 0 (any terminal) loses nothing and
every failure degrades silently**. Two things distinguish this run from dbc's:

1. **A forgotten branch merges first.** `origin/migrate-to-bytdb` (one commit,
   `436b9f7`) replaces DuckDB with two bytdb databases (public plaintext +
   private whole-DB-encrypted), drops the cache layer, bumps Go to 1.26.1.
   Master is 6 ahead (all web/Monaco work); the only file both sides touch is
   `.gitignore` — a near-clean merge. The branch touches neither `tui/` nor
   the markdown round-trip. Data safety rail: **markdown export before the
   merge, import after** (user's call); the branch's own `scripts/migrate`
   runs into a scratch dir only as a cross-check.
2. **The TUI gets a full conformance revamp**, including a Bubble Tea v1→v2
   migration — v1 has no kitty keyboard support, so ⌘ chords could never
   arrive; v2 makes them free (see windfalls).

Flagship payoff: a long `$EDITOR` session over a note ends → cats "finished"
badge/toast/phone push. Plus the genuinely novel inverse flow neither ced nor
dbc had: **capture a sibling agent pane's output into a note** (ctrl+g).

**Key architectural translation:** ced invented a `catsEvent` for its raw
tcell loop; dbc posted closures through `QueueUpdateDraw`. Bubble Tea is the
native home for the rule: *cats-layer goroutines may touch only the transport
objects and `p.Send`; every model/screen/style mutation happens in `Update`
on a typed message.* Verified in v2.0.8: `p.Send` selects on the program
context created in `NewProgram` (tea.go:614, :1183), so it is safe at any
time and a no-op after exit — no `stopping` channel needed, unlike tview.

**Verified windfalls (checked in the module cache / source, they shrink the work):**
- The charm v2 stack is **stable and GA**: bubbletea/v2 v2.0.8, bubbles/v2
  v2.1.1, lipgloss/v2 v2.0.6, glamour/v2 v2.0.1, teatest/v2 (pseudo-version,
  actively maintained). Gotcha: **canonical import paths are `charm.land/*`**,
  not `github.com/charmbracelet/*` — the github paths fail with a module-path
  mismatch. Both bubbletea and lipgloss ship `UPGRADE_GUIDE_V2.md` in the
  module root, written for automated migrations.
- **bubbletea v2 unconditionally engages the kitty keyboard protocol**
  (cursed_renderer.go:134-140; minimum flag 1 always). cats' ⌘-forwarding
  gate is exactly "pane registered kitty flags" — so every gonotes-on-v2 pane
  passes `cmdGoesToPane` with zero enabling work, the same class of windfall
  tcell handed dbc. Chords arrive as `KeyPressMsg` `"super+x"` (match
  `"meta+x"` defensively).
- `tea.View.WindowTitle` is declarative — the renderer diffs it and emits
  OSC 2 only on change (cursed_renderer.go:816). No push/pop bookkeeping.
- `tea.SetClipboard` is native OSC 52 in v2 — remote-safe clipboard is one
  command if ever wanted; no atotto, no dedicated phase.
- The API marshals `models.Note` with its `sql.Null*` fields under plain json
  tags, and that wire shape round-trips **losslessly back into the same
  structs** — the HTTP store needs no DTO layer.
- `md_export.go`/`md_import.go` round-trip guid, title, description, tags,
  categories (+subcategories), private, flagged, created, updated + body —
  the migration safety rail already exists and is guid-anchored/idempotent.

## Design decisions (settled)

- **Bubble Tea v2 + conformance revamp** (user-confirmed). Keep the screen
  stack; migrate to `charm.land/*/v2`; introduce a `key.Binding` keymap
  (today: `switch msg.String()` hardcoded across six files), palette-driven
  theming, measured form layout (kills the `height-14` magic constant),
  rune-safe truncation/filtering, two-pane wide layout, cached glamour
  renderer. v2 breaks that matter: root `View() tea.View` (AltScreen +
  WindowTitle declarative); `KeyMsg` → `KeyPressMsg` at 8 switch sites;
  **space is `"space"`, not `" "`** (form.go:206 private toggle silently dies
  otherwise); viewport/textinput `Width =` become `SetWidth`; lipgloss
  `AdaptiveColor` removed → `lipgloss.LightDark(isDark)` keyed off
  `tea.BackgroundColorMsg`; glamour `WithAutoStyle` removed → explicit
  Dark/LightStyleConfig, which *forces* the cached-renderer fix (no terminal
  queries under a host). `tea.ExecProcess`, `WindowSizeMsg`,
  `Sequence`/`Batch` survive unchanged.
- **Data mover across the merge: md export → md import** (user-confirmed).
  Export per user on the DuckDB build (with `GONOTES_ENCRYPTION_KEY` set so
  private notes export decrypted — plaintext on local disk, accepted as the
  backup); import per user into fresh bytdb databases after the merge;
  `scripts/migrate` into a scratch dir as a count/spot-check cross-check.
  The old `.ddb` is retained untouched.
- **Hybrid data access** (user-confirmed). bytdb is single-process, so a
  cats-hosted TUI must not open a data dir a running GoNotes server/app owns.
  `runTui` probes `GONOTES_URL` (default `http://localhost:8444`) with a
  short-timeout GET `/api/v1/health` requiring the envelope shape; a live
  server → HTTP client mode and **`models.InitDB` is skipped entirely**;
  otherwise in-process bytdb as today. Seam: a ~17-method `Store` interface
  in `tui/store.go` with `store_local.go` (thin wrappers over `models.*`)
  and `store_http.go` (gn-clip.sh conventions: `GONOTES_USER` /
  `GONOTES_PASSWORD` / `GONOTES_SYNC_PASSWORD_B64`, token cached at
  `~/.gonotes/.api_token`, validated via `/auth/me`; on 401 one silent
  re-login then the login screen). Every TUI models call has an API
  equivalent except `ListUsernames` — deliberately **not** added (username
  leak on a hub); HTTP-mode login loses the prefill list and prefills from
  `GONOTES_USER` instead. `GetCategoryByName` is client-side
  (`ListCategories` + scan).
- **Transport package**: top-level `cats/` in gonotes, hand-copied from
  `../dbc/cats/{detect,client,hooks,events}.go` (~880 source lines,
  twice-ported, stdlib-only) with the upstream-file comment convention.
  **Never import the cats module.** Request id `"gonotes"`. Verb set: dbc's
  seven (`ping`, `pane.list`+`ResolvePane`, `pane.focus`, `pane.send_input`,
  `chat.send`, `config.get`, `events.subscribe`) **plus one new verb**:
  `Capture(pane, lines)` mirroring `CaptureParams{scope:1, unwrap:true}` /
  `CaptureResult` (cats `internal/app/command_vocab.go:449-470`).
- **Hook states**: source/agent `"gonotes"` (unprefixed — `cats:` is
  reserved). `working` spans: (a) the `$EDITOR` session — `working` +
  `custom_status = "editing: <title>"` (32-byte rune-safe clamp) around
  `tea.ExecProcess`; ExecProcess releases the *terminal*, not the hook
  socket, so the reporter keeps working — this is the flagship edge; (b)
  save/delete/category-sync via the form's existing `busy` flag transitions.
  No honest `blocked` (every gonotes modal is user-invoked; the login screen
  under a plugin launch is deliberately NOT blocked — it would badge every
  launch). Port the `asking` plumbing + `blocked > working > idle` priority
  anyway. Report on change only; seq seeded `UnixNano()`; `Release()` after
  `p.Run()` returns **even when the TUI fails to start** (dbc's stale-badge
  lesson); armed from env alone before the probe.
- **catsInit placement**: in `tui.Run()` before `NewProgram`, not in
  `appModel.Init` — the reporter arming, the Release guarantee, and the
  synchronous startup theme fetch (bounded by `ProbeTimeout`, 500 ms worst
  case, first frame paints in host colors) all live outside the program
  lifecycle. `catsState` is a **pointer** field on `appModel` (appModel is a
  value copied every Update); zero value = Tier 0 inert. Typed messages:
  `catsReadyMsg`, `catsThemeMsg`, `catsEventMsg`, `catsPanesMsg`,
  `captureDoneMsg`.
- **Theme**: `tui/palette.go` `Palette` struct + `setPalette` reassigning the
  `styles.go` vars (which lose `AdaptiveColor`); default palette picks
  light/dark from `tea.BackgroundColorMsg`. Host mapping ports dbc's: core 7
  must be hex or the synthesis is abandoned; `sel` = accent blended over bg
  at 0.30 (cats' `sel-fill` recipe). Propagation: root broadcasts a
  `paletteChangedMsg` to **every screen in the stack**, exactly like the
  existing `WindowSizeMsg` loop (tui.go:89-100), via an optional `restyler`
  interface — bubbles list **delegate styles are captured at construction**
  (the direct analog of dbc finding 6's TextView trap) and must be rebuilt;
  the cached glamour renderer is keyed on (width, palette) and regenerated.
  Subscription **unfiltered** (theme_changed is session-scoped, pane 0).
- **Host identity**: title is a pure function of model state assigned to
  `v.WindowTitle` each frame — idle `"gonotes"`, browsing `"<n> notes —
  gonotes"`, editing `"<title> — gonotes"` (control bytes stripped). OSC 7
  once at startup via a stubbed-in-tests `ttyWrite func(string) error`,
  skipped silently when `/dev/tty` won't open (port dbc's `fileURLPath`).
- **Capture-to-note** (user-confirmed; the novel inverse of dbc's Ctrl+G):
  one door, **ctrl+g** in browse. Modal picker of agent panes from a cached
  `pane.list` (2 s rate limit; refreshed on Tier-1-up, picker open,
  `pane_agent`/`pane_added`/`pane_removed`; own pane excluded by handle;
  ranked blocked > working > idle). Enter → `Capture(pane, 200)` on a
  goroutine → `captureDoneMsg` → push the note form prefilled: title
  `"Capture: <agent> — <timestamp>"`, body = captured text (ansi:false +
  residual control strip), tag `"capture"`. Tier 0: one quiet status line.
- **⌘ layer** (`tui/metakeys.go`): first branch in root `Update`; translate
  claimed `super+`/`meta+` chords to their existing twins and re-dispatch;
  **swallow every other armed chord** ("⌘S didn't work" must never type an
  s). Table — all on cats' `CMD_TO_PANE` allowlist `{S,P,E,F,D,G,Slash}`,
  none ⌘-only, test-enforced: ⌘S→save, ⌘E→edit, ⌘F→flag, ⌘D→delete,
  ⌘G→capture picker, ⌘/→filter. No arming gate needed: chords can only
  arrive when a kitty-speaking host forwards them, and v2 makes every
  gonotes pane a kitty pane.
- **Plugin manifest** (user-confirmed; the piece neither ced nor dbc had):
  `gonotes/cats-plugin.toml` on the `../cats-todo/cats-plugin.toml` template
  — `id`, `[[build]]` `go build -o bin/gonotes .`, `[[actions]]` `tui` →
  `./bin/gonotes tui -d ~/.gonotes` (absolute default dir — a plugin tab
  starts wherever the target workspace points), optional `serve` action.
  Native client + plugin host are orthogonal; gonotes ships both.
- **cats-side**: one line — `gonotes: 4,` in `AGENT_HUE`
  (`cmd/catway/web/index.html` ~:1709), continuing the house seating
  claude 1, ced 2, dbc 3. Verify the FNV fallback slot at implementation
  like dbc did rather than assuming.

## Upstream asks

None blocking. The `capture` verb already exists in the §7 vocabulary;
`custom_status` remains stored-but-unread in cats' own UI (carried over from
dbc's nice-to-have list).

## Risks / open items

- **Kitty *set* vs *push***: bubbletea v2 emits `CSI = flags ; 1 u` where
  tcell used `CSI > 1 u`. cats' emulator handled tcell's; the set form is
  equally standard, but `modes.kitty` registering must be confirmed live
  (Phase 5 manual pass). High confidence; dbc precedent one encoding away.
  **Half-resolved in Phase 5.** The encoding is now observed rather than
  assumed: the pty capture of the real binary shows
  `ESC[>4m ESC[=0;1u ESC[>4;2m ESC[=1;1u` at startup — the **set** form, with
  flags 1 (disambiguate), paired with modifyOtherKeys. What is still open is
  only whether that registers on the host side, and the code path says it
  should: cats does not parse this itself, it reads
  `e.term.KittyKeyboardFlags()` from ghostty-vt
  (`internal/terminal/ghostty.go:270`), which implements the whole kitty
  protocol including set. Confirming it needs gonotes running in a real cats
  pane — a fake control socket cannot answer this — so it stays on the live
  checklist rather than being called done.
- ~~**Server-side decryption over the API**: whether GET note decrypts private
  bodies when the server holds the key (web UI implies yes). If not, HTTP
  mode shows ciphertext for private notes and must say so (Phase 4).~~
  **Resolved in Phase 4: it decrypts, and the question was framed on the old
  architecture.** Per-note AES died with the bytdb merge — `Note.EncryptionIV`
  is retained for transport compatibility but no longer persisted. bytdb
  encrypts the *whole private database* at rest, so a server that opened it
  with the key reads plaintext and every handler serializes plaintext. Proven
  live, not inferred: a private note seeded with the body
  `CLEARTEXT-MARKER-9931` renders that marker in the HTTP-mode preview pane.
  No warning banner is needed.
- ~~**Sync hub caution** (Phase 1): md import records fresh change entries for
  every note; a live hub will receive a full push, reconciled by GUID
  (delete-wins then LWW).~~ Moot — the cutover went via `scripts/migrate`,
  which records no change entries, and there are no peers. See Phase 1.
- **Probe false-positive**: some other service on 8444 — the health probe
  requires the gonotes envelope shape, not just a 200.
- ~~bubbles v2 list/textarea behavioral nuances were audited by signature
  only — the Phase 2 teatest suite is the net.~~ Settled in Phase 2. The
  nuance that mattered was not behavioral but stylistic: every bubbles v2
  widget hardcodes a dark default style set and copies it in at
  construction. See Phase 2.
- ~~Two-pane preview re-render cost — render from a cache keyed by note id,
  only on selection settle.~~ Settled in Phase 3: two caches (renderer on
  width+palette generation, output on note id + updated timestamp), no debounce
  needed since a re-render of an unchanged selection is a cache hit.
- **bubbles v2.1.1 `help.shouldAddItem` stops truncating** once the ellipsis no
  longer fits, appending every remaining item. Worked around locally with
  `clampPane`; an upstream ask if it survives a version bump. See Phase 3.

## Phases (each shippable alone; commit per phase; gonotes commits on master, cats on main)

### Phase 0 — ✅ export (pre-merge, on the DuckDB build) — done 2026-08-14
- File-copy `~/.gonotes/data/notes.ddb` to a dated backup.
- `gonotes export-md` per user (discover usernames at run time) into a dated
  dir, e.g. `~/.gonotes/export-premigration-2026-0814/`. Verify exported file
  count vs note count. This export is kept permanently as the human-readable
  backup and is Phase 1's `import-md` input.

**What happened, and the five things worth knowing next time:**

1. **Encryption was never enabled.** `GONOTES_ENCRYPTION_KEY` is absent from
   the server's environment, the app plist, LaunchAgents, shell profiles, and
   `~/.gonotes/config/cfg_files/` does not exist at all. Private bodies were
   already plaintext, so the export needed no key and exposed nothing storage
   did not already hold. The plan's "export with the key set" step was moot.
2. **The lock holder was the *stale* binary.** `~/bin/gonotes` is an Apr 22
   build whose `--help` lists no subcommands — no `export-md`, no `tui`. It
   had been serving since 24 Jul. Phase 0 therefore had to build a current
   binary from master first; the installed one could not have done the job.
   DuckDB's lock is absolute: `duckdb -readonly` failed with
   `Conflicting lock is held in ~/bin/gonotes (PID 61161)`.
   Stopping the server is not optional, and it is the first step, not a
   courtesy.
3. **SIGTERM checkpointed the WAL, which is why the backup is trustworthy.**
   `notes.ddb` went 20,983,808 → 22,556,672 bytes on shutdown and the 172 KB
   `notes.ddb.wal` disappeared into it. Copying *after* the stop yielded a
   byte-identical backup (SHA-256 verified). Copying a live DuckDB file would
   have caught a torn mid-write state and silently missed the WAL.
4. **Result: 30 live notes exported (5 private), 0 errored**, for the single
   account on this machine, into 19 category folders, 404 KB. Verification
   went past file
   counting: the 30 exported GUIDs are set-identical to the 30 live DB GUIDs
   (`diff` empty), and every body matches the DB byte-for-byte modulo the
   +1/+2 trailing newline the exporter appends. 1 soft-deleted note is
   correctly absent.
5. **A verification trap worth avoiding.** Comparing DuckDB `length(body)`
   against `wc -c` appears to show 14 of 30 notes corrupted, one wildly
   (6,089 vs 10,815). That is measurement error, not data loss: `length()`
   counts *characters* and `wc -c` counts *bytes*, and these notes are full
   of em dashes and arrows. `strlen()` is the byte-accurate function
   (`octet_length` rejects VARCHAR, and casting a non-ASCII body to BLOB
   errors out and dumps the note into your terminal). Trailing-whitespace
   normalization in the extraction pipeline also produced spurious negative
   deltas. Measure bytes on both sides, and strip nothing.

The server was restarted exactly as found (same stale `~/bin/gonotes`,
`/api/v1/health` answering). **Phase 1 will need it stopped again** — and
after Phase 1 is pushed, `mac-install.sh` hard-resets `~/.gonotes-src` to
origin/master, so the MacApp rebuilds itself onto bytdb on its next run.

### Phase 1 — ✅ merge migrate-to-bytdb + cut over — done 2026-08-14

Merged as `bdc0796` (gonotes master, pushed). The merge itself was the easy
part: `.gitignore` was the only overlap and auto-merged, and the six Monaco
commits master had gained never touch `models/`, so `go build`, `go vet` and
all three test packages passed on the first try.

**The plan had the two data paths backwards.** It named `import-md` the
migration and `scripts/migrate` the cross-check. Running both into scratch
dirs and exporting each *back* to markdown for a byte-diff against the Phase 0
export inverted that:

| | `scripts/migrate` | `import-md` |
|---|---|---|
| notes | 31 (incl. the soft-deleted one) | 30 |
| re-export vs Phase 0 export | **byte-identical, 30/30** | 6 files differ |
| private-note timestamps | preserved | **reset to import time** (5 notes) |
| trailing whitespace | preserved | trimmed (1 description, 2 bodies) |
| user identity | **original GUID + bcrypt hash** | new GUID, new password |
| categories / links | 28 / 42 | 28 / 42 |

None of the import path's losses are accidental — they are in the code.
`md_import.go:196-205` routes private notes through `CreateNote` rather than
`CreateNoteWithTimestamps`, so their `created`/`updated` become the moment of
import; body and description trimming costs a single trailing newline on two
notes. And `import-md --user` requires the user to *already exist*, which a
fresh data dir has no way to provide — the scratch run needed a registration
through the HTTP API first, minting a new user GUID. For a hub-and-spoke sync
design that GUID is identity, so the import path silently changes who you are.

So `scripts/migrate` produced the live database and the markdown import became
what it is actually good at: an independent witness. Its 30 files re-exported
byte-identical from the migrated DB, which is a much stronger statement than
any count.

**Verification that mattered more than counting.** The migrate output
(1 user, 28 categories, 31 notes, 42 links) matches the *old* server's own
startup log — `notes_count=31, categories_count=28, relationships_count=42` —
so two independent readers agree. Password-hash preservation was confirmed by
pulling the bcrypt string from DuckDB and finding it in `notes_public.bytdb`;
note that this needs `grep -aF`, since an unescaped `$2a$12$…` as a regex
quietly fails to match and reads as data loss.

**The live file had drifted from the Phase 0 backup.** After the clean stop,
`~/.gonotes/data/notes.ddb` was the same 22,556,672 bytes as the 17:39 backup
but a *different* SHA-256. Row counts and `max(updated_at)` were unchanged, so
it was DuckDB checkpoint churn rather than edits — but the lesson holds: take
a fresh copy at cutover time and migrate from that, not from the archival
backup. Both copies are kept (`backup-premigration-` and `backup-cutover-`).

**Cutover, in the order it has to happen:** stop the server → fresh copy →
migrate from the copy → verify by round-trip export → drop the two `.bytdb`
files into `~/.gonotes/data` → replace `~/bin/gonotes` (the April DuckDB build
is kept as `~/bin/gonotes-duckdb-2026-0422`) → restart → `./mac-install.sh`.

That last step is not optional housekeeping. `notes.ddb` is still sitting in
the data dir, and the MacApp only starts its bundled binary when nothing is
already healthy on 8444 — so a stale DuckDB binary in the app bundle would
have opened the old database and served notes that are no longer the source of
truth, with no error anywhere. The bundle now reports `bdc0796`.

**The sync-hub caution in §Risks turned out moot**: `scripts/migrate` records
no change entries at all, sync is disabled, and `sync_state` is empty — no
peers exist. The 134 `note_changes` rows in the old DB are inert local history
and were deliberately not carried across (neither are `sync_state`,
`sync_conflicts`, `invite_tokens`).

**Verified live in the MacApp**: all notes present, and a deliberate logout /
login round trip succeeded with the *original* password. The first launch did
not ask for sign-in at all, which is its own confirmation — the JWT's subject
is the user GUID, not the row id (`models/token.go:31`, "Using UserGUID
instead of ID allows tokens to work across sync scenarios"), so the surviving
WebKit session only resolved because migrate preserved that GUID. The import
path would have invalidated it.

That said, the session surviving is *partly* an accident worth naming:
`InitJWT` falls back to the literal string
`development-only-secret-do-not-use-in-production` when `GONOTES_JWT_SECRET`
is unset (`models/token.go:42-49`), and it is unset here — no env var, and
`~/.gonotes/config/cfg_files/.env` does not exist, same as the encryption key
in Phase 0. Every token this instance issues is signed with a publicly known
constant. Pre-existing and unrelated to the migration, but it becomes load
bearing at Phase 5, when hooks and phone push start carrying gonotes auth off
this machine. Set a real secret before then.

Remaining: if the cats-mobile rev pin matters, run its `tool/regen.sh` flow.

### Phase 2 — ✅ Bubble Tea v2 migration — done 2026-08-14 (gonotes `c00daee`)
Versions landed: bubbletea v2.0.8, bubbles v2.1.1, lipgloss v2.0.6, glamour
v2.0.1, teatest `v2.0.0-20260813141921`. 578 insertions / 159 deletions.
`go build`, `go vet`, all four test packages green; TUI suite stable over 5
consecutive runs.

**The import-path trap runs both ways.** The four libraries are
`charm.land/*/v2`, but teatest is **not** — it is still
`github.com/charmbracelet/x/exp/teatest/v2`, and requiring the charm.land
form fails with `module declares its path as ...`. Each module's own go.mod
is the only authority; assuming either convention is uniform costs a
resolution error.

**What the port actually cost, in order of how quietly it fails:**

1. **`" "` → `"space"`.** Nothing catches it — the private-note checkbox
   would just stop toggling. `Key.String()` returns `Text` when non-empty
   *except* for space, the one printable character with an invisible literal,
   which falls through to `Keystroke()`. Every other binding (letters,
   `esc`, `ctrl+s`, `shift+tab`, `Y`/`N`) stringifies unchanged.
2. **`View() string` → `View() tea.View`, root model only.** Screens still
   return strings. `tea.WithAltScreen()` is gone from the program options;
   `AltScreen` is a field on the returned view, so losing it yields a program
   that runs happily and paints nothing.
3. **`KeyMsg` → `KeyPressMsg`.** `KeyMsg` survives as an *interface* over
   press and release, so `case tea.KeyMsg:` still compiles — and would
   double-fire every binding the moment release reporting is enabled, which
   is exactly what Phase 5's kitty work would turn on.
4. viewport `New(WithWidth, WithHeight)` + `SetWidth/SetHeight`; textinput
   `SetWidth`. Mechanical.

**The plan's one wrong call: `WithStyles(styles.DarkStyleConfig)` as an
interim.** lipgloss v2 deleted `AdaptiveColor`, which forces the palette
question *now*, not in Phase 3 — hardcoding dark is a live regression on
light terminals, and glamour v2 also dropped `WithAutoStyle()`. So
`styles.go` grew `setPalette(dark bool)` (both glamour configs exist:
`styles.DarkStyleConfig` / `LightStyleConfig`).

Placing the detection is the subtle part, and the two obvious spots are both
wrong: `lipgloss.HasDarkBackground` blocks on an OSC 11 reply for 2s **per
fd, and tries both**, so a terminal that ignores the query costs ~4s of black
screen; and a package-var initializer would run it during `gonotes serve`
too, since main imports `tui` unconditionally. The answer is v2's own
mechanism — `tea.RequestBackgroundColor` from `Init()`, `setPalette` on
`tea.BackgroundColorMsg`. Non-blocking; a silent terminal simply stays dark.
(It is passed **uncalled**: its signature is `func() tea.Msg`, i.e. already a
`tea.Cmd`. Its own doc comment shows it invoked, which does not compile.)

**bubbles v2 compounds this**: `list.NewDefaultDelegate()`,
`textinput.New()` and `textarea.New()` all bake in a hardcoded *dark* style
set — its source flags that as temporary — and copy it in at construction.
So each construction site re-applies `DefaultStyles(isDark)`, and
`loginScreen` (built before the program starts, hence the only screen alive
when the reply lands) implements a one-method `restyler` interface. Phase 3
inherits both seams and broadcasts to the whole stack.

**Tests — `tui/tui_test.go`, the package's first (249 lines).** A key-name
table pinning every binding string the six screens dispatch on, so upstream
renames fail here instead of silently disabling a feature; teatest boot flows
for login and a seeded browse list; an altscreen-requested assertion.

Two harness gotchas worth keeping:
- **`tm.Output()` is one consumable stream.** A second `WaitFor` accumulates
  from wherever the first stopped reading, so text already pulled into the
  first call's buffer is gone. It reads exactly like a rendering bug. One
  `WaitFor` per program, with a condition over all the substrings.
- `WithInitialTermSize` races any message injected at startup; the browse
  test re-sends `WindowSizeMsg` after `loggedInMsg` or the list sizes to 0x0.

**Verified past the suite, on a real pty.** `script -q /dev/null` is useless
here — it inherits a 0x0 winsize when the parent isn't a tty, so the program
correctly renders nothing and looks broken. A small python `pty.fork` +
`TIOCSWINSZ` harness that also *answers* the OSC 11 query showed the full
login card, and flipping the answer from `rgb:1c1c…` to `rgb:ffff…` showed
clean separation: dark truecolor (`#7D79F6`) through byte 2063, light
(`#5A56E0`) from 2237 on — the async palette swap working end to end.

### Phase 3 — ✅ conformance revamp — done 2026-08-14
Landed as specified: `tui/keymap.go` (207), `tui/palette.go` (169),
`tui/markdown.go` (235) new; 441 insertions / 212 deletions across the eight
existing files; 984 lines of tests in three files plus two goldens. `go build`,
`go vet`, `go test -race ./...` green; TUI suite stable over repeated runs.

**The palette is hex strings, not `color.Color`.** Three reasons converge:
glamour's `ansi.StylePrimitive.Color` is a `*string` and takes nothing else; a
struct of strings is comparable with `==`, which is what lets `setPalette`
return "nothing changed" and skip the broadcast; and the cats host theme
(Phase 6) *arrives* as hex, so the mapping becomes assignment plus a validation
gate rather than a conversion layer. `Sel` is derived — `blendHex(Primary, Bg,
0.30)`, cats' own sel-fill recipe — because a host supplies an accent and a
background but never a selection color.

**`setPalette` returns whether it changed anything**, and `paletteGen`
increments only then. `tea.BackgroundColorMsg` is not a once-per-program event
(repaints, focus re-announcements), and each spurious accept would rebuild every
widget and flush the markdown cache for no visual difference. The broadcast is a
`paletteChangedMsg` rather than a direct loop specifically so Phase 6's
socket-goroutine theme change lands on the event loop.

**The renderer cache turned out to be load-bearing, not an optimization.** The
wide layout re-renders the selected note's markdown on every frame — every arrow
key, every cursor blink — and a `TermRenderer` builds a goldmark chain and a
chroma formatter at construction. Two caches: the renderer on (wrap width,
palette generation), the rendered output on (note id + updated timestamp, width,
generation). The timestamp in the key is what makes an edit invalidate its own
entry. Verified safe without a lock: all three `p.render(model)` call sites in
bubbletea v2.0.8 are on `Run()`'s goroutine, immediately after `Update`. The
rule that must hold is that nothing in `markdown.go` is ever called from a
`tea.Cmd`.

**A bubbles bug the wide layout exposed.** `help.shouldAddItem` appends the
ellipsis when an item overflows — but only if the ellipsis itself still fits;
otherwise it falls through to "ok" and appends the item *and every one after
it*. At 48 columns the browse footer truncates correctly; at 80 it renders 111
columns wide and the terminal wraps it, shearing the layout. Fixed at the pane
boundary with `clampPane` (ANSI-aware truncate + pad), which is the right place
regardless: the screen is what promises the panes total the terminal width.
Upstream-ask candidate; not filed.

**`keys.Scroll` carries `up`/`down` it never matches on.** A `key.Binding` with
an empty key set reports `Enabled() == false`, so a help-only row renders as
nothing. The detail screen's ↑/↓ hint needs keys to exist even though the
viewport is what consumes them.

Also: `form.go`'s `height-14` is gone — `chrome()` returns the blocks above and
below the textarea and `layout` measures them with `lipgloss.Height`, which is
exact and survives a wrapped heading or an added field. `FilterValue` truncates
in runes. Every screen now implements `restyler`; `confirmScreen` deliberately
does not, holding no widget.

**Tests.** `keymap_test.go` pins every binding's key strings and asserts no
footer advertises a key its screen does not handle (the handled sets are written
out by hand, not derived, or the test would be tautological).
`palette_test.go` covers parse/blend, the no-change early return, and the
broadcast reach via a spy screen. `layout_test.go` carries the narrow/wide
goldens, a fits-the-terminal check at four widths, the markdown cache
behavior, and the form's measured chrome including a wrapping heading.

One finding worth carrying: **bubbles v2.1.1's `textinput.DefaultStyles(true)`
and `(false)` render a focused, empty input identically** (they differ in the
blurred and cursor styles). So the form/login restyle tests assert on the style
sets, not on `View()` — a rendered-output test there passes whether or not
restyle ran. The list delegate does differ visibly, which is what
`TestRestyleRebuildsListDelegate` uses.

Verified past the suite on a real pty at 120×40 and 80×24 against the shipped
binary: the two-pane preview renders live markdown, and flipping the OSC 11
answer shows dark accent `#7D79F6` through byte 1470 then light `#5A56E0` from
1644 on, with the derived selection fill swapping alongside it (`#39385D` → 6
occurrences dark, `#CECCF6` → 6 light).

### Phase 4 — ✅ hybrid data access (store seam + HTTP mode) — done 2026-08-14

Landed as specified: `tui/store.go` (103), `tui/store_local.go` (103),
`tui/store_http.go` (736) new, plus 1,247 lines of tests in two new files;
321 insertions / 123 deletions across the eight existing files. `go build`,
`go vet`, `go test -race ./...` green; 130 test results in `tui/`.

**The interface is 18 methods and every one mirrors a models function the TUI
already called, with the arguments it already had.** That is what keeps
`store_local.go` a file of one-line pass-throughs with nothing to get wrong —
the seam only pays off if the default path stays trivially correct. Two
methods exist that models has no analog for:

- `ResumeSession() (*models.User, error)` — the store's chance to get past the
  login screen without a password. Local always declines.
- `ListUsernames` returning the new `ErrNoUserList` sentinel. `httpStore`
  returns it always, because "list every account" is an endpoint a shared hub
  must not have. The distinction from an *empty* list is load-bearing: an
  empty list is what puts the login screen into first-run registration mode,
  and greeting a returning user with an account-creation form against a server
  that already holds their notes is the worst available failure.

**`userGUID` is dead weight in `store_http.go` and stays anyway.** The server
scopes every query from the JWT, so all eleven note/category methods take the
argument and ignore it (`_ string`). Dropping it would give the two
implementations different signatures, which is not a seam.

**The 401 path is why `httpStore` holds the password in memory.** A JWT
expires; a TUI can sit open for days. One silent re-login with the
credentials from the last successful login (falling back to the environment),
then retry — once, never a loop, because a wrong password would otherwise be
re-sent forever. On failure the *original 401* is reported, not the re-login
error: "unauthorized" is the accurate description of what happened to the
user's action. `TestExpiredTokenTriggersOneSilentRelogin` asserts the count,
not just the recovery.

**The probe requires the envelope, not a 200.** Port 8444 can be held by
anything, and guessing wrong starts the TUI in HTTP mode against a stranger.
`ProbeServer` demands `success: true` and `data.status == "ok"`; the test
table lists five services that would pass a laxer check, including a JSON API
answering `{"status":"healthy"}`.

**A bug the seam surfaced.** `models.AuthenticateUser` reports bad credentials
as `(nil, nil)`. The old `loginCmd` only checked `err`, so a typo left `busy`
set and the screen sat on "Signing in..." ignoring every subsequent key.
Writing `fakeStore.AuthenticateUser` to match the real contract made it
obvious. Fixed and pinned by `TestBadPasswordClearsTheBusyFlag`.

**Tests (1,247 lines, two files).** `fake_store_test.go` is an in-memory
`Store`; `storeFixtures()` reruns the boot flows against both it and the local
store, so a screen that ever reaches around the Store back into `models.*`
fails the fake run — there is no database open at all. It also has a
`failWith` hook, which is the only sane way to produce a storage failure on
demand. `store_http_test.go` runs the real `httpStore` against a real
`httptest.Server` backed by a `fakeStore`, so round trips are genuine: a note
created over HTTP is one the next GET returns. That is what lets
`syncNoteCategories` — the one piece of real logic in `commands.go` — be
exercised across the wire, over six endpoints.

**Verified past the suite**, same pty harness as Phases 2–3, against a scratch
server on port 18444 and a scratch data dir. Three runs on the *same* data
directory with the *same* command, differing only in what was available:

| run | mode | login screen | API calls the server logged |
|---|---|---|---|
| server up, env credentials | HTTP | flashes at byte 423, gone by 1760 | `health`, `auth/login`, `notes` |
| server up, cached token only | HTTP | **never renders** (count 0) | `health`, `auth/me`, `notes` |
| server stopped | local | prefilled `phase4`, password typed | — (probe refused) |

The cached-token run is the cleanest: `/auth/me` answers faster than the first
paint, so browse *is* the first frame. A login round trip does not, which is
why the env-credential run shows the login card briefly. Both are correct; the
difference is worth knowing before someone reports the flash as a bug.

Note for the next phase: `gonotes serve` is **not** a subcommand — serving is
the default action, so `gonotes serve -d <dir>` silently ignores `-d` and
opens `~/.gonotes`. The invocation is `gonotes -d <dir> -p <port>`.

### Phase 5 — ✅ cats transport + hooks + host identity (the phone-push win) — done 2026-08-14

Landed as specified. 1,885 lines of new `cats/` package (source + its four
ported test files), 505 lines of `tui/cats_glue.go`, 746 lines of TUI tests in
two new files, 113 insertions / 6 deletions across three existing files.
`go build`, `go vet`, `go test -race ./...` green; the TUI suite stable over
three consecutive runs.

**The Phase 2-4 suites needed zero changes, which is the Tier-0 proof.**
`newAppModel`'s signature is unchanged: it constructs an inert `catsState`
whose zero value is "not in cats, nothing connected", and detection happens
only when `Run` calls `init` on it. Every test in the package therefore
constructs the model with no host at all, and passes.

**The probe is a `tea.Cmd`, not a goroutine — and that is a deadlock fix, not
a style choice.** The plan said "probe/subscribe goroutine". Bubble Tea's
`Program.Send` *blocks* while the program has not started yet, and is a no-op
only once it has terminated. A goroutine started before `p.Run()` that posted
into that window — and then a stream reader doing the same — would make
`close`'s `stream.Close()`, which waits for its reader, hang forever on a TUI
that failed to start. A Cmd cannot run until the event loop already is, so
nothing downstream of the probe (including the subscription, opened from
`ready` on the loop) can ever reach the blocking window. `cs.send = p.Send` is
still set before `p.Run()`, as planned; only the trigger moved.

**`close` runs after `p.Run()` returns even when Run failed**, because `init`
claimed the pane before the program existed. The error return is deferred
until after the release for exactly that reason.

**Only the external editor is a reported span.** cats turns a working→idle
edge into a "finished" toast and a phone push, so a span is a claim that the
user might reasonably have walked away during it. The form's save — one bytdb
write, or one POST — resolves faster than the badge would render; reporting it
would be how the channel earns being muted. The editor, where the user is in
another program for as long as they like, is the one that qualifies. The span
opens inside `openEditorCmd` rather than at its call site, because only that
function knows an editor is actually going to run: the temp-file write can
fail, and a pane badged "editing" for an editor that never launched would stay
badged until the next transition.

**Events are subscribed to and dropped.** `frame` has no consumers until Phase
6 (theme) and Phase 7 (pane cache). Subscribing now is deliberate: it is what
puts the handshake and the shutdown ordering under test before two more phases
are built on top of them. The pane-cache half of the planned handshake test
("prime cache") is correspondingly absent — there is no picker yet — so the
test pins probe → resolve → subscribe → release.

**The `capture` verb landed with the client**, ahead of its Phase 7 consumer,
with `ansi` and `unwrap` both deliberately off (a note stores markdown, so VT
styling would arrive as escape noise and unwrapping would rewrap prose to the
pane's width) and a 5s timeout rather than the default 3s, because capture is
not a local answer — cats forwards it to the cathost daemon.

**Verified past the suite, on a real pty**, with a scripted cats host: a fake
control socket answering ping / pane.list / events.subscribe, and a fake hook
socket recording every report. Two runs of the real binary:

| | control methods | hook reports |
|---|---|---|
| launch, sit at login, ctrl+c | `ping`, `pane.list`, `events.subscribe` | `idle` claim → `release` |
| register → new note → ctrl+e (3s editor) → ctrl+c | same | `idle` → `working "editing: Recipes"` → `idle` (+3.26s) → `release` |

The window title tracked it live: `GoNotes` → `editing: Recipes — GoNotes` →
`GoNotes`. OSC 7 was emitted once, before the alternate screen. A
`theme_changed` frame and a deliberately unknown frame were both delivered on
the stream and dropped without incident.

**Two traps this cost, both pre-existing and neither a Phase 5 bug:**

1. **An empty `GONOTES_URL` does not mean "no server".** `ServerURL()` falls
   back to `DefaultServerURL`, so the first smoke runs came up in HTTP mode
   against the live MacApp server on 8444 — which is why a fresh data dir
   never showed the first-run registration screen (`ErrNoUserList`, correctly,
   does not enter registering mode). Force local mode with a dead port, e.g.
   `GONOTES_URL=http://127.0.0.1:9`. Confirmed pre-existing by reproducing it
   on the Phase 4 binary.
2. **`gonotes -d <dir> tui` silently ignores `-d`.** The `tui` command declares
   its own `--dir` with the same default, and command-level flags win on
   lookup — so the global form resolves to the command flag's *default*, i.e.
   `~/.gonotes`. The working form is `gonotes tui -d <dir>`. (Sibling of the
   Phase 4 `gonotes serve` trap: serving is the default action, not a
   subcommand.)

Original spec:
- New `cats/` package: `detect.go`, `client.go` (+ the `capture` verb),
  `hooks.go`, `events.go`, each with `_test.go` — hand-copied from
  `../dbc/cats/` with dbc's hard-won test conventions (short
  `os.MkdirTemp("", "g")` socket paths — macOS 104-byte `sun_path`;
  `Decoder.UseNumber` for the clock-seeded seq).
- New `tui/cats_glue.go` — `catsState`, typed messages, `catsInit`/`close`,
  reporter transitions, title builder, OSC 7 `ttyWrite`. The goroutine rule
  stated at the top of the file.
- Edits: `tui/tui.go` (`Run` wiring: init before `NewProgram`, `cs.send =
  p.Send`, probe/subscribe goroutine, close after `Run` returns; root
  `Update` cats cases; root `View` sets `v.WindowTitle`),
  `tui/commands.go` (`openEditorCmd` working-span), `tui/form.go` (busy-flag
  transition hooks).
- Tests: fake-socket suites ported wholesale; a handshake-pinning test
  (probe → resolve → subscribe → prime cache) against one fake control
  socket (dbc's `TestCatsInitCompletesTheTier1Handshake` pattern); teatest
  editor-span `working("editing: t") → idle` against a scripted hook socket.
  Tier-0 inertness proven by the untouched Phase 2-4 suites. ~1,900 lines
  (mostly copied tests).

### Phase 6 — ✅ host theme sync — done 2026-08-14

Landed as specified, plus one guard the plan did not anticipate. `tui/catstheme.go`
(209) and `tui/catstheme_test.go` (416) new; 50 insertions / 9 deletions across
`tui/tui.go`, `tui/cats_glue.go`, `tui/catsinit_test.go`, `go.mod`. `go build`,
`go vet`, `go test -race ./...` green; TUI suite stable over three consecutive
runs.

**The terminal's own background report would have undone the sync, on the first
repaint.** Phase 3's `tea.BackgroundColorMsg` handler installs
`DefaultPalette(msg.IsDark())` — and inside cats the OSC 11 answer comes from
cats' own emulator, reporting the background the host theme just supplied. So
the reply to a query the program itself issues at startup would trade the full
host palette for the built-in one chosen by a single bit derived from that same
background. `hostThemed` (set on any successful synthesis, read through
`hostOwnsThePalette`) makes the host outrank the terminal. This is a
gonotes-specific hazard — dbc has no equivalent because tview never asks.

**`Palette.Dark` is derived from the background's luminance, not read.**
`config.get`'s theme registry does carry a per-theme dark flag, but
`theme_changed`'s payload is the effective appearance alone — no flag, no
registry. Reading the authoritative value on the one path that has it would let
a live theme change and a fresh launch on the same theme disagree, and the
symptom would be widget style sets that depend on how the session reached its
current theme. Rec. 709 luma on the sRGB values as they come, midpoint
threshold — the same non-linear arithmetic `blendHex` already uses, for the
same reason.

**The startup fetch runs before `newAppModel`, not before `NewProgram`.** Bubbles
widgets copy their style set in at construction and `newAppModel` builds the
login screen, so a palette that landed even one step later would repaint rather
than paint. That is why `catsThemeAtStartup` repeats the env sniff instead of
reading `catsState`: the state does not exist yet, which is the whole point. It
does not ping first — a `config.get` that fails is already the negative answer.

**Seven keys, all required, all hex.** `accent→Primary  fg→Fg  bg→Bg
muted→Subtle  ok→Success  warn→Warn  err→Danger`, with `Sel` blended at the
same 0.30 the default palette uses. cats' eighth core key, `line`, is
deliberately *not* required: GoNotes draws its rules in `Subtle` and its focus
borders in `Primary`, and rejecting a theme over a key nothing reads would
refuse usable palettes for nothing. Anything non-hex (cats emits `rgba()` for
its translucent keys) abandons the whole synthesis — half the host's colors and
half ours reads as a rendering fault.

**The glamour half cost nothing**, which is Phase 3 paying out: the markdown
renderer and its output cache are both keyed on `paletteGen`, so a host theme
invalidates them by the same increment a terminal background change does. The
spec's "glamour style regenerated from the palette" needed no code.

**Verified past the suite, on a real pty**, against a scripted cats answering
`config.get` with a dark green theme and later pushing a light violet one on the
stream. Seven checks, all passing:

| | |
|---|---|
| `config.get` was the **first** control call | before `ping` — one round trip, not two |
| host green on screen at byte 325 | the first frame is already the host's |
| GoNotes' own dark accent — **never** | no flash of the built-in palette |
| its light accent — **never** | the white OSC 11 answer did not clobber |
| violet at byte 2200, 10ms after the frame | live retheme, and a Dark flip with it |

Original spec:
- New `tui/catstheme.go` (+test) — host colors → `Palette` (hex gate,
  fallbacks, sel blend), the synchronous startup fetch pre-first-frame when
  `DetectEnv().InCats`, `theme_changed` → `catsThemeMsg` → same-palette
  early-return → `setPalette` + broadcast (Phase 3 machinery); glamour style
  regenerated from the palette.
- Tests: mapping table; teatest live-retheme reading restyled output.
  ~250 lines.

### Phase 7 — ✅ capture-to-note (agent collaboration, inverted) — done 2026-08-14

Landed as specified. `tui/capture.go` (413) and `tui/capture_test.go` (472) new;
78 insertions / 20 deletions across `tui/cats_glue.go`, `tui/tui.go`,
`tui/browse.go`, `tui/keymap.go`, `tui/form.go` and three existing test files.
`go build`, `go vet`, `go test -race ./...` green; TUI suite stable over three
consecutive runs.

**The plan's one reversal: a successful probe now says something.** Phase 5 chose
silence on Tier-1-up, reasoning that a notice displacing the login screen's own
feedback would be a regression. Phase 7 is what changes that calculus — Tier 1
acquired a *door*, and a door nothing advertises is a door nobody opens. It could
not go in the browse footer: footers render in every terminal, and a permanent
"ctrl+g capture" row would advertise a key whose only answer in a plain shell is
that the feature is unavailable. So the hint is one status line at Tier-1-up,
where it is true by construction. The Phase 5 concern turns out not to bind: the
status bar is cleared by the first keypress, and login feedback only exists after
the user has typed. Observed on the pty run at byte 372 — on the login screen,
gone the moment typing started.

**Stripping an escape BYTE is worse than leaving it.** The plan said "ansi:false
+ residual control strip", and the obvious reading — drop C0 and DEL, keep tab —
is what the first draft did. It turns an *invisible* `\x1b[2K` into a *visible*
`[2K` in the note's text: the ESC goes and its payload stays. So
`stripEscapeSequences` removes sequences whole, as a scanner rather than a regexp
— a CSI ends at a byte in a RANGE (0x40-0x7E, after any run of parameters and
intermediates) and an OSC ends at BEL or ST, so "what terminates this" differs
per introducer, which a scanner states directly and a pattern only approximates.
It is deliberately not a full VT parser: DCS loses two bytes rather than its
payload, and it cannot arrive here anyway, since cats answers ansi:false by
stripping styling itself. This is the second line of defense, not the first.

**The three pane events are handled identically, and that is the design.**
`pane_agent`, `pane_added` and `pane_removed` all answer the same question —
the cached layout is now wrong — so which one arrived changes nothing, and
`pollPanes`' own 2s rate limit is what keeps a tab that opens four panes at once
from costing four round trips. `focus_changed` is subscribed but deliberately
*not* acted on: the picker does not render which pane is focused, so refreshing
on it would be a round trip that changes nothing on screen.

**`captureDone` is the one data message not routed to the active screen.** Every
other result in the package lands on whatever screen asked for it; a capture is
different because it takes seconds (cats forwards it to the cathost daemon) and
the user is free to open a note or a category meanwhile. Delivering to the top of
the stack would mean a capture the user explicitly asked for vanishing because
they navigated while it was in flight. It is handled at the root, which pushes
the form regardless of what is showing.

**The picker is hand-rendered, and that is a simplification rather than a
regression.** It holds three or four rows, no filter, no pagination — so unlike
the two bubbles/list screens it stores nothing derived from the palette and needs
no `restyle()` at all. Every color it draws with is read fresh from `styles.go`
each frame. `confirmScreen` is the precedent, and the same rule produced it.

The other decisions worth naming, each of which had an alternative:

- **`CaptureRecent` (scope 1), 200 lines.** The visible viewport alone loses the
  top of a long answer, which is exactly the thing being captured; the whole
  buffer buries that answer in the conversation that led to it, and the user has
  to delete more than they keep. `ansi` and `unwrap` stay off for the reasons
  Phase 5 wrote down when the verb landed — the plan's original
  `unwrap:true` did not survive contact with "a note stores markdown".
- **The form opens UNSAVED.** The same rule the outbound half keeps, where
  `pane.send_input` stages text without pressing Enter: what was captured is the
  agent's words, and whether they are worth keeping is the user's call.
- **No hook span for the capture.** Consistent with the save: cats turns a
  working→idle edge into a toast and a phone push, and a five-second action the
  user is watching does not qualify.
- **No picker when there is nothing to pick.** At Tier 1 with no sibling agent,
  ctrl+g answers with a status line rather than an empty modal — a dialog the
  user has to dismiss just to learn it was empty.

**Tests (472 lines).** Organized by the four seams and how each fails: the cache
offers the wrong panes, the door dials from a keystroke, the wire asks for the
wrong scope, the note arrives full of padding. The self-exclusion is tested twice
(by resolved id and by handle fallback) because GoNotes reports *itself* as the
agent "gonotes" and so appears in `pane.list` looking exactly like a target.
`TestCaptureRequestsRecentScopeWithoutAnsi` asserts on the decoded wire params,
not on the wrapper that built them. One harness note: `drainCmd` flattens
`tea.Batch`, which is not transparent — it returns a `BatchMsg` carrying the
sub-commands for the runtime — while `tea.Sequence`'s message type is unexported,
which is why the enter-to-capture path is exercised through a real program
instead. That is also why `captureDone` returns a Batch: the push and the status
line are independent, so nothing was bought by the opaque form.

**Verified past the suite, on a real pty**, against a scripted cats answering
`capture` with a padded, ANSI-styled, blank-row-wrapped buffer. The real binary
registered, pressed ctrl+g, and pressed Enter:

```
Capture from an agent pane
  codex           blocked · w1:p4
  claude          idle · w1:p9
  ↑/↓ move • enter capture • esc back
```

| | |
|---|---|
| picker rows | blocked ranked first; the plain shell (`w1:p3`) and our own pane absent |
| wire | `capture {"pane":4,"scope":1,"lines":200}` — no `ansi`, no `unwrap` |
| form | title `Capture: codex — 2026-08-14 20:48`, tags `capture` |
| body | 3 lines: padding, leading/trailing blank rows and `\x1b[38;5;204m` all gone, the interior blank line kept |
| hooks | `idle` claim → `release`, and nothing in between |
| `pane.list` calls | 3 — resolve, prime at Tier-1-up, refresh on picker open |

One trap re-encountered rather than discovered: the smoke harness' socket
directory has to be short. `AF_UNIX path too long` from a scratchpad path is the
same 104-byte `sun_path` limit the test suite's `catsSockPath` works around, and
it reads like a permissions failure.

Original spec:
- New `tui/capture.go` (+test) — `agentPickerScreen`, pane cache + rate
  limit, `captureCmd`, prefilled form push.
- Edits: `tui/keymap.go` + `tui/browse.go` — the ctrl+g door; one Tier-1-up
  log hint advertising it. ~300 lines.

### Phase 8 — ✅ ⌘ accelerators — done 2026-08-14

Landed as specified. `tui/metakeys.go` (232) and `tui/metakeys_test.go` (347)
new; 47 insertions across `tui/tui.go`, `tui/browse.go`, `tui/categories.go`,
`tui/form.go`, `tui/login.go`, `tui/confirm.go` and `.gitignore`. `go build`,
`go vet`, `go test -race ./...` green.

**The plan's one addition: every chord needs TWO twins, not one.** The plan said
"translate claimed chords to their existing twins and re-dispatch", which reads
as one keystroke per chord. It cannot be: GoNotes spends unmodified letters on
commands in the list screens (`e` edits, `f` flags, `d` deletes) and on content
everywhere text is entered, so ⌘E means the `e` that opens the form on the note
list and the `ctrl+e` that opens `$EDITOR` on the form — the same verb, one level
down. So each row carries a command-mode twin and a typing-mode twin, the latter
allowed to be *empty*, which is the instruction to swallow. Which applies is
decided per keystroke by a new `texter` interface (`takingText() bool`), sibling
of `refresher`/`restyler`: the form, login and prompt screens answer true
unconditionally; the two list screens answer true only while their fuzzy filter
prompt is up. Not implementing it is command mode, which is the safe default — a
new screen has to opt IN to having its keystrokes protected, and forgetting costs
a chord that does nothing rather than one that types.

**The hazard is in the translation, not the fall-through — which reverses dbc's
reasoning for the same code.** dbc swallowed unclaimed ⌘ chords because a chord
that fell through "would reach the focused widget as a bare letter and type it".
Under v2 that is no longer true: `key.Matches` compares `Key.String()`, which is
`"super+f"` and matches no binding, and every text widget inserts `Key.Text`,
which the CSI-u decoder deliberately leaves EMPTY for any modifier above shift
(decoder.go, "we need to clear the text if we have a modifier key other than a
ModShift key"; the Windows console path guards it the same way). An unclaimed
chord is already inert. What is *not* inert is the twin this layer synthesizes —
a real printable keystroke, `Text` and all, because it has to be
indistinguishable from the user pressing the key. So ⌘D translated to a bare `d`
without the mode check does not do nothing during a search: it appends a `d` to
the search box. That is the one reachable bug here, and it is what the typing
column prevents. The swallow stays anyway, demoted from repair to guarantee: it
makes "a ⌘ chord stops here" a property of this file rather than a coincidence of
three upstream implementation details.

**No arming gate, and now there is a reason rather than an assumption.** dbc
needed one because a terminal set to Option-as-Meta produced the same tcell
`ModMeta` as ⌘, so `⌥e` could have fired an accelerator. v2 has no such
ambiguity: Option-as-Meta arrives ESC-prefixed and decodes to `ModAlt`, while
`ModMeta`/`ModSuper` come only from the kitty modifier bits (32 and 8) — so a
chord can only arrive from a host that speaks the protocol and chose to forward
it. Both mods are matched anyway; the two names for the same physical key differ
by terminal and matching the unexpected one costs nothing.

**Table shape.** `⌘S`→`ctrl+s` (both modes), `⌘G`→`ctrl+g` (both), `⌘E`→`e` /
`ctrl+e`, `⌘F`→`f` / swallow, `⌘D`→`d` / swallow, `⌘/`→`/` / swallow. `⌘/`'s twin
binding comes from `list.DefaultKeyMap().Filter` — the fuzzy filter belongs to
bubbles, not to our keymap, and the invariant test needs a real binding to check
against. `metaChord` prefers `BaseCode` over `Code` for the same reason
`Key.Keystroke` does: it is the PC-101 physical key, which is what cats matched
on (`KeyboardEvent.code`) when it decided to forward the chord, so an AZERTY
keyboard resolves the same row. Shifted forms are claimed but not translated.

**Verified on a real pty**, Tier 0, against the built binary with kitty CSI-u
sequences at modifier 9 (1 + the super bit) — the exact bytes cats' input encoder
and Ghostty emit. Register → create a note → ⌘E opened `Edit: Alpha`; ⌘F/⌘D/⌘P on
the form drew nothing and left the title `Alpha`; ⌘G answered "Capturing an agent
pane needs cats — GoNotes is running standalone" (the Tier-0 door, so the chord
reached `keys.Capture`); ⌘F on the list flagged the note (`⚑ Alpha`); ⌘/ opened
`Filter:`; ⌘D during that search opened nothing, and a subsequent `z` left the box
reading `z` rather than `dz`. 10/10.

One trap re-encountered: **a pty smoke test must force local mode**. The MacApp's
embedded server answers on `localhost:8444`, so `gonotes tui -d <tmpdir>` probes,
finds it, and comes up in HTTP mode against the *real* notes database — the temp
directory is silently irrelevant. `GONOTES_URL=http://127.0.0.1:1` is the fix.

### Phase 9 — ✅ plugin manifest + cats-side seat — done 2026-08-14

- New `../gonotes/cats-plugin.toml`, on the `../cats-todo/cats-plugin.toml`
  template. Validated through cats' own `plugin.LoadManifest`, not by eye.
  `[[build]]` → `mkdir -p bin && go build -o bin/gonotes .` (run and confirmed;
  `/bin/` added to gonotes' `.gitignore`). Two actions: `tui` first, so a bare
  `plugin run` opens the TUI, and `serve` second for the web server.
- Edit `cmd/catway/web/index.html` `AGENT_HUE`: `gonotes: 4,`.

**The `-d` flag the plan specified is deliberately absent, and specifying it
would have been a bug.** The plan said the action should be
`./bin/gonotes tui -d ~/.gonotes` — an absolute default dir, because an action's
argv[0] is anchored to the plugin root while the pane it opens has the *user's*
project as its cwd, so a cwd-dependent data directory would put different notes
behind every tab. The reasoning is right and the flag is the wrong way to get it:
this argv is exec'd directly, with no shell to expand the tilde, so it would
create a directory literally named `~`. It is also unnecessary — `gonotes tui`
already defaults `--dir` to `$HOME/.gonotes` via `os.UserHomeDir` and chdirs into
it before anything opens (confirmed: `--help` prints the resolved absolute path).

**The FNV fallback was checked rather than assumed, as the plan asked, and it is
what justifies the entry.** Hashing "gonotes" lands on slot **1** — claude's. A
note-taking pane sits beside a claude pane more often than beside anything else,
since capturing that pane's output is what it is for, so this is exactly the
collision the seating chart exists to prevent. (For the record the same check on
"dbc" gives 5 and "ced" gives 6, matching what the existing comment claims.)

## Verification

- Phases 0-1: counts (exported files vs notes, imported vs exported);
  spot-check a private and a categorized note; old `.ddb` untouched; full
  suite green post-merge.
- Per code phase: `go vet && go test -race ./...` in gonotes; the
  pre-existing suite untouched at Phase 5 proves Tier-0 inertness (dbc's
  proof pattern). Real-binary pty smoke against a scripted hook + control
  socket before calling Phase 5 done (this caught dbc's only post-suite bug).
- Manual, live against the running catway (end state): launch gonotes in a
  pane → sidebar shows `gonotes idle`; open a note in `$EDITOR`, wait, quit
  → `working` badge → "finished" notification; theme change in cats →
  gonotes repaints incl. markdown preview; ctrl+g from browse → agent rows →
  capture from a sibling claude pane lands in the form; ⌘S/⌘E/⌘G from
  browser-cats and a bare Ghostty; `modes.kitty` registered (the set-form
  question); gonotes appears in the ⌘K palette and launches via
  `catctl plugin run <id> tui`; with the GoNotes server running, the same
  pane comes up in HTTP mode with no DB conflict.
