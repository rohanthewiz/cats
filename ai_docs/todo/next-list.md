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
- **Nothing leaves Open or Roadmap without a line in another section**
  (Closed, Non-goals, or the other of the two). Moving between Open and Roadmap
  keeps the header line unchanged.
- Open and Roadmap stay in ID order. New items append to the end of Open (or
  Roadmap, for future work) with the next ID.

**Next ID:** N-047

## Open

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
    drag the seam above Panes and Agents with real rows, check the grip goes
    inert under a folded section, and that a trackpad drag inside the
    WKWebView keeps the row-resize cursor for the whole drag.

- **N-003** · raised `2026-0905-1938-plugin-panes-in-the-agents-section` · value low
  A plugin started by hand from a shell (e.g. `cats-todo` typed at a prompt)
  has no `CATS_PLUGIN_ID`, so it gets no row in PLUGINS. Editors are the
  exception since `2026-0922-1713`: `editor.agents` types them `editor` however
  they were started.

- **N-007** · raised `2026-0914-0158-catway-restart-cats-todo-release` · value low
  A window opened or reloaded while catway is down stays blank after
  recovery. `catwayBack` only evaluates JS in existing pages
  (`cmd/catapp/backenddown.go`), and a failed load leaves no page to run it
  in. The fix would reload windows that have no loaded URL on `catwayBack`.
  Minor, because automatic restarts take well under a second.

- **N-008** · raised `2026-0914-0158-catway-restart-cats-todo-release` · value low
  `waitReady` (`cmd/catapp/supervise.go`) has no caller: it is only a wrapper
  over `waitReadyOrExit(…, nil)`. Remove it or leave it. It is a candidate for
  Non-goals if nobody minds.

- **N-009** · raised `2026-0914-1051-ced-cats-bin-dir` · value low
  `catctl integration install shell zsh` (the OSC 133 marks that feed the
  command ledger and HISTORY) has not been run on this machine; the plain
  `shellinit` eval only covers PATH. This is a user action.

- **N-010** · raised `2026-0914-1051-ced-cats-bin-dir` · value low
  ced repo: the README still documents Homebrew / `install.sh` installs, now
  positioned as "without cats", and the Makefile's `alt` target (installs
  `~/bin/ce`) has a comment about "a brew-installed ced". The Homebrew
  formula step was removed from ced's release on 2026-09-14. Whether those
  install paths stay is the user's call.

- **N-012** · raised `2026-0916-1547-peer-sync` · value low
  Cross-OS home translation in peer sync is unit-tested only; the first
  Mac ↔ Linux sync is the real test of it.

- **N-013** · raised `2026-0918-1901-dictation-text-sink` · value low
  IME users compose blind: marked text lives in the invisible text sink until
  it commits. A visible preedit overlay at the cursor would fix it.

- **N-014** · raised `2026-0918-1901-dictation-text-sink` · value low
  Dead keys (⌥e) are still `preventDefault`ed by `onKey` unless the engine
  reports them as `keyCode 229`.

- **N-015** · raised `2026-0922-1301-cats-pane-input-queue` · value low
  `errPaneInputFull` emits one error event per refused message, so a flood
  into a dead pane could spam them. Rate-limit if it ever shows up.

- **N-016** · raised `2026-0922-1301-cats-pane-input-queue` · value low
  Legacy X10 / urxvt mouse encodings are never droppable (the conservative
  choice). Fine while the browser encoder emits SGR.

- **N-018** · raised `2026-0922-1713-plugin-types-and-releases` · value medium
  ced repo: `TestThemeAfterSave_RepaintsLive` fails about 1 run in 10. TempDir
  cleanup reports `themes/` "directory not empty", so something writes into
  the directory after the test ends. The assertion itself passes.

- **N-019** · raised `2026-0922-1713-plugin-types-and-releases` · value low
  ced repo: `release.yml` still describes the `release` branch + auto-bump
  route. That route hasn't been used since 0.2.0; releases are now a
  hand-made "Release ced X" commit plus a tag. Retire it or go back to it.

- **N-020** · raised `2026-0922-1713-plugin-types-and-releases` · value low
  cats-mobile: nothing draws `Session.Plugins` yet. If the phone gets a
  plugins list, split it by `PluginPane.Type` the way the desktop does.

- **N-031** · raised `2026-0923-1443-settings-json-and-screen` · value low
  `docs/reference/configuration.md` still shows every section example in YAML
  (the keys are identical in JSON; the intro says so). Convert them to JSON,
  and decide whether `config.example.yaml` stays as the commented reference.

- **N-032** · raised `2026-0923-1443-settings-json-and-screen` · value low
  ⌘+/⌘- and sidebar drags now write `ui` prefs via config.set, which is a
  Recorded command, so zooming while a macro records captures a config.set
  step. Skip ui-only config.set in the recorder, or don't persist while
  recording.

- **N-033** · raised `2026-0923-1443-settings-json-and-screen` · value low
  Other open browsers don't pick up a `ui` pref (font size, sidebar width)
  changed elsewhere until they reload; there is no broadcast for it.

- **N-034** · raised `2026-0923-1545-perf-shifts-builder-deflate` · value low
  cats-mobile does not list `pane_shift` in `Init.Features`, so a scrolling
  pane still reaches the phone as a full frame per tick (now compressed). Its
  grid (`internal/catsclient/grid.go`) would apply `PaneDiff.Shift` as the
  browser does: scroll the cells up, blank the vacated rows, then the cells.

- **N-037** · raised `2026-0924-1953-context-usage-warn-demotion-tool-types` · value low
  The context used/window segment on agent rows (`43k/1M`) is claude-only.
  Copilot's `events.jsonl` would need its own reader, if it records usage at
  all (`lastCopilotModel`, `cmd/catway/agentmodel.go`).

- **N-038** · raised `2026-0924-1953-context-usage-warn-demotion-tool-types` · value low
  `tools.types` has no field on the settings screen, because the screen has no
  key→value widget (`33-settings.js` OPTION_TABS). It is file-only plus
  `catctl reload`. Adding it would mean adding `tools` to `optionSections` and
  a map widget.

- **N-039** · raised `2026-0924-2005-no-such-pane-exited-panes` · value low
  `capture` / `read` of an exited pane now fail at once with "pane N has
  exited" (they used to time out), but they still cannot return the text. The
  daemon drops the emulator at PTY EOF. catway's grid mirror (`rt.grid`) still
  holds the last screen, which is often what you want from a dead pane (the
  crash message), so serve at least `scope: screen` from it.

- **N-040** · raised `2026-0925-1102-n011-peer-pairing` · value low
  `detach-peer` deletes the paired token file but cannot revoke the grant on
  the other catway, which stays live until someone runs `revoke-peer-grant`
  there (catway logs a reminder). A self-revoke route (`POST /peer/v1/unpair`,
  authenticated by the grant itself) would make detach clean up both ends.

- **N-042** · raised `2026-0926-2211-plugin-update-checks` · value medium
  Plugin update checks: drive the parts that were only reasoned about. The
  badge, dialog rows and ↻ were checked in a browser on a dev catway (light
  theme). Not run: a real **update** from the dialog, and whether the badge
  then clears by itself via the +30s/+90s/+4m rechecks (skipped so the
  installed cats-todo wasn't changed mid-session); **update all** with two or
  more pending; the dark theme; and the reinstalled Cats.app. cats-todo is
  behind upstream right now (78c85ac → 9bfe73e), so updating it is the natural
  test.

- **N-043** · raised `2026-0926-2211-plugin-update-checks` · value low
  Linked plugins are skipped by the update check, because `plugin update`
  refuses them. A linked checkout that is behind its own upstream (ced, when
  its origin has moved on) could still get a quiet "behind origin" hint. That
  needs a fetch in the developer's own repo, which is why it was left out.

- **N-044** · raised `2026-0926-2226-default-plugin-seed-cats-todo` · value medium
  Push cats-todo `56c5c4d` and cut v0.42.1. Fresh installs clone cats-todo's
  default branch, so until the headless-offer fix is on GitHub the automatic
  install still spends the one-time "set up a backlog here?" offer on
  catway's log.

- **N-045** · raised `2026-0926-2226-default-plugin-seed-cats-todo` · value medium
  Watch the default-plugin seed through a real catway start. It has only run
  as a direct `SeedDefaults` call against a scratch root. Start a catway (and
  the rebuilt Cats.app) with `CATS_PLUGINS_DIR` pointing at a directory that
  does not exist yet, then confirm the install log line in `daemons.log` and
  that cats-todo shows in the plugins dialog and picker once they are
  reopened. Also start once without Go on PATH to see the retry and give-up
  warnings.

- **N-046** · raised `2026-0926-2226-default-plugin-seed-cats-todo` · value low
  A default plugin that failed to seed is only reported in the daemon log. A
  fresh-install user without Go never learns why cats-todo is missing. The
  plugins dialog could show a "cats-todo could not be installed: …" line from
  `.cats-defaults.json`'s `last_error`, with the install button next to it.

## Roadmap

Wanted, but not next. Items move here from Open (or straight here when raised
as future work) and back to Open when they become next, with their header line
unchanged.

- None yet.

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

## Closed

Closures before this file existed live in the session docs' own write-ups.
Newest first. The unnumbered entries at the end were found done while seeding,
so they are not carried.

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
