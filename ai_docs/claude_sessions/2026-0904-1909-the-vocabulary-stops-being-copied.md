# The vocabulary stops being copied

Session: https://claude.ai/code/session_01AaoUtxuk5QmaSW7YqYSxJj
Date: 2026-09-04
Repos: `~/projs/go/cats` (read only), `~/projs/go/cats-todo`, `~/projs/go/cats-mobile`

## Request

> update the protocol to cats-todo and cats-mobile accordingly

Following on from the sleep/clean work in `5d1e4a6` (see
`2026-0904-1846-sleeping-and-cleaning-workspaces.md`).

## What actually changed on the wire

`5d1e4a6` touched exactly two files under `wire/`, and nothing else downstream
could see:

- `wire/vocab.go` — `CmdWorkspaceClean` / `.Sleep` / `.Wake`, their three
  `commandSpecs` rows, `CleanWorkspaceParams`, `CleanWorkspaceResult`,
  `ParkedAgentInfo`, and `asleep` / `parked` on `WorkspaceEntry`.
- `wire/down.go` — `asleep` / `parked` on `WorkspaceInfo`.

No new down-message *type*. That is the fact that made cats-mobile's side a
one-liner.

## cats-mobile — a pin bump (`6d5c5d8`)

The phone already imports `github.com/rohanthewiz/cats/wire` directly, and its
`go.mod` comment says so in as many words: the require line *is* the pin.

```
go get github.com/rohanthewiz/cats@5d1e4a6716fe89a8e6cd60e27289a8eedb191cf5
go mod tidy
```

`d58ce46` → `5d1e4a6`. Build, vet and `go test ./...` green, including
`TestEveryDownTypeHasAnArm` — nothing new to arm.

**Nothing to render, and that is correct.** The phone lists the *window census*
(`app/windows.go`, one row per `WindowInfo`), not the workspace list. A sleeping
workspace has no window, so it leaves the list on its own. `Locked` is not drawn
either, so `Asleep` needing no glyph is consistent with what was already there.

## cats-todo — the copy goes away (`6d8eb6d`)

### Why it was a copy, and why it stopped being one

`internal/app/command_vocab.go` was a hand-maintained copy of cats'
`internal/app/command_vocab.go`. Its own header gave the reason:

> …which cannot be imported across the module boundary because it lives under
> `internal/`.

That reason died with cats' `c0a250f` ("wire: carve the browser protocol out of
internal into a leaf package", 2026-09-02), which moved the vocabulary to the
public `wire` package. Inside cats, `internal/app/wire_aliases.go` re-exports it,
so `app.Cmd*` and `app.PaneInfo` there are aliases — there is only one
declaration left anywhere.

Meanwhile the copy had drifted **~1200 lines behind** (last synced ~2026-08-19).
It kept working only because cats-todo uses a small subset of the vocabulary.

### The migration

Every symbol cats-todo used was already in `wire`; the `app.Dispatcher` /
`app.JSONParamDecoder` / `app.EventPane*` references turned out to be comments
only. So:

1. `go get github.com/rohanthewiz/cats@5d1e4a6 && go mod tidy`
2. `git rm internal/app/command_vocab.go` (the package had no other file)
3. `app.X` → `wire.X` across `client.go`, `context.go`, `drop.go`, `ui.go`,
   `context_test.go`, `schedule_test.go`; import swapped and `gofmt`'d, since
   `cats/wire` sorts differently than `cats-todo/internal/app`.

**One rename to absorb:** `app.WorkspaceInfo` (the `workspace.list` row) is
`wire.WorkspaceEntry` now — in `wire`, `WorkspaceInfo` is the *layout
down-message's* type. `workspaceList()` and the `export.go` comment follow it.

`go.sum` grew by **two lines**. `wire` imports nothing outside the standard
library, which is what makes taking the dependency free rather than a trade.

### No behavioral change was needed — checked, not assumed

- `workspaceList` feeds only `workspaceLabels` and `gatherExportSources`. Export
  targets are project *roots* resolved from each workspace's pane cwd; a
  sleeping workspace has no live PTY, so it reports no cwd and drops out of the
  picker on its own.
- Every `tab.create` cats-todo sends omits `Workspace`, so it lands in the active
  workspace — which cats guarantees is never asleep (`SleepWorkspace` moves the
  active index away and refuses the last awake one). The "refused, use
  workspace.wake" path is unreachable from here.

### Prose that would have misled the next person

- `internal/ctlproto/proto.go` — its comments pointed at `app.Cmd*`,
  `app.CommandNames()`, `app.ReadResult`, `internal/app`. Retargeted at `wire`
  where the symbol moved there; left as "the server's dispatcher" for
  `Dispatcher`/`JSONParamDecoder`, and as literal strings for the event names,
  which stay in cats' `internal/app/events.go` and are *not* in `wire`.
- `README.md` "How it talks to cats" and
  `.claude/skills/cats-todo-dev/SKILL.md` (the file table and the "Lockstep with
  cats" contract) — both still instructed a `cp` of a file that no longer exists.
- `go.mod` gained a note at the require line, mirroring cats-mobile's.

## What is still copied

`internal/ctlproto/{client,proto}.go` (the socket envelope) and
`internal/integration` (the `CATS_PANE_ID` sliver). Both are still under cats'
`internal/`, so the original argument holds for them. Neither changed in
`5d1e4a6`. Note that cats-todo's `ctlproto` is itself an older copy — cats has
grown `server.go`, `stream.go` and more methods since — but that is the transport
envelope, untouched by this protocol change, and was left alone.

## Not done / next

- cats-todo's version was **not** bumped. The dev skill's two-place bump
  (`const version` in `main.go` + `version =` in `cats-plugin.toml`) is for a
  feature or fix; this is internal plumbing with no user-visible change.
- cats-todo's `internal/ctlproto` re-sync against cats' current file is still
  outstanding, whenever the envelope next matters.
- Nothing in cats itself changed this session; it was read only.

## Commits

- cats-mobile `6d5c5d8` — "wire: pin cats at the sleep/clean protocol"
- cats-todo `6d8eb6d` — "wire: import cats' vocabulary instead of copying it"

Both pushed to `main`.
