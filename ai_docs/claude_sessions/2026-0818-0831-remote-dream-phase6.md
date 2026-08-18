# Session: remote dream phase 6 — five follow-ups, and what only a frozen daemon could tell us

- Session id: `6c4aa0fc-c26c-4187-be2b-18b55eed8790`
- Date: 2026-08-18
- Branch: `feat/working-with-remotes` (cats)
- Plan/record: `ai_docs/plans/remote-dream.md` — Phase 6 marked **DONE** with its
  "as built" section
- Predecessor: the phase 1–5 commits `a212d81`, `8a9431e`, `ae5d4c5`, `5649d7c`,
  `8e6e0d4`

## What this session was

Phase 6 was a list, not a feature: per-host meters, remote `path.list`, hook
relay, latency in `HostItem`, catapp host presets — plus the gap left standing
since Phase 3, where an unqualified `pane.split` took the workspace's host.

Seven commits, `make test` and `make test-ghostty` green throughout, `gofmt` and
both `go vet` passes clean.

## The thing they turned out to have in common

Every one of them is a **wrong answer being replaced, not a missing feature
being added**. catway had been answering questions that only the other machine
can answer, using this machine's answer:

- a split of a guest pane went to the workspace's box
- `~` in the picker was expanded against catway's `$HOME`
- a remote pane's `CATS_SOCKET_PATH` named a file on catway's filesystem
- the sidebar's HOST meters described the browser's machine, whatever the pane
  was running on

Once that was the frame, each fix had the same shape: send the question
unresolved, let the machine that owns the answer answer it, and treat the
absence of an answer as an absence rather than as a fallback.

## Capabilities, because the version ladder cannot do this

Every new request hangs off `Welcome.Features` rather than a `ProtocolVersion`
bump, and that is forced rather than tasteful. `NegotiateVersion` refuses a peer
*newer* than its own build:

```go
if peer < MinProtocolVersion || peer > ProtocolVersion { return 0 }
```

So a catway that bumped to v4 to announce one new message would be rejected
outright by every already-deployed cathost still on v3 — the exact fleet the
range was widened for in Phase 4. And an unknown request is not ignored the way
an unknown field is: `dispatch` answers it with an error event, which surfaces
as a toast in somebody's browser.

A daemon therefore lists what it can answer, and catway sends a request only
when it appears in the list. An empty list — every build before this — reads
correctly as "the base protocol only". `ProtocolVersion` stays at 3.

## 6a — a split lands beside the pane it split

`Workspace.splitHost` fills an empty spec host from the source pane. Both split
entry points run through it, so they cannot disagree.

The load-bearing detail is that a stored `""` is inherited **verbatim** rather
than resolved. An empty `PaneState.HostID` means *the default host*, so copying
it puts the new pane exactly where its neighbour is; resolving it to `w.HostID`
would move a re-homed pane's split back onto a machine its neighbour no longer
runs on — undoing Phase 5's work.

`tab.create` and `workspace.create` deliberately keep taking the workspace's
host. They have no neighbouring pane to ask, which is what the workspace-level
field is *for*.

## 6b — latency, and a link that can be told is dead

`ping`/`pong`, `latency_ms` on each roster row. Fractional, because a local unix
socket lands under a millisecond and whole milliseconds would report every
healthy session as "0" — a number that reads as broken rather than as instant.

cathost answers a ping on its **normal event queue**, not straight back. A pong
that overtook the pane frames ahead of it would report a healthy link on a
daemon whose output the user is watching arrive in slow motion.

The visible half is the number. The load-bearing half is the timeout — see
below, because the first draft of it did not work.

## 6c — per-host meters

`internal/hostmeter` is the memory/CPU/disk readers lifted out of `cmd/catway`.
Two processes now need the same reading of two different machines, and the
**rows** are built by the same code on both sides: if only the numbers travelled
and each end phrased them, the local and remote halves of one sidebar section
would drift apart one fix at a time. Nothing in the package imports
`browserproto`.

The wire is a **subscription**, and that follows from what a CPU reading is.
Utilization is a rate — it does not exist as a value to be read, only as a
difference between two readings an interval apart — so a daemon that started
measuring when asked and answered immediately would have nothing to say.

The corollary is the rule that keeps a cathost from becoming a monitoring agent:

- nothing is sampled until somebody subscribes
- the subscription dies with the connection, or with `interval_ms: 0`, taking
  the sampler and (on darwin) its `iostat` with it — `hostmeter.Sampler` grew a
  `Stop` for exactly this
- catway paces it off the same attention tier as the account poll, whose **dark
  tier is now "stop"**. A box nobody has a sidebar open on measures nothing.

catway never subscribes to its *local* host: it measures that machine directly,
and in managed mode the local cathost is a child of this process on this box —
subscribing would put two CPU samplers, and two `iostat`s, on one machine to
draw one row.

The section is composed on the way out (`usageMsg`) rather than stored composed,
because the poll's half and each host's half arrive on unrelated clocks.

## 6d — the picker works on the other machine

`path.list` used to refuse a remote pane with "local-host only". It now asks
that pane's cathost, and everything that used to be expanded here travels
**unexpanded**: `~` is the remote user's home, `$VAR` its environment, `.` a
directory only its kernel resolves.

`internal/pathpick` grew the whole listing so both halves run the same code and
differ only in which process runs it.

Two decisions worth keeping:

- the request carries the session's live cwds **on that host**, and the daemon
  merges and stats them. An unfiltered list would offer a picker on devbox this
  laptop's project directories, and any that happened to exist there too would
  be offered as if they were the ones on screen.
- when the anchor pane is on a different machine from the one being listed, its
  cwd is **not** sent. A relative path resolved against a directory from another
  filesystem is worse than no anchor; with none, the answering machine starts at
  its own home.

`PathListParams.host` exists because the new-workspace dialog picks a host
*before* anything exists there. `HostItem.lists_dirs` is separate from `local`
because the two used to be the same answer and are not any more.

## 6e — hook relay

Each cathost opens a hook socket of its own, advertises it as
`welcome.hook_socket`, and catway injects that into the panes it creates there.
What arrives is forwarded as `hook_report` and answered through the same
`answerHook` a local report gets — same arbitration, same idempotency token,
same error codes, because a relayed transition is not a different kind of event.

The payload is **bytes, verbatim**. The pane belongs to the orchestrator's model
and the hook API is the orchestrator's to define, so relaying bytes is both the
correct division and what keeps the next field added to that API from needing a
cathost release.

Two lifetime rules that are easy to get wrong:

- the path is in the **welcome**, not behind a request, because catway needs it
  before its first pane — a pane spawned before the answer arrived would have
  inert hooks until something respawned it
- the path is stable for the **daemon's** lifetime, not the connection's. Panes
  outlive a reconnect and their environment cannot be rewritten afterwards, so
  catway keeps the advertised path across a disconnect and ignores an empty
  re-advertisement.

A host with no relay gets no hook environment at all. Dormant hooks beat hooks
dialing whatever answers on the other machine.

## 6f — host presets

The thin client remembered exactly one catway URL, so a laptop that follows its
owner between a home server, a work VPN and a relay had to be moved by editing
`app.json`. Each connection is now a preset, in a native **Connect** menu
(⌘1–⌘9, a checkmark on the one in the window, "Connect to Another…" at ⌘K) and
on the connect page, which is reachable at any time rather than only on a first
run.

- switching is a **navigation in the same window**, so the session cookie
  WKWebView holds per host survives being away from it
- the current URL is stored on its own, not as an index: the app must open on
  the last-used catway even if `presets` was hand-edited into nonsense — and
  whatever it opens on is folded into the list, so a client that connected once
  before presets existed finds it in the menu
- **forgetting is not disconnecting.** Removing a row leaves the window where it
  is; the current connection is a live session and tidying a list is not a
  reason to end it
- `upsertPreset` keeps insertion order on update, because a menu whose items
  move when you use them is one you cannot build muscle memory for

## The live run, and the three things it found

Two persistent cathosts, one catway, and a stdlib WebSocket stand-in written for
the occasion — catway creates no PTYs without a viewport, and measures no remote
host without a watcher, so a browser (or something wearing its shape) is part of
the apparatus rather than a nicety.

Cathost B was restarted under a different `HOME`, which is what made the
`path.list` claim falsifiable: `~` on devbox came back `/tmp/p6/fakehome` while
catway's own stayed `/Users/…`. Same trick would not have been available with
both daemons sharing a home.

Then B was SIGSTOPped. Three findings, in the order they hurt:

### 1. The ping timeout could never fire

```go
if !d.pingAt.IsZero() && time.Since(d.pingAt) > hostPingTimeout { ... }
d.pingAt = time.Now()   // ← every probe resets it
```

The silence was measured from the *last probe sent*, which moves every twenty
seconds, so the gap could never exceed one interval. A frozen cathost sat
"connected" indefinitely while catway queued keystrokes into it — the exact
failure the probe exists to catch.

The unit test passed the whole time, because it backdated `pingAt` by hand and
then called `sendPing` once. It tested the branch, not the loop.

`pingSince` is now the start of the unanswered **run**; `pingAt` only times the
probe being answered. The regression sends a *second* probe and asserts the
run's start did not move — the assertion the original was missing.

### 2. A dial can succeed against a daemon that will never answer

The kernel completes a unix or TCP connect on its own, so a stopped cathost
accepts the connection and says nothing. The hello/welcome read had no deadline,
so the dial loop parked on it forever with the host showing "not connected" and
**no error at all**.

`handshakeTimeout` (10 s) bounds that exchange and is cleared once the pump
starts, since the pump is *supposed* to idle for minutes. The ping probe cannot
cover this case: it does not start until the handshake completes.

### 3. The roster blamed catway for its own fix

The read error that follows our own `Close` is "use of closed network
connection", which on a host row reads as a bug here rather than as a machine
that went quiet. `daemon.stalled` carries the real reason to the dial loop.

Worth recording that this one could not be verified from outside: the redial's
own handshake timeout supersedes the message within a second. That is the right
answer for the row — the current reason is that we cannot reconnect — but it
leaves too narrow a window to observe, so the text is unit-tested and the plan
doc says so rather than claiming a live sighting.

## A wrong answer that had nothing to do with the phase list

Dumping a remote pane's environment turned up:

```
CATS_CONTROL_SOCKET=/var/folders/…/cats-ctl-34660.sock
```

Nobody injected that. The pane inherited it from **cathost's own environment**,
and cathost had been launched from inside another cats session. An in-pane
`catctl` there would have driven somebody else's terminals.

Not setting the variable is no better: `ResolveSocket` falls back to
`/tmp/cats-control.sock`, which on a box that runs cats is the same hazard by a
more predictable route. So `ctlproto.SocketNone` (`"-"`) now means "no control
socket reachable from here", remote panes are given it explicitly, and `Call`
refuses with a message naming the variable — "connection refused" would send
someone looking for a dead server.

The general lesson: for an environment variable that selects a *server*,
"unset" is not a safe default. Silence inherits.

## Odds and ends

- `capture` returns `""` in a headless rig; `wait` works. Not investigated —
  possibly needs the output stream armed. Worth a look if it bites again.
- `pane.send_input` needs `"submit": true`; a trailing `\r` in `text` does not
  press Enter.
- The `/tmp` vs `os.TempDir()` choice for the relay socket is deliberate: macOS
  `TMPDIR` is a long per-user path and unix socket addresses cap around 104
  bytes, which this project has been bitten by before.
- One `hookrelay_test.go` case was racy on arrival — it started a second
  `Attach` before the first had finished detaching, and `Attach` clears the
  session's outbound sink on the way out. Attaching serially, the way the
  daemon's own accept loop does, is both the fix and the accurate model.

## Left standing

- **cats-mobile has not been regenerated.** catgen-dart goldens changed; that
  needs cats pushed first (see memory: cats-mobile regen flow).
- **A control-socket relay.** The one thing 6e could not finish: `catctl`,
  cats-todo and plugins inside a remote pane have nothing to dial. The control
  API is duplex where the hook API is a one-shot line, so it is its own piece of
  work — though the relay machinery built here is most of the shape.
- Remote worktrees stay local-only, for the reason they always did.
