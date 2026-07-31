# Build steps get the invoking directory and (conditionally) the terminal

- **Session ID:** `4f37aea1-3489-4178-b3ae-1a753a3ecb71`
- **Date:** 2026-07-31 00:03
- **Branch:** `main`
- **Scope:** `internal/plugin/install.go`, `internal/plugin/plugin_test.go`,
  `docs/subsystems/plugins.md`, `README.md`
- **Driven by:** a cats-todo session (see that repo's
  `ai_docs/claude_sessions/2026-0731-0003-both-repos.md`) — cats-todo's project
  backlog became a committed file, and it wanted to offer, once at install
  time, to create one for the project you installed from.

## The blocker

`runStep` — shared by the git invocations and the `[[build]]` steps — set
`cmd.Dir` to the plugin root and never touched `Stdin` (no `Stdin` reference
existed anywhere in the package). A build step therefore could not:

- **see which project the user is in.** Its working directory is the plugin
  root, by design and correctly so for building. The plugin root is the only
  directory it can name.
- **ask the user anything.** With `cmd.Stdin` nil the child gets `/dev/null`,
  so a prompt reads EOF instantly.

## The change

`runBuild` now calls a new `runBuildStep`; `runStep` survives unchanged for the
host's own git calls. The split is the point — the two callers want opposite
things.

`runBuildStep` adds exactly two things:

| | |
|-|-|
| `CATS_PLUGIN_INSTALL_CWD` (exported const `InstallCwdEnvVar`) | the host process's own working directory — catctl inherits the user's shell cwd, so this is the answer to "which project?" |
| stdin | the host's terminal, **only when `hostHasTerminal()`** |

Both are conditionalized or additive, so no existing plugin changes behavior.

### Why stdin is conditional

Inherited unconditionally, a build step that reads stdin would hang a headless
or scripted install **forever** instead of seeing the immediate EOF it gets
today. A plugin author is expected to handle both: check whether stdin is a
terminal, and fall back to printing instructions. That is documented.

### Why git keeps the old behavior

With a terminal attached, a clone of a private repo would sit at a credential
prompt inside what the user experiences as "catctl is installing" — an
invisible stall. Without stdin it fails fast, which is the better failure.

## `hostHasTerminal` is an ioctl, not a mode check

The obvious implementation is wrong:

```go
fi.Mode()&os.ModeCharDevice != 0   // ← says /dev/null is a terminal
```

`/dev/null` is a character device, and it is exactly what stdin becomes when
catctl itself is run with stdin unset — so the check would hand a build step
"a terminal" that answers every prompt with instant EOF. It now asks the fd for
its window size (`pty.Getsize`, a terminal-only ioctl) using `creack/pty`,
already a direct dependency. No new dep.

## Tests & docs

- `TestBuildStepSeesInvokingDirectory` — a build step writes `$PWD` and
  `$CATS_PLUGIN_INSTALL_CWD` to a file; the test asserts the first is the
  plugin root and the second is the host's cwd (EvalSymlinks-normalized, since
  macOS temp dirs live behind `/var` → `/private/var`).
- `docs/subsystems/plugins.md` gains a **Build step environment** section: the
  table, why stdin is conditional, and the instruction to design steps to work
  either way. The manifest field table links to it; the README manifest example
  gains a three-line comment pointing there.

Verified end-to-end with `expect`: `catctl plugin link` of the cats-todo
checkout, run from an unrelated project directory with an isolated
`CATS_PLUGINS_DIR`, reached the plugin's prompt and named the invoking project
rather than the checkout.
