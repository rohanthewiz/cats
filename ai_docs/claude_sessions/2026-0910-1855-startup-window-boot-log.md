# The startup window, and the log that says where it hung

Session: https://claude.ai/code/session_01TNYu5mRMMccrzWrDZKNHpq
Date: 2026-09-10
Repo: `~/projs/go/cats` (branch `main`)

## Request

> When starting up show a popup with a log of what is loading / initing, so if
> something hangs up we'll have an idea at what point it does

## The problem

Everything between a double-click and a window was invisible. `catapp` asks the
user's login shell for a PATH (running their whole rc chain, bounded at 5s),
spawns two daemons, waits up to 10s for a TCP listener, then waits for a page to
load and for that page's WebSocket to bring a session back. Any one of those can
be slow and any one can hang outright — and all the user sees is a Dock icon
that never becomes a window. A slow launch and a wedged launch look identical.

## The reorder that made it possible

`runLocal` did this:

```
hydratePATH() → startBackend() → startWindowShell() → m.run()
```

The whole launch happened **before AppKit drew anything**, and nothing could
report progress from there because the thread that would draw it is the thread
that is blocked. So the order flipped:

```
startWindowShell()   NSApp + delegate
showSplash()         the startup window, on the boot log
go bootLocal(...)    hydratePATH, startBackend, then onMainThread: setBase + restore
m.run()              [NSApp run] — the loop, so the log can paint
```

`winManager` gained `setBase`, because local mode now builds the shell before
the catway has a port.

## The pieces

| file | what it is |
|---|---|
| `cmd/catapp/bootlog.go` | the record: ordered entries with `running`/`ok`/`warn`/`fail`/`note` states, ms since process start, a coalesced (40ms) push to whatever surface is listening, a transcript on disk, and `bootTap` — an `io.Writer` that files daemon output as notes |
| `cmd/catapp/splash_darwin.m` | the window. Deliberately **not** a `CatsWindowController`: those live in `gWindows`, are what the restore list is made of, and closing the last one quits. The splash would have been saved into `app.json` as a window to reopen |
| `cmd/catapp/splash_darwin.go` | open/push/raise/close, plus the watch over the last leg of startup |
| `splashPageHTML()` in `pages.go` | the page — the launcher's third built-in, alongside the connect form and the error sheet |

What it looks like mid-launch:

```
✓ reading app settings                          2ms   mode: local
✓ reading PATH from the login shell           890ms
✓ starting cathost                             11ms   pid 4211 on /var/folders/…
✓ starting catway                               9ms   pid 4212
⋯ waiting for the catway to accept connections  5.2s  ← still going
· catway: dialing cathost socket…
```

**The ticking clock is the whole point.** A launch that has stopped moving looks
exactly like one that is working unless something on screen is counting. The
page holds a skew against the launcher's clock (`Date.now() - snapshot.now`) and
re-derives the running rows' elapsed every 200ms, so a step keeps counting
between pushes rather than freezing at whatever the last push said.

Steps are never trimmed; notes are, oldest first, at 400 entries — a launch that
produced 400 lines of daemon output is precisely the one whose steps you want to
be able to read. Entry handles are ids, not slice indices, for that reason.

## The daemon output tap

`command()` now sets `io.MultiWriter(os.Stdout, newBootTap(name))` per stream
(one tap each — shared partial-line buffers written by two goroutines interleave
into nonsense). This is what turns "waiting for the catway — 9.4s" into a
*reason*: the catway's own retry line lands under the step that is waiting on
it. The tap goes inert the moment startup ends — the daemons write for the life
of the session and none of that is boot.

## The last two steps come from outside the process

The launcher's own work ends when it hands a URL to a web view. Whether that URL
becomes a working session is the next thing that can go wrong and was invisible
from Go:

- a `WKNavigationDelegate` (`didFinishNavigation`, `didFail*Navigation`) closes
  or fails the "opening the window" step;
- a new **`catsBoot` bridge** lets the page report its own phases —
  `ws-connecting` / `ws-open` / `welcome` / `ready` — from `37-session.js` and
  `19-messages.js`, guarded by `window.catsBoot` so a browser is unaffected.

`ready` is the first `layout` message: the moment there is actually a workspace
on screen, and what closes the window. Without this the launcher would have to
assume a page that loaded is a page that works, and **a catway that serves HTML
but cannot reach cathost would look like a successful start** — it now shows as
"connecting to the session" that never ends. A `ws-closed` is noted while the
step is deliberately left running: a connect loop that keeps closing is a hang
with extra steps.

A page that never reports (an older catway, a login form, the connect page) ends
startup 3s after it loads.

## When the window comes forward

Not always, or the diagnostic becomes the annoyance. The workspace window opens
on top of the splash; it is raised back only if the launch was **already slow**
(>3s) when the page loaded, or if it **goes quiet afterwards** (a 4s timer that
raises only when boot is not done) — the page-loaded-but-no-session case, which
is exactly the failure that would otherwise hide behind the window it is meant
to explain.

## Failure surfacing

The failing step goes red with the error under it, above every step that
succeeded and every daemon line on the way. Nothing else opens. `showError` is
**not** the fallback any more and cannot be: it builds a `webview_go` window,
and `webview_go` makes itself `NSApp`'s delegate and calls `[NSApp run]` —
re-entering a running loop and displacing the delegate that owns teardown. It is
kept for a failure with no AppKit behind it. If the user had closed the log,
`bootFailed` reopens it; the record is in memory, so the reopened window shows
the whole launch.

Closing the window when it is the only one quits the app — the way out of a
launch that is going nowhere, and it runs `catappCleanup` on the way.

## Three holes the reorder opened, and closed

| hole | fix |
|---|---|
| ⌘N before the catway has a port would open a window on `"/"` — a window that cannot load, whose failed load would register as a **failed startup** | `windowAddressReady`: `openURL` drops an address with no scheme |
| a failed navigation *after* the UI is up was fatal — and WKWebView reports a navigation superseded by another as a failure, so ordinary use walks that path | only a failure before anything has loaded ends the launch; later ones are noted |
| a quit mid-startup (closing the log, ⌘Q) found nothing registered and orphaned the daemons; and the write of `cleanupFn` now races the main thread's read | `registerCleanup(b.stop)` moved ahead of the first exec (`stop` is safe on a partially-started backend); `cleanupMu` + `cleanupDone`, so a registration after a quit reaps immediately |

## Verification

- `make check` — passes except the pre-existing `internal/inputenc` golden
  failure (`cmd/catgen-dart/testdata/golden/keys.g.dart` was never committed and
  needs a Flutter SDK, which is not on this machine).
- `go test -race ./cmd/...` — clean, which is the one that matters given the new
  goroutine.
- New Go tests: the log's state transitions, warn-is-not-fail, note trimming
  keeping steps, id stability across a trim, sink backlog flush, the tap's line
  splitting / endless-line flush / inertness after boot, the transcript, and the
  page/session phase mapping (the cgo entry points are one line each over
  testable methods — a `_test.go` file cannot `import "C"`).
- The splash page's script was smoke-tested under node with a small DOM shim
  (not committed): escaping of daemon output, state classes, the failure header
  and footer, and that the clock actually advances between pushes.
- **Not run in the GUI.** Sessions here are inside Cats.app with no screen
  access, and a blind launch would steal focus and leave a window that could not
  be inspected.

## Docs

- `docs/reference/troubleshooting.md` — "The app window shows an error page"
  became "The app is slow to open, or never opens", with a table of what being
  stuck on each step means.
- `docs/architecture/standalone-mac.md` — startup sequence diagram redrawn for
  the new order (main thread / splash / boot goroutine), and failure surfacing
  rewritten.
- `docs/architecture/mac-client-linux-server.md` — the thin client gets the same
  window; its steps are the page load and the session, so an asleep or
  off-the-VPN host shows as one of them not finishing.

## Left for later

- `make macapp && open` to see it on screen — the one thing this session could
  not do.
- The transcript is one file overwritten per launch (`boot.log`). If launches
  ever need comparing, that is where to start.
- The splash has no bridges at all, so there is no "copy this log" button; the
  path is in the failure footer instead.
