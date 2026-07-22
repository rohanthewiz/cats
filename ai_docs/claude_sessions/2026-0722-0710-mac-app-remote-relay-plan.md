# Session: Mac app + remote-relay plan

- **Session ID:** 5791b453-13cb-47f5-94fd-a0972bd957bd
- **Date:** 2026-07-22 07:10
- **Branch:** main
- **Deliverable:** planning only — `ai_docs/mac-app-and-remote-relay-plan.md` (no product code changed)

## Goal

Produce a plan for two large requests:
1. Package the backends (`gateway`, `termhost`, `herdrctl`) + the browser front-end as a native macOS `.app`.
2. Allow full remote use of a home herdr instance over the network (browser/Mac app at work ↔ mini-PC at home), without VPN/port-forwarding.

## Approach

Ran three parallel `Explore` agents to map (a) the gateway↔termhost transport, (b) the browser remote-access stack (auth/TLS/WS), and (c) build/packaging + macOS specifics. Asked the user four scoping questions and locked the decisions below.

## Decisions locked in (user Q&A)

| Question | Choice |
|---|---|
| Remote topology | **A — remote front-end, home backend.** `gateway`+`termhost` both run at home; the seam stays a local Unix socket. |
| Mac app shell | **Minimal Go webview** (`github.com/webview/webview_go`) + a small Go supervisor; `.app` hand-assembled by a Makefile target. |
| Reaching home over NAT | **Build a relay/rendezvous** — home gateway dials out; front-end connects to the relay. |
| Packaging polish | **Personal / unsigned** `Herdr.app`. |

## Key findings that shaped the plan (with anchors)

- **Seam is already transport-agnostic → Topology A needs zero seam changes.** `Host.Serve`/`Host.Attach(ctx, io.ReadWriteCloser)` at `internal/orchestration/host.go:315,209`; gateway names the transport only at `net.DialTimeout("unix", …)` `cmd/gateway/daemon.go:64`; termhost listens `net.Listen("unix", …)` `cmd/termhost/main.go:73,141`. Framing `[u32-LE len][JSON]`, `ProtocolVersion=2`, 8 MiB cap — `internal/orchestration/protocol.go:604-646`.
- **Browser edge already supports remote.** Binds all interfaces (`--addr :8421`, `cmd/gateway/main.go:76`); `--tls` auto self-signed cert with hostname + non-loopback interface-IP SANs (`internal/gwtls/gwtls.go:41,131-148`); shared-secret auth with HMAC `hsess` cookie + `Bearer` (`internal/gwauth/gwauth.go:66,73,84-110`); one auth-gated `/ws` (`cmd/gateway/auth.go:46-64`, serve loop `cmd/gateway/gateway.go:1100`).
- **Remote gaps:** `gwauth.OriginOK` is strict same-origin, **no allowlist** (`internal/gwauth/gwauth.go:133-142`); no `X-Forwarded-*` trust; WS auth checked once at upgrade.
- **Mac bundle is easy:** libghostty links **statically** (`libghostty-vt.a`, `!dynamic` cgo file) — `otool -L` shows only system frameworks, no `@rpath`. Web UI fully embedded (`//go:embed web/index.html`, `cmd/gateway/main.go:72-73`). **Gateway never spawns termhost** (dial-only, `cmd/gateway/daemon.go:60-83`) → the app must supervise both. Zig is build-time only; runtime deps = a login shell + `git` (worktrees only). Defaults: socket `/tmp/herdr-termhost.sock`, config `~/.config/herdr/config.yaml`, state `~/.local/state/herdr` (`internal/config/config.go:148-163,284-293`, `internal/persist/persist.go:65-83`).

## Plan shape (see the full doc for detail)

- **Shared groundwork:** per-user private `$TMPDIR` sockets; app data dir `~/Library/Application Support/herdr/`.
- **Request 1:** new `cmd/herdrapp/` launcher (webview + supervisor, no `-tags ghostty`) starts `termhost -persistent` + `gateway --addr 127.0.0.1:<port> --auth none` on a `$TMPDIR` socket, waits for readiness, opens WKWebView, reaps on quit. New `make macapp` assembles `dist/Herdr.app` (4 static binaries + `Info.plist` + icon).
- **Request 2:** (2a) add `server.allowed_origins` into `gwauth.OriginOK`; (2b) `internal/relay` tunnel client + `--relay/--relay-token`, yamux, splices streams to the local gateway (WS rides through, existing auth unchanged); (2c) new `cmd/relay/` server — agent listener + public HTTPS on `*.relay.herdr.dev`, subdomain routing so `Origin==Host`; (2d) Mac app remote mode via `app.json` + a Connect chooser.
- **Trust call-out:** relay terminates browser TLS (ngrok model) → self-host it; app-layer E2E is a future option.
- **Sequencing:** groundwork → Request 1 (medium, self-contained) → 2a (small) → relay 2b+2c (large; LAN/Tailscale covers remote in the meantime) → 2d (small).

## Open items before building

- Spike `webview_go` `.app` lifecycle (dock icon / quit / main-thread) early.
- Relay cert strategy: BYO wildcard cert vs Caddy-in-front vs ACME-in-relay.

## Next step

User to choose: start Request 1 (`cmd/herdrapp` + `make macapp`) or spike the webview shell first.
