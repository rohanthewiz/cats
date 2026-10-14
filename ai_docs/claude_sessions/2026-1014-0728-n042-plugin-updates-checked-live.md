# Session: N-042 plugin update checks driven live

Session ID: 2cc261f5-be44-47d3-b2c2-f00e921136cb
Date: 2026-10-14

A `/work-next-list` run over the whole Open and Validate sections.

## 1. Triage

| Doable | Plan |
|---|---|
| N-042 | Scratch catway + cathost with their own plugins dir. Install ced and cats-todo there, roll both back, then drive **update**, the badge's self-clearing, **update all** and the dark theme. |

| Skipped | Reason |
|---|---|
| N-009 | User action (`catctl integration install shell zsh`). |
| N-010 | The user's call (ced's Makefile `alt` target). |
| N-016, N-020 | Contingent, deferred by design. |
| N-037 | This machine has no copilot CLI and no `~/.copilot/session-state` (only `~/.copilot/jb`), so there is no way to see whether events.jsonl records context usage. |
| N-001, N-012, N-013, N-014 | Hand checks in Cats.app, with a real IME, or on a Linux peer. |
| N-054, N-056 | Need a real claude pane in Cats.app. |

## 2. N-042: what was run

The setup was all in the scratchpad, and the user's own catway, config and
plugins were left alone:

- `make binaries` from the current tree (8252220).
- `CATS_PLUGINS_DIR`, `CATS_BIN_DIR`, `XDG_CONFIG_HOME`, `XDG_STATE_HOME` and
  `XDG_DATA_HOME` all pointed into `scratchpad/n042/`.
- `catctl plugin install rohanthewiz/ced` and `rohanthewiz/cats-todo` into
  that directory (ced v0.4.3 at 05a180c, cats-todo v0.45.0 at 453c075).
- Each tree was rolled back two commits (`git fetch --deepen 3`, then
  `reset --hard HEAD~2`), to cats-todo b779863 (v0.44.0) and ced 2176e90.
  `catctl plugin check` then reported 2 updates available.
- `bin/cathost -socket … -persistent -idle-timeout 0 -manifest-update=false`
  and `bin/catway --addr 127.0.0.1:8531 --auth none --persist=false`, with
  their own control socket and `--hook-socket none`.

The checks, in claude-in-chrome:

- The badge read `2`, with the tooltip "plugins — 2 updates available". The
  dialog had two `.row.plg.upd` rows, each with a `.hot` **update**, plus
  **update all (2)**.
- **update** on ced opened an "update rohanthewiz.ced" tab. The tree moved
  to 05a180c, and `bin/ced` was rebuilt at 07:23:36. The tab closed itself
  on the clean exit (the tidy-exit auto-close). Polling the badge from the
  click: still `2` at 18s, then `1` at 31s, which is the +30s
  `pluginRecheckSoon`. The tooltip changed to "1 update available".
- With ced reset to 2176e90 again, ↻ (forced) brought back `2` and
  **update all (2)**. Clicking it opened one "update plugins" tab, which
  rebuilt cats-todo (07:24:37) and then ced (07:24:38). The tab was gone by
  14s, and the badge was empty at 30s. `catctl plugin check` reported both
  up to date.

The theme captures came from headless Chrome over CDP (`scratchpad/cap.mjs`:
a throwaway profile, load, wait 12s, click `#pluginsbtn`, capture), with both
trees reset to the old SHAs:

- cats-green, the default: `--bg #1f2420`, the card in `--panel2`, and the
  update rows tinted with the accent at 7%. Everything read cleanly.
- solarized-light, via `catctl theme solarized-light` on the scratch control
  socket, which wrote only the scratch config: the blue accent, legible
  pills, and **uninstall** muted until hover, as designed.

## 3. Findings and gotchas

- **The 2026-09-26 "light theme" check was most likely the dark default.**
  cats-green is a dark theme, and claude-in-chrome forces an inversion, so
  the cream-coloured dialog in its screenshots is the dark theme inverted
  (see the chrome-force-dark memory). The real light theme has now been
  captured headless.
- **Unix socket paths.** The scratchpad path is past the 104-byte
  `sun_path` limit (`bind: invalid argument`). The sockets went to the short
  `/private/tmp/claude-501/n042s/` instead, which was deleted afterwards.
- **A plugin update leaves a shallow tree.** `Update` fetches with
  `--depth 1`, so `HEAD~2` no longer resolves afterwards. Rolling back again
  needs the old SHA, whose objects are still there.
- **Headless canvas.** In both headless captures the terminal area is black
  rather than `--term-bg`. That is a capture artifact: the live Chrome tab
  painted the pane normally.
- The scratch catway and cathost were stopped by PID (85832, 85829), and the
  Chrome tab was closed.

## 4. List changes

- N-042 closed. The reinstalled-Cats.app half shares N-001's action and
  blocker (sessions run inside Cats.app, where GUI automation is blocked), so
  it is now a bullet under N-001, the same way N-045 was folded in.

Commits: 8e60cb3 (the list), plus this doc.

## Next

Closed: N-042. Declined: None. Raised: None.
Deferred: None. Promoted: None. Moved: None.
Updated: N-001 (gained the Cats.app plugin-update bullet from N-042). Full list: `ai_docs/todo/next-list.md`.
