# 2026-10-22 19:22 — N-068: catapp restarts cathost on an unexpected exit

Session: `6ca48c93-d0fd-4b83-a4fc-8a128fbca9ff`

## Request

N-068, from the next-list via cats-todo. When cathost crashed (the
libghostty-vt grapheme overflow, `2026-1022-1835-cathost-grapheme-overflow-crash`),
the app got stuck. catapp logged the exit and did nothing else, catway kept
retrying the dead socket, and the page showed only "cathost daemon not
connected — retrying". The `catwayDown` overlay only went up when catway's
own restart budget ran out, so a cathost-only exit showed nothing. There were
two fixes to choose from: (a) restart cathost and say plainly what was lost, or
(b) keep N-025 (no restart) and show the overlay on a cathost exit too.

## Decision

The user chose **(a)**. Two findings settled it before the question was asked:

- catway already handles a fresh cathost on its socket. Its reconcile
  (`cmd/catway/daemon.go`) finds none of the panes there, and `syncDaemon` →
  `createPane` respawns them as fresh shells. That is the same path as a cold
  start.
- Quitting and reopening Cats (what (b) would ask of the user) gives exactly
  the same result: the same layout with fresh shells. So (b) loses the same
  processes and adds a manual step.

## What changed

- **`cmd/catapp/hostrestart.go`** (new): `superviseCathost` waits on cathost's
  `daemonProc.done`.
  - The exit was ours (`b.isStopped()`): return.
  - Otherwise it spends from a separate `cathostRestarts` budget (same policy
    as catway's: 5 per minute, 250ms→4s backoff) and calls `relaunchCathost`
    with the original args on the same socket. cathost removes the stale
    socket file itself.
  - Readiness is `waitAlive(p, cathostSettle=1s)`, meaning the process did not
    exit. There is deliberately no socket probe. A persistent cathost does
    serial `Attach`, so a probe would either be attached itself (logging a
    spurious "session ended") or wait in the backlog behind catway.
  - On success: `ui.cathostLost(cathostLostDetail(last))`.
  - Budget spent: `ui.cathostDown(...)` and return.
- **`cmd/catapp/supervise.go`**: the backend keeps `cathostPath`/`cathostArgs`,
  `cathostReady` and `cathostRestarts`; adds `currentCathost()`.
  `startBackend` records the cathost launch args and sets the readiness check.
- **`cmd/catapp/main.go`**: `go b.superviseCathost(windowBackendUI{})` beside
  the catway supervisor.
- **`cmd/catapp/restart.go`**: `backendUI` gains `cathostLost`/`cathostDown`.
  The header paragraph saying "cathost is deliberately not restarted" now
  records the N-025 → N-068 history.
- **`cmd/catapp/backenddown.go`**: the shared builder is now `overlayJS(id,
  title, detail, overlayButton)`, and `backendOverlayJS` is a wrapper with an
  unchanged signature.
  - Buttons: none, "Restart catway" (unchanged behaviour), or "OK", which only
    removes the card in the page.
  - The cathost card uses its own element, `cats-cathost-notice`, so the
    clear that `catwayBack` runs can't take down an unread "pane processes
    were lost" card.
  - The card is modal on purpose: the restarted layout looks identical to the
    old one, so a toast that fades could be missed.

## Verification

- New `cmd/catapp/hostrestart_test.go`, which tests the following:
  - a restart after one crash produces the "lost" card;
  - spending the budget produces the "down" card and ends the supervisor;
  - a stop of our own causes no relaunch and no UI event;
  - a stop during the backoff ends the supervisor;
  - `waitAlive` handles both an early exit and a process that stays up;
  - the detail texts.
- `backenddown_test.go`: the cathost card uses its own element, the catway
  clear doesn't touch it, OK dismisses instead of restarting, and the
  no-button card has no button.
- `go test -race ./cmd/catapp` passes (untagged and `-tags ghostty`). The new
  tests passed `-count=10`.
- Headless live check: a throwaway scratch test (deleted afterwards) ran
  ghostty-built cathost and catway with temp `HOME`/`XDG_*`, then SIGKILLed
  cathost.
  - The watcher logged the exit, a restart came 250ms later, and the new
    cathost listened on the same socket.
  - catway's redial attached (`client connected`) within about a second,
    before the 1s settle ended, and then the card fired.
  - The scratch run's own assertion failed only because it expected
    `client connected` after the "cathost restarted" note.
- Not checked: Cats.app itself (sessions run inside it, and GUI automation is
  blocked by TCC). That check is now N-068 under Validate.

## Next

Closed: N-025 (reversed by N-068; Non-goals → Closed). Declined: None.
Raised: None. Deferred: None. Promoted: None. Moved: N-068 → Validate.
Updated: N-068. Full list: `ai_docs/todo/next-list.md`.
