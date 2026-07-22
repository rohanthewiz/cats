# mac-app-request1-plus-gateway-hardening

**Date:** 2026-0722-1650
**Session ID:** `9e544f52-3944-4ac1-8472-4f9c0e29917e`
**Project:** `~/projs/go/herdr-web`

---

## Scope

Start executing `ai_docs/mac-app-and-remote-relay-plan.md`. Delivered the plan's
first three phases — shared groundwork, **Request 1** (the native macOS app,
both variants), and **2a** (gateway origin-allowlist hardening) — then did a
round of **Mac-app polish**. Did **not** start the relay (2b/2c); paused there
for ops decisions.

## What shipped

### Request 1 — the Mac app (`cmd/herdrapp/`, new, `//go:build darwin` package)

One launcher codebase, two build variants (mode baked via
`-ldflags "-X main.defaultMode=local|remote"`, overridable in `app.json`).

- **`main.go`** — webview launcher. `runtime.LockOSThread` (Cocoa main-thread
  rule). Dispatches on `defaultMode`/`app.json`. A single `sync.Once`-guarded
  `runCleanup` is shared by all three teardown paths (window-close deferred,
  Cmd-Q via the cgo export, and SIGINT/SIGTERM) so daemons are reaped once and
  never orphaned. Mode-aware title (`herdr` vs `herdr — <host>`).
- **`supervise.go`** — local-mode supervisor: `termhost -persistent` then
  `gateway --auth none` on an ephemeral loopback port; **all three** daemon
  sockets (termhost/control/hook) placed under private per-pid `$TMPDIR` paths
  (extends the plan's groundwork beyond just the termhost seam, so agent
  hook-reporting keeps working even alongside another gateway). TCP-readiness
  wait mirrors `cmd/gateway/daemon.go` backoff. Reaps gateway→termhost on quit.
- **`config.go`** — `app.json` in `~/Library/Application Support/herdr/`,
  separate from the daemons' XDG dirs. Graceful fallback to baked `defaultMode`.
- **`pages.go`** — self-contained connect form (thin client) + startup-error
  page, in the gateway's palette (raw-string style, matches
  `cmd/gateway/auth.go`).
- **`menu_darwin.go` + `menu_darwin.m`** (cgo/Objective-C) — the real polish
  gap: webview bundles an app with **no menu bar**, so out of the box Cmd-Q
  can't quit and Cmd-C/V/X/A don't work. Added an App menu (About/Hide/Quit)
  with **Cmd-Q routed through the Go cleanup** (`//export herdrappCleanup`) so a
  Cmd-Q reaps daemons cleanly, plus an Edit menu (Undo/Redo/Cut/Copy/Paste/
  Select-All → responder chain → WKWebView).

### Bundlers

- **`scripts/build-macapp.sh`** `<self|client> <AppName> <bundle-id> <version>`
  — assembles the `.app`: builds `herdrapp` (plain cgo, no ghostty tag), copies
  the three static ghostty daemons for the self variant, writes `Info.plist`,
  copies `AppIcon.icns` if present. Unsigned/personal.
- **`make macapp`** → `dist/Herdr.app` (self-contained, ~43 MB, local mode;
  depends on `binaries`). **`make macapp-client`** → `dist/Herdr Client.app`
  (thin, ~4.4 MB, remote mode; no backend, no ghostty toolchain needed).
- Confirmed **zero `@rpath` dylibs** in both `herdrapp` and `gateway` — links
  only system frameworks (Cocoa/WebKit/AppKit; libghostty is static).

### App icon

- `scripts/icon/herdr-icon.svg` (terminal-prompt mark: blue `>` chevron + green
  cursor on a dark squircle) + `scripts/gen-icon.sh` (rsvg-convert/magick →
  10-slice iconset → `iconutil`) → committed `scripts/AppIcon.icns`.

### 2a — gateway hardening

- `gwauth.OriginOK(origin, host, allowed)` now takes an operator allow-list
  (full origins or bare `host[:port]`) via new `originAuthority` helper —
  `internal/gwauth/gwauth.go`. Test cases added.
- Config `server.allowed_origins` (`internal/config/config.go`, `omitempty`) +
  gateway flag `--allowed-origins` (comma-split), threaded through `buildGuard`
  → `authGuard.allowedOrigins` → middleware. Documented (commented) in
  `config.example.yaml`.

## Correctness fixes made along the way

- **CI safety:** `cmd/herdrapp` is `//go:build darwin` on **every** file, so
  CI's ubuntu `make build` (`go build ./...`) doesn't drag in webview's Linux
  GTK cgo. Verified `GOOS=linux go list ./...` excludes it with no error; darwin
  still sees it. (ci.yml runs build/vet/test on ubuntu-latest.)
- **Config round-trip:** `allowed_origins` uses `,omitempty` so an unset list
  round-trips as `nil` (not `[]`) and stays equal to `Default()` —
  `TestSaveRoundtrip`/`TestExampleConfigParses` stayed green. Also switched
  `TestExampleConfigParses`'s `!=` struct compare to `reflect.DeepEqual` (Server
  now holds a slice).

## Verification

- **`make check` fully green** (fmt, vet, build, test, vet-ghostty,
  race-ghostty).
- **Daemon supervision exercised at runtime** (isolated to temp XDG dirs):
  gateway served the real 102 KB UI (`<title>herdr-web</title>`), both children
  reaped on SIGTERM, all sockets removed, **no orphans**.
- Static-link + framework links confirmed via `otool -L`; icon rendered and
  wired into the bundle (`CFBundleIconFile`).
- **Not run:** the live GUI window (needs a display). User to try:
  `make vt && make macapp && open dist/Herdr.app`.

## Decisions / open items

- **Relay TLS (decided, for the future relay phase):** terminate with
  **`github.com/rohanthewiz/rweb` in front, doing ACME there** — not BYO-cert or
  certmagic-in-relay.
- **Remaining plan work:** relay 2b (tunnel client `internal/relay` + gateway
  `--relay/--relay-token`, yamux) and 2c (`cmd/relay` server, subdomain routing).
  2d (Mac remote mode) is effectively already wired — the thin client + connect
  form works today against a LAN/VPN/Tailscale gateway URL.
- Possible further app polish (not done): a "Connect…"/"Switch to local" menu
  item; a keep-sessions-alive-in-background option (leave termhost persistent).

## Files touched

New: `cmd/herdrapp/{main,config,pages,supervise,menu_darwin}.go`,
`cmd/herdrapp/menu_darwin.m`, `scripts/build-macapp.sh`, `scripts/gen-icon.sh`,
`scripts/icon/herdr-icon.svg`, `scripts/AppIcon.icns`.
Modified: `Makefile`, `go.mod`, `go.sum`, `config.example.yaml`,
`cmd/gateway/{main,auth}.go`, `internal/config/config.go` (+test),
`internal/gwauth/gwauth.go` (+test).
