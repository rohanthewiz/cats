# Session: plugins dialog row layout + cats-todo scope-pinned actions

Session ID: `7cbbef7b-28e3-4b06-9185-1e37c6ac7010`
Date: 2026-07-26 (continuation of the same session as the 0825 doc — this one
covers everything after that wrap)

## cats — plugins dialog row layout (`225a088`, pushed)

After the v0.2.0 plugin update landed, the dialog row read
`rohanthewiz.cats-todo v0.2.0 — ` with the name truncated behind the three
action buttons. Two causes, both in `cmd/catway/web/index.html`:

- The palette's `.kind` slot reserves a fixed 52px even when a normally
  installed plugin has no status to show. The status span is now only created
  when there IS a status (linked/broken), and `.modal.plugins .row .kind`
  gets `width:auto` so it hugs its text.
- The plugins modal widened 520px → 620px (within `.modal`'s existing 640px
  max-width cap).

Reminder: `index.html` is `go:embed`ded into catway — layout changes need a
rebuild + server restart to show at :8421.

## cats-todo — v0.2.2 bump, then scope-pinned actions (v0.2.3)

- `296a8ea` — user-requested version bump to 0.2.2 (manifest + main.go const).
- `7cfee2b` — **the real change**: the user saw "gowex + global" in the header
  of a "this project" launch (an empty gowex backlog showing only global rows
  under a project label). That was the manager's historical merged view doing
  its job — but under the picker's "this project" label it reads as leakage.
  Now each plugin action is pinned to a single backlog:
  - `GlobalOnly bool` generalized to `launchScope` (`launchBoth` /
    `launchProjectOnly` / `launchGlobalOnly`) on `RunContext.Scope`.
  - New `-p|--project` mirrors `-g|--global`: `loadStores` withholds the far
    store (and skips resolving the global path a project-only launch never
    reads). Project-only still walks up to the project root, and new-session
    drops still root there.
  - Header note: project-only shows `<project> only` (global-only already
    showed "global only"). Pane titles: `todo: global` vs `todo: <project>`.
  - The add form's ctrl+g scope toggle + its footer hint now require BOTH
    stores available, so an only-mode can't save into an unavailable store
    (which writes to nothing).
  - Manifest: "Cats Todo — this project" runs `--project`; "— global only"
    runs `--global`. Bare `cats-todo` in a shell keeps the merged view.
    NOTE: `catctl plugin run rohanthewiz.cats-todo` (first action = default)
    is therefore project-only now.
  - Version 0.2.3; README + help text updated; tests cover the walk-skip,
    walk-keep, titles, and both only-mode `loadStores` cases.

## Verified working (user screenshots)

- Plugin update tab: `updated rohanthewiz.cats-todo to v0.2.0` (then 0.2.2 /
  0.2.3 by the user afterward).
- The dialog "run" button opens the picker with both actions.

## State at session end

- cats `main` = `225a088` (row layout), pushed.
- cats-todo `main` = `7cfee2b` (v0.2.3), pushed; session docs now live in
  cats-todo's own `ai_docs/claude_sessions/` too (started this session).
- User updates the installed plugin themselves via the dialog's update button.
