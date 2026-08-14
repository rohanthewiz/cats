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
- **Server-side decryption over the API**: whether GET note decrypts private
  bodies when the server holds the key (web UI implies yes). If not, HTTP
  mode shows ciphertext for private notes and must say so (Phase 4).
- ~~**Sync hub caution** (Phase 1): md import records fresh change entries for
  every note; a live hub will receive a full push, reconciled by GUID
  (delete-wins then LWW).~~ Moot — the cutover went via `scripts/migrate`,
  which records no change entries, and there are no peers. See Phase 1.
- **Probe false-positive**: some other service on 8444 — the health probe
  requires the gonotes envelope shape, not just a 200.
- bubbles v2 list/textarea behavioral nuances were audited by signature
  only — the Phase 2 teatest suite is the net.
- Two-pane preview re-render cost — render from a cache keyed by note id,
  only on selection settle.

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

### Phase 2 — Bubble Tea v2 migration (mechanical, behavior-identical)
- `go.mod`: drop the four charm v1 deps, add the four `charm.land/*/v2`
  (comment the import-path gotcha). Edits across all 9 `tui/` files:
  `KeyMsg`→`KeyPressMsg`, `" "`→`"space"`, root `View() tea.View{Content,
  AltScreen: true}` (drop `tea.WithAltScreen()`), viewport
  `New(WithWidth,WithHeight)` + `SetWidth/SetHeight`, textinput `SetWidth`,
  glamour `WithStyles(styles.DarkStyleConfig)` interim (palette-driven in
  Phase 3).
- New `tui/tui_test.go` — the TUI's first tests, teatest/v2
  (`NewTestModel` + `WithInitialTermSize`): boot → login renders; seeded
  temp DB → browse renders; **a key-name regression test pinning every
  binding string the six files use** (makes future key-name drift visible).
- ~150-line diff + ~150 test lines.

### Phase 3 — conformance revamp
- New `tui/keymap.go` — `key.Binding` keymap replacing the string switches;
  list help footers feed from it. New `tui/palette.go` — `Palette`,
  `Default(isDark)`, `setPalette`; root handles `tea.BackgroundColorMsg`.
  New `tui/markdown.go` — glamour renderer cached on (width, palette).
- Edits: `styles.go` (vars from palette), `form.go` (measure chrome with
  `lipgloss.Height` instead of `height-14`), `browse.go` (rune-safe
  `FilterValue`; **two-pane wide layout** — at width ≥ 100 the right pane
  renders the selected note's markdown preview; bodies are already loaded),
  `detail.go` (cached renderer), `tui.go` (`paletteChangedMsg` broadcast +
  `restyler` interface).
- Tests: goldens narrow/wide; palette-swap asserting delegate restyle;
  tiny-size form layout. ~450 lines.

### Phase 4 — hybrid data access (store seam + HTTP mode)
- New `tui/store.go` (interface), `tui/store_local.go`, `tui/store_http.go`
  (+ tests against an `httptest.Server` speaking the envelope; token-cache
  tests under a temp HOME).
- Edits: `tui/commands.go` (`models.*` → `sess.store.*`; `syncNoteCategories`
  logic unchanged), `tui/tui.go` (`Run(st Store)`), `tui/login.go`
  (HTTP-mode degradations: env prefill, cached-token skip straight to
  browse), `main.go` `runTui` (health probe, conditional `InitDB`).
- The seam is the test win: all teatest flows rerun against a `fakeStore`
  with no DB. ~700 lines incl. tests.

### Phase 5 — cats transport + hooks + host identity (the phone-push win)
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

### Phase 6 — host theme sync
- New `tui/catstheme.go` (+test) — host colors → `Palette` (hex gate,
  fallbacks, sel blend), the synchronous startup fetch pre-first-frame when
  `DetectEnv().InCats`, `theme_changed` → `catsThemeMsg` → same-palette
  early-return → `setPalette` + broadcast (Phase 3 machinery); glamour style
  regenerated from the palette.
- Tests: mapping table; teatest live-retheme reading restyled output.
  ~250 lines.

### Phase 7 — capture-to-note (agent collaboration, inverted)
- New `tui/capture.go` (+test) — `agentPickerScreen`, pane cache + rate
  limit, `captureCmd`, prefilled form push.
- Edits: `tui/keymap.go` + `tui/browse.go` — the ctrl+g door; one Tier-1-up
  log hint advertising it. ~300 lines.

### Phase 8 — ⌘ accelerators
- New `tui/metakeys.go` (+test) — chord table, super/meta fold, swallow
  unclaimed, the "nothing ⌘-only" invariant test. Wired as the first branch
  in root `Update`. ~150 lines.

### Phase 9 — plugin manifest + cats-side seat
- New `../gonotes/cats-plugin.toml` (cats-todo template).
- Edit `cmd/catway/web/index.html` `AGENT_HUE` (~:1709): `gonotes: 4,` with
  a one-line comment. (cats-mobile rev pin follow-up if needed.)

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
