# Session: Tab Auto-Naming + Pane Header Restore

- **Session ID:** `d5358e1a-d0d9-4a8a-91ac-87ad2d2c71fd`
- **Date:** 2026-07-28
- **Branch:** main

## Request

Started as "the cats-todo plugin tab says 'Cats Todo — this project'; make it say
'Todo - <actual-proj-name>'", then widened:

1. A tab's panes can run very different things — design an algorithm that names
   a tab from its current pane(s).
2. Restore the per-pane header strip (dropped in the 2026-0727 green-theme
   facelift), and move the ⌘K palette / ⚙ gear there "if that is where it
   applies".

## Decisions (user accepted all recommendations)

- **Tab auto-naming: best-pane ladder, server-side.** Explicit `tab.rename`
  always wins. Otherwise the best-scoring pane names the tab:
  pane custom name (4) > `agent · cwd-basename` (3) > OSC title (2) >
  cwd basename (1) > tab number (0). Ties → the tab's focused pane, then first
  in layout order. Multi-pane tabs get a ` +N` suffix. Agent panes never use
  their own OSC title — Claude Code's churns with its spinner.
- **⌘K + ⚙ stay on the tab-row right edge** — they're app-global; putting them
  in a per-pane header would repeat them per pane and imply pane scope. What
  *did* move into the header: the pane-scoped ZOOM chip and copy-mode
  indicator that had been squatting on the tab row.
- **Plugin launches stop pinning tab.create's Title** — the pinned manifest
  title was a permanent custom name blocking auto-naming forever. Unpinned,
  cats-todo's tab auto-names to its OSC title: `todo: <project>`. Solved the
  original request for every plugin at once, with zero manifest changes.

## What was done

### Server: tab auto-naming

- `internal/app/tabname.go` (new): `Session.TabDisplayName(tab, metaFn)` +
  `paneAutoLabel` ladder + `cwdBaseName` ("/" is never a name) + 40-rune clip.
- `internal/app/query.go`: `ListTabs` takes a `meta func(uint32) PaneMeta`
  (nil ⇒ plain custom-name-or-number); dispatcher passes `d.backend.PaneMeta`
  (`internal/app/commands.go`) so `catctl tabs` shows derived names.
- `cmd/catway/catway.go`:
  - `viewportLayout` patches derived names into `msg.Tabs` after `BuildLayout`
    (same patch-after pattern as the agent summary) and records them on new
    orch field `lastTabNames`.
  - `refreshTabNames()` rebroadcasts the layout **iff** a derived name changed;
    deliberately `o.broadcast(o.viewportLayout())`, not `BroadcastLayout()` —
    no saveSoon/structural-event side effects for a presentation-only change.
  - Hooked into every meta ingest path: OSC title + OSC 7 cwd handlers
    (`cmd/catway/daemon.go`), agent arbitration `publishAgent`
    (`cmd/catway/notify.go`), and `BroadcastPaneTitle` (pane.rename).
- `cmd/catway/notify.go`: notification context line uses the derived name.

### Plugin launches unpinned

- `cmd/catctl/plugin.go` (`pluginRun`) and the web plugins dialog
  (`pluginRunAction`) no longer send `Title` in `TabCreateParams`. The
  `pluginCatctlTab` install/link/rebuild tabs keep their pinned titles —
  they're catctl output tabs, not plugin UIs.

### Web: pane header restored, chips moved in

- `cmd/catway/catway.go`: `chromeRows` 0 → 1 (the reservation mechanism was
  kept at removal time exactly for this).
- `cmd/catway/web/index.html`:
  - `.pane .chrome` CSS re-added on the green vars (`--chrome`/
    `--chrome-focus`); text colors adapted (unfocused `var(--muted)`, focused
    `#e6efe7`, buttons `#cfe0d3`).
  - `newPane`: chrome DOM + `mkBtn` buttons (⊟ ⊞ ⤢ ⬚ ⧉ ✎ ✕) + focus/rename/
    menu/drag-swap handlers. Sidebar row mirrors kept.
  - `renderChrome`: pub · title · cwd · agent · exited; **copy mode replaces
    the identity line** with a COPY chip + key hints while active; a zoomed
    focused pane gets a clickable **ZOOM ⤢** chip (new `tabZoomed` global from
    `applyLayout`).
  - Statusbar reduced to `#palhint` + `#gear`; `#mode`/`#modehint`/`#zoomind`
    spans, CSS, refs, `setModeBar`, and `renderStatusExtras` all removed.
  - `pane_title`/`pane_cwd`/`pane_agent`/`pane_exited` handlers re-render
    chrome again.
  - Help modal: "pane header buttons" section restored; swap/copy-mode/gear
    wording updated.

## Verification

- `go test ./internal/...` and full `make test-ghostty` pass; new
  `internal/app/tabname_test.go` covers every ladder rung, tie-breaks, +N,
  clipping, and `ListTabs` with/without meta.
- Live smoke (session-doc recipe): `bin/cathost -socket ... -persistent` +
  `bin/catway --addr 127.0.0.1:8477 --auth none --persist=false`, headless
  Chrome screenshots. Confirmed: header strip with buttons; tab auto-named
  `cats` (cwd rung) → `todo: demo` after an OSC 2 write (title rung, live
  rebroadcast) → `todo: demo +1` after a split; ZOOM chip in the zoomed
  pane's header; `catctl tabs` reports the same derived names.

## Gotchas / notes for next time

- **Already-pinned tabs keep their names** — a custom name is durable session
  state, so tabs created by the old pinning launchers (e.g. "Cats Todo — this
  project") stay pinned until renamed to empty or closed. Auto-naming only
  governs auto-named tabs.
- **The MacApp needs a rebuilt/reinstalled bundle** to pick this up — all
  changes are in catway/catctl (user runs from the MacApp; stale-bundle
  confusion is a known trap).
- Unix socket paths have a ~104-char limit on macOS — the long scratchpad-dir
  socket path failed with `connect: invalid argument`; smoke sockets went to
  short `/tmp/cats-smoke-*.sock` paths instead.
- `refreshTabNames` fires on every title/cwd/agent event; the `lastTabNames`
  diff is what keeps pre-agent-detection title churn (braille spinners) from
  spamming layout broadcasts. If layout spam ever shows up, look here first.
- gopls flags `cmd/catway/*.go` as excluded (needs `-tags ghostty`) — harmless,
  same as last session.
