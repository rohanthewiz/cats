# WS4 — control-API/CLI seam reuse (ctlproto + herdrctl) + browser selection/copy polish

**Session id:** `24e0461f-d202-4475-8ddf-2e0c4cc0d29e`
**Date:** 2026-0713-1832 · **Branch:** `roh/phase-b` (herdr-web)
**Continues:** `2026-0713-1731-ws2-hoist-command-dispatch-app-backend.md`, whose top leftover was
"WS4 CLI/control-API can now reuse the whole §7 table by implementing Backend + Responder + a
ParamDecoder." Did exactly that, then finished the browser selection/copy leftovers.

> Two halves. **Part 1 (WS4):** proved the `app.Backend`/`Responder`/`ParamDecoder` seam by adding a
> second, non-browser front-end onto the §7 command table — a local control API on gateway2 (unix
> socket) + a libghostty-free CLI (`herdrctl`) — with **zero per-command server logic duplicated**
> (orch is reused as `app.Backend`). **Part 2:** finished the three browser selection/copy leftovers
> in `web/index.html`. Committed as **5 independent, green slices**; Part 1 live-verified end-to-end.

---

## Part 1 — WS4 control-API/CLI seam reuse

### Architecture (mirrors the Rust `src/api` reference)

Read the Rust reference (`~/projs/rust/herdr/src/api/{mod,server}.rs`, `src/cli*`): a newline-framed
JSON `Request{id,method,params}` per connection over a unix socket, dispatched onto the app loop via
a channel and answered with one JSON `Response`, socket restricted 0600 (local trust boundary, no
token — that gates the *network* browser path). Ported that shape to Go. The key realisation: **orch
already implements `app.Backend`**, so the only new server-side code is the transport + a one-shot
responder; the §7 dispatch is untouched. `read`/`capture` still resolve asynchronously (daemon
round-trip) — the control responder is channel-backed so sync and async commands both land in one
`dispatchAndWait`.

**Import direction (all untagged except gateway2):** `ctlproto → app`; `herdrctl → {ctlproto, app}`;
`gateway2(main,ghostty) → {ctlproto, app, browserproto}`. No cycles; `herdrctl` links **no libghostty**.

### Slice A — `refactor(app)` (`8f03e5e`)

- Hoisted gateway2's `jsonParamDecoder` → **`app.JSONParamDecoder{Raw}`** (empty ⇒ `ErrNoParams`), the
  one JSON-params contract shared by the browser cmd path and the control API. `handleCmd` now uses it
  and drops the local copy.
- **`app.CommandNames()`** — canonical §7 enumeration for front-ends (CLI validate/list) without
  re-spelling the table. Guarded by **`TestCommandNamesAllRouted`**: every enumerated name is routed
  by `Dispatch`, none hits the unknown-command default. (Guards the enumeration→switch direction.)

### Slice B — `feat(ctlproto)` (`2903635`) — new untagged `internal/ctlproto`

- `proto.go`: `Request`/`Response`/`Pong` envelope + newline framing (`readRequest` tolerates a
  request flushed without a trailing newline before close).
- `server.go`: `Server` parameterized by a `Dispatch func(method, params, app.Responder)`; a
  **`chanResponder`** (`app.Responder`) delivering OK/Fail once (`sync.Once` + buffered chan) onto a
  channel, so a synchronous command *or* an async `read`/`capture` resolved later on the loop goroutine
  both land in `dispatchAndWait`; per-request **backstop timeout** above orch's 5s read/capture timeout
  (a late resolve after timeout is dropped, not blocked). `ping` answered directly (Pong).
- `client.go`: `Call` (dial → one request → one response → close) + **`ResolveSocket`**
  (flag → `HERDR_CONTROL_SOCKET` → `DefaultSocket=/tmp/herdr-control.sock`), shared by server + client.
- 9 tests: envelope round-trip, no-trailing-newline, ping, sync OK/Fail, async resolve, backstop
  timeout, full unix-socket `Serve`/`Call`, dial error. Race-clean.

### Slice C — `feat(gateway2)` (`39c1d64`)

`cmd/gateway2/control.go` + `main.go`. The whole adapter is **`o.controlDispatch`**: `o.post(func(){
app.NewDispatcher(o.session, o).Dispatch(method, app.JSONParamDecoder{params}, r) })` — mirrors
`handleCmd` exactly. `serveControl` removes a stale socket (dials to check no live listener; leaves a
real non-socket file alone), listens, `chmod 0600`, serves in a goroutine, returns a cleanup that
unlinks on `server.stop`. New `--control-socket` flag (env `HERDR_CONTROL_SOCKET`). Listen failure is
**non-fatal** (browser front-end works without it).

### Slice D — `feat(herdrctl)` (`a71f5b6`) — new untagged `cmd/herdrctl`

Pure ctlproto socket client (~4 MB vs gateway2 ~13 MB — no libghostty). `ping`, `commands` (list),
and generic invocation of any §7 method with `--params JSON`; method validated locally against
`app.CommandNames()`. Flags may appear before *or* after the method (re-parse the tail — Go's `flag`
stops at the first positional). Exit **0** ok, **1** command failed, **2** usage/transport error.

## Part 2 — browser selection/copy polish — Slice E `feat(gateway2/web)` (`55efefe`)

`cmd/gateway2/web/index.html`, all three leftovers:

1. **Selection-wash dismissal** — a completed drag is marked `done`; the next keystroke to the terminal
   (`clearStaleSelections`) dismisses the now-stale fixed-viewport highlight; active drags left alone.
   Read+copy factored into shared **`readAndCopy`**.
2. **Copy-scrollback button** (chrome `⧉`) — `capture` over the whole buffer (`scope:1,lines:0,unwrap`)
   → clipboard. The browser's first use of `capture`.
3. **Keyboard copy-mode** (chrome `⬚`) — tmux-style: `hjkl`/arrows move (edge `k`/`j` scroll into
   history), `v` select, `r` rect, `0/$/g/G` jumps, `y`/`Enter` yank (via `read`), `Esc`/`q` exit; a
   mouse drag or the pane's removal supersedes it; a distinct outlined cursor marks the cell. Keyboard
   fully captured while active (nothing to the PTY).

## Files

- `internal/app/commands.go` (JSONParamDecoder) · `internal/app/command_vocab.go` (CommandNames) ·
  `internal/app/commands_test.go` (drift-guard test)
- `internal/ctlproto/{proto,server,client,ctlproto_test}.go` (new package)
- `cmd/gateway2/commands.go` (use app.JSONParamDecoder) · `cmd/gateway2/control.go` (new) ·
  `cmd/gateway2/main.go` (flag + serveControl + stop cleanup)
- `cmd/herdrctl/main.go` (new) · `cmd/gateway2/web/index.html` (copy polish)

## Verification (macOS, go1.26.1)

- **Build/vet/gofmt:** `go build ./...` + `-tags ghostty ./...` clean; `go vet` both clean; gofmt clean
  on all touched/new Go files. `PKG_CONFIG_PATH=~/projs/rust/herdr/vendor/libghostty-vt/zig-out/share/pkgconfig`.
- **Tests:** `go test -race ./internal/...` green (incl. new `ctlproto` 9/9 + app drift-guard);
  `go test -race -tags ghostty ./cmd/gateway2` green.
- **Part 1 live** (real termhost + gateway2 `--auth none`, isolated sockets, driven by `herdrctl`):
  control socket created `srw-------` (0600); `ping`→pong `{protocol:1,service:gateway2}`;
  `tab.create`/`pane.split`/`pane.focus`/`scroll`→ok; `capture` (visible) returned real shell output;
  `read` row0 cols0-8 → `"Sourcing"`; whole-buffer `capture` + **rect `read`** (2×11 block) returned
  exact regions; unknown pane → `error: unknown pane 9999` exit 1; **`server.stop`** → ok, gateway2
  exited, **termhost survived**, control socket unlinked. 0 stray procs after teardown.
- **Part 2:** JS compiles (vm compile-only, 679 script lines); rebuilt gateway2 serves the page with
  the new controls embedded; the exact commands the UI issues verified server-side (above). *Interactive
  browser behavior (clicks, copy-mode motions) not click-tested this session — needs a real browser.*

## Notes / leftovers

- **WS4 depth so far = the seam proof** (local control socket + CLI + browser copy). Not yet ported from
  Rust `src/api`/`src/cli`/`src/config`: read-only query methods (`*.list`/`*.get`), streaming
  (`events.subscribe`, `pane.wait_for_output`), TOML config + keybindings, richer CLI subcommand tree.
  `herdrctl` today is generic (`<method> --params JSON`) — ergonomic subcommands are a clean follow-up.
- **Browser copy-mode not click-tested** here — recommend a live pass: `gateway2 -auth none`, open the
  page, try `⬚` (hjkl/v/r/y/Esc) and `⧉` on a pane.
- Two stray root build artifacts (`gateway2`, `herdrctl`) from `go build` without `-o` were removed
  before committing. Still worth a `.gitignore` for built binaries (and the tracked root `termhost`),
  out of scope here.
