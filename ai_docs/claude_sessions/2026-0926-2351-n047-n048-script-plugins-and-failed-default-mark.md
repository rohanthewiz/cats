# Session: N-047 script plugins typed at a prompt; N-048 toolbar mark for a failed default plugin

Session ID: b6de846d-7755-4add-8eb3-f476b56cf448
Date: 2026-09-26
Driven from: cats

The user pasted two next-list items in turn, N-047 and then N-048. Both are
follow-ups to the plugin work earlier the same day (N-003 hand-started plugin
rows, N-046 failed-default notice).

## 1. N-047: script plugins typed at a prompt get a PLUGINS row

Committed as `c410a39`.

### The gap

N-003 matched a hand-typed plugin by the foreground job leader's executable
(`pane_job.Exe`), checking whether the path is inside an installed plugin's
dir. A plugin whose `bin` entry is a `#!` script runs as its interpreter, so
the exe is `/bin/sh` or `python3` and never matches.

### Design

The kernel runs a `#!` file by exec'ing the interpreter with the script's path
added to argv, so the script is visible there:

```
$ cats-notes list
  exe:  /bin/sh
  argv: [/bin/sh ~/.cats/bin/cats-notes list]
                 └─ resolves to <plugins-root>/<id>/bin/cats-notes
```

(This was confirmed on macOS by a test that runs a real `#!/bin/sh` script.)

- **Daemon.** `detect.ProcessArgs(pid)` reads argv on darwin
  (`KERN_PROCARGS2`, via the existing `procArgv`) and on linux
  (`/proc/<pid>/cmdline`); the stub returns nil. The host's detect pump reads
  it next to `ProcessExe`, under the same throttle. `pane_job` gains `Argv`
  (omitempty), which `NewPaneJob` caps at `orchestration.JobArgvMax` = 4
  entries: interpreter, one shebang argument, the script, and one spare for
  an `env -S` split. The full argv of a glob-expanded command can be
  megabytes. `jobArgvHead` clones the head so retained pane state does not
  pin the full array. `setJobMeta` now compares exe and argv, and resync
  replays both.
- **catway** (`cmd/catway/handplugin.go`). `lookupPluginForJob` tries
  `pluginForExe` first, then `pluginForScript`. That function resolves every
  argv entry after argv[0] that is not a flag (relative ones against the
  pane's cwd, `rt.cwd`, captured on the loop) and compares it with the
  resolved **declared `bin` entries** of each installed plugin.
- **Guarding against readers.** `vim bin/cats-notes` or `less` would put the
  bin entry in argv too. A match therefore also requires the script's own
  `#!` interpreter, as parsed by `shebangInterpreter`, to equal argv[0] by
  base name. The parser looks through `env`, skipping its flags, `-u NAME`
  and `NAME=value`. For a real run the kernel (or env) sets argv[0] from that
  line.
- `paneRuntime.jobArgv` sits next to `jobExe`. `applyPaneJob` re-resolves
  when either changes, and the stale-answer check compares both.

**Known remaining gap:** a script run through a different interpreter than
its shebang names (`python3 some.sh`) is not matched. This is recorded in
N-047's Closed entry.

### Tests

- `TestProcessArgsNamesTheScriptBehindAnInterpreter` (darwin): argv of self,
  and of a live `#!/bin/sh` script.
- `pane_job` round-trip with argv trimmed to 4.
- `TestPluginForScriptMatchesARunningBinEntry`: the bin-farm link, a relative
  path plus cwd, a shebang argument, an `env -S` split, an editor, the wrong
  interpreter, a file that is not a bin entry, a relative path with no cwd.
- `TestShebangInterpreter`.
- `TestAgentsRollupListsHandStartedScriptPlugins`: end to end through
  `applyPaneJob`, plus a later `sh` job with other argv clearing the row.

The Linux `ProcessArgs` was only reviewed by eye. A cross-compiled
`GOOS=linux go vet` fails in the Go runtime's own cgo on macOS, unrelated to
this change.

## 2. N-048: toolbar mark for a failed default plugin

### Choice

The item offered a toolbar mark or a one-time toast. I chose the mark: it
stays until the cause is dealt with, does not repeat on connects, and sits in
the place that already carries the plugin update count.

### Design

```
⧉ plugins [!] [2]
          │    └ .n — updates available (accent chip, a count)
          └ .w — a default plugin could not be installed (warn chip)
```

- **Wire.** A new down message, `plugin_notice {failed_defaults: [ids]}`
  (`wire.PluginNotice`, `NewPluginNotice` turns nil into `[]`,
  `MsgPluginNotice`). It was added within protocol v1; clients that don't know
  it skip it.
- **Why a push.** On a first start the seed fails minutes after the page
  connected, and the hourly update-check poll would show the mark up to an
  hour late.
- **catway** (`cmd/catway/plugins.go`). `orch.failedDefaults` is a
  loop-owned cache. `setFailedDefaults` broadcasts only on change;
  `readFailedDefaultIDs` and `refreshPluginNotice` read off the loop. The
  list is re-read at:
  - the end of the seed pass (`main.go` wraps `seedDefaultPlugins`),
  - `plugin.list`, which reuses the read it already makes,
  - `plugin.dismiss_default`,
  - `plugin.uninstall`,
  - `plugin.check_updates`, the page's background cadence. This is what
    catches a `catctl plugin install` run in a tab.

  A failed read keeps the last answer. The connect burst always sends the
  notice, as it does `record` and `runbook_runs`.
- **Page.**
  - `19-messages.js` routes the message to `applyPluginNotice`.
  - `30-plugins.js`: `paintPluginBadge` now draws both marks and a combined
    tooltip (e.g. "cats-todo could not be installed — open for details").
  - The failed row's **install** button also calls `pluginRecheckSoon()`, so
    the mark clears once the install lands.
  - `mainarea.go` renders the `.w` slot; the CSS is in `24-toolbar.css`.

### Found in the live check: contrast on light themes

The first chip was `--warn` text on a 22% `--warn` tint, the dialog row's
avatar treatment, and it nearly vanished on a light theme. That theme's
toolbar is a pale beige and `--warn` stays #e0b64e. Every built-in `--warn`
is an amber between #b07800 and #ffc66b, so the chip is now a solid `--warn`
fill with fixed near-black ink (`#1b1606`, about 5:1 at the darkest amber).
`--accent-fg` was not an option: it derives from `--bg` and is light on light
themes. The dialog row likely has the same problem, raised as N-049.

### Tests and live check

- `wire/proto_test.go`: sample message, type list and count (41 → 42).
- `cmd/catway/pluginnotice_test.go`:
  - `TestPluginNoticeFollowsTheSeedState`: a real state file, one broadcast,
    no re-send for an unchanged read, an empty list once the plugin dir
    exists.
  - `TestPluginNoticeKeepsTheMarkOnAReadError`.
- `cmd/catway/web/jstest/pluginbadge.test.mjs`: idle, failed, retracted, a
  missing list, and both marks together.
- **Live:** a scratch catway and cathost in the scratchpad on
  `127.0.0.1:18517`, with `CATS_PLUGINS_DIR`, `CATS_BIN_DIR`, `XDG_*` and the
  sockets all in the scratch dir, and a hand-written `failed` entry. The mark
  arrived from the connect burst, and the tooltip named cats-todo. **dismiss**
  in the dialog cleared the mark and the tooltip. The push at the end of a
  real seed pass was not watched live; the unit tests cover it, and N-045 now
  asks for it.

Docs: a `docs/protocols/browser-protocol.md` table row, a paragraph in
`docs/protocols/control-api.md`, and a "The toolbar flags them too" bullet in
`docs/subsystems/plugins.md`.

## 3. Gotchas

- **Chrome zoom coordinates.** The `zoom` action's region is in the last
  screenshot's frame. After a scaled screenshot (1311 px wide) the zoom
  rejected in-bounds CSS-pixel regions as "exceeds viewport". Taking a fresh
  screenshot and zooming in its frame worked.
- **Built binaries.** `go build ./cmd/catway` from the repo root writes the
  gitignored `./catway`, and `make binaries` rewrites the gitignored `bin/`.
  Both were rebuilt from the current tree.
- **Restarting the scratch catway** had to be done by PID (`lsof -t
  -iTCP:18517`). The CSS is embedded, so a style change needs a rebuild and a
  restart.

## Next

Closed: N-047, N-048. Declined: None. Raised: N-049.
Deferred: None. Promoted: None.
Updated: N-045. Full list: `ai_docs/todo/next-list.md`.
