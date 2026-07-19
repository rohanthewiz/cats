# WS10 (auth + TLS) & WS8 (styled chrome + live split/close)

**Session id:** `0686f4c3-d357-4663-82d2-16010191fb5c`
**Date:** 2026-0712-2257 · **Branch:** `roh/phase-b` (herdr-web)
**Continues:** `2026-0712-2147-live-handoff-harness-macos-reaper.md` — WS9 was fully done;
this starts the next two phase-plan workstreams (`ai_docs/fbl_go_port_feasibility_analysis.md`
§WS8/§WS10), both building on the `cmd/gateway2` WS9 Stage-4 harness.

> Landed the first substantial increment of **WS10** (browser auth + TLS + origin check on
> gateway2) and **WS8** (real HTML chrome — sidebar/tabs/status/pane-headers — plus live
> pane split/close). All unit tests green; end-to-end verified live (daemon + gateway2 +
> wsprobe2 + headless Chrome): auth gate, bearer bypass, real-PTY split/close, and TLS(wss).

---

## Decisions locked (asked up front — flagged "lock before coding" in the feasibility doc)

1. **Auth model:** password + HMAC-signed httpOnly session cookie (login page), with a
   shared-secret **bearer token** bypass for headless clients (wsprobe2/scripts).
2. **TLS:** auto-generate a self-signed cert when `--tls` is set and no cert given; `--tls-cert`/
   `--tls-key` override with operator PEMs.
3. **WS8 depth:** full styled chrome **and** live split/close now (model mutation + daemon
   create/close-pane), not just a restyle.

## WS10 — new pure-Go packages (no build tag → build under plain `go build ./...`, unit-tested)

- **`internal/gwauth`** (`gwauth.go` +tests): `Authenticator` around one shared secret with a
  per-process random cookie-signing key (restart ⇒ re-login; no secret on disk).
  - `CheckSecret` (constant-time), `CheckBearer` ("Bearer <secret>"), `IssueSession`/
    `ValidSession` (stateless cookie `"<expiryUnix>.<hex(hmac_sha256(signKey,expiry))>"` — no
    identity, just proof-of-mint + expiry), `GenerateSecret`, and pure `OriginOK(origin,host)`
    (empty Origin = non-browser ⇒ allowed; else exact `u.Host==host`).
- **`internal/gwtls`** (`gwtls.go` +tests): `EnsureSelfSigned(dir)` → cert/key PEM paths.
  ECDSA P-256, SANs = localhost/127.0.0.1/::1/hostname/non-loopback IPs, 825-day validity,
  reuse cached cert unless within 30d of expiry, key file `0600`. rweb loads certs from **files**
  (`tls.LoadX509KeyPair`), so auto-gen must write to disk — hence this package.

## WS10 — gateway2 wiring (`cmd/gateway2`, `-tags ghostty`)

- **`auth.go`** (new): `authGuard{a *gwauth.Authenticator, secure bool}`.
  - `middleware` (installed via `s.Use`): public paths `/login`,`/favicon.ico` pass; `/ws` also
    runs `OriginOK` (→ 403 cross-origin); authed (cookie **or** bearer) → `Next`; else `/ws`→401,
    browser nav→302 `/login`.
  - `handleLoginGet`/`handleLoginPost` (self-contained dark login page); POST parses the body with
    `url.ParseQuery`, checks the secret, sets `hsess` cookie (HttpOnly, SameSite=Strict, Secure iff
    TLS, MaxAge=TTL) → 303 `/`.
  - `resolveSecret`: `--password` → `HERDR_PASSWORD` → generated (logged so the operator sees it).
- **`main.go`**: flags `--auth password|none`, `--password`, `--session-ttl` (24h), `--tls`,
  `--tls-cert`, `--tls-key`. `buildGuard` (nil guard = `--auth none`, warns). `resolveTLS`
  (BYO both-or-neither, else `gwtls.EnsureSelfSigned` under `os.UserConfigDir()/herdr`). rweb
  `TLSCfg{UseTLS,TLSAddr:addr,CertFile,KeyFile}` → single HTTPS listener; scheme logged.
- **`cmd/wsprobe2`**: `--token` (adds `Authorization: Bearer` to the WS handshake) and `wss://`
  support (`tls.Dial`, `InsecureSkipVerify` — test client).

## WS8 — live split/close (`cmd/gateway2/gateway.go`, `daemon.go`)

- `pane.created bool` added: tracks whether the daemon has spawned this pane's PTY.
  - `applyLayoutLocked` now: uncreated pane → `createPaneLocked` (β `CreatePane`); created +
    size-changed → `Resize`. `reconcile` sets `created` from the daemon's surviving-pane set on
    every (re)connect (both branches end created=true).
- `handleUp` `cmd` switch dispatches `pane.split`/`pane.close` (protocol vocab already existed):
  - `handleSplitLocked`: `paneTargetLocked` (opt pane → focused), `ws.SplitPane(dir, focusNew=true)`,
    new `pane{}`+`inputenc.New()`, then `applyLayoutLocked` spawns it (uncreated) + resizes the
    sibling; broadcast layout.
  - `handleCloseLocked`: guard ≥1 pane kept, `ws.ClosePane`, drop from model, β `ClosePane`,
    re-layout survivors + rebroadcast agents.
- **wsprobe2** new ops: `split:PANE:h|v`, `close:PANE` (`optPane`: empty/`f` = focused), `panes:N`
  (poll layout pane count).

## WS8 — styled chrome (`cmd/gateway2/web/index.html`, near-total front-end restructure)

- CSS-grid app shell: left **sidebar** (brand · Workspaces ●/○ · Panes tree w/ focus + agent
  badge, click-to-focus · **Agents rollup** — the `agents` message was previously *dropped*, now
  rendered), top **tab bar**, bottom **status bar**, and a styled per-pane header with
  `⊟`/`⊞`/`✕` (split-h/split-v/close) buttons + click-to-focus.
- Grid is now measured from the `#panes` container (`clientWidth/Height`), **not** `window` — so
  the sidebar offset is honored and the server's layout rects still map 1:1 (verified: reports
  108×29, not the full-window width).
- Canvas renderer / input / mouse / paste / clipboard / toasts kept intact.

## Verification (live, macOS)

- `go build ./...` (untagged) + `go build -tags ghostty ./...` + `go vet` (both) + `go test ./...`
  → all green. `gwauth`/`gwtls` unit tests cover session tamper/expiry/cross-key, bearer parsing,
  origin, cert SANs/perms/reuse/regen.
- Harness = termhost daemon + gateway2 + wsprobe2 (+ headless Chrome). **Socket path must be short**
  (`/tmp/…`) — the scratchpad path exceeds macOS's ~104-char unix `sun_path` limit (bind: invalid
  argument); not a code issue.
  - AUTH (curl): `/login`→200, `/`(no cookie)→302 `/login`, POST wrong→401, POST right→303 +
    `Set-Cookie hsess … HttpOnly SameSite=Strict`, `/ws` cross-Origin→403.
  - WS no token → wsprobe2 FAIL (401). WS + token → `expect:2:READY_A`, `split:f:h`→panes=3,
    **`expect:3:IN_SPLIT_C`** (the split pane spawned a real working shell), `close:f`→panes=2. PASS.
  - TLS: `--tls` auto-generates the self-signed cert under `~/Library/Application Support/herdr`;
    `wss://` + token → split/close PASS.
  - Headless Chrome (`--auth none` isolates the front-end): DOM builds (2 pane canvases, sidebar
    ws/pane entries, tab, pub ids), status `connected · 108×29`, no JS throw; screenshot confirms
    the layout.

## Notes / leftovers

- **Uncommitted** (this session did not commit). New: `internal/gwauth/`, `internal/gwtls/`,
  `cmd/gateway2/auth.go`. Modified: gateway2 `main.go`/`gateway.go`/`daemon.go`/`web/index.html`,
  `cmd/wsprobe2/main.go`.
- Session-cookie signing key is per-process (restart = re-login). If session survival across
  restarts is wanted, persist the key (deliberately not done — no on-disk secret).
- TLS uses a single HTTPS listener on `--addr` (no HTTP→HTTPS redirect port); rweb's
  `RunWithHttpsRedirect` is available if we later want the redirect.
- WS8 gestures still bounded by the hard-coded model: **tab create/switch, rename, zoom, workspace
  ops** remain WS2/WS3 work; split/close + focus are the live ones now.
- Real WS8/WS10 completion tracks the phase plan: multi-workspace/tab model (WS2), tagged-union
  Node (WS3), then WS11 cutover.
