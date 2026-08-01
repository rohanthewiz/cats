# Session: Flutter Mobile Client — Plan, and the Push Bridge (Phase 0.1)

- **Session ID:** `845b4984-1684-4e3b-8ac0-660cf72bde0f`
- **Date:** 2026-08-01
- **Branch:** main
- **Repo:** `cats`

## Request

> What would it take to build a mobile app for cats on say Flutter?
> Responsiveness and convenience are my top concerns

Ran in plan mode. The plan was approved, implementation started, and a mid-turn
message scoped the coding to **"stop after Phase 0.1"** — so this session
delivers the full plan plus the first item.

Plan file: `~/.claude/plans/what-would-it-take-peaceful-treehouse.md`

## Four decisions taken up front

Asked before designing, because each changes the shape of the work:

| Question | Choice |
|---|---|
| What is the app? | **Tiered**: agent console first, real terminal renderer second |
| Multi-client conflict | **Viewer mode** — the phone never reshapes the desktop |
| Reachability | **Build the relay** (designed in `ai_docs/mac-app-and-remote-relay-plan.md`, unbuilt) |
| Push | **ntfy/Pushover webhook** posted by catway |

## Four findings that shaped the plan

These came out of exploration and were each verified against the source before
being planned around.

**1. `init` resizes the desktop, not just `resize`.** `cmd/catway/catway.go`
`registerConn` sets the global `o.area` from the *first* message, so a phone
honestly reporting 40×20 reflows every pane for everyone. The guard is `> 0`,
which means **`init{cols:0, rows:0}` is a working viewer mode today with zero
server changes** — the explicit `init.viewer` flag is about making intent
legible, not about enabling the behaviour. Also easy to miss: `o.cellW/cellH`
need the same gate, since they ride β `create_pane`/`resize`.

**2. `pane.send_input` cannot send control bytes.** It routes text through
`rt.enc.Paste()`, and its own doc comment cites "ghostty's control-byte
sanitizing"; `submit` synthesizes only Enter. So there is no Esc or Ctrl-C to a
*named* pane today. This is why Tier 1 is prose-only, and why a pane-addressed
`key` is the highest-value protocol addition.

**3. There is no WebSocket keepalive anywhere.** rweb exposes `WritePing`,
`SetPongHandler` and `SetReadDeadline`, and declares `defaultPingInterval` /
`defaultPongTimeout` — but never uses them, and catway calls none of them
(verified: zero hits in `catway.go`). A phone that loses signal leaves a zombie
`client` in `o.conns` forever. This is already a bug for slept laptops; mobile
would make it constant.

**4. `key`/`paste` ride global focus; `mouse` does not.** `Mouse` carries
`Pane`; `Key` and `Paste` do not. A phone typing steals the desktop's cursor —
and if the desktop user then clicks elsewhere, the phone's next keystroke lands
there.

## Two places the plan departs from prior design

**The relay should not terminate TLS.** `ai_docs/mac-app-and-remote-relay-plan.md`
§2c assumes a terminating relay on `*.relay.…`, optimized for a *browser's*
`Origin.Host == Host` check. A native client sends no Origin at all
(`gwauth.OriginOK` returns true for an empty Origin), so that constraint does
not bind — and SNI passthrough is strictly *less* work: no wildcard cert, no
ACME dep, no HTTP parsing, ~300 LOC instead of ~700. It also dissolves the trust
problem: a terminating relay would see `Authorization: Bearer <secret>` on every
mobile reconnect, and that one secret is both password and bearer token, grants
shell access, and has no rotation or revocation. Hybrid: passthrough for native,
terminating behind Caddy on a second hostname for browsers.

**Per-connection viewport belongs in an up-message, not the command table.**
`app.Dispatcher.Dispatch` is structurally connection-blind, and the control
socket — its other front end — is one-shot request/response with no connection
that outlives the command, so it *cannot* have a viewport. `init` and `resize`,
the two existing per-connection concerns, are both up-messages for the same
reason.

## What was built: Phase 0.1 — the push bridge

The reason it goes first: **one day of Go gets agent alerts onto a phone with no
Flutter code at all.** Configure an ntfy topic, subscribe in the ntfy app, done.
Nothing else in the plan beats its value-per-day, and it is an ordinary outbound
POST — so it needs no inbound reachability and keeps working when no client is
connected and (later) when the relay is down.

### `internal/push/` (new, untagged)

Untagged deliberately, so it and its tests build with a plain `go test` — no
Zig, no libghostty. Follows the `outscan.go` / `page.go` precedent of keeping
logic out of the tagged tree.

Two properties are the whole design:

- **It never blocks its caller.** `Send` does the debounce check under a small
  mutex and hands the request to a goroutine, so the single-threaded
  orchestrator loop pays one map lookup and nothing else.
- **It never calls back.** Unlike the usage poller, which posts its result onto
  the loop, a push has no result the session needs — so there is no `o.post`
  half, and nothing in the package knows what an `orch` is.

Debounce is keyed on `(pane, kind)` — deliberately not on the message, so a pane
whose title changes between two blocks still reads as one agent flapping.
Failure logging follows `logUsageOnce`, so a permanently broken webhook does not
narrate itself on every transition; a success clears the ledger so a second
outage is still reported.

Smaller decisions worth keeping: bodies clamp on a rune boundary, header values
strip CR/LF (a pane title is arbitrary terminal output, and would otherwise
smuggle headers — there is a test for this), and an agent name containing a
comma is dropped from `Tags` rather than mangled into two tags.

### The `notifyAll` choke point

`NewNotify` had exactly one production call site — inside `publishAgent`, where
both detection and the hook API converge by design. Rather than bolt the bridge
onto that line, the fan-out became a named function, so a notification source
added later cannot reach browsers without also reaching the phone.

Ordering is deliberate: the browser broadcast goes first and unconditionally, so
a misconfigured or wedged webhook can never delay or suppress the toast on a
screen somebody is actually looking at.

`orch.push` is a `pushSink` interface (so tests record without HTTP), and it is
initialised in `newOrchWith` to a **typed nil** `(*push.Bridge)(nil)`. That
detail matters: `(*Bridge).Send` is nil-safe, but a *nil interface* would panic
on the same unconditional call — which every existing test and every
unconfigured deployment would hit.

### Config and flag

`push{enabled, url, kinds, priority, click_url, min_interval}`, plus
`--push-url` (passing it is itself the opt-in, so there is no need to set both a
URL and an enable flag).

Defaults chosen against real failure modes:

- `kinds: [attention]` only. `finished` fires on every completion of every
  agent, and pushing those is how a bridge trains its owner to ignore it.
- Priority tops out at `high`, never ntfy's `urgent`/5 — that bypasses Do Not
  Disturb on Android, and a blocked agent is not a 3am emergency.
- `min_interval: 60s`.

**The token is `CATS_PUSH_TOKEN` env-only, never config.yaml.** The reason is
sharper than "don't commit secrets": `config.set` marshals the whole config back
to disk, so a token *field* would write a carefully-env-supplied secret into
`~/.config/cats/config.yaml` the first time the user changed a theme colour.
`Push.Validate` is exported (unlike sibling sections' checks) because main
re-validates after the flag layer — `--push-url` can enable a bridge over a
config that left it off.

The startup log prints only the webhook's **host**, never the topic path: an
ntfy topic is a capability URL, and catway's log gets pasted into issues.

## Verification

`make check` fully green, including the ghostty-tagged race suite.

Beyond unit tests, a live end-to-end run: an isolated cathost + catway on their
own sockets and port (the user's running catway was never touched), driven
through the hook API with a real `claude → blocked` transition.

```
POST /cats-smoke
Title: claude needs attention
Priority: high
Tags: warning,claude
Authorization: Bearer <redacted>
body: "cats · 1\n~/projs/go/cats"
```

Then both suppression paths, confirmed by push count holding steady: a
`finished` transition filtered by the default `kinds`, and a second `attention`
inside the window debounced.

Two incidental findings from doing it:

- The hook API's `pane_id` is the **public handle** (`w1:p1`), not the numeric
  pane id — `hooks.go` resolves it via `session.PaneByPublicID`.
- Deeply-nested scratchpad paths blow the ~104-byte unix socket limit. Sockets
  went in a short `/tmp` dir instead. (The relay plan doc already warns about
  this; now confirmed the hard way.)

## Where this leaves the plan

Phase 0 has two items left, both still tracked and both independently valuable
regardless of whether the app or relay ever happen:

- **0.2 — WebSocket keepalive** (~0.5 d). Fixes finding #3, and benefits LAN
  users today.
- **0.3 — protocol additions** (~1.5 d, one additive PR, no version bump).
  `Init.Viewer`, `Key.Pane`/`Paste.Pane` behind an `inputTarget` helper
  (visibility- and `workspace.lock`-gated), a `Clients` census down-message, and
  `Welcome.Caps`.

Notably **no `ProtocolVersion` bump** is needed for any of it: `catway.go`
requires exact equality, so a bump would reject every browser holding a cached
`index.html`. New down-message types are safe because the JS message switch has
no `default:` clause, and new fields are safe because `encoding/json` ignores
unknown ones.

Full sequencing, effort estimates, and the Flutter/relay designs are in the plan
file.

## Files

**New**
- `internal/push/push.go`, `internal/push/push_test.go`

**Modified**
- `cmd/catway/notify.go` — `notifyAll` choke point, `pushSink`, `pushEvent`, `shortenHome`
- `cmd/catway/catway.go` — `orch.push` field, typed-nil init in `newOrchWith`
- `cmd/catway/main.go` — `--push-url`, flag layer, bridge construction, `mustPushInterval`, `pushHostOf`
- `cmd/catway/auth.go` — `resolvePushToken`
- `cmd/catway/notify_test.go` — push delivery, nil-bridge safety, `shortenHome`
- `internal/config/config.go` — `Push` section, `Interval`, `KindSet`, `Validate`
- `internal/config/config_test.go` — push defaults, parsing, validation rejections
- `config.example.yaml` — documented `push:` block
