# Session: Picker Height, a Stale Tab Pin, and One-Pass Install

- **Session ID:** `7254be66-ddb0-4fcd-9e9a-8134e56f51d2`
- **Date:** 2026-07-29
- **Branch:** main

Three loosely related items, each starting from something the user saw on screen:
a picker that showed too few rows, a plugin tab wearing the wrong name, and two
installs of cats that had drifted apart.

## 1. Start-path picker: 15 rows instead of ~10

> Make the max height of the Start Path section to accommodate 15 rows instead
> of about 10.

`cmd/catway/web/index.html:298` — `.picker` `max-height` 210px → 336px.

A row is ~22px (16px line box + 3px padding top and bottom), so 15 rows is 330px,
plus the picker's own 6px of vertical padding = 336px. The list still scrolls past
that; only the window onto it grew.

## 2. "Cats Todo — this project" on a plugin tab: stale data, not stale code

> My cats-todo plugin's tab when launched at project scope simply says "Cats
> Todo - this project", I want it to say "Todo - <project-name>". I thought this
> work was already done.

It **was** done. The diagnosis walked the whole chain, and every code link was
already correct:

- `cats-todo` advertises OSC title `todo: <project>` (`context.go:88`,
  `paneTitle`).
- cats' tab auto-naming picks the OSC title up at rung 2
  (`internal/app/tabname.go:76`).
- Both launch paths deliberately send **no** title, precisely so auto-naming
  stays live: `cmd/catctl/plugin.go:173` and `cmd/catway/web/index.html:2360`.
- The running build (`/Applications/Cats.app`, `4f5171d`) contained that fix —
  confirmed by `strings`-grepping the embedded UI for its comment marker.

The break was in **persisted state**. Auto-naming applies only while
`CustomName == ""` (`internal/workspace/tab.go:71`), and `~/.local/state/cats/session.json`
still carried pins written by an older build:

```
ws "cdx" tab 4: custom_name "Cats Todo — this project"
ws "ced" tab 2: custom_name "Cats Todo — this project"
```

A pin wins forever, so those two tabs could never re-derive their name. That the
PANES sidebar showed `todo: ced` correctly the whole time was the tell: pane
labels read the OSC title live, tab names go through the pin.

**Fix (data, no code change).** `tab.rename` with an empty name clears the pin
(`internal/app/command_vocab.go:385`), so:

```
catctl rename-tab 4 ""   # ws cdx  → "todo: cats"
catctl ws wD
catctl rename-tab 2 ""   # ws ced  → "todo: ced"
catctl ws wC             # restore the active workspace
```

The user chose to keep the `todo: <project>` wording rather than change
`paneTitle` to `Todo — <project>`, since that string also labels the panes that
already read correctly.

**Lesson worth keeping:** a pinned name is permanent state. When a build stops
pinning, already-created tabs do not heal themselves — the session file has to be
cleared for the fix to become visible.

## 3. Two installs, two builds

Hunting the stale build surfaced three copies of the daemons in play:

| location | version | state |
| --- | --- | --- |
| `/Applications/Cats.app` | `4f5171d` | current, and the running process |
| `~/Applications/Cats.app` | `54e7d5c-dirty` | Jul 27, predates tab auto-naming |
| `~/bin/{catway,cathost,catctl}` | — | Jul 27 |

`54e7d5c` predates `804096e` ("auto-name tabs from their panes"), and its
embedded UI proved it: no `Deliberately no title` marker, no `tab drills in`
picker hint. So that bundle would still re-pin the manifest title on any plugin
tab it opened.

`CFBundleShortVersionString` is the fast way to tell two installs apart — it
carries git-describe output, so a `-dirty` suffix also flags a bundle built from
an uncommitted tree.

`~/Applications/Cats.app` was deleted (build outputs only, no user data), leaving
one registered copy of bundle ID `dev.cats.app`. Two copies of one bundle ID make
"which build launches" a LaunchServices coin toss — the real hazard of keeping
both, more than the disk space.

## 4. Makefile: one build, both installs

> Please change the macapp target to build local simultaneously so there is no
> drift.

`make macapp` only ever wrote `dist/Cats.app` (`scripts/build-macapp.sh:29-30`) —
no install step at all, which is exactly how the two bundles drifted: each
installed copy was a frozen snapshot of whatever `dist/` held when someone last
copied it by hand.

**`local` now installs rather than compiling** (`Makefile:79-95`): it depends on
`binaries` and copies `bin/<cmd>` → `~/bin/<alias>`.

- Copying, not a second `go build`, is the point. Two compiles of the same source
  can still differ (a mid-build edit, a stale `PKG_CONFIG_PATH`); "same bytes" is
  the only version claim worth making.
- Copy-to-`.new`-then-`mv`, because overwriting a *running* binary in place fails
  with `ETXTBSY` on macOS. A rename swaps the directory entry and leaves the live
  process on its old inode.
- New constraint, noted in the comment: every `LOCAL_MAP` cmd must also be in
  `BINS`, since nothing is compiled per-alias any more.

**`macapp: binaries local`** — one `binaries` build feeds the bundle and `~/bin`
both, so they cannot drift.

## 5. Installing to /Applications

> I would like to install to /Applications, but think there might be a permission
> issue. Okay to give it a shot and find out.

No permission issue: `/Applications` is `drwxrwxr-x root:admin` and the account is
in `admin`, so group write covers it with no `sudo`.

- `APP_DEST := /Applications` (`Makefile:106-112`), overridable —
  `make macapp APP_DEST=~/Applications` needs no privilege at all. A non-admin
  account needs `sudo`.
- The install stages `Cats.app.new` beside the target, then `rm -rf` + `mv`. It
  replaces the bundle **wholesale**: `cp -R` over a live bundle merges, leaving
  files the new build no longer ships behind in `Contents/MacOS`. Staging on the
  same filesystem makes the swap a rename.
- Removing the bundle of a *running* app is safe — the live process holds its
  inodes — but it is then not the version on disk, which is why the recipe prints
  `CFBundleShortVersionString` as its last line.

A Makefile comment that started life inside the recipe was moved above the target:
recipe comments get echoed to the terminal as shell comments.

## Verification

- `make -n macapp` → `binaries` → `local` → `build-macapp.sh` → install, with
  `binaries` running exactly once.
- Full `make macapp` run succeeded end to end.
- sha256 identical across `bin/`, `~/bin/`, and
  `/Applications/Cats.app/Contents/MacOS/` for all three daemons; no `.new`
  leftovers; one `Cats.app` in `/Applications`.
- Tab names after clearing the pins: `todo: cats` (cdx tab 4), `todo: ced`
  (ced tab 2), via `catctl tabs`.

## Loose ends

- The picker height was not visually confirmed — the user opted to test it.
- The running app (started 19:27) is still the pre-install `/Applications` copy;
  its sidebar sha will not match disk until relaunch.
- `VERSION` read `4f5171d-dirty` during this session because the Makefile edits
  were uncommitted at build time.
