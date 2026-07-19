# herdr-web rebrand + deferred chrome (launcher, markers, pane swap)

**Session id:** `98291bca-f9a9-4e1f-a265-64be9d88e9be`
**Date:** 2026-0718-2350 · **Branch:** `roh/phase-b` (herdr-web)
**Continues:** `2026-0718-2305-ws11-cutover-v0.1.0-release.md` (its two
deferred leftovers — the herdr-web rebrand and the niche chrome — are this
session).

> Two commits. **Rebrand** (`7b17b1b`): all user-facing "herdr" branding now
> reads "herdr-web". **Chrome** (`5495194`): three of the four deferred
> chrome items shipped — global launcher menu, bell/activity (seen) markers,
> and pane drag-swap — live-verified with Playwright (13/13 checks).
> Only **onboarding** remains deferred (user's call — not selected).

---

## Rebrand (commit `7b17b1b`)

- `cmd/gateway/web/index.html`: page `<title>`, brand div, dynamic title
  fallback ("herdr-web · <pane title>"), update banner, stop-server dialog.
- `README.md`: gateway row now "The herdr-web server".
- Left as "herdr" deliberately: internal identifiers (`window.__herdrKeys`,
  notification tag `herdr-pane-`), on-disk paths (`~/.config/herdr`,
  `~/.local/state/herdr`), socket names, herdr-parity code comments.

## Global launcher (web adaptation of herdr's corner menu)

Rust reference: `src/ui/menus.rs render_global_launcher_menu` +
`src/app/input/{sidebar,modal}.rs` — labels settings / keybinds / reload
config / update-ready ("what's new") / detach, with an attention badge.

Web: the status-bar gear (`#gear`, title now "menu") opens an `openCtx`
menu anchored above it instead of jumping to settings. Items map to the
existing palette functions: `openSettings`, `openHelp` (keybinds),
`server.reload_config`, `confirmStopServer` (danger, stands in for detach —
the web has no client to detach). New `updateInfo` state persists past a
dismissed banner: the `update_ready` case stores {version, command}, adds
`.badge` (accent dot via `#gear.badge::after`) and the menu grows an
"update ready — vX" item that re-shows the banner (`showUpdateBanner`).

## Bell/activity markers (herdr's pane.seen model)

**Key discovery:** browserproto `AgentItem.Seen` and the front-end's
`it.seen ? it.state : "done"` rendering already existed — gateway just
hardcoded `Seen: true`. The work was the server-side attention model.

- Rust semantics (`app/actions.rs apply_pane_state_change`,
  `ui/status.rs state_dot`, `ui/sidebar.rs workspace_attention_priority`):
  seen=false on a completion while not (active tab + terminal focused);
  any non-idle state or switching to the tab sets seen=true. Priority
  blocked(4) > idle-unseen/done(3) > working(2) > idle-seen(1) > unknown(0);
  done renders teal.
- Go: `paneRuntime.unseen` (inverted so zero value = seen). `publishAgent`
  computes the notify kind first, then: state != idle → unseen=false;
  kind=="finished" → unseen = !visible. `applyModel` clears unseen on panes
  entering the viewport. Browser focus is per-client (the standing notify
  deviation), so **viewport visibility stands in for herdr's
  active-tab+focused rule** — visible panes are always seen.
- `agentsMsg` now sends `Seen: !rt.unseen` and a new `AgentItem.Tab`
  (stable tab.Number) for tab grouping.
- Front-end: `markerState()` folds seen into a "done" display state;
  `attentionRank()` orders it; workspaceSummary counts a done tier;
  new `tabMarker()` puts the highest-attention dot on tab-bar tabs
  (rollup filtered by active workspace + tab number); agents-panel dot
  uses markerState. `renderAgents` re-renders the tabbar too. New themable
  `--done` color (#4fd1c5), `.st-done` class, `done` in
  config.example.yaml (Theme.Colors is a free CSS-var map — no Go change).
- Test: `TestAgentSeenMarkers` (visible completion stays seen; off-viewport
  completion unseen + Tab field; re-working clears; tab.focus re-seen).

## Pane drag-swap (web-native; Rust only has directional swap)

- Rust has no pane *drag* — only `swap_pane(direction)`; the layout
  primitive `swap_panes` was already ported as `layout.SwapPanes` (WS1),
  and `pane.swap` (directional) already existed as a command.
- New command `pane.swap_with {pane, target}`: vocab + CommandNames +
  `SwapWithParams` (internal/app/command_vocab.go), dispatch case
  (commands.go) → new `Session.SwapPanes(a,b)` (active tab,
  `tab.Layout.SwapPanes`, fails on same/unknown ids) → ApplyModel.
  browserproto aliases added. Available to browser + herdrctl + control
  API automatically (one command table). Test: `TestDispatchSwapWith`.
- Front-end `beginPaneSwapDrag`: mousedown on a pane's chrome arms a 4px
  threshold drag (same shape as `beginReorderDrag`, incl.
  `dragConsumedClick`); hit-test via live pane rects (survives mid-drag
  rebroadcasts); `.pane.dragging` (opacity .5) + `.pane.droptarget`
  (accent outline); release on a target sends pane.swap_with.
- Help modal mouse section: added swap-panes, tab/workspace reorder, and
  launcher-menu rows.

## Live verification (Playwright, 13/13 PASS)

- Scratch stack: `bin/termhost -manifest-update=false` +
  `bin/gateway -addr 127.0.0.1:<port> -auth none -persist=false`, short
  socket dir (`/tmp/...` — macOS 104-byte sun_path), `make binaries`
  (plain `make build` does NOT refresh bin/ — stale-binary trap: the page
  still served the old title until rebuilt).
- playwright-core + system Chrome headless; script asserts launcher menu
  items, help rows, split → mid-drag classes (dragging+droptarget) →
  swapped `style.left`, hook-driven markers, and marker clear on tab focus.
- Agent-state simulation without a real agent: the hook API socket
  (`pane.report_agent`, source/agent "hermes" — non-reserved sources get
  state authority; newline JSON `{"id","method","params":{pane_id:"w1:p1",
  source,agent,state,seq}}`). working(seq1) → new tab (backgrounds pane) →
  idle(seq2) ⇒ teal ● on tab 1, `●1` workspace badge, "hermes w1:p1 ·
  done" agents row, "hermes finished" toast; refocusing tab 1 cleared all.
- Screenshots eyeballed: droptarget outline mid-drag; done markers.
- Only console error: the known favicon 404.

## Gotchas / techniques

- zsh `nomatch`: an unmatched glob (`rm -f dir/*.sock`) aborts the whole
  `&&`/`;` line — the daemon after it silently never started.
- gopls still shows the phantom "No packages found" for `-tags ghostty`
  files; trust `make check`.
- `openCtx` clamps into the viewport, so anchoring a menu at the bottom
  status bar "just works" (it grows upward).
- Clicking the gear while its menu is open reopens rather than toggles
  (global capture-mousedown closes, click reopens) — accepted quirk,
  consistent with other ctx menus.

## Files

- Rebrand: `cmd/gateway/web/index.html`, `README.md`.
- Chrome: `cmd/gateway/web/index.html` (launcher, markers UI, drag-swap,
  help), `cmd/gateway/gateway.go` (unseen field, applyModel clear,
  agentsMsg Seen/Tab), `cmd/gateway/notify.go` (publishAgent seen rule),
  `internal/browserproto/down.go` (AgentItem.Tab) + `cmd.go` (aliases),
  `internal/app/{command_vocab,commands,session}.go` (pane.swap_with),
  tests `cmd/gateway/notify_test.go`, `internal/app/commands_test.go`,
  `config.example.yaml` (done color).
- Memory: `pref-project-name-herdr-web` (rebrand DONE),
  `goal-full-go-migration` (chrome: only onboarding remains).

## Notes / leftovers

- **Onboarding** is the last deferred chrome item (not selected this
  session; scope open-ended — propose a design first).
- Open bugs from the resume session still stand: resumed pane should exit
  with its agent (no respawn-shell); dedupe resume winner by pane id.
- Rust checkout `~/projs/rust/herdr` still around — proved useful again
  this session as the parity reference; deleting is the user's call.
