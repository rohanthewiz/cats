# dbc × cats: Native Integration (full scope)

*Mirror of the ced × cats integration (`../ced/ai_docs/cats-native-plan.md`), applied
to dbc (`../dbc`). Written 2026-08-14. This document is both the plan and the
running status record — phases get checkboxes as they land.*

## Context

ced was integrated with cats via a phased roadmap: a stdlib-only transport package
of hand-copied wire mirrors, a glue layer obeying an events-only threading rule, a
hook reporter that lets the app page the user, host-theme sync, and a ⌘ layer — all
behind a capability ladder where **Tier 0 (any terminal) loses nothing and every
failure degrades silently**. dbc (a tview/tcell TUI database client) has *zero*
environment awareness today: no env sniffing, no OSC emissions, no sockets. This
plan replicates the ced integration in dbc at full scope: transport + hook reporter
+ host identity + host theme + remote-safe clipboard + agent collaboration + ⌘
accelerators, plus a one-line cats-side polish.

The flagship payoff: a 4-minute query's `working → idle` edge becomes a cats
"finished" badge/toast/phone push. Walk away; your phone tells you the query
finished.

**Key architectural translation:** dbc has no custom event loop —
`tview.Application.Run()` owns it, and background→UI handoff is
`QueueUpdateDraw(closure)`. ced's "EVENTS ONLY / PostEvent" rule becomes: *cats-layer
goroutines may touch only `a.catsPost` and values they were handed; every App field
access happens inside the posted closure.* No invented event type — closures already
carry what ced's `catsEvent.kind` enum had to encode.

**Verified windfalls (checked in the module cache, they shrink the work):**
- tcell v2.13.10 already enables the kitty CSI-u protocol for XTermLike TERMs
  (`tscreen.go:332`: `"\x1b[>4;2m" + "\x1b[>1u" + …`), and cats sets
  `TERM=xterm-256color` → every dbc pane registers nonzero kitty flags, which is
  exactly cats' `cmdGoesToPane` gate. ⌘ chords arrive today as `KeyRune + ModMeta`
  in `SetInputCapture`. Zero enabling work.
- tview v0.42 has `Application.SetTitle` (application.go:226) reaching tcell's
  OSC-2 emitter; tcell pushes/pops the title stack itself on engage/Fini. Only
  OSC 7 needs a raw `/dev/tty` write.
- tcell has `Screen.SetClipboard([]byte)` (OSC 52, tscreen.go:1301);
  `SimulationScreen` exposes `GetTitle()` and `GetClipboardData()` — titles and
  clipboard are directly testable.
- `export.Render(r, f) (string, error)` already exists — headless
  `export.ToClipboard`/`sdb` stay untouched.

## Design decisions (settled)

- **Package**: top-level `cats/` in dbc (house style is flat packages; no
  `internal/` exists). Stdlib-only, hand-copied wire mirrors with ced's comment
  convention (name the upstream cats file, state why the mirror is partial).
  **Never import the cats module.**
- **Verb set** (client.go): `ping`, `pane.list` (+`ResolvePane`), `pane.focus`,
  `pane.send_input`, `chat.send`, `config.get`; `events.subscribe` in events.go.
  Not ported: split/tab.create/read/capture/wait_for_output/path.list/
  clipboard.read. Growth path: `capture`+`wait_for_output` unlock "read the
  agent's answer back"; `pane.split` unlocks "open results in a sibling dbc";
  `clipboard.read` is unneeded (dbc is write-only to the clipboard — paste
  arrives as bracketed paste). Request id `"dbc"`.
- **Hook states**: `working` + `custom_status = runTag` at end of `beginRun`;
  `idle` at end of `endRun`. No honest `blocked` today (every dbc modal is
  user-invoked; blocked = an *unbidden* question) — but port the `asking`
  plumbing + priority `blocked > working > idle` so the rule is one line for a
  future interrupting modal. Report on change only. Reporter armed **from env
  alone before the probe**; seq seeded from `time.Now().UnixNano()` (the
  server's per-source high-water mark outlives the process); `Release()` after
  `app.Run()` returns. Source/agent: `"dbc"` (unprefixed — `cats:` is reserved).
- **Tier detection**: `DetectEnv()` free/inline (`CATS_ENV=1` + non-empty
  `CATS_PANE_ID`), `Probe()` on a goroutine (ping must *answer* to set
  `Control`; hook socket only `os.Stat`'d). `Tier1() = InCats && Control`.
  Zero-value `catsState` = Tier 0; no enabled flag. Link-down never tears down
  the client. Env-only config — no `[cats]` TOML section.
- **Theme**: `theme/` consts stay (HTML exports remain dbc-branded); add
  `theme/palette.go` with a `Palette` struct + `Default()`. `ui/theme.go`: six
  tag **consts → vars**, nine `col*` vars set via `setPalette(p)`. Extract all
  inline widget styling from `build()` into `restyle()`. Startup: if
  `DetectEnv().InCats`, fetch `config.get` synchronously (bounded by
  `ProbeTimeout`, 500 ms worst case) *before* `build()` — the first frame paints
  in host colors, no flash. Live: `theme_changed` frames → same-palette
  early-return → `setPalette` + `applyTheme()` + `restyle()`. Mapping: core 7
  (`bg fg muted line accent warn err`) must be hex or the synthesis is
  abandoned; `panel`/`panel2` fall back to `bg`/`panel`; `Sel` synthesized as
  accent blended over bg at 0.30 (cats' own `sel-fill` recipe). Subscription
  must be **unfiltered** (theme_changed is session-scoped, pane 0).
- **Clipboard**: `ui/clip.go` — `clipWrite(text)`: try atotto
  `clipboard.WriteAll`; on error, OSC 52 via `a.scr.SetClipboard` (cap 1 MiB;
  over the cap return the original error — silent truncation is worse than
  visible failure). Requires `Run` to create the screen explicitly
  (`tcell.NewScreen()` + `SetScreen`) and store `a.scr`. Rewire `copySelection`
  (y/Y) and `doExport`'s clipboard branch (→ `export.Render` + `clipWrite`).
  Headless untouched by construction.
- **Agents**: one door, **Ctrl+G** (free; the capture claims R K E O T P L C Q).
  Modal list: agent panes from a cached `pane.list` (rate-limit 2 s; refreshed
  on Tier-1-up, picker open, `pane_agent`/`pane_notify`/`pane_added`/
  `pane_removed` frames; own pane excluded by handle compare; ranked
  blocked > working > idle) + a final "cats chat" row. Payload: selection else
  statement-under-cursor via `sqlsplit.IndexAt`, fenced as ```` ```sql ````;
  append the last run error if the last run failed (new `lastRunErr` field).
  Panes get `pane.send_input` **staged, submit:false** (the user reviews and
  hits Enter there — an agent should not be fired blind); chat row →
  `chat.send`. Tier 0: Ctrl+G logs one quiet explanatory line.
- **⌘ layer** (`ui/metakeys.go`): gate = `catsTier1() || metaKittyHost()` (env
  sniff: kitty/Ghostty/WezTerm; iTerm2 excluded — Option-as-Meta ambiguity).
  Table: **⌘E → export, ⌘P → history, ⌘G → agent picker** (all on cats'
  `CMD_TO_PANE` allowlist; all have Ctrl twins — nothing ⌘-only,
  test-enforced). `metaChord` folds `'E'` vs `'e'+Shift`. Pass-through never
  claimed/swallowed: `c z v k b = + - 0`. Every other armed ModMeta rune is
  **swallowed** ("⌘S didn't work" must never type an s). Wired as the first
  branch in **both** capture sections (modal + main); actions fire only from
  main.
- **Host identity** (`ui/hostident.go`, a Tier-0 feature — works in any
  terminal): title via `a.app.SetTitle`, change-key `(active, runTag)`: idle
  `"<conn> — dbc"`, busy `"<runTag> · <conn> — dbc"` (control bytes stripped —
  conn names are user config text). OSC 7 with process cwd, once at startup,
  via a `ttyWrite func(string) error` App field (nil in tests, skipped silently
  when `/dev/tty` won't open).
- **cats-side**: add `dbc: 3,` to `AGENT_HUE`
  (`cmd/catway/web/index.html:1708`) — house tools seated in order claude 1,
  ced 2, dbc 3; the FNV fallback would land dbc on crowded slot 5.

## Upstream asks

None blocking. Nice-to-haves for a later cats pass: surface `custom_status` in
cats' own UI (it is stored but unread today); a hex form of `sel-fill` in the
resolved colors.

## Risks

- tview's update channel blocks after `app.Stop()` → every cats goroutine posts
  through `catsPost`, which checks a `stopping` channel first.
- OSC 52 size caps vary by emulator → 1 MiB cap, visible failure beyond it.
- Old log lines keep their pre-retheme inline hex tags after a live theme swap —
  accepted; a "theme synced to host" log line marks the seam.
- Pre-existing and out of scope: `doExport` runs synchronously on the UI
  goroutine (ui/modals.go:32-54) — a huge export freezes the TUI; the reporter
  deliberately does not claim `working` for it (it can't — the loop is blocked).

## Status — all seven phases landed 2026-08-14

Every phase below is done, one commit each (dbc `c4be386`, `3e0db4b`,
`ad3d249`, `4cc0311`, `681bd8a`, `0682e3b`; cats `137976d`). `go vet` and
`go test -race ./...` are green in both repos, and dbc's pre-existing suite
was not modified — the zero-value `catsState` being inert Tier 0 is what let
it keep passing untouched.

**Nine findings from building it, in the order they cost time.** Each one is
a thing the plan above got wrong or did not know:

1. **The ⌘ layer needed no enabling at all.** tcell v2.13 already emits the
   kitty CSI-u push for XTermLike terminals, and cats sets
   `TERM=xterm-256color`, so every dbc pane already registered non-zero kitty
   flags with cats' emulator — the exact gate `cmdGoesToPane` checks. The
   chords were arriving all along and nothing was reading them.
2. **hostident shrank to a third of ced's.** tcell saves and restores the
   terminal's own title itself (`\x1b[22;2t` / `\x1b[23;2t`), and tview's
   `Application.SetTitle` reaches tcell's OSC 2 emitter. Emitting either
   again would push a second copy onto that stack and leave one behind. OSC 7
   is the only sequence dbc spells out.
3. **A clock-seeded seq cannot be read back as float64.** `UnixNano()` is
   ~1.8e18, past float64's 53-bit mantissa, so two consecutive seq values
   decode EQUAL through `map[string]any`. The test harness needs
   `Decoder.UseNumber`; the real server parses uint64 and was never affected.
4. **`t.TempDir()` cannot hold a unix socket on macOS.** It embeds the test
   name, and `sun_path` is 104 bytes — a descriptive test name plus the
   system TMPDIR overruns it and fails with `bind: invalid argument`, which
   reads like a permissions problem. Tests use a short `os.MkdirTemp("", "d")`.
5. **Making the palette a runtime value broke `go vet`.** A concatenated
   color tag is no longer a constant format string, so the seven `logf` calls
   that passed a finished message became "non-constant format string". They
   go to a new non-formatting `log()`.
6. **Two places kept colors that no longer followed the palette**, and only
   one was obvious. The layout containers between the panes were expected.
   The other was not: a `TextView` keeps a SECOND style for its text area,
   captured from `tview.Styles` at construction, and `pane()` never reaches
   it because it takes a `*Box`. The log pane's interior had therefore always
   been the deep surface rather than the panel its border wears — set
   explicitly now to exactly that color, so nothing looks different and
   everything follows a theme.
7. **`QueueUpdateDraw` runs the draw AFTER the closure returns.** A test that
   reads cells as soon as its closure finishes is looking at the frame before
   the change; it needs a second sync queued behind the draw.
8. **`Release()` blocks on the write, not on the server's bookkeeping.** A
   test asserting the request had been RECORDED by the time Release returned
   was asserting something Release does not promise.
9. **The AGENT_HUE seating chart was already full** (18 labels, 6 slots), so
   `dbc: 3` shares with codex — which is what every entry past the first six
   does. Verified rather than assumed: FNV would have put `dbc` on 5, beside
   cursor.

Two decisions worth recording because they are the kind that get quietly
reversed later:

- **Sending to an agent pane FOCUSES that pane.** The text is staged and
  unsubmitted, so it is inert until somebody presses Enter there; sending
  without focusing would leave the user hunting for where their question
  went.
- **Only agent panes are offered as destinations.** A fenced SQL block typed
  at a plain shell prompt is junk, so a pane cats has not identified as an
  agent is not a destination; cats' own chat panel is the other one, and the
  one that remains when there are no sibling agents.

## Phases (each shippable alone; commit per phase)

### Phase 1 — ✅ transport + detect + hooks (the phone-push win)
New in dbc (each with a `_test.go` sibling):
- `cats/detect.go` — port of `ced/internal/cats/detect.go` near-verbatim: env
  consts (`CATS_ENV`, `CATS_PANE_ID`, `CATS_CONTROL_SOCKET`,
  `CATS_SOCKET_PATH`), `Caps`, `DetectEnv`/`Probe`/`Tier1`/`ParsePaneHandle`,
  `ProbeTimeout` package var (collapsible in tests).
- `cats/client.go` — one-conn-per-call newline-JSON; reply read via
  `json.Decoder` (never a line read — large replies); the six verbs + types
  (`Pong`, `PaneInfo` with the meta fields flattened, `ConfigTheme`,
  `ConfigGetResult` subset).
- `cats/hooks.go` — verbatim port: separate socket + envelope, `Reporter`
  (fire-and-forget, clock-seeded seq allocated at call time, `clampStatus`
  32-byte rune-safe, blocking nil-safe `Release`).
- `ui/cats_glue.go` — `catsState{caps, client, reporter, asking,
  lastState/lastStatus, stopping, …}`; `catsInit()` (env detect → arm reporter
  → goroutine probe + ResolvePane → `catsPost` ready → initial report),
  `catsPost` (checks `stopping` before `QueueUpdateDraw`), `catsTier1`,
  `catsSelfState`, `catsReportNow` (change-only), `catsAfterTransition`,
  `catsAsking`, `catsClose`. The goroutine rule stated at the top.

Edits to `dbc/ui/app.go`: App struct gains `cats catsState` (~:84); `Run` calls
`a.catsInit()` after `refreshConnList()` (~:97) and `a.catsClose()` right after
`a.app.Run()` returns (~:132); `beginRun` tail → `catsAfterTransition()`
(~:407); `endRun` tail likewise (~:427); `quit()` closes `a.cats.stopping`
before `app.Stop()` (~:464).

### Phase 2 — ✅ hostident
New `ui/hostident.go` (+test): `hostIdentSync` (change-key → `SetTitle`), the
title builder, control-byte strip, OSC 7 subset port (`fileURLPath`, hostname),
`ttyWrite` field. Hooked into `catsAfterTransition` and `setActive`'s
completion closure (app.go:373-381); one OSC 7 emission at startup.

### Phase 3 — ✅ host theme
New `cats/events.go` (+test) — Stream port: one decoder for ack+events (two
decoders eat frames), no read deadline post-subscribe, 500 ms→30 s backoff
(clean end resets), `closeOnce` + `<-done`; payloads `theme_changed`,
`focus_changed`, `pane_agent`, `pane_notify`, `pane_added`, `pane_removed`.
New `theme/palette.go` (+test). New `ui/catstheme.go` (+test) — host→`Palette`
mapping (hex gate, fallbacks, Sel blend), the synchronous startup fetch in
`Run` pre-`build()` (only when `DetectEnv().InCats`), `catsThemeArrived`, the
subscription started in the Tier-1-ready handler (unfiltered).
Edits: `ui/theme.go` tags const→var + `setPalette`; `ui/app.go` `build()`
styling extracted to `restyle()`.

### Phase 4 — ✅ remote-safe clipboard
Edits: `ui/app.go` `Run` creates the screen explicitly, stores `a.scr` (the
test harness already does `SetScreen`). New `ui/clip.go` (+test) with
`clipWrite`. Rewire `copySelection` (app.go:293) and `doExport`'s clipboard
branch (modals.go:44).

### Phase 5 — ✅ agent collaboration
New `ui/catsagents.go` (+test): pane cache + rate limit, `showAgentModal`
(rows, rank, own-pane exclusion), payload composer, staged sends on goroutines
via `catsPost`. Edits `ui/app.go`: `KeyCtrlG` case (~:252), `lastRunErr` field
set in the run-completion closure, one Tier-1-up log hint advertising Ctrl+G.

### Phase 6 — ✅ ⌘ accelerators
New `ui/metakeys.go` (+test): `metaChord`, `metaKittyHost`, the armed gate,
table {e, p, g}, the reserved pass-through set, swallow-unclaimed. Wired first
in both branches of the input capture (app.go:216/:233).

### Phase 7 — ✅ cats-side polish
Edit `cmd/catway/web/index.html` `AGENT_HUE` (~:1708): add `dbc: 3,` with a
one-line comment. (Follow-up consideration: if cats-mobile needs the rev pin
updated, run its `tool/regen.sh` flow after pushing cats.)

## Verification

- Per phase: `go test ./...` and `go vet ./...` in dbc (the 78 existing tests
  must stay green — the zero-value `catsState` being inert Tier 0 is proven by
  the untouched suite).
- `cats/` package tests: fake unix-socket servers in `t.TempDir()`; probe/tier
  matrix via `t.Setenv`; hook envelope + seq monotonic across two Reporters;
  clamp at rune boundary; stream reconnect/backoff/Close-interrupts-read;
  timeouts collapsed via package vars (ced's pattern).
- `ui` tests ride the existing `newTestAppScreen`/`onUI`/`waitFor`/`press`
  harness: reporter transitions on a slow query (`working/"query"` → `idle`),
  title via `screen.GetTitle()`, live retheme read off sim-screen cells (like
  `ui/theme_test.go`), clipboard via `GetClipboardData()` after stubbing atotto
  to fail, agent modal rows + staged `send_input` payload capture against a
  fake control socket, metakeys armed/unarmed matrix + the "nothing ⌘-only"
  invariant test.
- Headless regression: `main_test.go` + `sdb`/`export` tests untouched and
  green.
- Manual, live against the running catway: launch dbc in a cats pane → sidebar
  shows `dbc idle`; run the demo slow query → `working`, cancel/finish →
  "finished" notification; theme change in cats → dbc repaints; `y` with atotto
  forced to fail → clipboard via OSC 52; Ctrl+G → agent rows; ⌘E/⌘P/⌘G from
  browser-cats (kitty-flag gate) and a bare kitty/Ghostty.

### What was actually verified, 2026-08-14

`go vet` and `go test -race ./...` green in both repos; the headless query
path re-run by hand against the sqlite demo.

Beyond the suite, the REAL BINARY was launched on a pty inside a fake cats
environment — a scripted hook socket and control socket standing in for
catway. That is what caught the one bug the tests could not: pre-creating
the screen made `EnableMouse` panic on a terminal whose `Init` failed,
because a `SimulationScreen` is usable the instant it exists and a real one
is not. The same run confirmed the pane is claimed at startup and released
on exit *even when dbc fails to start*, which is the case that would
otherwise leave a stale `dbc` badge on a pane forever.

`TestCatsInitCompletesTheTier1Handshake` now pins the whole startup
conversation (probe → resolve our pane → subscribe → prime the cache)
against one fake socket, because the per-piece tests each mocked the step
before them.

**Still needs a live catway** — a running session is the one thing a test
cannot stand in for: the sidebar actually showing `dbc`, a real "finished"
push on the working→idle edge, a real `theme_changed` repaint, and the ⌘
chords arriving from browser-cats and from a bare kitty/Ghostty. Note the
pty harness cannot substitute here: under `script` the startup conversation
stopped after `pane.list`, which the Go test proves is the harness rather
than dbc, and chasing it further would be measuring the harness.
