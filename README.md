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

## Getting started

Go 1.26+ and `git` are the only prerequisites — the VT engine is vendored, and
building it downloads a pinned Zig into `.tools/` (gitignored, no system
changes).

```bash
make vt          # one-time: build the vendored libghostty-vt
make local       # catway + cathost + catctl into ~/bin (keep it on your PATH)
```

Then either run the binaries by hand:

```bash
cathost -socket /tmp/cats-cathost.sock -persistent &   # terminal backend
CATS_PASSWORD=changeme catway --addr :8421             # the cats server
# open http://localhost:8421 and sign in
```

…or run it as a self-contained Mac app, which supervises its own `cathost` and
`catway` on a private socket and shows their UI in a WebKit window:

```bash
make macapp        # builds dist/Cats.app and installs it to /Applications
open -a Cats
```

Two things worth doing on day one:

```bash
echo 'eval "$(catctl completion zsh)"' >> ~/.zshrc   # completion knows live pane ids
catctl integration install claude                    # richer agent state via hooks
```

**Install the [`cats-todo`](https://github.com/rohanthewiz/cats-todo) plugin**
— it is the recommended companion, and the reference plugin. Keep a backlog of
prompts per-project or globally, then *drop* one into a Claude Code session,
either an existing agent pane or a fresh tab that launches the agent for you:

```bash
catctl plugin install rohanthewiz/cats-todo
catctl plugin run rohanthewiz.cats-todo
```

Fuller walkthrough in [Getting started](docs/getting-started.md); more on
`cats-todo` [below](#cats-todo--prompt-backlog).

## Documentation

Full docs live in [`docs/`](docs/) as an mkdocs-style site (`mkdocs.yml` at the
repo root), served with [gkdocs](https://github.com/rohanthewiz/gkdocs):

```sh
MKDOCS_CONFIG=mkdocs.yml PORT=8000 gkdocs

# without gkdocs installed (it is not a dependency of this module):
MKDOCS_CONFIG=mkdocs.yml PORT=8000 go run github.com/rohanthewiz/gkdocs/cmd/gkdocs@latest
```

Start with [Architecture → Overview](docs/architecture/index.md) for the
component map and the three run topologies (standalone Mac, Mac client + Linux
server, web client + Mac server), or
[Getting started](docs/getting-started.md) to build and run.

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
catctl help [verb]                    # the verb list, or one verb's page
```

Shell completion covers all of it — and reaches into the live session, so
`catctl focus <TAB>` offers real pane ids labelled with their agent or title:

```bash
eval "$(catctl completion zsh)"       # ~/.zshrc, anywhere; also bash / fish
```

Plugins are completed too: installed ids and their actions, plus any command a
plugin claims in its manifest (`cats-todo add -<TAB>`).

`catctl integration install claude` installs the cats hook integration
into an agent's own config tree (offline — no catway needed); `catctl probe`
is a stdlib-only WebSocket probe for exercising the browser protocol headlessly.

### cats-todo — prompt backlog

[`cats-todo`](https://github.com/rohanthewiz/cats-todo) (ported from
[herdr-todo](https://github.com/rohanthewiz/herdr-todo)) is a TUI
prompt-backlog manager built on the same control socket: save prompts of
future work per-project or globally, then *drop* one into a Claude Code
session — an existing agent pane or a fresh tab that launches the agent first.
It lives in its own repo and installs through the plugin host:

```bash
catctl plugin install rohanthewiz/cats-todo
catctl plugin run rohanthewiz.cats-todo
```

## Plugins

The plugin host (`internal/plugin`) manages `~/.config/cats/plugins/`.

### Core principles

Read these first — a plugin is much less machinery than the word suggests.

1. **A plugin is a directory, not an API surface.** It holds a
   `cats-plugin.toml` manifest plus whatever its build produces. The host's
   whole job is directory management:
   `~/.config/cats/plugins/<id>/` — a real directory (install) or a symlink
   (link).
2. **An action is just an argv.** There is no plugin runtime, no callback
   registry, no lifecycle to implement. You declare a command; cats runs it.
3. **The server never parses a manifest.** `catctl plugin run` (and the
   server's own `plugin.list`) reads the manifest *host-side* and resolves it
   to an argv, then launches it in a fresh tab via `tab.create`'s spawn
   params. Nothing plugin-shaped crosses the wire, so the blast radius of a
   bad manifest is one tab — never the server.
4. **A plugin is an ordinary program in an ordinary pane.** It gets a real
   PTY, so a TUI works. It is equally runnable by hand from any shell — which
   is exactly how you should develop it.
5. **Talking back to cats is optional, and is just the control socket.** Every
   pane already exports `CATS_CONTROL_SOCKET`; a plugin drives cats with the
   same §7 command table the browser and `catctl` use. A plugin that only
   prints things needs no cats knowledge at all.
6. **Any language works.** Go, a shell script, Python — if a `[[build]]` step
   can produce it and a shell can exec it, it is a plugin.
7. **No sandbox, by design.** A plugin is code you chose to install, built by
   commands its own manifest names, run as you. Treat it like anything else
   you build from source.

### Writing a plugin

Three steps: make a directory, write the manifest, link it.

```
~/dev/cats-hello/
  cats-plugin.toml
  hello.sh
```

`cats-plugin.toml` — the whole contract:

```toml
id          = "you.cats-hello"     # also the install directory name
name        = "Hello cats"
version     = "0.1.0"
description = "Renames its own pane, then lists every pane cats knows about"
platforms   = ["macos", "linux"]   # omit for "everywhere"; GOOS names also work

# Run once in the plugin root at install/link time. Usually a `go build`.
# A step is told where the user invoked the installer from
# ($CATS_PLUGIN_INSTALL_CWD) and inherits the terminal when there is one, so it
# can offer first-run setup in their project — see docs/subsystems/plugins.md.
[[build]]
command = ["chmod", "+x", "./hello.sh"]

# Launchable entrypoints. Paths are relative to the plugin root.
# The first action is the default for a bare `plugin run`.
[[actions]]
id      = "hello"
title   = "Hello cats"
command = ["./hello.sh"]
```

`hello.sh` — the plugin itself, using only what the pane environment hands it:

```sh
#!/bin/sh
# Every cats pane exports CATS_PANE_ID and CATS_CONTROL_SOCKET.
# A plugin pane also gets CATS_PLUGIN_ID and CATS_PLUGIN_DIR (find your own
# assets there — no argv[0] games).
catctl rename-pane "$CATS_PANE_ID" "hello from $CATS_PLUGIN_ID"
echo "my files live in $CATS_PLUGIN_DIR"
catctl panes
read -r _   # hold the pane open so you can read it
```

Link it and run it:

```bash
catctl plugin link ~/dev/cats-hello     # symlink the checkout + run [[build]]
catctl plugin list                      # confirm: id, version, actions
catctl plugin run you.cats-hello        # launches in a fresh tab
catctl plugin run you.cats-hello hello  # or name the action explicitly
```

Edit, then `catctl plugin link ~/dev/cats-hello` again (or **rebuild** in the
plugins dialog) to re-run the build steps against your local edits.

For anything beyond `catctl` shell-outs, speak the control protocol directly:
one newline-framed JSON request in, one response out, over
`$CATS_CONTROL_SOCKET` (`internal/ctlproto`; method names are the `app.Cmd*`
values in `internal/app/command_vocab.go`, e.g. `pane.list`, `pane.send_input`,
`tab.create`). [`cats-todo`](https://github.com/rohanthewiz/cats-todo) is the
reference implementation of that client — its
[`cats-plugin.toml`](https://github.com/rohanthewiz/cats-todo/blob/main/cats-plugin.toml)
is the reference manifest, and it lives in its own repo precisely to prove the
plugin path works from outside this tree.

Manifest fields in full, plus the install/link/update mechanics, are in
[`docs/subsystems/plugins.md`](docs/subsystems/plugins.md).

### Installing and managing plugins

Install, link, list, update and uninstall are **offline** — no running
`catway` needed. Only `run` dials the control socket, and only to issue
`tab.create`.

```bash
catctl plugin install rohanthewiz/cats-todo     # clone from GitHub + build
catctl plugin install <git-url> --ref v0.1.0    # pin a branch or tag
catctl plugin link ./cats-todo                  # dev mode: symlink a checkout
catctl plugin update rohanthewiz.some-plugin    # fetch recorded source + rebuild
catctl plugin list                              # ids, versions, actions
catctl plugin run rohanthewiz.cats-todo         # launch in a new tab
catctl plugin uninstall rohanthewiz.cats-todo
```

The web UI has the same surface: the toolbar's **⧉ plugins** (also in the ⌘K
palette) lists installed plugins with run / update / uninstall per row and an
**add…** prompt. Uninstall resolves over the §7 `plugin.list`/`plugin.uninstall`
commands; install, link and rebuild spawn `catctl plugin …` in a fresh tab so
the git + build output streams live in a pane (the server resolves the catctl
path — override with `CATS_CATCTL` if it lives somewhere unusual).

Local checkouts go through the same **add…** prompt: a source shaped like a
path (`./dir`, `../dir`, `~/dir`, `/dir`) links it in place instead of cloning,
matching `catctl plugin link`. A relative path resolves against the **focused
pane's cwd** — so with a pane sitting next to a `cats-todo` checkout,
`./cats-todo` links it in place, and `../cats-todo` works from inside a sibling
project. The leading `./` matters: only a path-shaped source links in place —
a bare two-segment `rohanthewiz/cats-todo` is the `owner/repo` GitHub
shorthand and clones instead. Linked rows show their checkout path, and swap `update`
(which has no remote to pull from) for **rebuild**, a re-link that re-runs the
manifest's build steps to pick up local edits; **unlink** removes only the
link, never the checkout.

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

**Theming note (source of truth):** the theme system lives in
`internal/theme` — the built-in palettes (`builtin.go`), the canonical color
keys and the derivation table that fills sparse themes (`theme.go`), and the
user/plugin theme-file loaders (`load.go`). `renderPage` (`cmd/catway/page.go`)
resolves `config.Theme` (name + overrides) against that registry and injects
the **full** palette as a `:root{…}` block *after* the stylesheet, so the
resolved theme always wins the cascade; the `:root` block in
`cmd/catway/web/index.html` is only the fallback for an uninjected page and
mirrors the `cats-green` built-in. When adding a color var: add it to the
derivation table (or required keys) in `internal/theme/theme.go`, give
`cats-green` its hand-authored value, and reference `var(--...)` (with the
same fallback value in `:root`) in the stylesheet. The canvas-side colors
(`term-fg/bg`, `sel-fill`, `cm-cursor`, `scroll-thumb*`) are re-read from the
CSS custom properties by `readThemeVars()` in the page script.

## History

This codebase replaced the Rust/ratatui cats through a phased migration:
Phase A (a thin web client attached to the Rust server), Phase B (Go-owned
PTY + VT emulation behind an orchestration seam), and Phase C (the
orchestrator, layout, detection, persistence, and web chrome in Go). The
design docs and per-workstream session notes live in
[`ai_docs/`](ai_docs/); retired phase code is recoverable from git history.
