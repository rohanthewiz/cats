# Plan: Cats Mac app + remote access over a relay

**Status:** planning (no code yet). **Date:** 2026-07-21.

> **Rebrand note (2026-07-22):** the project was renamed **herdr-web → Cats**.
> Module is now `github.com/rohanthewiz/cats`; binaries are `catway` (server,
> was `gateway`), `cathost` (terminal daemon, was `termhost`), `catctl` (CLI,
> was `herdrctl`); the Mac launcher is `cmd/catapp` producing `Cats.app` /
> `Cats Client.app`. Env vars are `CATS_*`, config lives under `~/.config/cats`,
> sockets are `/tmp/cats-*.sock`. The external **`herdr.dev`** host is
> intentionally left as-is (see TODO below) — it still serves the live
> agent-detection manifest catalog.
>
> **TODO — replicate `herdr.dev` under Cats:** stand up a `cats.dev` equivalent
> of the agent-detection manifest catalog (`https://herdr.dev/agent-detection/
> index.toml`, consumed by `internal/detect/update.go`), then flip
> `defaultCatalogURL` (and the `relay.herdr.dev` relay base in §2c) over to it.
> Until that host exists, keep pointing at `herdr.dev` so manifest fetches and
> the future relay keep working.

**Scope:** two large requests —
1. Package the backends (`catway`, `cathost`, `catctl`) + the browser front-end as a native macOS `.app`.
2. Allow full remote use of a home cats instance over the network (browser/Mac app at work ↔ mini-PC at home), via a hosted relay so no VPN or port-forwarding is needed.

---

## Context

cats is three cooperating binaries plus an embedded web UI. Today it is a
local, single-machine, launch-two-daemons-by-hand tool. These two requests turn
it into (a) a double-click desktop app and (b) a remotely-reachable service. The
research below found the codebase is already most of the way there for both —
the seam is transport-agnostic and the browser edge already has TLS + auth — so
the net-new work is a launcher/bundler and a relay.

### Decisions locked in (from planning Q&A)

| Question | Choice |
|---|---|
| Remote topology | **A — remote front-end, home backend.** `catway`+`cathost` both run on the home mini-PC; the browser/Mac app at work connects over HTTPS/WSS with the existing password + TLS. The catway↔cathost seam stays a local Unix socket. **The home mini-PC runs Linux (32 GB RAM); the Mac `.app` is front-end only.** |
| Mac app shell | **Minimal Go webview** (`github.com/webview/webview_go`) + a small Go supervisor. `.app` bundle hand-assembled by a Makefile target. |
| Reaching home over NAT | **Build a relay/rendezvous.** The home catway dials out to a public relay; the front-end connects to the relay, which brokers the two. |
| Packaging polish | **Personal / unsigned.** A `make` target that produces a runnable `Cats.app`; no Apple Developer signing/notarization (Gatekeeper right-click→Open on other Macs). |

---

## Findings that shape the design (with anchors)

**The catway↔cathost seam is already transport-agnostic — Topology A needs zero seam changes.**
- `Host.Serve(ctx, conn io.ReadWriteCloser)` / `Host.Attach(ctx, conn io.ReadWriteCloser)` — `internal/orchestration/host.go:315,209`.
- Catway side holds a `net.Conn` and only names the transport at one dial site: `net.DialTimeout("unix", d.socket, …)` — `cmd/catway/daemon.go:64`. cathost listens at `net.Listen("unix", socket)` — `cmd/cathost/main.go:73,141`.
- Framing is `[u32-LE length][JSON]`, `ProtocolVersion = 2`, `MaxFrameSize = 8 MiB` — `internal/orchestration/protocol.go:604-646,28,32`.
- Because Topology A keeps both processes on the home machine, **none of this changes.** (A future "split backend over the network" would only need to generalize those two call sites + add auth — noted, not in scope.)

**The catway browser edge already supports remote access.**
- Binds all interfaces by default (`--addr :8421`) — `cmd/catway/main.go:76`.
- TLS: `--tls` auto-generates a self-signed cert whose SANs include the hostname and every non-loopback interface IP (deliberately, for LAN/remote), or BYO cert via `--tls-cert/--tls-key` — `internal/gwtls/gwtls.go:41,131-148`; wiring at `cmd/catway/main.go:220-229`.
- Auth: one shared secret (`--password`/`CATS_PASSWORD`), HMAC-signed session cookie `hsess` for browsers, `Authorization: Bearer <secret>` for headless — `internal/gwauth/gwauth.go:66,73,84-110`; guard/login at `cmd/catway/auth.go:46-108`.
- Single `/ws` endpoint, auth-gated **pre-upgrade** by the global middleware — `cmd/catway/main.go:246`, `cmd/catway/auth.go:46-64`, WS serve loop `cmd/catway/catway.go:1100`.
- **Gaps for hardened remote:** the WS origin check is strict same-origin with **no configurable allowlist** (`gwauth.OriginOK`, `internal/gwauth/gwauth.go:133-142`); no reverse-proxy `X-Forwarded-*` trust; auth is checked once at upgrade (no mid-session expiry). These matter for the relay (below).

**The Mac bundle is easy — static link, embedded UI, no supervisor exists.**
- ghostty links **statically** (`libghostty-vt.a` via `-tags ghostty`, the `!dynamic` cgo file in the `go.mitchellh.com/libghostty` module). `otool -L bin/catway` shows only system frameworks — **no `@rpath` dylib, no relocation fixups.**
- Web UI is one self-contained embedded file — `//go:embed web/index.html`, `cmd/catway/main.go:72-73`; no CDN/external assets.
- **The catway never spawns cathost** — it only dials (`cmd/catway/daemon.go:60-83`). A bundle must add a supervisor that launches `cathost -persistent` then `catway`.
- Zig is build-time only; runtime deps are just a login shell (present) and `git` (only if worktrees are used).
- Defaults: socket `/tmp/cats-cathost.sock`; config `~/.config/cats/config.yaml`; state `~/.local/state/cats`; TLS cert cached in `~/.config/cats` — `internal/config/config.go:148-163,284-293`, `internal/persist/persist.go:65-83`.

---

## Build targets & platforms (confirmed: Linux backend)

The two requests build for **different platforms** — keep them separate.

- **Backend (home mini-PC): `linux/amd64`** (confirmed x86-64). `catway` + `cathost` + `catctl` + the tunnel client build for `GOOS=linux GOARCH=amd64`.
  - `make vt` on Linux is a **native Zig build** — the macOS SDK `.tbd` slice patch in `scripts/build-libghostty-vt.sh` is macOS-only and is skipped, so the Linux path is simpler. CI already exercises the ghostty-tagged race tests on Linux, and `release.yml` attaches a per-platform Linux tarball.
  - **CGO cross-compile from macOS → Linux is painful** (needs a Linux cross-toolchain + libghostty built for the Linux target). Prefer **building on the mini-PC itself** (`make vt && make binaries`) or pulling the Linux tarball from a `v*` release. On Linux, CGO links glibc dynamically — fine when built on the same distro family it runs on.
  - Home service = **systemd** unit (not launchd): `cathost -persistent` + `catway --tls --relay … --relay-token …`.
- **Front-end (work Mac): macOS.** `cmd/catapp` + `make macapp` → `Cats.app`. The Mac never runs the backend, so it needs no ghostty/Zig toolchain for the app shell (the launcher is plain Go + webview). In pure remote mode the `.app` ships **only** `catapp` — no catway/cathost binaries required in the bundle. (Local-mode bundling from Request 1 still needs the macOS-built backends.)
- **Relay: Linux VPS.** `cmd/relay` is pure Go — trivial `GOOS=linux` build.

## Shared groundwork (do first)

- **Per-user, private socket paths.** The default `/tmp/cats-*.sock` is world-visible and collides between users. In the launcher, point both daemons at `$TMPDIR` (on macOS `$TMPDIR` is a per-user, 0700 dir under `/var/folders/…`), e.g. `--socket $TMPDIR/cats-cathost.sock`. Solves privacy + uniqueness with no code change (flags already exist: catway `--socket`, cathost `-socket`).
- **App data dir on macOS:** use `~/Library/Application Support/cats/` for the app's own config (`app.json`: mode + remote URL). Keep the daemons' existing XDG paths as-is.

---

## The two Mac-app build variants

Both ship as `.app` bundles built from **one `cmd/catapp` codebase**; the Makefile
target chooses what gets bundled and the baked-in default mode
(`-ldflags "-X main.defaultMode=local|remote"`). The launcher decides local-vs-remote
at runtime by that default (overridable in `app.json`), and only supervises daemons
when they are actually present in the bundle.

| | **Variant 1 — Self-contained** | **Variant 2 — Thin client** |
|---|---|---|
| Target | `make macapp` → `Cats.app` | `make macapp-client` → `Cats Client.app` |
| Bundles | `catapp` + `catway` + `cathost` + `catctl` (macOS-built, static) | `catapp` **only** |
| Runs | fully local & offline (supervises its own daemons) | pure front-end; loads a **remote catway URL** |
| Backend | in-bundle, on the Mac | on the **x86-64 Linux mini-PC** |
| Connectivity | none needed | relay (NAT) **or** direct LAN/VPN/Tailscale — the client only needs a reachable URL |
| Default mode | `local` | `remote` |

Variant 1 is a superset (it *can* also point at a remote URL), but keeping the thin
client as its own tiny target means the common "frontend at work" build carries no
backend binaries and needs no ghostty/Zig toolchain to produce.

## Request 1 — Variant 1: self-contained Cats.app (local, all-in-one)

### 1a. New supervisor + webview launcher — `cmd/catapp/`
A small Go binary (built **without** `-tags ghostty`; it only supervises and shows a window). New dep: `github.com/webview/webview_go`.

Responsibilities:
1. Resolve sibling binary paths relative to the bundle via `os.Executable()` → `Contents/MacOS/{cathost,catway}`.
2. Pick an ephemeral loopback port and a `$TMPDIR` socket path.
3. Spawn `cathost -persistent -socket <sock>` detached (`SysProcAttr{Setpgid:true}`), then `catway --addr 127.0.0.1:<port> --auth none --socket <sock>`. **Local mode uses `--auth none` bound to loopback** — no login friction, safe because it is 127.0.0.1-only.
4. Wait for readiness by TCP-dialing `127.0.0.1:<port>` with a short backoff (mirror the dial-retry shape in `cmd/catway/daemon.go:61-70`).
5. `w := webview.New(...)`, set title/size, `w.Navigate("http://127.0.0.1:<port>")`, `w.Run()` (blocks on the main OS thread — `runtime.LockOSThread` in `main`).
6. On window close / quit: SIGTERM `catway` then `cathost` (clean teardown; a later "keep sessions alive in background" option can leave cathost persistent).

Reuse: the readiness-dial/backoff idiom from `daemon.go`; no changes to catway/cathost themselves for local mode.

### 1b. Bundlers — `make macapp` and `make macapp-client`
Two Makefile targets (extend the existing `binaries`/`dist` section, `Makefile:48-74`) sharing a helper that assembles a `.app` skeleton:
- **`make macapp` (Variant 1, self-contained):** build `catway`, `cathost`, `catctl` (`-tags ghostty`, static — unchanged) **and** `catapp` (plain, `-X main.defaultMode=local`). Assemble `dist/Cats.app/Contents/`:
  - `MacOS/catapp` (`CFBundleExecutable`), `MacOS/{catway,cathost,catctl}`.
  - `Resources/AppIcon.icns`; `Info.plist` — bundle id (`dev.cats.app`), name, version from `git describe` (already `VERSION`, `Makefile:15`), `NSHighResolutionCapable`, min-system.
- **`make macapp-client` (Variant 2, thin):** build **only** `catapp` (plain, `-X main.defaultMode=remote`) → `dist/Cats Client.app` with just `MacOS/catapp` + `Info.plist` (bundle id `dev.cats.client`). No backend binaries, no ghostty/Zig toolchain needed to produce it.
- No dylibs to copy, no rpath fixups (static link). Unsigned: document the right-click→Open Gatekeeper step for other Macs.

**Deliverables:** double-click `Cats.app` → cats opens in a native window, fully local; double-click `Cats Client.app` → connect prompt → remote session.

---

## Request 2 — Remote access over a relay (Topology A)

Three parts: (2a) minimal catway hardening, (2b) a tunnel client at home, (2c) the relay server. Plus (2d) the Mac app's remote mode.

> **Works today, no code:** on a LAN or over Tailscale/VPN, `catway --tls` + a password is already remotely usable now. The relay only adds NAT traversal so work reaches home without a VPN. Recommend shipping the relay as its own phase after the app.

### 2a. Catway hardening (small)
- **Configurable allowed-origins.** Add `server.allowed_origins []string` (config) + `--allowed-origins`, and thread it into `gwauth.OriginOK` (`internal/gwauth/gwauth.go:133-142`) so the relay's public host is accepted. With subdomain relay routing (below) `Origin.Host == Host`, so this is mostly a safety valve, but it closes the "no allowlist" gap and future-proofs a reverse-proxy deployment.
- Leave the rest of the auth/TLS/WS stack unchanged — it already works end-to-end over a byte tunnel.

### 2b. Tunnel client (home side) — `internal/relay` + catway flag
- New catway flags `--relay <wss-url>` / `--relay-token <t>` (+ config `server.relay{url,token,home_id}`).
- New `internal/relay/client.go`: dial the relay over WSS, authenticate with `home_id`+token, then run a **yamux** (`github.com/hashicorp/yamux`) session as the *server* end. For each stream the relay opens, dial the catway's own `--addr` listener and `io.Copy` both directions. Reconnect with backoff (reuse the `daemon.run()` pattern, `cmd/catway/daemon.go:60-83`).
- Started from `cmd/catway/main.go` as `go relay.Run(...)` alongside `go o.daemon.run()` (`main.go:215`) when a relay URL is configured. Because the tunnel forwards the browser's raw HTTP/WS to the catway itself, **the existing password/cookie/WS auth applies unchanged** — WebSocket upgrade rides through the byte splice transparently.

### 2c. Relay server — `cmd/relay/` (new, deployed to a VPS, e.g. `relay.herdr.dev`)
- **Two listeners:**
  - *Agent listener* (WSS): home catways dial in, present `home_id`+token (checked against a relay-side map/config), and hold a yamux session keyed by `home_id`.
  - *Public listener* (HTTPS on `*.relay.herdr.dev`): route by `Host` → `home_id`, open a yamux stream to that catway, and splice the browser TCP ↔ stream after TLS termination.
- **Routing = subdomain** (`<home-id>.relay.herdr.dev`), not path-prefix: no path rewriting, and `OriginOK` sees `Origin.Host == Host == <home-id>.relay.herdr.dev` so it passes. Needs wildcard DNS `*.relay.herdr.dev` + a wildcard TLS cert (BYO via `--tls-cert/--tls-key`, or put Caddy in front for ACME DNS-01; relay code accepts a cert path to keep v1 simple).
- **Pairing (v1):** a shared `home_id`+token, configured on both the catway (`--relay-token`) and the relay's map. Simple, sufficient for personal use.
- Deps: `hashicorp/yamux`; optional `certmagic`/`lego` for ACME later.

**Trust model (call out explicitly):** the relay terminates the browser's TLS, so it can see plaintext (the ngrok model) — including the password on login. This is acceptable for a **self-hosted** relay (you control it). Document it. Future hardening: an app-layer E2E key negotiated at pairing so even the relay can't read Mac-app traffic (browsers can't easily do custom E2E). Not in v1.

### 2d. Variant 2 remote path — same `cmd/catapp/`, thin build
This is the `make macapp-client` variant (`defaultMode=remote`) — the same launcher, no bundled backends.
- `app.json` holds `Remote{url,label}` (or `Local` for Variant 1); the baked `defaultMode` is the fallback on first run.
- First run or a "Connect…" menu item shows a tiny chooser (a small built-in HTML form served on loopback, or a `webview` bind/eval prompt) that writes `app.json`.
- **Remote:** start no daemons; `w.Navigate(remoteURL)`. The remote catway's own login page collects the password; the webview persists the `hsess` cookie across launches (WKWebView data store), so re-launch is one click. The URL is either a relay host (`https://<home-id>.relay.herdr.dev`) or a direct LAN/VPN address — the client is agnostic.
- **Home mini-PC (Linux)** runs the plain binaries as a **systemd** service, not the app: `cathost -persistent` + `catway --tls --password … --relay wss://relay.herdr.dev --relay-token …`. The Linux `make dist` tarball already ships these binaries; only the relay flags are new. Ship a `cats.service` (or a `cathost.service` + `catway.service` pair with `After=`/`Requires=`) unit template under `scripts/` so the mini-PC install is `systemctl enable --now`.

---

## Verification

**Request 1 (local app):**
1. `make vt && make macapp` → `open dist/Cats.app`; cats window appears, panes work, splits/agent-detection behave as in the browser.
2. `ps` shows `cathost` + `catway` children on loopback; quitting the window reaps both (no orphans).
3. Copy the `.app` to a second Mac → right-click→Open works (unsigned path).

**Request 2 (remote):**
1. *LAN baseline (today):* home `catway --tls --password X`; from another machine hit `https://<home-ip>:8421`, log in, drive a pane. Confirms the edge before adding the relay.
2. *Relay:* run `cmd/relay` on a VPS with a wildcard cert; start the home catway with `--relay … --relay-token …`; from a network that cannot reach home directly, open `https://<home-id>.relay.herdr.dev`, log in, and drive a live pane. Kill/restart the home catway and confirm the tunnel reconnects (backoff) and panes survive (persistent cathost adopts — `cmd/catway/daemon.go:124-167`).
3. *Mac app remote mode:* `Cats.app` → Connect → enter the relay URL + password → same session in a native window; relaunch reconnects via the persisted cookie.

Tests: unit-test `internal/relay` framing/reconnect and the `OriginOK` allowlist with plain `go test` (untagged — the relay client and gwauth don't need ghostty). Keep `make check` green.

---

## Sequencing, effort, risk

1. **Shared groundwork** (`$TMPDIR` sockets, app data dir) — tiny.
2. **Request 1** (`cmd/catapp` + `make macapp`) — **medium**, self-contained, immediately useful. No changes to existing binaries.
3. **Catway hardening (2a)** — **small**; unblocks the relay and any reverse-proxy use.
4. **Relay (2b+2c)** — **large**; the real net-new system (tunnel client, relay server, DNS/cert/pairing ops). Independently shippable; LAN/Tailscale covers remote in the meantime.
5. **Mac app remote mode (2d)** — **small** once 2c exists.

**Risks / open items:**
- `webview_go` main-thread + `.app` lifecycle (dock icon, quit) — validate early with a spike.
- Relay wildcard DNS + cert is an ops task, not just code — decide BYO-cert vs Caddy-in-front vs ACME-in-relay.
- Relay-terminates-TLS trust caveat — fine self-hosted; note it, and keep app-layer E2E as a later option.
- Unix socket path length (~104 B) — `$TMPDIR` paths are safe; just don't nest deeply.
