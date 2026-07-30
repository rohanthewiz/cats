# Session: A Start-Path Picker on the New-Workspace Dialog (cdx-backed)

- **Session ID:** `747b68f2-26d3-423e-becf-f368d7f4a17f`
- **Date:** 2026-07-29
- **Branch:** main

## Request

> Need a path picker on new workspace popup. Use local `cdx` in `~/bin/cdx` or
> code at `~/projs/go/cdx`

The new-workspace dialog already had a **start path** field (prefilled with the
focused pane's live cwd), but it was a bare text box: no view of what is on the
server's disk, no recents, nothing to complete against.

## Design decisions

### 1. Read cdx's state file, don't shell out to cdx

`cdx` is a TUI: it draws on stderr and prints the chosen path on stdout, so it
cannot be embedded in a browser dialog. The reusable part is its **memory** —
`~/Library/Application Support/cdx/state.json` (`os.UserConfigDir()/cdx`), which
its `chpwd` hook keeps current for every shell `cd`.

So `internal/pathpick` reads that file directly and reproduces cdx's frecency
ranking (visit count × recency weight, zoxide-style: `<1h` ×4, `<1d` ×2, `<1wk`
×0.5, else ×0.25) so the picker offers the same order cdx itself shows. No
subprocess per keystroke, nothing to install, and **no new go.mod dependency** —
cdx's own ranking uses `github.com/sahilm/fuzzy`, which we avoid because the
fuzzy matching happens in the browser (see 3).

Degradation matters: no cdx ⇒ no recents. So `catway` appends the **session's
own live pane cwds** behind cdx's list (`liveCwds` + `mergeDirs`), which means a
machine without cdx still gets a useful list seeded with the projects open right
now.

### 2. A new §7 command, `path.list`

Read-only but filesystem-touching, so it went on the `Backend` seam as
`StartPathList` (off-loop, like the worktree/plugin commands — a cold network
mount must not stall the orchestrator):

```
path.list {dir?, pane?, recents?}
  → {dir, cwd, home, exists, error?, dirs[], truncated?, recents[]}
```

`dir` is a path **as typed** — `~`, `$VAR`, relative, absolute — resolved
leniently by `pathpick.Expand`. Relative resolves against the addressed (or
focused) pane's live cwd; `worktrees.go`'s `worktreePaneCwd` was renamed
**`anchorPaneCwd`** since two features now share it.

Key protocol choice: a path that does not resolve answers `exists:false` +
`error` **rather than failing the command**. Mid-typing is the normal state of a
path; a failed `cmd_result` per keystroke would just spray toasts.

### 3. Filter client-side, list server-side

`path.list` returns one directory's *whole* listing (sorted, hidden names
included, `truncated` past `MaxDirs`=2000) and ranks nothing against a query. The
front end caches per directory and fuzzy-matches locally with the palette's
existing `fuzzyScore`.

Why: cats supports a **web client with a remote server** topology. A round trip
per keystroke would feel awful over a WAN; this way keystrokes inside a directory
are local and a round trip happens only when you walk into a new directory.

### 4. Picker semantics, borrowed from cdx

Two modes, as in `cdx`'s `computeCandidates`:

| Typed | Mode | Candidates |
|-------|------|-----------|
| no `/` yet | fuzzy | recents + the anchor directory's children, ranked together |
| a path under way | completion | children of the named directory, prefix matches first, fuzzy as fallback |

Keys: `↑↓` / `^n^p` move · `Tab` (or click) completes **and drills in** · `Enter`
creates in the highlighted directory · a leading `.` includes dotted dirs.

Two subtleties that took thought:

- **Default selection.** A non-empty fragment selects row 0 (cdx's "Enter cds to
  the selection"), but an *empty* fragment — a trailing `/`, or the untouched
  prefill — selects **nothing**, because there the text already names the
  directory the user wants. Without that split, typing `~/projs/go/cats/` and
  pressing Enter would have created the workspace in `cats/cmd`.
- **Committed values are absolute.** `commit()` (called by `dialogFields` just
  before it reads the fields) rewrites the field to the chosen row's absolute
  path, or the resolved form of what was typed. Otherwise a relative path could
  be resolved against a *different* base by `workspace.create` than the picker
  showed it against. The one exception is a **cleared field**, which stays empty
  — that is the documented "start in my home directory" escape hatch.

## What landed

| File | Change |
|------|--------|
| `internal/pathpick/pathpick.go` (+`_test.go`) | new: cdx frecency recents, `Subdirs`, lenient `Expand` |
| `cmd/catway/paths.go` (+`_test.go`) | new: `StartPathList`, `liveCwds`, `mergeDirs`, `listErr` |
| `internal/app/command_vocab.go` | `CmdPathList`, `PathListParams`/`Result`, `CommandNames` |
| `internal/app/commands.go` | `Backend.StartPathList` + dispatch case (result-only) |
| `internal/app/commands_test.go` | fake backend method + `TestDispatchPathList` |
| `internal/browserproto/cmd.go` | wire aliases |
| `cmd/catway/worktrees.go` | `worktreePaneCwd` → `anchorPaneCwd` (shared anchor) |
| `cmd/catway/web/index.html` | `attachPathPicker`, `.picker` CSS, `dialogFields` `pick:` flag, help section, `newWorkspace` wiring |
| `docs/protocols/control-api.md` | `path.list` row + rationale paragraphs |

`dialogFields` grew one field flag (`pick: true`) and a `commit()` hook, so the
worktree dialogs' path fields can adopt the same picker later with one word.
Deliberately not done here — out of the asked scope.

## Verification

Server half, through the control API against a scratch instance:

```
catctl --socket /tmp/cats-pp-ctl.sock path.list --params '{"dir":"~/projs/","recents":true}'
```

`~` and `$HOME` expand, `internal/` resolves against the pane's cwd, `~/nope/`
answers `exists:false, error:"no such directory: …"`, `go.mod` answers `"not a
directory: …"`, `/etc/ssl/private/` (unreadable) degrades cleanly, and cdx's
frecency list came back in cdx's order.

Browser half, driven in headless Chrome over CDP (a small `websockets`-based
driver in the scratchpad: `Runtime.evaluate` + `Input.dispatchKeyEvent` +
`Page.captureScreenshot`). Confirmed, with screenshots:

```
=== opened, path field focused (prefilled with the pane's cwd)
  value: '~/projs/go/cats'   head='in ~/projs/go'
    >dir cats/
     dir cats-todo/

=== empty field: cdx recents, then subdirectories here
    recent ~/projs/go/ced · recent ~/projs/go · recent ~ · …

=== typed '~/projs/go/ca' → ArrowDown → Tab
  value: '~/projs/go/cats-todo/'   head='in ~/projs/go/cats-todo'

=== typed '~/nope/x'
    note: no such directory: ~/nope

=== after Enter        workspaces: ['○ cats', '● cats-todo']
```

Also verified: fuzzy `cats` reaching a cdx recent, `^n`/`^p`, mousedown-to-drill
(no blur), Enter on a highlighted **recent** creating there (`● dbc`), and the
cleared-field escape hatch creating `● ~`. `make check` exits 0 (fmt, vet, build,
test, vet-ghostty, race-ghostty).

## Gotchas / notes for next time

- **macOS unix sockets still cap at ~104 chars.** The scratchpad path blew past
  it again; every dial failed with `connect: invalid argument`, which reads like
  a missing socket. Dev sockets must live at `/tmp/cats-pp-*.sock`.
- **`cathost --exit-on-disconnect` dies with the first catway.** Restarting only
  catway then loops on `no such file or directory` — restart the daemon too, or
  drop the flag for a dev session.
- **A stale catway holds the port.** After a failed start, `lsof -nP -iTCP:8499`
  and check `ps` before killing: the user's **MacApp** catway
  (`/Applications/Cats.app/...`) shows up in `pgrep catway` and must not be
  touched.
- **CDP beats `--dump-dom` for this.** `Input.dispatchKeyEvent` is what exercises
  the real keydown path (arrows, Tab); `Input.insertText` fires `input` but no
  keydown, so use it only for typing. Python's `websockets.sync.client` is
  already installed — no npm needed.
- **`text-transform:uppercase` on a path is wrong.** The picker's head line first
  rendered `~/PROJS/GO/CATS`; paths are not labels. It now reads `in ~/projs/go`.
- **Hidden-file policy belongs to the front end.** The server returns dotted
  directories always; only a leading `.` in the typed fragment shows them. Keeps
  `path.list` free of query semantics.
- `pathpick.Recents` prunes vanished dirs and `mergeDirs` re-stats — a small
  double stat, kept because each has its own contract.
- gopls still flags `cmd/catway/*.go` (incl. the new `paths.go`) as excluded
  without `-tags ghostty`. Harmless.
- The MacApp needs a rebuilt bundle (`make macapp` / `make local`) to show the
  picker — the recurring stale-bundle trap.
