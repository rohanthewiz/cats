# Session: multi-window — a connection is a view, not a mirror

- Session id: `c6e6fde7-c9a8-4923-9fe1-0e39bcc9f8c7`
- Date: 2026-08-19
- Branch: `worktree/multi-window` (worktree at `~/.cats/worktrees/cats/worktree-multi-window`),
  fast-forwarded into `main`
- Plan: `ai_docs/plans/multi-window.md` — all four phases done
- Commits: cats `942e7f8`; cats-mobile `55eec71`
- Predecessor: `2026-0819-1459-record-a-macro.md`

One ask: implement `ai_docs/plans/multi-window.md`. It was implemented end to
end — the per-connection viewport, the page, the native Mac windows, and the
phase-4 polish — then merged to `main`, pushed, and the Dart wire layer
regenerated and pushed in cats-mobile.

The plan's own framing turned out to be exactly right and is worth restating,
because everything below follows from it: **the browser protocol already
described this world and the implementation never built it.** §Visibility
filtering said "is the pane in *this connection's* active workspace + tab" and
§Multiple connections listed "the connection's viewport" as per-connection
state. In code there was one `Session.active`, one `orch.visible`, one
`orch.area`. The work was closing that gap, not inventing a feature.

## Phase 1 — the view

### The shape

`view{ws, area, visible}` hangs off `client` (`cmd/catway/view.go`). Three
things fell out of it that are worth naming separately, because they are three
different questions that used to be one:

- **"Is anyone looking?"** — `o.visible` became the *union* of every view's set.
  That is what the unseen badges, the branch refresh, the agent chrome and the
  output scanners actually want, and it is what decides whether a frame is worth
  translating at all.
- **"Is *this window* looking?"** — the per-view set, which gates addressed
  input (`key{pane:N}`, mouse) and decides which connections a pane-scoped
  message reaches. Every `if o.visible[pid] { o.broadcast(...) }` became
  `o.sendVisible(pid, msg)`.
- **"What grid is this workspace laid out against?"** — `o.wsArea[wsID]`.
  `desiredGrids` used to size *every* tab of *every* workspace from one shared
  area, which is the bug that made a second window unusable: window 2's 120×40
  reflowed window 1's 200×60 the moment it connected.

`orch.area` survives as *the primary view's* grid, because `Backend.Area()` and
the `Clients` census still need one number.

### The primary view

`o.focusOrder` is the connections in OS-focus recency order; `primaryView()` is
the first non-viewer still connected. It costs nothing — a `Focus` report
already arrived for every foreground change — and it is what every caller with
no window of its own resolves through: `catctl`, hook actions, runbook steps,
`ui.notify` click-throughs, and every viewer (the phone). `Session.active`
tracks it via `syncPrimaryActive()`, so persistence still means "where you were"
and a cold start opens there.

Two ordering bugs showed up here and both are recorded in the code:

- The primary must be claimed **before** the `c.focused == m.Focused` early
  return, or a window that never reported a blur (it connected while already in
  front, or an older page that only reports focus-in) can never become primary,
  and the whole session keeps resolving through a window the user has left.
- `noteViewArea` on focus-in must be in the same pre-guard block, for the same
  reason — and *after* `c.focused` is set it would have broken
  `TestWindowBlurReachesTheFocusedPane`, which is how the first attempt was
  caught.

### Commands carry their view

Inside `internal/app`, every `Session` method that read `s.active` implicitly
grew an explicit `…In(workspaceID)` twin and the old name delegates with `""` —
the `CreateTabIn` pattern, finished. `internal/app/view.go` holds the new forms;
`session.go`'s originals became one-line delegations.

`Dispatcher` gained a `View`; `NewDispatcherFor(s, b, view)` + `viewBackend`
(embedding `*orch`, overriding `Area()` and `SetViewWorkspace()`) route a browser
command through the window that issued it. `workspace.focus` stopped being a
session mutation and became a `Backend.SetViewWorkspace` effect — which is what
makes it move only the window that sent it.

The guard that matters most for the future is
`TestDispatcherUsesNoViewportImplicitSessionCall`: it parses `commands.go`'s AST
and fails on any `d.session.X` whose workspace is implicit, naming the `…In`
form to use instead. The next command added has to answer the question rather
than quietly defaulting to the primary window's workspace.

### Three places the view had to be threaded by hand

- `BroadcastLayout` had to start refreshing viewports. It is documented as
  "focus moved, pane set unchanged", which is true of the *session* and false of
  a *window*: `pane.focus` onto a pane in another workspace takes that window
  with it. (It also fixes a latent pre-existing bug — focusing a different pane
  inside a zoomed tab changes what the tab shows.)
- `worktree.open` / `worktree.create` focus or create a workspace, so they are
  workspace switches by another name. They are `Backend` methods with no view, so
  `viewBackend` overrides them to pass the issuing client through.
- The dispatcher's `tab.move_to_workspace` must resolve `from == ""` to
  `d.viewWorkspaceID()` before calling `MoveTabTo`, which reads an empty source
  as the *session's* active workspace. Caught by a test that failed with "the tab
  is already in that workspace" — the same class of bug the AST guard exists for,
  in a method the guard does not cover because it takes an explicit id.

### Protocol

Additive, no version bump: `Init.workspace`, `Clients.views[]` of the new
`ClientView{workspace, cols, rows, focused, viewer, primary}`, and capability
`window` so a client can tell "the server honoured my workspace" from "the
server dropped it".

## Phase 2 — the page

`?ws=<id>` → `init.workspace`. "Open in new window" on the sidebar row and in
the ⌘K palette. An "also open in another window" mark from the census.

The piece not in the plan, added because phase 3 needs it: the page keeps its
own URL in step with the workspace it shows (`syncWindowURL` →
`history.replaceState`). A reload then lands back on the same workspace, and —
the reason it exists — the Mac app can read `?ws=` off each window's live URL to
remember the layout, so a workspace switch *inside* a window is recorded with no
extra protocol at all.

`catctl probe` gained `--workspace` plus `viewws:` and `views:` ops.

## Phase 3 — Mac windows

`cmd/catapp/window_darwin.{m,go}`: one `NSWindow` + `WKWebView` per window, our
own `CatsAppDelegate`, Window menu with New Window ⌘N, `window.open` intercepted
in `WKUIDelegate` (so the sidebar action works with zero app-specific JS),
clipboard and connect-form bridges as script message handlers, and window
restore in `appConfig.Windows`.

Decisions taken while writing it:

- **Both modes moved, not just local.** The plan deferred the remote connect
  form to `webview_go`, but that cannot coexist with our own `NSApp` delegate —
  `webview.New` replaces it and `Run()` is `[NSApp run]`. The form turned out to
  be three fire-and-forget callbacks, so a `catsApp` message handler plus a
  user-script shim was cheaper than leaving mode 2 single-window. `webview_go`
  now backs only the startup error sheet.
- **`WKProcessPool` dropped.** Deprecated and a no-op since macOS 12 — every web
  view in a process already shares one. `WKWebsiteDataStore.defaultDataStore` is
  what actually makes one login cookie serve every window.
- **A main-thread pump** (`mainQueue` + `catsDispatchMain` → `catappMainTick`),
  because the window snapshot reads `NSWindow.frame` and is armed from a debounce
  timer on a goroutine.
- **`setFrameAutosaveName` deliberately unused** — it keys by a fixed name, and N
  windows need N frames we choose the names of. `app.json` is that.

## Phase 4 — polish

- **Focused-window-wins sizing.** Two windows on one workspace mirror, so exactly
  one grid can be right for both, and the one to pick is the window in front.
  `areaFor` walks `focusOrder`; a focus change reconciles only when
  `sharedSizerWorkspace` says two sizers actually differ, so a single-window
  session pays nothing.
- **Per-window titles.** `c.view.title` replaces the one `o.lastTitle` gate.
- **`tab.move_to_workspace`** — `Session.MoveTabTo` over new
  `Workspace.DetachTab` / `AdoptTab`. Panes and their terminals travel unchanged;
  the tab and its panes are renumbered on arrival, since both are per workspace.
  The consequence is documented rather than fixed: a moved pane's
  `CATS_PANE_ID` still names its old handle, because it was set when the process
  spawned. Nothing breaks (`p_<raw>` resolves anywhere), but a cached `w1:p3` is
  stale.

## Verification

`make test`, `make test-ghostty`, `make race-ghostty`, `make vet`,
`make vet-ghostty` all green; catgen-dart goldens regenerate to no diff.

The soak the plan asked for, run headlessly: a scratch `cathost` + `catway` on
`127.0.0.1:8799`, two `catctl probe` connections.

- A on `w1` at 200×60 held **200 cols** while B was connected on `w2` at 100×30,
  and B held 100. That is the reflow bug, proven gone.
- Each view saw only its own workspace's frames.
- `workspace.focus` from A moved **only A**; B stayed on `w1`.

Both probes PASS. Sockets had to live at `/tmp/cats-mw-*.sock` rather than in
the scratchpad — the unix socket path caps around 104 bytes and the scratchpad
path is longer than that.

## Two things worth flagging

- **`make macapp` installs.** Running it as the plan's verification step also
  copied `catway`/`cathost`/`catctl` into `~/bin` and replaced
  `/Applications/Cats.app` with this branch's build. Reported at the time; not
  reverted.
- **`tool/regen.sh` defaults to `../cats`**, which is the *primary* worktree —
  and that is sitting on `fix/agent-name-bug` at `2613ae6`, not `main`. The
  default would have pinned `CATS_REV` to the wrong commit and generated from
  pre-multi-window code. Pointed it at the multi-window worktree instead (exactly
  `main` at `942e7f8`) and left the primary checkout alone. **This is a trap any
  future regen from a worktree will hit.**

## Merge, push, mobile

`main` had moved to `69c21b1` (the agent-name-bug fix, PR #3) — fetched, local
`main` fast-forwarded, branch rebased onto it. Clean, despite upstream touching
`agentmodel.go`, `catway.go` and `daemon.go`, all three of which this work also
changed. Re-ran everything after the rebase rather than trusting the clean merge,
and checked that upstream had introduced no new `if o.visible[...] { broadcast }`
sites needing conversion. It had not.

`main` is not checked out in any worktree, so the merge was a ref move
(`git fetch . worktree/multi-window:main`) — a strict fast-forward, keeping the
linear history the rebase was for. Pushed.

cats-mobile: regenerated against `942e7f8`, `dart test` green (72), `dart
analyze` clean, generated files byte-identical to cats's golden, `keys.g.dart`
untouched (`FLUTTER_ROOT` unset). Committed as `55eec71` and pushed.

The phone's behaviour is unchanged, which is what the plan asked to *verify*
rather than alter: `CatsConnection._handshake` still sends `viewer: true` with
zero geometry and no workspace, and the generated `toJson` omits `workspace`
while empty — so it follows the primary view, whichever desktop window the user
touched last.

## Open

- **The Mac manual runbook has not been run on a device.** It is written into
  `standalone-mac.md` (⌘N, open-in-new-window, close the primary, quit with three
  windows and relaunch). The Objective-C is compile-verified and the bundle
  builds and installs clean, but nothing has driven the GUI. This is the one
  piece of the work with no test behind it.
- **`Clients.views` in the mobile UI** — showing which desktop window the phone
  is following and letting it pick another. App work, and the Flutter app package
  is still commented out of cats-mobile's workspace `pubspec.yaml`, so there is
  no UI to put it in yet.
- **Tab-level independence inside one workspace** is documented as mirroring,
  not as a limitation to fix (plan decision 2). Two windows on one workspace
  would need two focuses in one `TileLayout` and two zoom flags on one tab.
