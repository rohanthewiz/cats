# Session: close N-005 as moot (freeze trigger never recurred)

Session ID: 6aba67e6-1fd0-4502-ad03-7568016d31bf
Date: 2026-09-26

The user pasted N-005 from the next list:

> Confirm the freeze trigger with logs. The autoclose reflow after a clean
> agent exit is still only inferred from timestamps. It needs a recurrence;
> stalls, drops, unexpected exits and restarts now land in `daemons.log`.

No code changed. The session checked the log for a recurrence, found none, and
the user chose to close the item as moot.

## 1. What `daemons.log` holds

`~/Library/Application Support/cats/daemons.log`: 65 lines, 2026-09-14 →
2026-09-24 (not rotated; the cap is 1 MiB, `cmd/catapp/daemonlog.go`).

- **No freeze signature.** No `cathost … answered no ping` line
  (`cmd/catway/daemon.go:686`), no stall, drop, unexpected exit, or catapp
  restart note (`cmd/catapp/restart.go`, `supervise.go`).
- **Every daemon pid change is a normal launch.** Each new catway/cathost pair
  opens with the `auth disabled (--auth none)` WARN and the codex manifest WARN.
- **The only burst:** 2026-09-21 14:10:15–16, about 60 × `daemon error (pane
  294): no such pane`. That's a different bug, already fixed as N-036
  (`1f1a960`, 09-24).
- **The current build is quiet.** It launched 2026-09-25 20:04 (`boot.log`) and
  has written nothing. The two startup messages were reworded and aren't
  warnings any more (loopback auth note, "manifest update skipped … keeping the
  cached one").

## 2. Why confirming the trigger no longer matters

- **The structural cause is fixed.** `0236300` (session `2026-0913-2313`)
  bounds every β write with `NewStallWriter` (20s without progress). A
  recurrence now shows up as a stall warning plus a reconnect instead of a
  frozen loop.
- **A recurrence still wouldn't have proved the trigger.** In
  `cmd/catway/reap.go`, a successful tidy-exit auto-close (`fireAutoclose`)
  logs nothing at all. Only a *refused* one uses `dlog.Warnf` (line 258). The
  exited-pane reap logs through plain `log.Printf` (line 119), which
  `dlog.Classify` treats as Routine, so the launcher drops it. A stall line would still
  have to be matched to the agent exit by timestamp (ledger/history), the same
  kind of inference the item was about.

## 3. Options offered, and the decision

1. Leave N-005 open: passive, and inconclusive even if the freeze recurs.
2. Keep auto-close events in `daemons.log` so a stall sits next to its cause.
   `WARN` means "something went wrong", so this would need a kept
   informational level or a narrow exception in `dlog.Classify`.
3. Close N-005 as moot. **Chosen.**

It went to Closed rather than Non-goals: Non-goals is for work that was
declined, and this item stopped mattering. Committed as `ef76289`. This doc
adds the session stem to the closing line.

If the freeze ever does come back, option 2 is the way to pin it down.

## Next

Closed: N-005. Declined: None. Raised: None.
Deferred: None. Promoted: None.
Updated: None. Full list: `ai_docs/todo/next-list.md`.
