# Session: Protocol Additions + `--tls-san` (Phases 0.3 & 0.4)

- **Session ID:** `8d52632e-2444-48b9-94a2-3bd900bb7ca5`
- **Date:** 2026-08-01
- **Branch:** main (from `5f199d6`)
- **Repo:** `cats`

## Request

> next do phase 0.3
>
> next do phase 0.4

Two more steps of the Flutter mobile client plan
(`~/.claude/plans/what-would-it-take-peaceful-treehouse.md`), continuing from the
previous session which shipped Phase 0.2 (the WebSocket keepalive).

---

# Phase 0.3 — Protocol additions

One additive PR, **no `ProtocolVersion` bump**: `catway.go` requires exact version
equality, so a bump would reject every browser holding a cached `index.html`.
Everything here is invisible to an old client — `encoding/json` ignores unknown
fields, and the JS message switch has no `default:` clause (verified), so a new
down-type falls out silently.

## `Init.Viewer`

A client that watches without owning the geometry. The session has **one grid,
shared by every connection**, so an unqualified second client reshapes the first:
a phone announcing 40x30 resizes the desktop's panes to fit a phone.

Gated in two places, and both are needed:

- **`registerConn`** — skips the grid *and* the cell pixel metrics. The metrics
  are the easy half to forget: they never touch the layout, they ride β
  `create_pane`/`resize` into every pane's environment, so a phone's cell size
  would reach panes whose rects never moved.
- **`handleUp`'s `Resize`** — the declaration is made once at init, but resize is
  the message that keeps arriving, every time the phone rotates. Gating only at
  init would let the first rotation undo it.

`client.viewer` is written once in `serve` before the client is published to the
loop, then read-only — no synchronisation, and the writer goroutine never sees a
half-built client.

## `Key.Pane` / `Paste.Pane`

Both `handleUp` cases collapsed onto one `inputTarget(pane)`. `pane == 0` is the
historical focus route, byte for byte — pane ids start at 1, so 0 is never real.

Addressed input clears two gates that focus-routed input does not:

- **Visibility**, the same rule `Mouse` already takes. Frames stream only for
  visible panes, so visibility is the client's freshness proof.
- **`workspace.lock`**, the same rule `pane.send_input` takes. Without it, a
  client wanting past a stated guardrail would just send `key{pane:N}` instead of
  `pane.send_input` — a real bypass of a real safety feature, introduced by us.

Refusals are **silent**, as `Mouse`'s are — an error toast per keystroke helps
nobody, and `layout.workspaces[].locked` already tells a client what it may not
do, so it can render the refusal before it types.

Focus-routed input is deliberately unaffected by the lock: it closes a workspace
to automation, not to the person whose keyboard owns the focused pane.

## `Clients` census

`{total, sizers, cols, rows}`, where `sizers` counts connections that declared a
grid — "2 clients, 1 sizer" reads as one desktop and one phone along for the ride.
`sizers:0` means nobody is driving the geometry and the layout is whatever the
last sizer left behind.

Per the plan it is **not** emitted from `dropConn`, whose hottest caller is
`enqueue` running inside a `broadcast`'s range over `o.conns` — the census would
iterate a map the outer range is still walking. Instead `dropConn` sets
`connsDirty` and `run()` flushes between mailbox closures, the one point where
the connection set is provably settled. That also coalesces three departures into
one census.

`flushClients` **loops** rather than sending once: the broadcast can itself drop a
wedged connection and re-flag mid-flush, leaving a census on the wire that is
already wrong. Each extra pass follows a strict decrease in `len(o.conns)`, so it
terminates.

`o.post` was not an option — it blocks on a full mailbox, and posting to your own
mailbox from the loop goroutine is a deadlock waiting for a busy moment.

## `Welcome.Caps`

`["viewer", "key.pane", "clients"]`. **Dropped `pane.watch` from the plan's list**
— it does not exist until Phase 3.0, and advertising a capability the server does
not honour defeats the entire point of the field. Absent on a rejection: that
socket is about to close and the only thing the client needs is the reason.

## Verification (0.3)

`make check` fully green including the race suite.

### Unit — `cmd/catway/multiclient_test.go` (new, ghostty-tagged, 11 tests)

Plus round-trip and wire-shape cases in `internal/browserproto/proto_test.go`.
The wire-shape tests pin that an unaddressed `key`, `paste`, and `init` are
**byte-identical** to what shipped before — the actual claim of an additive
change.

Neutered the implementation: eight tests fail for the right reasons, and the
visibility and lock gates were neutered separately to confirm each has exactly
one test that bites.

### Live — isolated cathost + catway (port 18499, own sockets)

The running catway was never touched.

| check | result |
|---|---|
| viewer sends `resize:40:12` | pane 1 stays 60 wide |
| **sizer** sends the same resize | pane 1 → 20 wide |
| `typeat:2` while pane 1 focused | lands in 2, absent from 1 |
| unaddressed `type` | lands in the focused pane 1, absent from 2 |
| locked workspace, `typeat:2` | refused; focus-routed `type` still lands |
| pane hidden behind another tab | refused — text never appeared on return |
| addressed `pasteat:2` | lands |
| census across a peer's arrival/departure | `1/0 → 2/1 → 1/0`, grid persisting |

The last one is the whole census contract in one run: a watching viewer saw the
grid stay at the departed sizer's value, exactly as documented.

### Probe additions

`--viewer`, and ops `typeat` / `pasteat` / `resize` / `caps` / `clients`, so all
of the above stays re-runnable. `paneArg` deliberately does **not** accept `"f"`
for the pane: the addressed form exists to name a pane that is *not* focused.

---

# Phase 0.4 — `--tls-san`

## The coverage check is the feature

`EnsureSelfSigned(dir, extraSANs)` was the easy half. The half that matters is the
SAN-coverage check in `usableCert`: a cached cert is valid for **825 days**, so
without it a newly added name would sit in the config doing nothing until the next
expiry. Adding a name has to be what invalidates the cache.

Two consequences the plan did not spell out, both forced by the cache:

- **Only the *requested* names are checked, never the auto-discovered ones.**
  Those follow the machine — a laptop changing Wi-Fi gets new interface IPs — so
  including them would re-mint the certificate, and change the fingerprint a
  client pinned, on every network hop. The discovered set is best effort at mint
  time; the requested set is a promise.
- **Normalisation is load-bearing, not cosmetic.** `CATS.LAN.` and `cats.lan` are
  one name to DNS, so without lowercasing and dropping the root dot, a config that
  never changed would re-mint on every restart. macOS reports its hostname as
  `MacBook-Air.local` while an operator types it lowercase — the common case, not
  an edge one.

Matching is exact against the SAN lists rather than via `VerifyHostname`, because
the question is "did we mint what was asked for", not "would a TLS client accept
this" — a wildcard would satisfy the second and still mean the literal name never
made it in.

## `ParseSANs`

Classifies each entry as an IP or a DNS name. **That ordering matters**: an IP in
`DNSNames` is not an error any parser reports, it just silently never matches.

Exported so `config.Validate` rejects a typo at load — a mistyped SAN would
otherwise surface months later as an unexplained browser trust warning, long after
anyone would connect it to that line. Accepts bracketed IPv6 (`[fd00::1]`, how it
appears when pasted from a URL) and a leading `*` label; rejects URLs, `host:port`,
stray paths, empty labels, and hyphen-edged labels.

## Wiring

`--tls-san` is comma-separated, following `--allowed-origins`' precedent, with
`server.tls.sans` in the config. It implies `--tls` the way `--tls-cert` does —
each is meaningless without HTTPS, so requiring `--tls` alongside would only be a
way to get it wrong. When operator PEMs are supplied the SANs are ignored **with a
log line**: silence there reads as "done", and the trust warning that follows
would look like a bug in the SAN handling.

## Verification (0.4)

`make check` green. Neutered three guards — dropping the coverage check fails only
the re-mint test, misclassifying IPs fails only the classification tests, skipping
normalisation fails only the stability tests.

Live, in an isolated `HOME` on its own port, inspected with real `openssl`:

| check | result |
|---|---|
| `--tls-san cats.lan,10.9.9.9` | `DNS:cats.lan` + `IP Address:10.9.9.9` beside the auto set |
| TLS served with no `--tls` | enabled by the SAN flag alone |
| `s_client -servername cats.lan -verify_return_error` | `Verify return code: 0 (ok)` |
| restart, same SANs | serial unchanged |
| `CATS.LAN.`, reordered | serial unchanged |
| subset of the list | serial unchanged |
| **one name added** | serial changed, new name present |
| `--tls-san https://cats.lan` | startup aborts, no cert written |
| operator PEMs + SANs | logs that they are ignored, and why |
| SANs via config file | cert re-minted covering them |
| bad SAN in config | fails at load, naming the offending value |

---

## Not verified

**That a real browser survives the 90 s keepalive window** — still open from the
previous session. The Chrome extension was not connected in either. Browsers pong
in the network stack, and the failure mode would be a visible reconnect loop
rather than breakage, but it is the primary client and worth a glance.

## Where this leaves the plan

Phase 0 has two steps left:

- **0.5 — `app.CommandSpecs()`** (~1 d). The name ↔ params ↔ result mapping lives
  in the `Dispatch` switch and doc comments, not machine-readably. Turning it into
  data turns 47 hand-written Dart call sites into 47 generated typed methods, and
  encodes the "silently dropped without an `id`" rule as a type-level fact.
- **0.6 — `catctl pair`** (~1.5 d). A short-lived single-use pairing token over
  the control socket, *not* a method that returns the password.

Then Phase 1, the Flutter Tier 1 console, in its own repo.

## Files

**New**
- `cmd/catway/multiclient_test.go`

**Modified — 0.3**
- `internal/browserproto/up.go` — `Init.Viewer`, `Key.Pane`, `Paste.Pane`
- `internal/browserproto/down.go` — `Welcome.Caps` + `Cap*` constants, `Clients`
- `internal/browserproto/proto.go` — `MsgClients` + its `DecodeDown` case
- `internal/browserproto/proto_test.go` — round-trip, wire shapes, caps
- `cmd/catway/catway.go` — `client.viewer`, `inputTarget`, `connsDirty` /
  `clientsMsg` / `flushClients`, viewer gates in `registerConn` and `Resize`
- `cmd/catctl/probe.go` — `--viewer`; `typeat`/`pasteat`/`resize`/`caps`/`clients`
- `ai_docs/phase-c-ws9-protocol.md` — §2 viewer + caps, §5 clients, §6 addressed input

**Modified — 0.4**
- `internal/gwtls/gwtls.go` — `EnsureSelfSigned(dir, sans)`, `ParseSANs`,
  `covers`/`hasIP`, `appendSANs`, `normalizeDNS`, `validDNSName`
- `internal/gwtls/gwtls_test.go` — SAN coverage, re-mint vs cache stability
- `internal/config/config.go` — `TLS.SANs` + validation
- `internal/config/config_test.go` — two rejection cases
- `cmd/catway/main.go` — `--tls-san`, `resolveTLS(cert, key, sans)`
- `docs/reference/cli.md`, `docs/reference/configuration.md`,
  `docs/subsystems/auth-and-tls.md`
