# Getting started

## Prerequisites

* **Go 1.26+**.
* A **login shell** (present everywhere) and **`git`** (only if you use
  worktrees or plugins).
* The VT engine, **libghostty-vt**, is vendored in
  `third_party/libghostty-vt`. Building it downloads a pinned **Zig 0.15.2**
  into `.tools/` (gitignored, no system changes).

`catway` and `cathost` need CGO and the `ghostty` build tag. `catctl` and most
`internal/` packages build with a plain `go build ./...` — no Zig toolchain
required.

## Build

```bash
make vt          # one-time: build the vendored libghostty-vt
make binaries    # catway + cathost + catctl into bin/ (-tags ghostty)
```

Other useful targets:

```bash
make local       # install the three binaries into ~/bin
make check       # everything CI runs: fmt, vet, untagged tests, tagged race tests
make dist        # release tarball for this host's OS/arch into dist/
make macapp      # dist/Cats.app        (self-contained, mode 1)
make macapp-client  # dist/Cats Client.app (thin client, mode 2)
```

See [Build and packaging](reference/build-and-packaging.md) for the details,
including the macOS 26 SDK workaround.

## Run it by hand

```bash
# 1. Terminal backend. -persistent means panes survive a catway restart.
bin/cathost -socket /tmp/cats-cathost.sock -persistent &

# 2. The cats server.
CATS_PASSWORD=changeme bin/catway --addr :8421

# 3. Open http://localhost:8421 and sign in.
```

Startup order is not load-bearing — `catway` dials `cathost` lazily and retries
with backoff, so you can start either first and restart either independently.

For a trusted-localhost setup, `catway --auth none` skips the login entirely.
`catway --tls` serves HTTPS with an auto-generated self-signed certificate.

Precedence for server settings is **flag > config file > default**. Run
`catway -h` for the full flag set.

!!! note "The web UI is embedded"
    `cmd/catway/web/index.html` is compiled into the `catway` binary with
    `//go:embed`. After editing it you must rebuild and restart `catway` — a
    browser reload alone keeps serving the old page.

## Run it as a Mac app

```bash
make vt && make macapp
open dist/Cats.app
```

`Cats.app` supervises its own `cathost` and `catway` on a private socket and a
loopback-only port, then shows their UI in a WebKit window. It is fully offline.
See [Mode 1 — Standalone Mac](architecture/standalone-mac.md).

The bundle is unsigned. On another Mac it needs a one-time right-click → **Open**
to clear Gatekeeper.

## First commands

Drive a running `catway` from the CLI over its control socket:

```bash
catctl panes                          # list panes
catctl split h 2                      # split pane 2 side-by-side
catctl run 1 make test                # type into pane 1 and press Enter
catctl wait 1 "BUILD SUCCESSFUL" 120  # block until pane 1 prints the pattern
catctl events 1                       # stream pane events until Ctrl-C
catctl help [verb]                    # the verb list, or one verb's page
```

## Turn on shell completion

Worth doing first — it saves looking up pane ids by hand, since the completer
asks the running server for them:

```bash
echo 'eval "$(catctl completion zsh)"' >> ~/.zshrc
```

Bash and fish are supported too, and installed plugins get completion for their
own commands. See the [CLI reference](reference/cli.md#catctl-completion).

## Wire up an agent's hooks

Detection works without any setup, but installed hooks give richer state —
permission prompts, task completion, resumable session ids:

```bash
catctl integration install claude
catctl integration status
```

This edits the agent's own config tree and needs no running `catway`. See
[Agent detection](subsystems/agent-detection.md) and the
[Hook API](protocols/hook-api.md).

## Where state lives

| What | Path |
|------|------|
| Config | `~/.config/cats/config.yaml` (`$XDG_CONFIG_HOME/cats`) |
| Session + scrollback state | `~/.local/state/cats` (`$XDG_STATE_HOME/cats`) |
| Auto-generated TLS cert | `~/.config/cats` |
| Plugins | `~/.config/cats/plugins` |
| Worktrees | `~/.cats/worktrees` (configurable) |
| `catapp` own settings | `~/Library/Application Support/cats/app.json` |
| Default sockets | `/tmp/cats-cathost.sock`, `/tmp/cats-control.sock`, `/tmp/cats-hooks.sock` |

`catapp` in local mode moves all three sockets under `$TMPDIR` keyed by its pid
— per-user, `0700`, and unique per launch.
