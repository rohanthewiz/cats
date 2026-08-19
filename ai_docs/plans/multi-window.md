# cats multi-window — one session, many views

## Context

Today cats has one view, mirrored. A second browser, or a second copy of the Mac app, is
"a second view, not a second workspace set" (`docs/architecture/web-client-mac-server.md`):
`workspace.focus` in one window switches the other, keystrokes go to *the* focused pane,
and the last client to `resize` sets pane sizes for everyone. That was the right first cut
— it made the phone viewer and the multi-host session possible without a notion of
"which window" anywhere in the model — but it means the obvious desktop workflow, a
window per project on a second monitor, is not possible on any topology.

The ask is: **open Cats as several windows, each showing its own workspace, each with
its own tab, focus and size, over the one running session.** Closing a window must never
close anything in the session; a workspace no window shows keeps running exactly as a
background workspace does today.

The protocol already describes this world and the implementation never built it: the
browser protocol's visibility section filters on "*this connection's* active workspace +
tab" and lists "the connection's viewport (which workspace/tab it is showing)" as
per-connection state (`docs/protocols/browser-protocol.md` §Visibility filtering,
§Multiple connections). In code the viewport is `orch.visible`, `orch.area` and
`Session.active` — one of each, shared. This plan closes that gap, and then gives the
Mac app the windows to use it.

## Verified facts that shape the design

> Line numbers below were read against the working tree on 2026-08-19 (which had
> unrelated edits in flight in `cmd/catway/catway.go` and `internal/app`), so they
> will have drifted by the time a phase starts. Treat them as a hint, not an
> address — grep for the named symbol.

- **One viewport, three globals.** `Session.active` (`internal/app/session.go:50`) names
  the active workspace; each `workspace.Workspace` has its own active tab; each tab's
  `layout.TileLayout` has its own focused pane. The *session's* focused pane is the
  composition active ws → active tab → focused leaf (`Session.FocusedPane`,
  `session.go:65`). `orch.visible` (`cmd/catway/catway.go:156`) is the pane set whose
  frames stream, recomputed from `Session.VisiblePaneIDs()`; `orch.area`
  (`catway.go:152`) is the one grid every pane is sized against.
- **All panes are already live and sized.** `desiredGrids` (`catway.go:736`) sizes *every*
  tab of *every* workspace from its own BSP tree over the shared area — off-screen tabs are
  not parked, they are just not streamed. Showing a second workspace in a second window
  therefore needs no PTY work at all; it needs a second area and a second visible set.
- **Frame fan-out is already per connection.** `client.trans` holds one
  `FrameTranslator` per pane per connection (`catway.go:2074`), and `registerConn` pushes
  a full resync to a late joiner. Per-client visibility is a gating change, not a
  streaming redesign.
- **The `Focus` report exists** (`browserproto.Focus`, `up.go:122`): each connection
  reports its OS-level window focus, `client.focused` holds it, and `anyClientFocused` /
  `syncAppFocus` / `noteUsageAttention` already reason "is anyone in front" per client.
  "Which window did the user touch last" is derivable from what is already on the wire.
- **Viewers exist** (`Init.Viewer`): a connection that declares no geometry, follows
  whatever the sizers settled on, and can type into a pane by id without stealing focus.
  The phone uses it. Viewers never sized the session; they must keep following the
  *primary* view after this plan rather than being orphaned by it.
- **Input routing defaults to the session focus.** `inputTarget(0)` (`catway.go:2347`)
  resolves an unaddressed `key`/`paste` to `Session.FocusedPane()`; an addressed one is
  refused unless in `o.visible`. Both become per-client questions.
- **Commands default to the active workspace, implicitly.** `SplitPane(nil)`,
  `ClosePane(nil)`, `ToggleZoom(nil)`, `CreateTab()`, `CloseTab(nil)`, `FocusTab`,
  `MoveTab`, `RenameTab`, `CyclePane`, `FocusLastPane`, `FocusPaneDirection`,
  `SwapPaneDirection`, `ResolvePaneTarget(nil)` all read `s.active`. The mobile/remote
  work already added explicit-workspace twins for some (`CreateTabIn`, `RenameTabIn`,
  `NewTabNeighborPaneIn`, `CreateTabInOn`) — the pattern exists; it is not complete.
- **Every dispatcher is built per call** — `app.NewDispatcher(o.session, o)` in
  `commands.go:17` (browser), `control.go:51` (catctl), `runbook.go:290` (runbook steps).
  That is the natural place to hand a command the view it was issued from: the browser
  path has the `*client`; the other two have no window and need a default.
- **Session persistence carries `Active`** (`internal/app/persist.go:21`) and nothing
  about windows. The Mac app persists its own `appConfig` (`cmd/catapp/config.go`) —
  mode, remote target, presets — and nothing about windows either.
- **`webview_go` is one window per process by construction.** Its cocoa backend makes
  each instance NSApp's delegate (`setDelegate:` in `webview.h:1572`), owns
  `applicationShouldTerminateAfterLastWindowClosed:`, and `Run()` is `[NSApp run]` — a
  second `webview.New` in the same process replaces the first's delegate. `catapp` already
  has a cgo/ObjC seam beside it (`menu_darwin.m`: the menu bar, ⌘+/⌘-/⌘0 key equivalents)
  and two page bridges bound per window (`catsClipWrite/Read`, `window.catsAdjustFont`
  via the process-global `uiWindow`, `main.go:283`).
- **The front end decides nothing about visibility.** `layout` is server-built for the
  viewport (`viewportLayout`, `catway.go:663`) and the page renders what it is sent;
  `workspace.focus` / `tab.focus` are plain `cmd`s (`index.html:2298`, `:2816`). Mode 3
  (any browser) gets multi-window for the price of `window.open`.
- **Tests that are the template:** `cmd/catway/multiclient_test.go` (two connections,
  viewer semantics, census), `focus_test.go` (Focus reports), `catctl probe`'s op script
  for headless protocol runs. Wire-struct changes ⇒ regen `cmd/catgen-dart/testdata/golden`
  and then cats-mobile per memory.

## Key decisions

| # | Decision | Choice |
|---|---|---|
| 1 | What a window *is* | **A connection with a view**: `{workspace, area, focused}`. Windows are not modelled in the session and are not persisted by catway. The session stays what it is — workspaces/tabs/panes — and a window is a lens on it that lives exactly as long as its WebSocket. This is the tmux shape (clients attach; a client has a current session) and it keeps `persist` and the mobile client untouched. |
| 2 | Granularity of independence | **Per workspace.** Two windows on different workspaces are fully independent; two windows on the *same* workspace mirror (same active tab, same focus, same zoom) exactly as every client does today. Tab-level independence inside one workspace would put two focuses in one `TileLayout` and two zoom flags on one tab — a model change for a case the "window per project" workflow does not need. Documented as mirroring, not as a limitation to fix. |
| 3 | Where "active workspace" lives | **On the view, with the session's `active` demoted to the primary view's workspace.** `Session.active` stays for persistence and as the default for view-less callers; the dispatcher never reads it directly for a browser command — it reads the view. One source of truth per window, one persisted default. |
| 4 | Which view a view-less caller gets | **The primary view = the most recently OS-focused sizer connection**, falling back to the last one that was, then to `Session.active`. catctl from a pane, hook actions, runbook steps, `ui.notify` click-throughs all resolve "the focused pane" through it. A `Focus` report already arrives for every foreground change, so this is bookkeeping on an existing signal, not a new one. |
| 5 | Sizing a workspace two windows show | **Last reporter wins, as today** — and the doc says so. The focused-window-wins refinement is phase 4; it is not needed for correctness and it changes nothing for the common case (one window per workspace). A workspace nobody shows keeps its last area, so its panes keep their shape for the window that comes back. |
| 6 | What "visible" means for the session-wide consumers | **The union** of every view's visible set. `unseen` badges clear when any window shows the pane; branch refresh, agent chrome, output scanners gate on "anyone looking". Pane-program focus (`paneSeen`) is "visible in a *focused* window" — the two existing facts composed per view. |
| 7 | Protocol shape | **Additive.** `Init.Workspace` picks the initial view (omitted ⇒ primary view's workspace, so today's clients are unchanged); `workspace.focus` becomes per-connection; `Clients` grows a per-view breakdown. No new message types; no change to frames, layout or chrome. Protocol version unchanged (§1: additive fields are not a bump). |
| 8 | Closing a window | **Never mutates the session.** Dropping the last view of a workspace leaves it exactly as a workspace you switched away from. The only session effect of a window going away is the primary view possibly moving. |
| 9 | The Mac shell | **Native `NSWindow` + `WKWebView` per window, in ObjC beside `menu_darwin.m`** — not N `catapp` processes, not a framework swap. `webview_go` stays for the single-window paths (error sheet, connect form) until the last caller moves; the bridges (`clipboard`, `zoomFont`) become key-window-relative. Multi-process was the cheapest and was rejected because N processes is N Dock entries, N menu bars, and a supervisor-ownership lock for the backend; a framework swap is a rewrite of a file that works. |
| 10 | Restoring windows on launch | **The Mac app's job, in `appConfig`** — the window list (workspace id, frame) is client state the same way presets are. catway persists nothing about windows (decision 1); a window whose workspace no longer exists opens on the primary view. |

## Phases (each independently shippable, tests green)

### Phase 1 — the view: per-connection viewport in catway — **DONE**

The architectural change. Everything after it is either a menu item or a window
frame. Nothing in this phase is visible to a user who opens one window; the whole
of it is that opening two no longer makes them fight.

#### The type

```go
// view is what one connection is looking at — the per-window half of the
// viewport that the session used to hold once for everyone.
type view struct {
    ws      string          // workspace id this window shows; "" ⇒ primary
    area    layout.Rect     // this window's grid (zero for a viewer)
    visible map[uint32]bool // panes this window streams (ws → active tab → leaves)
}
```

It hangs off `client`. `orch.visible` becomes the union, recomputed with the
per-client sets; `orch.area` becomes *the primary view's* area, kept because the
`Backend.Area()` contract and every `layout.Rect` caller that has no window
(directional nav from catctl, `desiredGrids` for an unshown workspace) need one.

#### Per-workspace area

`desiredGrids` today sizes every tab of every workspace from one area. It becomes:
for each workspace, the area of the sizer view most recently reporting on it
(decision 5), else its last-known area (`orch.wsArea map[string]layout.Rect`),
else the primary area. A `Resize` from a window touches only its own workspace's
entry, then `applyModel`. This is what stops window 2's 120×40 from reflowing
window 1's 200×60 — the bug that makes today's "second window" unusable.

#### The viewport, per client

`viewportLayout()` takes the view's workspace (`BuildLayout(wss, idx, area)` already
takes an index and an area — it was built for this). `applyModel` and
`BroadcastLayout` loop over connections and build each one's layout; `agents`
and `title` stay broadcasts. `refreshViewport` recomputes per client and returns
per-client `added`, so the newly-visible full frame + chrome resync goes only to
the windows that gained the pane (`resyncPane` per client rather than the
every-translator reset it is today).

The sixteen `o.visible[...]` readers sort into two groups, each a one-line
change once the union exists:

* *is anyone looking* (union): `notify.go` unseen badges, `gitbranch.go` refresh
  gating, `agentmodel.go` chrome pushes, `daemon.go` frame/chrome forwarding — the
  forwarding then fans out through `sendVisible(pid, msg)`, which sends to the
  connections whose view contains the pane rather than broadcasting.
* *is this window looking* (the view): `inputTarget` (the addressed-key visibility
  refusal), and the per-client resync in `registerConn`.

#### Commands carry their view

`handleCmd` builds a `viewBackend{orch, view}` that satisfies `app.Backend` with
`Area()` answering the view's area, and passes the view's workspace to the
dispatcher. Inside `internal/app`, every Session method that reads `s.active`
grows an explicit-workspace form and the `nil`/default form delegates to it with
`s.active` — the `CreateTabIn` pattern finished: `SplitPaneIn`, `ClosePaneIn`,
`FocusTabIn`, `CloseTabIn`, `MoveTabIn`, `ToggleZoomIn`, `CyclePaneIn`,
`FocusLastPaneIn`, `FocusPaneDirectionIn`, `SwapPaneDirectionIn`,
`ResolvePaneTargetIn`, `FocusedPaneIn`, `VisiblePaneIDsIn`. The dispatcher gets
a `View` field (`WorkspaceID string`) set by `NewDispatcherFor(s, b, view)`; the
old constructor passes the primary view. `pane.focus` with an explicit pane in
another workspace still works and still switches *that window's* workspace to
reveal it (`RevealPane` semantics), but only that window's.

`workspace.focus` — the command that made every client switch — becomes "set this
view's workspace": the issuing connection gets a new `layout` and the frames; no
one else hears about it, except through `Clients`. From catctl with no view, it
sets the primary view's workspace, which is what it does today.

`key`/`paste` with `pane: 0` resolve through the view (`FocusedPaneIn`); with a
pane, the refusal is "not in *this* view" (decision 6's second group). Locks are
unchanged.

#### The primary view

`orch.primary *client` — the most recently OS-focused sizer, maintained where
`Focus` reports are handled (`catway.go:2410`) and in `dropConn` (fall back to
the most recent remaining sizer, else nil ⇒ `Session.active`). `Session.active`
is written whenever the primary view's workspace changes, so persistence keeps
meaning "where you were" and a cold start opens there. `structFocus` / the
`focus_changed` control event follow the primary view — one event stream, one
focus, as subscribers expect; a window that is not primary moving its focus is a
window-local fact until it becomes primary.

Viewers follow the primary view: their `view.ws` is `""`, resolved at use. A
phone watching a desktop with three windows sees whichever one the user touched
last, which is the phone's whole idea of "the desktop".

#### Protocol

* `Init.Workspace string,omitempty` — initial view. Unknown id ⇒ primary (never
  an error; the id came from a URL the user may have bookmarked last week).
* `Clients` gains `Views []ClientView{Workspace, Cols, Rows, Focused}` so a page
  can show "w2 is open in another window" and a viewer can label which view it
  follows. Additive; old fields keep their meaning.
* The `layout` message already carries `workspaces[].active` — it is now
  per-connection true. No other wire change.

Regen catgen-dart goldens; cats-mobile per memory (it sends no `Workspace` and
is a viewer, so it follows the primary view — verify that, nothing more).

#### Tests

`multiclient_test.go` is the shape. New cases, each a two-connection scenario
asserting what reaches which queue:

* two sizers on different workspaces: `workspace.focus` in A changes only A's
  `layout`; a frame for a pane in A's tab reaches A only; `resize` from B leaves
  A's pane grids alone (`desiredGrids` per workspace).
* `key{pane:0}` from B lands in B's focused pane while A's focus is elsewhere.
* `pane.focus` on a pane in another workspace switches the issuing window only.
* primary view: focus reports move it; dropping the primary hands it to the
  other sizer; `Session.active` tracks it across a save (`persist_test`).
* viewer follows the primary across a primary change.
* union visibility: a completion in a pane shown only by B clears `unseen`; a
  pane shown by nobody keeps it.
* `catctl` (control path) with two windows acts on the primary.

`internal/app`: table tests for each `*In` form mirror their default twin, and
a test that every command in `commandSpecs` that touches focus/layout routes
through the view (the `TestCommandSpecsRouted` approach — the next command added
has to answer the question).

#### Docs

`browser-protocol.md` §Visibility filtering / §Multiple connections become true
as written, with the primary-view rule and decision 5 added;
`web-client-mac-server.md` "Multiple simultaneous clients" rewritten;
`session-model.md` gains "Views" beside the dispatcher. `concepts.md` gets the
one-paragraph mental model: *a window shows a workspace; windows on different
workspaces are independent; windows on the same one mirror.*

#### Not in this phase

No UI for opening a window (phase 2), no Mac windows (phase 3), no
focused-window-wins sizing (phase 4). No change to the control API vocabulary —
`workspace.focus` keeps its name and its catctl meaning.

### Phase 2 — open a window, from the page — **DONE**

Small, and it is the phase that makes phase 1 usable on Mode 3 (any browser)
and in any WKWebView that lets the page open windows.

* `GET /?ws=<id>` — the page reads it and sends it as `Init.Workspace`. A
  bookmarked `?ws=w2` is a window that opens on w2. Unknown ⇒ primary (phase 1).
* Sidebar workspace row and the ⌘K palette grow **"Open in new window"**:
  `window.open(location.pathname + "?ws=" + id)`. In a browser that is a tab or
  a window per its own settings; in the Mac app it is handled natively (phase 3
  intercepts `window.open`).
* The page renders the `Clients.Views` breakdown: a small "also open" mark on a
  workspace row another window shows, and — for a viewer — which workspace it
  is following.
* `catctl workspace focus --window`? No. catctl has no window (decision 4) and
  should not grow one here; `catctl probe` gets a `workspace:` op so the headless
  harness can open a connection on a chosen view.

Tests: a `probe` script with two connections on two views, asserting per-
connection layouts; a page-level check that `?ws=` reaches `Init`. Docs:
`getting-started.md` one line under the browser topology.

### Phase 3 — Mac windows — **DONE** (untested on device: the manual runbook in `standalone-mac.md` has not been run)

`catapp` grows a window manager, in ObjC, and `webview_go` stops being the thing
that owns the main UI window.

#### Shape

`cmd/catapp/window_darwin.{m,go}`:

* `CatsWindowController` — `NSWindowController` owning one `WKWebView` with a
  shared `WKProcessPool` and website data store (so the `hsess` cookie a thin
  client holds per host is one cookie jar across windows, as it is today across
  navigations). It loads `<ui base>?ws=<id>`.
* The app delegate moves here — `applicationShouldTerminateAfterLastWindowClosed`
  stays YES for local mode (the last window closing reaps the backend, as `Run`
  returning does today), and the Window menu gets its standard items plus **New
  Window ⌘N**, which opens on the primary view's workspace (the server resolves
  `""`).
* `WKUIDelegate createWebViewWithConfiguration:` — the page's `window.open` from
  phase 2 becomes a native window instead of a blocked popup. That is how the
  sidebar's "Open in new window" works in the app with zero app-specific JS.
* The two bridges: `catsClipWrite/Read` are `WKScriptMessageHandler`s per
  configuration (the pasteboard is process-wide; nothing per window);
  `zoomFont` evaluates on `NSApp.keyWindow`'s web view instead of `uiWindow`.
  The font size itself stays in the page's storage, so it is already shared.
* `showError` and the remote connect form keep `webview_go` — they are modal,
  single-window, and before any of this exists. They move last, if ever.

`runLocal`/`runRemote` become: start backend (local) → build the app delegate →
open the restored window set (decision 10) or one window → `[NSApp run]`. The
cleanup path is unchanged; `runCleanup` fires from the delegate's terminate
hook.

#### Window restore

`appConfig.Windows []savedWindow{Workspace, Frame}` written on every window
open/close/move (debounced, like catway's save). On launch, each entry opens
on its workspace; a workspace that no longer exists falls back to the primary
— never an error, the user's window layout should survive them cleaning up
projects. `setFrameAutosaveName` is *not* used: it keys by a fixed name, and N
windows need N frames we choose the names of.

#### Tests

The ObjC has no Go test; what can be tested is: `appConfig` window round-trip
(beside `config_test.go`), the URL the window manager builds for a workspace, and
the fallback for a vanished workspace. A manual runbook in the docs
(`standalone-mac.md`): ⌘N, open-in-new-window from the sidebar, close the
primary, quit with three windows, relaunch and find three.

#### Docs

`standalone-mac.md` and `mac-client-linux-server.md` each gain a "Windows"
section; `build-and-packaging.md` notes the new ObjC file in the cgo set.

### Phase 4 — polish — **DONE in this repo**; the mobile client's `Clients.Views` UI is a cats-mobile change and is not started

Each is independent and none is needed for the feature to be true.

* **Focused-window-wins sizing** (decision 5's refinement): when two sizers show
  one workspace, the one that last reported focus owns the area until it loses
  focus. Cheap once the primary bookkeeping exists, and it turns "last reporter
  wins" — an accident of timing — into something a user can predict.
* **Move a tab to another window** — drag from the tab strip onto another
  window, or a palette action "Move tab to …". This is `tab.move` across
  workspaces, which does not exist; it needs `Session.MoveTabTo(ws, num, dstWS)`
  and is a model change worth its own tests. The pane ids travel unchanged;
  public tab numbering is per workspace and the tab gets a new number, as a new
  tab would.
* **Per-window title** — `title` is a broadcast of the session's focused pane;
  a window should show its own. Make `broadcastTitle` per view, the same way
  `layout` went. Trivial after phase 1; deferred only because the Mac app sets
  its own title today.
* **`Clients.Views` in the mobile client** — show which desktop window the phone
  is following and let it pick another. Dart side, after the goldens regen.

## Verification

- Every phase: `make test` and `make test-ghostty`; regen catgen-dart goldens on any
  `browserproto` wire change and `go test ./cmd/catgen-dart`; `TestCommandSpecsRouted`
  and the new view-routing test for the dispatcher; then cats-mobile per memory.
- Phase 1 is the one with an invariant worth a soak: run catway with two browser windows
  on two workspaces and a phone viewer for a working session, and watch for the two silent
  wrongs the multiclient tests exist for — a frame landing in the wrong window, or a
  resize from one window reshaping the other. `catctl probe` with two connections is the
  CI form of the same check.
- Phase 3 gets the manual runbook above on a clean `make macapp` install (memory: MacApp
  bugs are often a stale bundle — check the installed build first).
