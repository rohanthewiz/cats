# Session: macapp Plugin Build Fails — GUI Launch Has No User PATH

- **Session ID:** `cb1e5c20-e0b5-4b58-ab9d-e322ffdf7aa8`
- **Date:** 2026-07-27
- **Branch:** main

## Request

Inside the standalone macapp (`Cats.app`), adding the `rohanthewiz/cats-todo`
plugin from the "add plugin" dialog fails. The clone succeeds, then:

```
sh: go: command not found
build step 1 (sh -c mkdir -p bin && go build -o bin/cats-todo .): exit status 127
```

The same install works fine from a terminal.

## Diagnosis

Nothing wrong with the plugin or the install code — it was the app bundle's
inherited environment.

A double-clicked `.app` is launched by launchd, **not** by a shell, so it starts
with the bare system PATH (`/usr/bin:/bin:/usr/sbin:/sbin` plus `/etc/paths.d`)
and none of the user's `.zprofile` / `.zshrc` additions (`/usr/local/go/bin`).

That environment propagates down the whole chain:

```
catapp (launchd env)
  └─ cathost  (internal/orchestration/host.go:1048 buildEnv → os.Environ())
       └─ pane (PTY child)
            └─ catctl plugin install   (spawned via tab.create, raw argv)
                 └─ runStep → sh -c "… go build …"   (internal/plugin/install.go:208)
```

Interactive panes look fine because their shell sources the user's rc files and
repairs PATH itself. The plugin build step runs `sh` **non-interactively**, so
nothing repairs it there — hence `go: command not found` only in that path.

## What was done

### `cmd/catapp/shellenv.go` (new)

Hydrate PATH from the user's login shell once at startup, before any child
process exists.

- `hydratePATH()` — no-op unless `__CFBundleIdentifier` is set (LaunchServices
  sets it, so it marks a GUI launch: double-click / Dock / `open -a`). A launch
  from a terminal (`go run ./cmd/catapp`) keeps the PATH it already has.
- `loginShellPATH()` — runs `$SHELL -ilc 'printf "…%s…" "$PATH"'`.
  - `-l` picks up `.zprofile` / `.bash_profile`, `-i` picks up `.zshrc` /
    `.bashrc`; users put PATH edits in either.
  - The value is fenced in `__CATS_PATH__` markers so an rc file that prints a
    banner (this user's prints `Sourcing custom configs...`) doesn't corrupt it.
  - 5s timeout via `exec.CommandContext`; stdin is `/dev/null` so an rc file
    that reads the terminal gets EOF instead of hanging.
  - `cmd.Output()` is parsed even on a nonzero exit — rc noise on stderr is
    normal; the markers decide whether the value made it out.
- `mergePATH()` — login-shell entries first, then any inherited entry the shell
  didn't mention (a managed Mac can inject one at the launchd level).
- Any failure leaves the inherited PATH alone: the bundled daemons resolve next
  to the executable (`resolveBinary`), not via PATH, so the app still starts.

### `cmd/catapp/main.go`

`runLocal()` calls `hydratePATH()` before `startBackend()`. Remote mode spawns
no children, so it is deliberately not hydrated.

### `cmd/catapp/shellenv_test.go` (new)

- `TestBetweenMarkers` — marker extraction incl. rc-banner noise, missing and
  unterminated markers.
- `TestMergePATH` — order + inherited-only entry preserved.
- `TestLoginShellPATH` — real `$SHELL` probe returns a PATH-shaped value.
- `TestHydratePATHOnGUILaunch` — the regression test: start from the bare
  launchd PATH, hydrate, assert `go` is resolvable again.
- `TestHydratePATHSkipsNonGUILaunch` — terminal launch leaves PATH untouched.

## Verification

- `gofmt` / `go vet` / `go test ./cmd/catapp/` — all pass (5/5 new tests).
- `make macapp` → rebuilt `dist/Cats.app`.
- Launched it with `open` (a real GUI launch) and read the spawned daemon's
  environment with `ps -Ewww`: PATH now contains `/usr/local/go/bin`. Quit that
  test instance afterwards; daemons reaped cleanly.

## Left to the user

`/Applications/Cats.app` is still the pre-fix build. Quit the running instance
and `cp -R dist/Cats.app /Applications/`, then the `rohanthewiz/cats-todo`
install builds cleanly.

## Notes / possible follow-ups

- Only PATH is imported. If a plugin build ever needs other login-shell vars
  (`GOPATH`, `CC`, toolchain managers' hooks), the same marker trick can carry
  more of the environment.
- A standalone `catway` started outside a shell (launchd agent, some CI) would
  hit the same class of problem; the fix lives in the launcher only.
