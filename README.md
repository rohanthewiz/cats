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

### cats-todo — prompt backlog

`cats-todo` (ported from [herdr-todo](https://github.com/rohanthewiz/herdr-todo))
is a TUI prompt-backlog manager built on the same control socket: save prompts
of future work per-project (`.cats-todo/todos.json`, committed with the repo)
or globally (`~/.config/cats-todo/`), then *drop* one into a Claude Code
session — an existing agent pane (the picker lists every detected agent pane
with its state and location) or a fresh tab that launches the agent first.

```bash
cats-todo                              # open the manager in the current pane
cats-todo add fix the flaky reconnect  # quick-capture to the project backlog
git log -p | cats-todo add -g -t huh   # capture piped stdin to the global backlog
```

In the manager: `enter` opens the target picker, then `enter` pastes the
prompt staged for review while `ctrl+r` submits it to run (and marks the todo
done). Outside cats it still manages backlogs; only drops need the socket.

### Plugins

The plugin host (`internal/plugin`) manages `~/.config/cats/plugins/`: a
plugin is a directory with a `cats-plugin.toml` manifest (id, version,
`[[build]]` steps, `[[actions]]` — the same shape as herdr's manifest; see
`cmd/cats-todo/cats-plugin.toml` for the reference) plus whatever its build
produces. Installing and linking are offline; `run` launches an action in a
fresh tab via `tab.create`'s spawn params, with the invoking directory as the
pane's cwd and `CATS_PLUGIN_ID`/`CATS_PLUGIN_DIR` (plus every pane's
`CATS_PANE_ID`/`CATS_CONTROL_SOCKET`) in its environment — the manifest never
crosses the socket (the server's own `plugin.list` reads it host-side and
answers with resolved argv).

The web UI has the same surface: gear menu → **plugins** (also in the ⌘K
palette) lists installed plugins with run / update / uninstall per row and an
**add…** prompt. Uninstall resolves over the §7 `plugin.list`/`plugin.uninstall`
commands; install, link and rebuild spawn `catctl plugin …` in a fresh tab so
the git + build output streams live in a pane (the server resolves the catctl
path — override with `CATS_CATCTL` if it lives somewhere unusual).

Local checkouts go through the same **add…** prompt: a source shaped like a
path (`./dir`, `../dir`, `~/dir`, `/dir`) links it in place instead of cloning,
matching `catctl plugin link`. A relative path resolves against the **focused
pane's cwd** — so with a pane sitting in the cats repo, `./cmd/cats-todo` links
the bundled todo plugin, and `../cats/cmd/cats-todo` works from a sibling
checkout. The leading `./` matters: a bare `cmd/cats-todo` is two segments, the
`owner/repo` GitHub shorthand. Linked rows show their checkout path, and swap `update`
(which has no remote to pull from) for **rebuild**, a re-link that re-runs the
manifest's build steps to pick up local edits; **unlink** removes only the
link, never the checkout.

```bash
catctl plugin install rohanthewiz/some-plugin   # clone from GitHub + build
catctl plugin install <git-url> --ref v0.1.0    # pin a branch or tag
catctl plugin link ./cmd/cats-todo              # dev mode: symlink a checkout
catctl plugin update rohanthewiz.some-plugin    # fetch recorded source + rebuild
catctl plugin list                              # ids, versions, actions
catctl plugin run rohanthewiz.cats-todo         # launch in a new tab
catctl plugin uninstall rohanthewiz.cats-todo
```

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
