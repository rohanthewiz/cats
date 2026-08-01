# Session: WebSocket Keepalive (Phase 0.2)

- **Session ID:** `0122dd3b-10a3-4283-87a2-d4c49385f64f`
- **Date:** 2026-08-01
- **Branch:** main
- **Repo:** `cats`

## Request

> next do phase 0.2

Phase 0.2 of the Flutter mobile client plan
(`~/.claude/plans/what-would-it-take-peaceful-treehouse.md`), continuing from the
previous session which shipped Phase 0.1 (the push bridge).

## The bug being fixed

Finding #3 from the planning session: **there is no WebSocket keepalive anywhere.**
rweb exposes `WritePing` / `SetPongHandler` / `SetReadDeadline` and even declares
`defaultPingInterval` / `defaultPongTimeout` — but never uses them, and catway
called none of them.

Nothing below the application protocol notices a peer that stops existing without
closing its socket. A laptop that suspends, a NAT that forgets its mapping, a phone
that walks out of signal — each leaves a `client` in `o.conns` forever, holding a
writer goroutine and a map of per-pane frame translators. Already a bug for slept
laptops; mobile would make it constant.

## What was built

### The three constants

`wsPingInterval` 30 s, `wsReadTimeout` 90 s, `wsWriteTimeout` 10 s, documented with
a timeline diagram at the top of the browser-connections section.

90 s is deliberately three unanswered pings, not one: a brief cellular stall or a
tab that resumes quickly should not be punished, and the cost of being wrong in the
other direction is only that a corpse lingers another minute.

The write timeout is the half that is easy to leave out. A half-open socket accepts
writes into the kernel buffer until it fills and *then* blocks forever — so without
a write deadline the writer goroutine parks indefinitely, outliving any read
deadline and stranding the connection anyway.

### Reply to pings from the writer, not the reader

The subtle part, and the reason `installKeepalive` exists as its own method.

rweb's default ping handler calls `writePong` **inline on the reading goroutine**.
That is harmless today — but `WSConn.SetWriteDeadline` writes `ws.writeDeadline`
without holding `writeMutex`, while `writeFrame` reads that field *under* the mutex.
So the moment this change started setting a write deadline from the writer
goroutine, an inline pong from the reader became a genuine data race.

It is not hypothetical: browsers never ping, but the plan's own Dart sketch sets
`ws.pingInterval = 20s`. The race would have arrived with the Flutter client.

The fix keeps every frame on one goroutine — the ping handler copies the payload
onto a small `c.pong` channel (cap 4, non-blocking send) and the writer's `select`
sends it. This also makes the `client` type's existing doc comment ("the writer
goroutine is the only WSConn writer") true, which it quietly wasn't.

The payload copy is required by RFC 6455 §5.5.3 (a pong echoes the ping's data) and
because the buffer belongs to rweb's reader.

### Where the read deadline is refreshed — two places, both needed

- **In the pong handler.** A pong never surfaces from `ReadMessage`; rweb dispatches
  control frames inside its own frame loop. For an idle-but-healthy client the pong
  is the *only* traffic, so if the handler doesn't push the deadline out, the
  keepalive reaps exactly the connections it exists to protect.
- **Per read in `serve`'s loop.** Covers the opposite case: a peer that is talking
  but whose pongs are being lost.

`serve` also now bounds the **init handshake** — a peer that completes the HTTP
upgrade and then says nothing previously owned that goroutine until the process died.
Not in the plan, but the same class of leak and three lines.

### `catctl probe` had to learn to pong

Found while thinking about who the peers actually are: `probe`'s `readFrame` treated
opcodes `0x9` and `0xa` identically — `continue`. It never answered a ping.

So the keepalive would have reaped the repo's own test client on any script idling
past 90 s, and the plan's own verification step (`kill -STOP` a probe, watch catway
drop it) would have proven nothing, since the probe would be dropped either way.

`writeText` was generalised into `writeFrame(w, b0, payload)`; `p.pong` takes the
same mutex `p.send` does, because the reader and main goroutines share one socket.

### Testability seams

Two small splits, both of which also read better than what they replaced:

- `writer()` → `writeLoop(pingEvery)`, so tests need not wait out 30 s.
- The handler wiring → `installKeepalive()`, so the read half can be exercised
  without a live orchestrator, and so `serve` doesn't grow another 20 lines.

No global test knobs, no injected clock.

## Verification

`make check` fully green, including the ghostty-tagged race suite.

### Unit — `cmd/catway/keepalive_test.go` (new, ghostty-tagged)

Five tests driving a real `rweb.WSConn` over a `net.Pipe` — no HTTP, no orchestrator
loop, no daemon — reading raw frames off the far end, because what matters here is
bytes on the wire, not anything the application protocol can observe. `net.Pipe`
being unbuffered and synchronous is the point: a peer that stops reading *is* a
stalled socket.

The pong-routing test asserts the payload reaches `c.pong` **before** the writer is
started. That is what separates the two designs: with rweb's default handler the
reply would go out inline and the queue would stay empty (and, with nothing draining
the pipe, deadlock).

Neutered the implementation to confirm the tests bite — three fail, for the right
reasons:

```
--- FAIL: TestWriterSendsKeepalivePings      read frame header: i/o timeout
--- FAIL: TestPeerPingIsAnsweredByWriter     peer ping never reached the writer's pong queue
--- FAIL: TestPongRefreshesReadDeadline      read ended after the pong refreshed the deadline
```

### Live — isolated cathost + catway on their own sockets and port

The running catway was never touched.

**Idle survival.** A probe idled 100 s, then round-tripped `server.reload_config`:

```
→ init v1 120x40
✓ server.reload_config acked      (t≈2s)
✓ server.reload_config acked      (t≈102s)
catctl probe: PASS
```

**A/B against the pre-fix probe.** Same script, a `catctl` built from the unmodified
`probe.go`:

```
catctl probe: FAIL: op 4 "reloadconfig": connection died: EOF
```

So the reaper genuinely fires *and* the probe's pong is load-bearing — one run
proves both halves.

**The zombie reaper itself.** Froze a probe with `kill -STOP`, watched catway's
socket via `lsof -nP -a -p <pid> -iTCP`:

```
11:47:07  STOPped probe
t+80s     ws conns=1
t+91s     ws conns=0   REAPED
```

Last up-message was `init` at ≈11:47:04, so the deadline landed at ≈11:48:34 —
exactly the 90 s design.

## Not verified

**That a real browser survives the 90 s window.** The Chrome extension was not
connected in this session. Browsers pong in the network stack (RFC 6455 §5.5.2, and
it is what every ping-based WS server on the web relies on), and the failure mode
would be a visible 1.5 s reconnect loop rather than breakage — `index.html`'s
`ws.onclose` retries, and a reconnect is a full resync. Still worth a glance on the
next catway restart, since it is the primary client.

## Upstream note (not acted on)

`rweb@v0.1.26`'s `WSConn.SetWriteDeadline` writes `ws.writeDeadline` without holding
`writeMutex`, though `writeFrame` reads it under that mutex. Any caller that sets a
write deadline from a goroutine other than the one answering pings inherits a race.
cats now sidesteps it structurally, but the field arguably wants the mutex.

## Where this leaves the plan

Phase 0 has one item left before the protocol work:

- **0.3 — protocol additions** (~1.5 d, one additive PR, no version bump).
  `Init.Viewer`, `Key.Pane`/`Paste.Pane` behind an `inputTarget` helper
  (visibility- and `workspace.lock`-gated), a `Clients` census down-message, and
  `Welcome.Caps`.

Then 0.4 (`--tls-san`), 0.5 (`app.CommandSpecs()`), 0.6 (`catctl pair`).

## Files

**New**
- `cmd/catway/keepalive_test.go`

**Modified**
- `cmd/catway/catway.go` — keepalive constants, `writeLoop`/`write`, `installKeepalive`,
  `client.pong`, handshake + per-read deadlines in `serve`
- `cmd/catctl/probe.go` — `p.pong`, `writeFrame` generalised out of `writeText`,
  `readFrame` hoists pings to an `onPing` callback
- `ai_docs/phase-c-ws9-protocol.md` — §1 documents the keepalive as a client-facing
  contract (clients MUST pong), since the Flutter client will need it
