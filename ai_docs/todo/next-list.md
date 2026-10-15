# Next list

The project's living list of follow-ups. Sessions edit this file in place
(see `/sess-save`) instead of copying a `## Next` section forward from one
session doc to the next. A session doc's `## Next` then just records what that
session changed here (`Closed: … Raised: …`), and anything that leaves the list
shows up as a deletion in git history rather than vanishing.

Seeded 2026-09-22 by `/next-list seed` from the last 15 session docs
(`2026-0905-1911` … `2026-0922-1713`). Items first written down before that
window were dated by grepping every session doc.

## Conventions

- **IDs are permanent** (`N-001`, …) and never reused, not even after an item
  closes.
- **`raised`** is the session-doc stem the item first appeared in.
- **Age is computed, never stored:** the number of session docs since
  `raised`, with the newest doc at 0. `/next-list` works it out.
- **Value** is the payoff of doing it, not the effort:
  - `high`: something is being worked around today, or a second independent
    consumer has arrived.
  - `medium`: it blocks one named thing, or it is a visible defect nobody has
    to route around yet.
  - `low`: a gap nobody has bumped into, or contingent on something that does
    not exist.
- **Open** is what we intend to pick up next. **Roadmap** is wanted but later.
  **Non-goals** is likely never, kept so it stays visibly declined.
- **Validate** sits directly below Open: items whose remaining work is purely
  testing (a hand check in a real terminal or app, a run on real data or
  hardware, or a test to write or repair), with no product change planned. A
  check that finds a defect raises it as a new Open item; an Open item whose
  fix has landed but is unchecked moves to Validate.
- **Nothing leaves Open, Validate or Roadmap without a line in another
  section** (Closed, Non-goals, or another of the three). Moving among Open,
  Validate and Roadmap keeps the header line unchanged.
- Open, Validate and Roadmap stay in ID order. New items append to the end of
  Open (or Validate, for a check, or Roadmap, for future work) with the next
  ID.

**Next ID:** N-064

## Open

- **N-009** · raised `2026-0914-1051-ced-cats-bin-dir` · value low
  `catctl integration install shell zsh` (the OSC 133 marks that feed the
  command ledger and HISTORY) has not been run on this machine; the plain
  `shellinit` eval only covers PATH. This is a user action.

- **N-010** · raised `2026-0914-1051-ced-cats-bin-dir` · value low
  ced repo: whether the Makefile's `alt` target (installs `~/bin/ce`) stays
  is the user's call. Corrected 2026-10-13: the Homebrew half was already
  done in ced `84e60d3` (2026-09-14), and the `install.sh` half went with
  N-019's retirement of the release pipeline (ced `2176e90`), which
  deleted the script and its README section.

- **N-016** · raised `2026-0922-1301-cats-pane-input-queue` · value low
  Legacy X10 / urxvt mouse encodings are never droppable (the conservative
  choice). Fine while the browser encoder emits SGR.

- **N-037** · raised `2026-0924-1953-context-usage-warn-demotion-tool-types` · value low
  The context used/window segment on agent rows (`43k/1M`) is claude-only.
  Copilot's `events.jsonl` would need its own reader, if it records usage at
  all (`lastCopilotModel`, `cmd/catway/agentmodel.go`).

- **N-063** · raised `2026-1015-1022-buildbadge-package-and-gonotes` · value low
  Make `buildbadge/` its own Go module (a nested `go.mod`, tagged
  `buildbadge/vX.Y.Z`) so a project importing it downloads ~300 lines rather
  than the whole ~33 MB cats module, and stops inheriting cats' requirements
  through MVS. That is what raised gonotes' `rweb` v0.1.26 → v0.1.31. Cats
  itself would then need the tag (or a workspace) to edit both together, and
  the `go get cats@…` flow in cats-mobile must not see a `replace`.

## Validate

Items whose remaining work is purely testing: hand checks in a real terminal
or app, runs on real data or hardware, and tests to write or repair. No
product change is planned unless a check finds a defect, which is then raised
as a new Open item. Split out of Open on 2026-10-07; each item kept its ID
and `raised`.

- **N-001** · raised `2026-0904-1753-a-dwell-before-the-hover-card` · value medium
  Hands-on pass in a rebuilt, reinstalled Cats.app. Sessions run inside
  Cats.app, where GUI automation is blocked (TCC), so every item below has
  only been checked by tests or by reading the code. Merged from checks raised
  in separate sessions, because they share one action and one blocker:
  - hover cards: the 400ms dwell and 800ms warm window are guesses; drive both
    cards by hand (`2026-0904-1753`, `2026-0909-0011`);
  - DEC 1004: blur a catway window with a card up in a cats-todo pane and
    watch the card go (`ReportFocus` via `syncAppFocus`, `2026-0909-0011`);
  - the startup window and its boot log on screen (`2026-0910-1855`);
  - the catway restart path: kill catway once, and the window comes back with
    `WARN catway restarted` in `daemons.log`; kill it 6× in a minute, and the
    overlay's **Restart catway** recovers with panes intact
    (`2026-0914-0158`);
  - the peers dialog, gear › *peers / sync…*, clicked through
    (`2026-0916-1547`);
  - dictation: Edit ▸ Start Dictation in a Claude Code pane; text arrives on
    commit and the mic popup sits near the cursor (`2026-0918-1901`);
  - PLUGINS below AGENTS, after relaunching cats-todo, ced and gonotes through
    the plugin host so their panes carry `CATS_PLUGIN_TYPE`. Check the type
    labels, and that AGENTS says "none" when only plugins are open
    (`2026-0922-1713`). roman is linked too since
    `2026-0922-2006-roman-http-client-plugin`: its row should read `http`
    with the title `roman: <project>`, and `(done/total)` while a batch runs;
  - the perf work (`2026-0923-1437`), which needs a NEW cathost as well as
    catway (restarting a persistent cathost ends its shells): the canvas's
    row-band repaint on a busy agent pane (no stale ink around box-drawing or
    emoji, no seams at band edges, cursor never left behind), sparse diffs
    and the frame gate live (a background tab's agent keeps streaming, and
    switching to it shows its current screen at once). And the second round
    (`2026-0923-1545`): `cat` of a long file and a streaming agent scroll
    smoothly (shifted diffs), a busy pane's typing echo stays snappy (row
    cache + frame builder), the WebSocket shows `permessage-deflate` in the
    devtools handshake, the command palette's hover, and an exited pane's
    countdown ticking without the header flickering.
  - the settings screen in the app (`2026-0923-1443-settings-json-and-screen`): Cats › Settings… (⌘,)
    opens it in the front window; the **app** tab lists the saved catways,
    and renaming or forgetting one redraws the Connect menu at once; first
    launch imports `app.json` into config.json's `app` section.
  - the flag menu's **note…** row (`2026-0924-1126-flag-note-menuitem-consolidation`):
    on a workspace and on a pane, it opens the flag dialog preset to ▤ note,
    keeps any existing note text, and no "flag with a note…" row is left.
  - the paw click (`2026-0926-2108-paw-click-opens-todo-pane`): clicking a
    workspace row's paw reveals its first todo-manager pane, across tabs and
    from another workspace; a locked row answers with a toast; the row does
    not also switch workspace. The heading's paw (`2026-0926-2119-n041-global-paw-click`)
    reveals a global manager, preferring one in the viewed workspace and
    skipping locked workspaces.
  - the sidebar's section splitters (`2026-0924-1144-sidebar-section-splitters-v0.3.0`):
    drag the seam above Agents with real rows (the Panes section it used to
    pair with is gone since `2026-1007-1436-panes-dropdown-in-workspace-rows`;
    Workspaces now holds the pane rows, so size it with a few dropdowns open),
    check the grip goes inert under a folded section, and that a trackpad drag
    inside the WKWebView keeps the row-resize cursor for the whole drag.
  - the pane dropdowns on workspace rows (`2026-1007-1436-panes-dropdown-in-workspace-rows`),
    checked so far in headless Chrome only. In the WKWebView: a fresh profile
    opens only the current workspace's dropdown; the ▶ toggles without
    switching workspace, starting a drag or renaming; Alt+click opens or shuts
    every workspace (that ⌥ reaches the page as `altKey`); rows read `p1`
    while the hover card still says `cats:p1`; and a press on a row in another
    workspace reveals it. Since `2026-1008-1941-untitled-panes-hidden-from-dropdowns`:
    a bare shell with no title is left out of its dropdown while
    `todo: cats (N)`-titled and renamed panes stay; the ▶ tooltip says
    "N untitled not listed"; and ticking Settings → appearance → interface →
    *list untitled panes* brings the bare shell back at once, without a reload.
  - the dropped OSC 52 clear (`2026-1007-1407-osc52-clipboard-clear-ignored`).
    This needs a new catway only; cathost can keep running. Copy something,
    run `printf '\033]52;c;\007'` in a pane, and ⌘V should still paste it.
    `daemons.log` should gain `WARN catway: ignored an OSC 52 clipboard
    clear from pane N`. Do not try this on the old build: it empties the
    pasteboard. If "clipboard has no text" recurs with no such line in the
    log, the 2026-10-05 clear came from outside cats (a password manager's
    timed clear, say).
  - closing onto an awake workspace (`2026-1008-1429-close-lands-on-awake-workspace`).
    This needs a new catway only. With a sleeping workspace just above a
    worktree workspace at the bottom of the list, remove the worktree. The
    window should land on the nearest awake workspace, and the sleeper should
    stay asleep. Then sleep every workspace but one and close that one. The
    workspace now shown should wake with a shell, and its parked agents should
    resume.
  - the default-plugin seed in the rebuilt Cats.app (`2026-0926-2226-default-plugin-seed-cats-todo`,
    folded in from N-045, whose catway half passed live). On a machine or
    profile with no plugins directory, the first launch should log
    `catway: installed default plugin rohanthewiz/cats-todo` to
    `daemons.log`, and cats-todo should show in the plugins dialog and
    picker once they are reopened.
  - plugin update checks in the rebuilt Cats.app (`2026-0926-2211-plugin-update-checks`,
    folded in from N-042, whose browser half passed live). With a plugin
    behind upstream, the toolbar's **⧉ plugins** shows the count, and the
    dialog's **update** opens an "update <id>" tab whose clean exit closes
    it. The badge should drop by one about 30s later without reopening
    anything.

- **N-012** · raised `2026-0916-1547-peer-sync` · value low
  Cross-OS home translation in peer sync is unit-tested only; the first
  Mac ↔ Linux sync is the real test of it.

- **N-013** · raised `2026-0918-1901-dictation-text-sink` · value low
  Fix landed (`2026-1011-2344-n034-n038-n014-n013-mobile-shift-tools-tab-ime`), hand check left. While a composition runs, the
  text sink is shown as the preedit (`showPreedit` in `42-textsink.js`). It
  sits on the cursor cell in the terminal font, scaled with the pane's
  zoom, on the terminal background with an accent underline, and is resized
  on every revision. It goes back to 1px and transparent on
  `compositionend`. The sink now sits on the cursor row's top edge rather
  than under it, so it can cover the cell. Checked in Chrome only with
  synthetic composition events: "かんじ" drew right after the prompt at the
  pane's font size, and on end the overlay hid and "漢字" reached the pane.
  Check by hand, in Cats.app and in Chrome, with a real IME (Japanese
  Romaji, or Pinyin): the marked text and its clause underlines show at the
  cursor while composing; the candidate window sits just under the cursor
  row; arrow keys move within the composition; committing types it into the
  pane once. Also check dictation (Edit ▸ Start Dictation): the hypothesis is
  readable while it is being revised, and the mic popup still sits near the
  cursor.

- **N-014** · raised `2026-0918-1901-dictation-text-sink` · value low
  Fix landed (`2026-1011-2344-n034-n038-n014-n013-mobile-shift-tools-tab-ime`), hand check left. `onKey` (`20-keys.js`) now
  returns on a keydown whose key is "Dead" without preventDefault, so the
  host composes the accent into the text sink and the committed "é" arrives
  as a paste. The keyup of a key whose keydown went to a composition is
  swallowed (`composedCodes`), so a kitty-protocol pane never gets a release
  without a press. Covered by `deadkeys.test.mjs` only. Check by hand, in
  Cats.app and in Chrome, on a US layout: ⌥e then e types "é" at a shell
  prompt and in a kitty-protocol editor (ced); ⌥u then u types "ü"; ⌥e then
  space types "´"; and plain typing and ⌥-chords an app binds (⌥b/⌥f in
  zsh) still work.

- **N-054** · raised `2026-1010-1836-pane-header-recent-agent-prompts` · value medium
  Hands-on check of the pane header's **prompts ▾** dropdown with a real
  claude pane, in Cats.app as well as a browser. It was verified only in a
  scratch catway with a stand-in `claude` binary and a fixture history. Check
  that the rows match what was actually typed, including a prompt sent by
  cats-todo through `pane.send_input`. Check that a click copies through the
  WKWebView clipboard bridge (`catsClipWrite`) and that the tooltip shows the
  full text. Also check a narrow pane: the chip is the last field and
  `flex:none`, so once the title and path have shrunk it is the first thing
  the header clips. If that hides it on ordinary widths, it may want to move
  before the agent name or into the toolbar.

- **N-056** · raised `2026-1011-1617-agent-session-origin` · value medium
  Hands-on check of session origins in a real Cats pane, read with
  `catctl pane list`. Check each of: a fresh `claude` (→ `startup`), `/clear`
  (→ `clear`), `/resume` inside the running claude (→ `resume`), and
  `claude --resume` from the shell (→ `resume`). Check both without the hook
  integration (detection only) and after re-running
  `catctl integration install` to get the v6 claude hook. With the hook,
  confirm the model readout and **prompts ▾** follow the pane into the new
  conversation after a `/clear` (they used to stay on the old one). Also
  check the blank dot: a fresh claude and a `/clear` show the light-brown dot
  ("new" / "cleared") in the agents list, header and workspace badge, and it
  turns to working the moment a prompt is sent. Then check the origin after
  the first prompt (`2026-1011-1949-session-origin-after-first-prompt`): the pane header reads
  "started / cleared / resumed N ago", the AGENTS row's tooltip carries the
  same phrase under the model, and the workspace dropdown's hover card has a
  "Session" row. The age should match the launch (claude's own `startedAt`)
  or the /clear, and survive a catway restart. Only unit-tested so far, plus
  a classifier run against real transcripts on this machine and scratch
  catways with stand-in claude processes for the dot and the origin.

- **N-061** · raised `2026-1015-1022-buildbadge-package-and-gonotes` · value low
  The cats side of the build badge now mounts through the shared package
  (`BuildBadge.mount` in `js/10-buildbadge.js`, with `armTip`/`showTip` as
  its show/hide hooks), and nobody has looked at it in a real page. In a
  stamped catway (`make binaries`) or Cats.app: the hash sits beside the
  wordmark in the `small.hash` style, the card opens only after the
  sidebar's dwell (and on contact within the warm window), shows Build /
  Commit / When with "· Nh ago", and closes on leave, a press, a key and
  window blur. Covered so far only by the package's node test, the
  page-injection Go tests and a headless render of gonotes.

- **N-062** · raised `2026-1015-1022-buildbadge-package-and-gonotes` · value low
  The gonotes rollout of the badge (gonotes `619126f`). Restart the live
  gonotes server from a stamped build and relink the cats plugin
  (`catctl plugin link .`, since `bin/gonotes` only refreshes then), then
  check the badge beside the GN logo in the real UI and the Mac app. While
  there, check that the restarted server holds `~/.gonotes/data/*.bytdb.lock`.
  The running one (an Oct 12 `go run` build) had the databases open without
  the lock, so a second gonotes could open them at the same time.

## Roadmap

Wanted, but not next. Items move here from Open (or straight here when raised
as future work) and back to Open when they become next, with their header line
unchanged.

- **N-055** · raised `2026-1010-1836-pane-header-recent-agent-prompts` · value low
  The header's **prompts ▾** list can only read claude and copilot histories
  (the `prompts` field of `modelResolvers` in `cmd/catway/agentmodel.go`).
  Every other agent pane shows the chip and opens a note saying cats cannot
  read its history. A reader for codex (and the other agents in the AGENT_HUE
  table) is one table entry each, once their on-disk history format is known.
  The same entry would also give those panes a model in the header.

## Non-goals

- **N-022** · declined `2026-0905-1938-plugin-panes-in-the-agents-section` —
  Dropping the trailing rule under the last plugin block. Every block is
  closed by a hairline, the last one included, because the request said
  "after each group". It is one line in `renderAgents` / `appendPluginBlocks`
  if it ever reads as unfinished.
- **N-023** · declined `2026-0909-0011-warm-window-and-sticky-cards` — A
  per-front-end warm constant. The two front ends keep the same timing on
  purpose (one feature, one timing to learn). If they ever need to diverge,
  that is a finding about one front end, not a knob to add ahead of it.
- **N-024** · declined `2026-0911-1345-workspace-sync-dot` — Making the
  workspace dot report the workspace's own branch. It reports the trunk on
  purpose: the pane header already names the branch, and the dot is about the
  shared mainline. A long-lived feature branch therefore shows its project's
  staleness, not its own.
- **N-025** · declined `2026-0914-0158-catway-restart-cats-todo-release` —
  Restarting cathost automatically. Its exit takes the panes with it, and
  relaunching it would hide that loss. The overlay tells the user to quit and
  reopen instead.
- **N-008** · declined `2026-1011-2349-n043-n008-linked-hint-and-run-end` — Removing `waitReady`
  (`cmd/catapp/supervise.go`). It still has no caller, since it is only
  `waitReadyOrExit(…, nil)`. But it is three lines that document the plain
  "wait for the port" case beside the variant that also watches the process,
  and the standing rule is not to remove code unless it is clearly in the
  way. The item named Non-goals as the default if nobody minded.

## Closed

Closures before this file existed live in the session docs' own write-ups.
Newest first. The unnumbered entries at the end were found done while seeding,
so they are not carried.

- **N-020** · raised `2026-0922-1713-plugin-types-and-releases` ·
  closed 2026-10-14, cats-mobile `2026-1014-1554-n020-plugin-panes-split-by-type` (85ebdec). `Session.SplitPlugins` divides
  the rollup's plugin panes on `wire.IsAgentPluginType`, so the phone and the
  desktop route unknown types the same way. The Agents screen draws
  agent-typed plugins under an AGENT PLUGINS heading below the state groups.
  A heading of their own, not a headless block as on the desktop, because a
  block after IDLE would read as idle agents. Everything else goes under
  PLUGINS, with a hairline between plugin-id blocks. Rows show the short id,
  the terminal title, the handle and a type badge, with no state or age.
  Covered by a catsclient split test, two app journeys and the
  `agents_plugins` golden. Not run on a device.

- **N-042** · raised `2026-0926-2211-plugin-update-checks` ·
  closed 2026-10-14, `2026-1014-0728-n042-plugin-updates-checked-live`.
  Driven live against a scratch catway and cathost on 127.0.0.1:8531 with
  their own `CATS_PLUGINS_DIR`, `CATS_BIN_DIR` and `XDG_*`, so the real
  plugins were not touched. ced and cats-todo were installed there, then
  each tree was reset two commits back. The badge read `2`. The ced row's
  **update** opened an "update rohanthewiz.ced" tab, which reset the tree to
  05a180c and rebuilt the binary. The tab closed itself on the clean exit,
  and the badge went to `1` on its own 31s after the click (the +30s
  recheck). With ced reset again, ↻ brought the badge back to `2`, and
  **update all (2)** ran both updates in one "update plugins" tab
  (rebuilt 07:24:37 and :38). The badge emptied at +30s, and `catctl plugin
  check` reported both up to date. Headless-Chrome captures of the open
  dialog in cats-green (dark) and solarized-light both read cleanly. The
  2026-09-26 "light theme" check was probably the dark default seen through
  claude-in-chrome's forced inversion. The reinstalled Cats.app half moved
  into N-001.

- **N-019** · raised `2026-0922-1713-plugin-types-and-releases` ·
  closed 2026-10-13, `2026-1013-1530-n019-ced-release-pipeline-retired`
  (ced `2176e90`). Retired, not revived, which was the
  user's call. Checking found that the 15 tags since v0.2.0 had no GitHub
  Release, so `install.sh` was serving a v0.2.0 binary. ced deleted
  `release.yml`, `.goreleaser.yml` and `install.sh`. Its README, CLAUDE.md
  and AGENTS.md now describe only the hand flow (a `Release ced x.y.z`
  commit plus an annotated tag on main) and say there are no binaries. The
  cats plugin or a source build is the install. ced's own next-list:
  N-001 (the parked pipeline) declined, N-009 moot. The `release` branch
  (fully contained in main) is deleted, remote and local. The v0.2.0 GitHub
  Release is kept, by the user's choice.

- **N-015** · raised `2026-0922-1301-cats-pane-input-queue` ·
  closed 2026-10-13, `2026-1013-1516-n015-refused-input-rate-limit` (cats
  `e3551fb`). Refused input is now reported once per episode, not
  once per message. That mattered because catway turns each error event
  into a WARN line in `daemons.log` and a toast in every window. A new
  `fullReporter` per pane (`internal/orchestration/paneinput.go`) reports an
  episode's first refusal at once. It then folds later refusals into at most
  one event per 10s, worded "…; N more refused in the last 10s". An accepted
  message ends the episode. Only dispatch's refusals count: the emulator's
  query replies share the queue but drop their errors, so counting them
  could have spent a first report on an event nobody sees. Tests:
  `TestFullReporterRateLimits` (fake clock) and
  `TestHostRefusedInputReportsOncePerEpisode`. The second uses a hand-built
  pane with an 8-byte budget and no writer, sends twenty-one refused
  messages, and gets one error. It got twenty-one with the old emit put
  back. Not seen live: a real pane needs 16MB of unread input to refuse
  anything.

- **N-059** · raised `2026-1011-2327-n053-n052-n039-headings-state-words-exited-capture` ·
  closed 2026-10-12, `2026-1012-0107-n059-state-word-shades`. Every built-in
  now holds its state words at 4.5:1 or better on panel, panel2, chrome and
  chrome-focus. `TestBuiltinStateWordContrast` checks all of them; the
  `held` set is gone. Each shade keeps the base hue and is lifted until it
  passes on the focused header. Where lifting alone faded a muted hue to
  grey or dusty pink, it also took a 30% step toward full saturation. Authored:
  - cats-green (the default, so its look moves): ok `#71c780`, done
    `#6ebbff`, err `#f39e9e`; the `:root` fallback in `01-theme.css`
    matches.
  - darcula: `#94d584` / `#a5cae6` / `#ffb1af`.
  - cool-blue: ok `#aed88b`, done `#dac3eb`, err `#f2c0b8`. Lifted at
    their own hues, the purple and the red became the same pale pink, so
    done moved to a lavender (hue 311 to 275) and err to a salmon (354 to 8).
  - err only: tokyo-night `#fb9dae`, super-warm `#f19181`, dark-game
    `#ff5778`, dark-city `#ff7171`.
  The dots keep the base hues. Picked from a headless-Chrome swatch sheet of
  the candidates on each theme's chrome-focus and panel2. Not checked in the
  running app.

- **N-043** · raised `2026-0926-2211-plugin-update-checks` ·
  closed 2026-10-11, `2026-1011-2349-n043-n008-linked-hint-and-run-end`. Done without a fetch, which was the
  reason it had been left out. `linkedBehind` (`internal/plugin/check.go`)
  reads the checkout's branch upstream from `branch.<b>.remote` / `.merge`
  and asks `git ls-remote` for that ref. It adds "origin/main has moved on
  (1a2b3c4)" to the skip reason when that commit is not an ancestor of HEAD
  (or is not in the object store at all). The status stays skipped, so there
  is no badge and no update button. catway now caches and times out linked
  checks like the others. The dialog shows the note in amber ahead of the
  path, since the path line ellipsizes at its end. `runCheck` no longer
  skips a dialog whose plugins are all linked. That filter would have hidden
  the hint whenever a linked plugin was the only one installed, which is how
  the live check found it. `TestCheckUpdateLinkedBehindHint` uses real
  repos: level, upstream ahead (and `origin/main` unmoved), local ahead, and
  no upstream. Live on a scratch catway with a linked checkout one commit
  behind its upstream: `catctl plugin check` printed "skipped (linked
  checkout · origin/main has moved on (e5cd86b))", and the dialog showed the
  note with the toolbar badge empty. Docs in plugins.md.

- **N-038** · raised `2026-0924-1953-context-usage-warn-demotion-tool-types` ·
  closed 2026-10-11, `2026-1011-2344-n034-n038-n014-n013-mobile-shift-tools-tab-ime`. The settings screen has a **tools** tab
  with a key→value widget (`mapControl` in `33-settings.js`: a label box, a
  type box and × per row, plus **+ add**). Its reader refuses a value with
  no label and a label given twice (case-insensitively). `tools` joined
  `optionSections`. `applyToolTypes` (`settings.go`) makes a `types` object
  replace the map, since decoding onto it could only add, and saves a
  default left out as `""`, since the load-time merge would otherwise
  restore it. `TestConfigSetToolTypesReplacesAndOptsOutDefaults` covers
  replace, opt-out, live `TypeFor`, a reload, and refusals that leave the
  map alone. Live in Chrome on a scratch catway: the tab listed dbc and
  gonotes, and removing dbc, adding `roman → http_client` and saving wrote
  `{"dbc": "", "gonotes": "notes_mgr", "roman": "http_client"}`. A
  duplicate `GONOTES` showed "tool types: "GONOTES" is listed twice". Docs
  in configuration.md.

- **N-034** · raised `2026-0923-1545-perf-shifts-builder-deflate` ·
  closed 2026-10-11, `2026-1011-2344-n034-n038-n014-n013-mobile-shift-tools-tab-ime`. cats-mobile `a228479` pins cats at
  v0.4.0, the first tag with `wire.FeaturePaneShift`, and puts
  `plugin_notice` on its ignore list. `8c050f3` adds `PaneGrid.shiftUp`:
  it scrolls all five column slices, blanks the vacated rows in the frame
  defaults, marks every row dirty, and runs before the diff's cells. The
  handshake now lists `pane_shift`. Unit tests cover shift-then-patch and an
  oversized shift. Live: a throwaway test (not committed) connected
  catsclient to a scratch catway while a pane printed 150 lines of random
  base64. 171 of 194 diffs carried a shift, no full frame was sent during
  the run, and the grid matched catway's own capture row for row. With
  "line N" output catway sent plain diffs, since a shift has to save two
  rows of cells. Not watched on a phone, but painting uses the same
  dirty-row path that full frames always took.

- **N-033** · raised `2026-0923-1443-settings-json-and-screen` ·
  closed 2026-10-11, `2026-1011-2335-n060-n040-n033-raw-params-unpair-ui-push`. A new down message, `ui_prefs`
  (`wire.UIPrefs`), carries the `ui` section. catway sends it in the connect
  burst and broadcasts it when `config.set` or `server.reload_config` changes
  the section (`broadcastUIPrefs`, compared against the section before the
  save). The page's `applyUIPrefsPush` (`33-settings.js`) applies only the
  fields that differ from its copy, so the saving window ignores its echo
  and a late echo can't snap back a gutter being dragged. A size that
  became unset is forgotten but left on screen. Tests:
  `TestConfigSetBroadcastsUIPrefs`, `uiprefs.test.mjs`, and the wire
  round-trip table. Live on a scratch catway: a WebSocket client saw the
  burst copy, then a push after `config.set` of `ui.font_px`, and nothing
  after a panes-only save. A Chrome tab already open on the page went from a
  278px to a 340px sidebar after `config.set` of `ui.sidebar_width`, with no
  reload. Documented in browser-protocol.md and configuration.md.

- **N-040** · raised `2026-0925-1102-n011-peer-pairing` ·
  closed 2026-10-11, `2026-1011-2335-n060-n040-n033-raw-params-unpair-ui-push`. New route `POST /peer/v1/unpair`
  (`handlePeerUnpair`) revokes exactly the grant the request is
  authenticated with. The password and other non-grant credentials get a 400,
  and the guard already keeps the route behind auth. `peersync.Client.Unpair`
  calls it. `PeerDetach` reads the credential first, saves the config,
  deletes the token file, then unpairs on a goroutine and replies after the
  attempt. On failure (offline, an older peer's 404, already revoked) it
  logs the old "stays live until revoked there" reminder with the reason.
  Tests: `TestPeerUnpairRevokesOnlyThePresentedGrant` (password refused, own
  grant revoked once, another grant untouched) and `TestUnpair`. Live with
  two scratch catways: B (password auth) minted a link, A attached by it, and
  B listed grant `f349c29a`. `catctl detach-peer bpeer` on A logged "revoked
  its grant f349c29a on the peer", B logged the revocation, and B's
  `peer-grants` came back empty. With B's catway stopped, a detach completed
  and logged "connection refused; its grant on the peer stays live…".
  `docs/subsystems/peer-sync.md` and `docs/reference/cli.md` updated.

- **N-060** · raised `2026-1011-2327-n053-n052-n039-headings-state-words-exited-capture` ·
  closed 2026-10-11, `2026-1011-2335-n060-n040-n033-raw-params-unpair-ui-push`. `--params` with no operands now takes
  the raw path for a verb spelled like its own method (capture, read,
  scroll), via `resolveVerb` in `cmd/catctl/subcommands.go`. Operands plus
  `--params` is still refused, and so is `--params` on a verb whose method
  has another name (`split` → `pane.split`). The help text was already right
  once this works, so it is unchanged. `TestResolveVerbRawFormForSameNamedVerbs`
  covers it. Live on a scratch catway, `catctl capture --params
  '{"pane":1,"ansi":true}'` returned VT-styled text, and the same for an
  exited pane returned N-039's "only its last screen" refusal.

- **N-039** · raised `2026-0924-2005-no-such-pane-exited-panes` ·
  closed 2026-10-11, `2026-1011-2327-n053-n052-n039-headings-state-words-exited-capture`. `capture` of an exited pane now answers
  from catway's grid mirror (`captureExited` in `cmd/catway/catway.go`,
  `Grid.ScreenText` in `internal/browserproto`). The visible scope gets the
  last screen. The recent scope gets that screen's tail, because it is what
  `catctl capture <pane>` sends and the screen is the buffer's tail while the
  view sat at the bottom. `ansi`, `unwrap` and a recent capture of a view
  scrolled into history fail with "has exited; only its last screen is
  kept". An invalid grid (frame-gated) keeps the plain "has exited". `read`
  is unchanged. Documented in `docs/protocols/control-api.md` and
  `catctl help capture`. Unit tests in both packages. Live on a scratch
  catway: a pane ran `printf "panic: boom…"; exit 3`, and `catctl capture 2`
  returned the screen ending "panic: boom / at main.go:42", while `capture 2
  1` returned just the last line. Known limits: soft-wrapped lines stay
  split, and a wide grapheme reads with a space after it (its spacer cell).
  The help's raw-form example turned out not to work: N-060.

- **N-052** · raised `2026-0927-0024-n051-warn-fg-text-app-wide` ·
  closed 2026-10-11, `2026-1011-2327-n053-n052-n039-headings-state-words-exited-capture`. Three new theme keys, `ok-fg`,
  `done-fg` and `err-text`, colour the idle, done and blocked state words as
  text (`.st-*` in `10-panelist.css`: pane header, pane rows, palette).
  Red is `err-text` because `err-fg` was already the banner's ink, and the
  default theme's is a pale pink. They copy the base hue unless authored.
  The dots, tab marks and workspace badge keep the base hues through a
  marker rule, as `.st-working` already did. Authored values:
  solarized-light `#486200` / `#484eb3` / `#ac1f1d`, corporate `#166a43` /
  `#0a6776` / `#ad2e2e`, and solarized-dark `#b1cc00` / `#b6baff` /
  `#ffa9a8` (no full red reaches 4.5:1 on its mid-teal focused header, so
  that one is a salmon). Each word is now 4.5:1 or better on panel, panel2,
  chrome and chrome-focus; `TestBuiltinStateWordContrast` holds those three
  themes there. cats-green authors the keys at its base values, so nothing
  on it moves. A headless-Chrome render of the real stylesheets showed the
  dots unchanged and the words darkened (or, on solarized-dark, lifted). The
  test found other themes short as well, raised as N-059. The item's side
  note (solarized-light's `.high` usage reading near the `#6f5f2a` group
  heading) was not touched or re-checked.

- **N-053** · raised `2026-1007-1436-panes-dropdown-in-workspace-rows` ·
  closed 2026-10-11, `2026-1011-2327-n053-n052-n039-headings-state-words-exited-capture`. Each heading's title is now a
  `span.htitle` (`headingTitle` in `cmd/catway/web/sidebar.go`) and is the
  part that gives way: it ellipsizes, keeps a stub of about "US…", and
  carries the full word as its tooltip. Every other heading item holds its
  width. USAGE's controls alone fill a 150px column, so past the title's
  stub its age stamp ellipsizes too (`.hage`); its tooltip keeps the time.
  The fold rule now spares `.htitle` instead of relying on the title being
  a bare text node. Checked in headless-Chrome renders of a scratch catway
  with `ui.sidebar_width` set. At 150px the headings read "USA… ↻ ⊞ ⊟ ▼"
  and "WORKS… ⊞ ⊟ ▼" (before: "3s ago" wrapped, both ▼ were cut off, and
  the sidebar scrolled sideways). At 180px: "USA… 3s a… ↻ ⊞ ⊟ ▼". At 278px
  nothing changed.

- **N-045** · raised `2026-0926-2226-default-plugin-seed-cats-todo` ·
  closed 2026-10-11, `2026-1011-2316-n044-n018-n045-seed-checked-live`. Watched live through real catway starts
  from the current tree, each with `CATS_PLUGINS_DIR` pointing at a directory
  that did not exist yet. With Go on PATH, the first start logged
  `installed default plugin rohanthewiz/cats-todo v0.45.0` about 4s after
  serving, and `catctl plugin list` showed it with both actions. With
  PATH=/usr/bin:/bin, three starts logged "attempt 1, will retry", "attempt 2,
  will retry" and "not installed after 3 attempts, giving up", each with the
  `sh: go: command not found` tail, and the state file moved the entry from
  `pending` to `failed`. `catctl plugin list` showed the "could not be
  installed" line after the first failed start. A WebSocket client connected
  before the seed pass finished got `plugin_notice` with `[]` in the connect
  burst, then `["rohanthewiz.cats-todo"]` pushed 0.75s later when the pass
  failed. A headless-Chrome render showed the toolbar's "!" mark. The
  Cats.app half moved into N-001.

- **N-018** · raised `2026-0922-1713-plugin-types-and-releases` ·
  closed 2026-10-11, `2026-1011-2316-n044-n018-n045-seed-checked-live`. No longer reproduces. ced at `d8732ef`:
  `TestThemeAfterSave_RepaintsLive` passed 800 times (`-run Theme
  -count=400 -cpu 1,4`, with three busy loops loading the CPU), plus 6 runs
  of `./internal/app` and 5 of `go test ./...`, with no TempDir cleanup
  error. No writer into the themes directory was found after a save: the
  atomic save's temp file is renamed before `saveTabAt` returns, and the
  background work it starts that was read (git diff, git status) only reads.
  Reopen it if the
  "directory not empty" cleanup error is seen again, noting the leftover
  file's name.

- **N-044** · raised `2026-0926-2226-default-plugin-seed-cats-todo` ·
  closed 2026-10-11, `2026-1011-2316-n044-n018-n045-seed-checked-live`. Already done: cats-todo `56c5c4d`
  ("a headless host install leaves the backlog offer unspent") is on
  origin/main and tagged v0.42.1 (2026-09-27 00:32), and v0.42.2 through
  v0.45.0 have shipped on top of it. Checked with `git tag --contains` in
  ../cats-todo.

- **N-058** · raised `2026-1011-1949-session-origin-after-first-prompt` ·
  closed 2026-10-11, `2026-1011-1959-n058-agent-row-name-gives-way`. The
  name now gives way: `.aname` ellipsizes and `.ameta` holds its width on
  agent rows as well as plugin rows, so the state word ends at the row's edge
  ("claude o…  proj:p1 · 1m ago · idle" at 278px). The flag mark moved out of
  `.aname` to a row item of its own, so the ellipsis cannot clip it. The
  workspace half of the handle stays.

- **N-057** · raised `2026-1011-1617-agent-session-origin` ·
  closed 2026-10-11, `2026-1011-1949-session-origin-after-first-prompt`. The origin now rides on
  `wire.AgentItem` / `PaneAgent` (`origin`, plus `origin_ms` for its age) and
  outlasts the first prompt. The pane header shows "cleared 5m ago" after the
  state word, the AGENTS row's tooltip and the pane hover card carry the same
  phrase, and "resumed" shows at all. The time comes from cathost
  (`PaneAgentSession.BeganMs`, `detect.AgentSessionBegan`), so it survives a
  catway restart; a hook ref is stamped on arrival and the earlier of the two
  wins. A blank resumed conversation still reads "new" in the blank slot, but
  the hover card says "resumed".

- **N-051** · raised `2026-0927-0014-n050-warn-fg-theme-key` ·
  closed 2026-09-27, `2026-0927-0024-n051-warn-fg-text-app-wide`. The twelve
  amber text rules now use `--warn-fg`. They are the usage list's `.gsum`,
  `.gleft.soon`, `.uval` and `.ureset.soon`, `.aage.stale-idle`,
  `.st-working`, the pane header's `.info .agent` and `.info .mode`, `.rtrig`,
  `#recbtn.on.empty .n`, and the peers dialog's `.hint.warn` and
  `.row.skipped .kind`. `.st-working` also colours the state markers (the
  agent-list dots, tab marks and the workspace "●N" badge). A new rule keeps
  those on `--warn`, since the hue is their signal, and a dark
  `#694f00` dot would drift toward solarized-light's olive ok. On the three
  themes the text now reads at 4.5 to 7.2:1 (was 2.1 to 4.1:1). The floor is
  solarized-dark's focused header: `--chrome-focus` is closer to `--warn-fg`
  than `--panel2` is, so the item's premise was slightly off, but it still
  clears 4.5:1. The pane header's agent name and "working" moved together,
  and stay distinct from the bold orange branch. Checked in a headless-Chrome
  render, before and after, on corporate, both solarized themes and
  cats-green. cats-green is pixel-identical.

- **N-050** · raised `2026-0927-0000-n049-failed-default-tile-contrast` ·
  closed 2026-09-27, `2026-0927-0014-n050-warn-fg-theme-key`. A new theme key,
  `warn-fg`, is derived from `warn` (`internal/theme/theme.go`). Three
  built-ins author a darker or lighter shade: solarized-light `#694f00`,
  corporate `#734e00`, solarized-dark `#f2b700`. The plugins dialog's amber
  text now uses it: `.l2.warn`, `.pill.st.missing` and `.plg-head .chk.warn`.
  The pill's tint and border stay `--warn`. On the three themes the text now
  reads at 4.8 to 6.0:1 (was 2.0 to 3.3:1), and every other theme is
  unchanged. `TestBuiltinWarnFgContrast` holds every built-in's `warn-fg` to
  4.5:1 on `panel2`. The key is also added to the `:root` fallback and to
  docs/reference/configuration.md. Checked in a headless-Chrome render,
  before and after, on corporate, both solarized themes and cats-green.
  cool-blue's pill stays at 4.35:1, since its `warn` is already pale. Other
  `--warn` text in the app is N-051.

- **N-049** · raised `2026-0926-2351-n047-n048-script-plugins-and-failed-default-mark` ·
  closed 2026-09-27, `2026-0927-0000-n049-failed-default-tile-contrast`. The failed-default
  row's "!" tile now uses the toolbar mark's treatment: solid --warn with the
  fixed near-black ink `#1b1606` (`.row.plg.dflt .av` in `css/21-plugins.css`).
  The old tile was --warn on an 18% tint, which measured 2.2:1 on
  solarized-light, 2.7:1 on corporate and 3.3:1 on solarized-dark. The solid
  tile is 4.8:1 or better on every built-in. The tint ring was dropped, since
  it matched the fill. The row's --warn edge and tint are unchanged: they
  frame the row, and the tile is what the eye lands on. Checked in a headless-Chrome render of the real
  stylesheets under corporate, solarized-light and cats-green, before and
  after. The claude-in-chrome screenshots could not be used: that browser
  force-darkens pages, so light themes came out inverted. The row's amber
  text is still faint on the light themes and solarized-dark, raised as
  N-050.

- **N-048** · raised `2026-0926-2329-n046-failed-default-plugin-notice` ·
  closed 2026-09-26, `2026-0926-2351-n047-n048-script-plugins-and-failed-default-mark`. The toolbar's plugins button now carries a "!" mark
  (a `.w` slot beside the update count, `--warn` on a warn tint) while any
  default plugin failed to install. Its tooltip names the plugin. The mark
  was chosen over a toast: it stays until the cause is dealt with, and does
  not repeat on every connect. It is a
  new browser message, `plugin_notice {failed_defaults: [ids]}`, broadcast on
  change and in the connect burst. catway caches the ids
  (`orch.failedDefaults`) and re-reads them off the loop at the end of the
  seed pass (so a first start's failure shows without waiting for a poll)
  and on `plugin.list`, `dismiss_default`, `uninstall` and `check_updates`.
  The last one is the page's hourly cadence, which catches a catctl install
  in a tab; the dialog's install button also triggers the post-update
  rechecks. A failed read keeps the last answer.

- **N-047** · raised `2026-0926-2256-n003-hand-started-plugin-rows` ·
  closed 2026-09-26, `2026-0926-2351-n047-n048-script-plugins-and-failed-default-mark`. `pane_job` now also carries the head of the job leader's
  argv (`Argv`, at most `JobArgvMax` = 4 entries, `detect.ProcessArgs`). When
  the exe is not inside any plugin dir, catway resolves each argv entry
  (relative ones against the pane's cwd) and matches it against the installed
  plugins' declared `bin` entries (`pluginForScript`,
  `cmd/catway/handplugin.go`). An editor or pager holding the script is kept
  out: the script's own `#!` interpreter (seen through `env`) must match
  argv[0] by base name. Running a script through a different interpreter
  than its shebang names (`python3 some.sh`) is therefore not matched.

- **N-046** · raised `2026-0926-2226-default-plugin-seed-cats-todo` ·
  closed 2026-09-26, `2026-0926-2329-n046-failed-default-plugin-notice`. The
  seed now records each failed attempt's error and
  output tail, and keeps defaults it gave up on in a `failed` list in
  `.cats-defaults.json` instead of dropping them. `plugin.list` carries them as
  `failed_defaults` (`plugin.FailedDefaults`, filtered to ones still not
  installed). The plugins dialog shows a warning row per failure, above the
  installed plugins, with **install** (a `catctl plugin install` tab) and
  **dismiss** (new `plugin.dismiss_default` command, which also stops retries).
  `catctl plugin list` prints the same notice. `plugin.Uninstall` now forgets a
  default too, closing a gap where a default installed by hand while pending,
  then uninstalled, was seeded back on the next start. Checked in a scratch
  catway with a fake failed state: the row renders and dismiss clears it.

- **N-031** · raised `2026-0923-1443-settings-json-and-screen` ·
  closed 2026-09-26. `docs/reference/configuration.md` now shows every
  section example as a JSON fragment. The inline comments those examples
  carried moved into the tables under them. The custom-theme example stays
  YAML, because theme files are `.yaml`. `config.example.yaml` **stays** as
  the commented reference: JSON cannot carry comments, the release tarball has
  no docs, and `--config x.yaml` is still a supported format. A new
  "Reference files" section in the doc says what each example file is for.
  The YAML file's drift is fixed (a pre-rebrand `herdr.dev` origin, missing
  `tls.sans` and `ui`). `make dist` now ships `config.example.json` beside it.

- **N-007** · raised `2026-0914-0158-catway-restart-cats-todo-release` ·
  closed 2026-09-26, `2026-0926-2308-n032-n007-recorder-ui-prefs-and-blank-windows`.
  Each window now remembers the URL of a navigation that
  failed with a network error (`failedURL` in `cmd/catapp/window_darwin.m`,
  cleared when a load finishes; cancelled loads are ignored). `catwayBack`
  clears the overlay and then reloads those windows
  (`catsReloadFailedWindows`), so a window opened or reloaded during an
  outage comes up once catway is back. The restore list also falls back to
  that URL, so a window that never loaded keeps its workspace instead of
  being saved as the primary view.

- **N-032** · raised `2026-0923-1443-settings-json-and-screen` ·
  closed 2026-09-26, `2026-0926-2308-n032-n007-recorder-ui-prefs-and-blank-windows`.
  The macro recorder now drops a config.set whose only
  content is the `ui` options section (`viewerPrefsOnly` in
  `cmd/catway/record.go`), so a ⌘+/⌘- zoom or a sidebar drag during a
  recording is still saved but no longer becomes a step. A config.set that
  carries ui together with another section is still recorded whole, because
  steps keep their params exactly as sent. Persisting stays on during a
  recording, so no preference is lost.

- **N-003** · raised `2026-0905-1938-plugin-panes-in-the-agents-section` ·
  closed 2026-09-26, `2026-0926-2256-n003-hand-started-plugin-rows`. cathost's
  `pane_job` now carries the foreground job leader's executable (`Exe`,
  `detect.ProcessExe`), and catway matches it by path prefix against each
  installed plugin's resolved dir (`cmd/catway/handplugin.go`). The match is
  runtime-only and lasts while the job runs. A recorded launch id outranks it,
  and `pane.list` reads the same answer. Script-based plugins were still
  missed until N-047.

- **N-005** · raised `2026-0913-2313-catway-cathost-write-deadlock` ·
  closed 2026-09-26, `2026-0926-2143-n005-close-freeze-trigger-moot`, as
  moot, with no recurrence. `daemons.log` (kept since
  2026-09-14) has no stall, ping-timeout, drop, unexpected-exit or restart
  line; every daemon pid change follows a normal launch. Confirming the
  trigger would change nothing: bounded β writes (`0236300`) turn a
  recurrence into a stall warning and a reconnect instead of a freeze. The
  confirmation would not have been conclusive anyway. A successful auto-close
  logs nothing (`fireAutoclose`, `cmd/catway/reap.go`), so a stall line would
  still have to be matched to the agent exit by timestamp.

- **N-004** · raised `2026-0910-1855-startup-window-boot-log` ·
  closed 2026-09-26, `2026-0926-2136-n004-boot-log-rotation` (cats `05f21d7`).
  The first transcript write of a launch
  shifts `boot.log` into `boot.log.1` … `boot.log.4`. It happens once per
  process, so the second write from `fail()` or `finish()` cannot push out the
  previous launch. There is still no "copy this log" button: the splash has no
  bridges, and the path is already in the failure footer.

- **N-041** · raised `2026-0926-2108-paw-click-opens-todo-pane` ·
  closed 2026-09-26, `2026-0926-2119-n041-global-paw-click` (cats `4117bba`) —
  the heading paw calls `gotoGlobalTodoPane`, which reveals (`agent.focus`) a
  global manager: one in the viewed workspace first, else the first in
  inventory order. Managers in locked workspaces are skipped; if all are locked,
  the click shows a toast.

- **N-011** · raised `2026-0916-1547-peer-sync` ·
  closed 2026-09-25, `2026-0925-1102-n011-peer-pairing` (cats `6d01456`) —
  `catctl pair peer [label]` mints a 5-minute, single-use peer-kind pairing
  token shown as a `cats://peer` link.
  `catctl attach-peer <id> '<link>'` (or the peers dialog's url field) makes
  the other catway redeem it at the public `POST /peer/v1/pair` for a durable
  `catspeer_…` grant, stored in `<state_dir>/peer-tokens/<id>.token`. The
  grantor keeps only hashes (`internal/peergrant`, btypedb), accepts the grant
  on `/peer/v1/*` only, and manages it with `peer-grants` and
  `revoke-peer-grant`. Re-attaching an id re-pairs it.

- **N-021** · raised `2026-0922-1713-plugin-types-and-releases` ·
  closed 2026-09-24, `2026-0924-2029-notes-send-to-gonotes` (cats-todo `52539fe`, gonotes `08eb409`) —
  cats-todo sends an info prompt to the pane typed `notes_mgr` in `pane.list`
  (`pickNotesPane`, own workspace first), never by plugin id. The intake
  contract is a pasted `<!-- cats-note v1 -->` envelope through
  `pane.send_input`: a sentinel line, YAML frontmatter, then the markdown body.
  cats' paste encoding brings it to the TUI whole as a bracketed paste, and
  gonotes' root Update opens it as an unsaved note form. No cats change was
  needed. `tools.types` (N-035) is what types a `gonotes` started from a shell
  as `notes_mgr` too.

- **N-036** · raised `2026-0924-1953-context-usage-warn-demotion-tool-types` ·
  closed 2026-09-24, `2026-0924-2005-no-such-pane-exited-panes` — Not a pane-close race, but
  exited panes. The daemon's read pump drops a pane from its map at PTY EOF,
  while catway keeps the exited pane on screen for the reaper. Several catway
  sends had no `rt.exited` check: the reconcile resize (any layout change hit
  the dead pane), `ScrollPane`, `StartRead` / `StartCapture` (which then sat out
  the 5s `reqTimeout`, because a daemon error names no request kind), the
  waiter capture-check, and `Raw` input. All are gated now; read, capture and
  scroll fail with "pane N has exited". What is left is the gap between EOF and
  `pane_exited` reaching the loop, which nothing can close. So
  `orchestration.ErrNoSuchPane` logs as informational and is no longer toasted
  as "error: no such pane". Other daemon errors stay WARN plus toast. Follow-up
  in N-039.

- **N-035** · raised 2026-09-24, the `db_client` commit (no session doc) ·
  closed 2026-09-24, `2026-0924-1953-context-usage-warn-demotion-tool-types` — A new config map,
  `tools.types`, types a tool by the agent label it reports, however it was
  started. It defaults to `dbc` → `db_client` and `gonotes` → `notes_mgr`,
  merges key-wise, and `""` opts a label out. catway's `resolvePluginType`
  (shared by the rollup and `pane.list`) checks `editor.agents`, then the map,
  then the manifest, so a shell-typed dbc reports `plugin_type: "db_client"`
  and `IsDropAgent` is false. `wire` is unchanged; `editor` is refused as a map
  value (that stays `editor.agents`).

- **N-006** · raised `2026-0914-0134-daemon-logs-bounded-cathost-and-next-list` ·
  closed 2026-09-24, `2026-0924-1953-context-usage-warn-demotion-tool-types` — Re-counted against the
  installed app's `daemons.log` (35 lines): the three routine ones were 27 of
  them, now all informational. `auth disabled (--auth none)` is `log.Printf` on
  a loopback bind (catapp's local mode) and still a WARN on any other address
  (`isLoopbackAddr`). `manifest requires engine N` wraps a new sentinel,
  `errNeedsNewerEngine`, and logs as "skipped" rather than "failed"; other
  manifest failures stay WARN, and `status.json` still records it as failed.
  `account usage unavailable` is informational, because the sidebar's USAGE note
  already shows the reason. Left as WARN: `daemon error (pane N): no such pane`
  (3), and the one-off socket-close lines around a restart.

- **N-026** · raised `2026-0922-1857-next-list-seed-and-adopted-exec-panes` ·
  closed 2026-09-24, the `db_client` commit — The manifest's
  invalid-type hint missed `http_client`. It is now generated from
  `wire.KnownPluginTypes` (added with the `db_client` type), and
  `TestValidateTypeHintNamesEveryKnownType` pins that it names every one.

- **N-017** · raised `2026-0922-1713-plugin-types-and-releases` ·
  closed 2026-09-24, `2026-0924-1144-sidebar-section-splitters-v0.3.0` — The
  fixed `release.yml`, on its first real tag (v0.3.0, run 36029600049): the
  `release` job ran first, the four `dist` jobs after it, the body is the tag
  message exactly once plus the compare link, and all four tarballs attached.

- **N-027** · raised `2026-0923-1437-perf-canvas-sparse-frames-and-frame-gate` ·
  closed 2026-09-23, `2026-0923-1545-perf-shifts-builder-deflate` — Scrolling
  output as a full frame per tick. Shifted diffs (`Frame.Shift`,
  `PaneDiff.Shift`, negotiated on both hops) and permessage-deflate (rweb
  v0.1.31). A one-line scroll on 200×50 is 2.2 KB instead of 133 KB, before
  compression. Plan §3, §3b.
- **N-028** · raised `2026-0923-1437-perf-canvas-sparse-frames-and-frame-gate` ·
  closed 2026-09-23, `2026-0923-1545-perf-shifts-builder-deflate` — Remaining
  frame-path costs. Row cache in the emulator and `FrameBuilder` (snapshot +
  diff for a one-cell change 1.4 ms → 40 µs, off `emuMu`), hand-written frame
  JSON shared per translator state (562 → 100 µs per full frame), empty frames
  suppressed, one socket write per frame and batches of queued frames. Plan
  §5–8.
- **N-029** · raised `2026-0923-1437-perf-canvas-sparse-frames-and-frame-gate` ·
  closed 2026-09-23, `2026-0923-1545-perf-shifts-builder-deflate` — Slow
  browsers dropped at 512 queued messages. Frames are held back above 4 MB
  unwritten, and the current screen is sent from catway's grid below 1 MB.
  Plan §4.
- **N-030** · raised `2026-0923-1437-perf-canvas-sparse-frames-and-frame-gate` ·
  closed 2026-09-23, `2026-0923-1545-perf-shifts-builder-deflate` — Front-end
  leftovers: visibility-gated tickers, palette highlight without a rebuild,
  in-place countdown, host badges redrawn only when they can change. Plan §9.

- **N-002** · raised `2026-0905-1938-plugin-panes-in-the-agents-section` ·
  closed 2026-09-22, `2026-0922-1857-next-list-seed-and-adopted-exec-panes`
  — `execCmd` for adopted panes. The flag is now durable pane state
  (`PaneState.ExecCmd`, persisted as `exec_cmd`). `createPane` writes it on
  every spawn, and `reconcile` restores it onto an adopted survivor's
  runtime, so a catway restart no longer turns an exec'd editor or build into
  an "idle shell" that cleaning would close. `job` needed nothing: cathost
  replays `pane_job` on resync. Tests: `TestReconcileAdoptionRestoresExecCmd`
  (fails without the fix), `TestCreatePaneRecordsAndClearsExecCmd`, and the
  workspace snapshot round trip.

- `2026-0922-1713`: v0.2.3's release body replaced with the tag message.
- `inputenc` tests reading the deleted `keys.g.dart` golden: re-homed
  ("inputenc: re-home the key-code tests from the deleted Dart golden").
- The four other flag kinds (`?` `★` `⚠` `✓`) are drawn now ("flags: draw
  the question, star, warn and done icons").
- Daemon logs kept (`daemons.log`), and catapp notices a catway exit
  (`2026-0914-0134`).
- cathost's `out` is bounded: emit never blocks ("beta: cathost's emit never
  blocks").
- `-race` coverage: CI runs `make race-ghostty`.
- cats-todo exits when its terminal goes away, and is released (v0.31.2,
  `2026-0914-0158`).
- catapp's quit no longer orphans a cathost: `stop` sends SIGKILL after the
  grace period (`cmd/catapp/supervise.go`).
- catway's SIGTERM path works with a jammed loop (`cmd/catway/signals.go`,
  with a shutdown deadline independent of the loop).
- A catway-only restart keeps panes (proven in `2026-0914-0134`), and catapp
  restarts catway on an unexpected exit (`2026-0914-0158`).
- The agent-status script is in the repo (`scripts/agent-status`).
- The "serving at localhost127.0.0.1:…" startup line: `serveURL`
  (`cmd/catway/serveurl.go`).
- Stale `cmd/catgen-dart` mentions: `wire/vocab.go` and `inputenc` comments now
  describe it as removed in `5add396`, and the stray root binary is gone.
- cats-mobile picked up `peer.*` and later the plugin types (it's a Go client
  now; `d46abf1`, `0b38a3c`).
- One-off user actions from the freeze incident (recover the frozen app,
  resume the fable/opus agents, update the cats-todo plugin to v0.31.2) are
  obsolete. The plugin relaunch now lives in N-001.
- `make jstest` failing on `main` over `openPeersDialog` (found
  `2026-0918-1901`): passes now.
