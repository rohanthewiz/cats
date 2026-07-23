# cats

[cats](https://herdr.dev) in Go, presented through the browser: a terminal
workspace manager for herding AI coding agents. This repo is the complete
application — the Rust implementation is retired, and no Rust checkout is
needed to build or run anything here.

Three binaries make up the app:

| Binary | Role |
|--------|------|
| `catway` | The cats server: workspace/tab/pane orchestrator, web UI over WebSocket, control + hook APIs, session persistence |
| `cathost` | Terminal backend daemon: owns PTYs + VT emulation (libghostty-vt) per pane; run `-persistent` so shells survive catway restarts |
| `catctl` | CLI client for the control API — the same command table the browser uses — plus offline agent-integration installers |

## Features

- **Workspaces → tabs → panes** with BSP splits, drag-to-resize, zoom, and
  per-pane titles; all state is a single-owner event loop over one `app.Session`.
- **Agent awareness**: panes detect the coding agent running in them (claude,
  codex, kimi, …) via process inspection plus a manifest catalog that updates
  from herdr.dev; agent hook reports (permission prompts, task completion)
  arrive on a local hook socket and surface as badges/toasts.
- **Session persistence & restore**: the model is saved to
  `~/.local/state/cats` on every mutation. A catway restart re-adopts live
  PTYs from the persistent cathost; a cold start re-spawns panes with captured
  scrollback replayed, and `resume_agents` relaunches supported agents into
  their native conversation sessions.
- **Git worktrees**: create a worktree checkout per agent/task from the UI.
- **Copy mode** with vim-style, rebindable keys; OSC 52 clipboard; OSC 8
  hyperlinks; window-title and notification passthrough.
- **Remote access**: shared-password login with HMAC-signed session cookies
  (headless clients use a Bearer token) and optional TLS (self-signed
  auto-generated, or bring your own cert).
- **Configuration** in YAML (`~/.config/cats/config.yaml`): server settings,
  theme colors/font, and keybindings — see
  [`config.example.yaml`](config.example.yaml). Theme/keybinding edits apply
  with `catctl reload`, no restart.

## Build & packaging

The VT engine (libghostty-vt, Zig) is vendored in `third_party/libghostty-vt`
— the repo is self-contained.

```bash
make vt             # one-time: build the vendored libghostty-vt (downloads pinned Zig 0.15.2)
make binaries       # catway + cathost + catctl into bin/ (-tags ghostty)
make check          # everything CI runs: fmt, vet, untagged tests, tagged race tests
make dist           # release tarball for this host's OS/arch into dist/
```

CI (`.github/workflows/ci.yml`) runs the untagged quick checks plus the
ghostty-tagged race tests on Linux and macOS. A `v*` tag triggers
`release.yml`, which attaches per-platform tarballs to the GitHub release.

The CGO terminal path is behind the `ghostty` build tag: `catway` and
`cathost` need `-tags ghostty` + `PKG_CONFIG_PATH` (the Makefile wires this),
while `catctl` and most `internal/` packages build and test with a plain
`go build ./...` — no Zig toolchain required.

## Run

```bash
# 1. Terminal backend (persistent: panes survive catway restarts/upgrades)
bin/cathost -socket /tmp/cats-cathost.sock -persistent &

# 2. The cats server
CATS_PASSWORD=changeme bin/catway --addr :8421

# 3. Open http://localhost:8421 and sign in
```

`catway --auth none` skips the login for trusted localhost use; `--tls`
serves HTTPS. Flags beat the config file, which beats built-in defaults
(`flag > config > default`); run `catway -h` for the full set.

> **Note:** the web UI (`cmd/catway/web/index.html`) is embedded into the
> catway binary at compile time (`//go:embed`) — after editing it, rebuild
> and restart the catway; a browser reload alone keeps serving the old page.

### CLI control & automation

`catctl` drives a running catway over the owner-only control socket:

```bash
catctl split h 2                      # split pane 2 horizontally
catctl panes                          # list panes
catctl wait 1 "BUILD SUCCESSFUL" 120  # block until pane 1 prints the pattern
catctl send 1 vim notes.md            # type into pane 1 (staged; nothing runs)
catctl run 1 make test                # type and submit with Enter
catctl events 1                       # stream pane events until Ctrl-C
catctl reload                         # re-render page after config edits
catctl help                           # the full verb list
```

`catctl integration install claude` installs the cats hook integration
into an agent's own config tree (offline — no catway needed); `catctl probe`
is a stdlib-only WebSocket probe for exercising the browser protocol headlessly.

## Layout

```
cmd/catway/          cats server: orchestrator event loop, web UI, WS bridge,
                      control/hook APIs, persistence + restore, auth/TLS
cmd/cathost/         terminal-backend daemon (orchestration Host over a socket)
cmd/catctl/         control-API CLI, agent-integration installers, and the
                      browser-protocol WebSocket probe verb (untagged)
internal/app/         session model + §7 command table (the Dispatcher seam)
internal/browserproto/  browser WebSocket protocol (spec: ai_docs/phase-c-ws9-protocol.md)
internal/orchestration/ catway↔cathost seam (protocol + terminal-backend Host)
internal/terminal/    VT emulator (Emulator iface + go-libghostty)
internal/layout/      BSP pane layout
internal/detect/      agent detection (process inspection + manifest catalog)
internal/config/      YAML config (server / theme / keybindings)
internal/persist/     on-disk session + history state
internal/ctlproto/    control-API protocol + server
internal/integration/ agent hook installers (claude, codex, kimi, …)
internal/gwauth/, internal/gwtls/  login/cookie auth, TLS setup
internal/worktree/    git-worktree creation
third_party/libghostty-vt/  vendored VT engine source (Zig)
scripts/build-libghostty-vt.sh  portable VT build (pinned Zig 0.15.2 + macOS SDK patch)
```

**Toolchain note (macOS):** the macOS 26 SDK dropped the plain `arm64-macos`
slice from its `.tbd` stubs and Zig 0.15.2 doesn't fall back arm64→arm64e, so
a native build fails to link libSystem. `scripts/build-libghostty-vt.sh`
patches a *copy* of the SDK to re-add the slice and points Zig at it via an
`xcrun` shim. Zig itself is downloaded to `.tools/` (gitignored); no system
changes are made.

## History

This codebase replaced the Rust/ratatui cats through a phased migration:
Phase A (a thin web client attached to the Rust server), Phase B (Go-owned
PTY + VT emulation behind an orchestration seam), and Phase C (the
orchestrator, layout, detection, persistence, and web chrome in Go). The
design docs and per-workstream session notes live in
[`ai_docs/`](ai_docs/); retired phase code is recoverable from git history.
