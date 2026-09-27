# Session: cats-todo seeded on a fresh install; headless background installs

Session ID: 5d81c282-e16e-4e89-97f9-e80669cb23d2
Date: 2026-09-26
Driven from: cats (with cats-todo alongside)

The user asked:

> Cats-Todo is to remain a plugin, however auto-install it on a fresh Cats install

and then, after the first pass surfaced two side effects:

> Address the two issues

A fresh install now gets cats-todo without being asked. catway installs it in
the background on first start, through the normal plugin install, so it stays
an ordinary plugin (update checks, uninstall, nothing special in the dialog).

## 1. Design

```
catway start ─▶ go seedDefaultPlugins() ─▶ plugin.SeedDefaults(plugin.Defaults)
                  │
                  ├─ CATS_NO_DEFAULT_PLUGINS set ───────────────────▶ nothing
                  ├─ <root>/.cats-defaults.json present ─▶ retry pending
                  └─ no state file
                       ├─ plugins root exists ─▶ existing install:
                       │                         write {"pending": []}
                       └─ no plugins root ────▶ fresh: write every default
                                                as pending, then install
```

- **Where it runs: catway.** That is the one process every kind of install
  runs (Cats.app, `make local`, a dist tarball on Linux). Plugins also live on
  catway's side of the wire, so a thin client pointed at a remote server seeds
  that server. It runs on a goroutine started just before `s.Run()`, after
  every fatal startup check, so a catway about to exit never starts a clone.
- **"Fresh" means no plugins root.** The root exists once anything has been
  installed or linked. An upgrading user who once uninstalled cats-todo still
  has it, so the plugin does not come back. The same holds after the seed:
  uninstalling a default is respected on every later start.
- **The state file is written before the first attempt.** Install itself
  `MkdirAll`s the root, so without the file a seed that failed offline would
  look like an existing install on the next start and never retry.
- **Retries are capped.** A failure stays pending with `attempts` and
  `last_error`. After `maxSeedAttempts` (3, one attempt per catway start) it is
  dropped. That covers a machine with no Go, where cats-todo's build always
  fails, so it doesn't clone on every start forever. Each failure is a
  `dlog.Warnf` with the last four lines of build output and the manual
  install command.
- **Already present is done.** A pending id found under the root (installed
  by hand, by peer sync, or by a pass that crashed after the rename) is
  dropped without cloning. `DefaultPlugin` carries the manifest id for exactly
  this, since `Install` only learns the id after the clone.
- **Unpinned.** The seed installs the default branch, the same thing the
  README's install line gives.
- **No broadcast.** The browser reads `plugin.list` on demand (dialog and
  picker open), so a plugin that lands mid-session shows up the next time
  either is opened.
- **PATH.** Under Cats.app, catway inherits the login-shell PATH that
  `cmd/catapp/shellenv.go` sets up (session 2026-0727-1324), so `git` and
  `go` resolve.

## 2. Headless installs (the two issues)

The first pass left two side effects, both fixed in the second:

1. **cats-todo's one-time backlog offer was used up.** Its
   `init --post-install` build step marks the offer made before deciding
   whether it can ask. An install nobody watches printed the hint into the
   daemon log and spent the offer.
2. **Peer sync could hang.** `internal/peersync` calls `plugin.Install`
   inside catway's HTTP handler. A catway started from a terminal handed that
   terminal to build steps (`hostHasTerminal`), so a prompting step would
   stall the sync on a question nobody sees.

The fix is one install mode for daemon callers:

- `plugin.InstallHeadless(source, ref, out)`. Build steps get no stdin, no
  `CATS_PLUGIN_INSTALL_CWD` (catway's cwd is not a project the user chose),
  and `CATS_PLUGIN_BUILD_HEADLESS=1`. `runBuild` / `runBuildStep` take a
  `headless` flag; `Install`, `Link` and `Update` pass false and behave as
  before.
- Used by `SeedDefaults` and by `peersync.applyPlugins`.
- A missing terminal alone could not carry the signal: a scripted
  `catctl plugin install` with stdin redirected has no terminal either, and
  that user does read the hint.
- **cats-todo** (`init.go`): `runInstallOffer` returns before touching the
  marker when `CATS_PLUGIN_BUILD_HEADLESS` is non-empty, and prints nothing.
  The offer then comes on the first install or update someone watches, e.g.
  an update from the plugins dialog, which runs in a tab. Known gap:
  `offerAlreadyMade` also treats an existing global config dir as "already a
  user", so someone who starts using cats-todo before any watched
  install/update is never asked. Left alone on purpose, since by then they
  know the tool.

## 3. What changed

**cats** (`53feaff`)
- `internal/plugin/defaults.go` (new): `DefaultPlugin`, `Defaults`,
  `NoDefaultsEnvVar`, `SeedDefaults`, `SeedOutcome`, and the state file
  (atomic temp + rename write).
- `internal/plugin/install.go`: `InstallHeadless`, `HeadlessEnvVar`, and the
  `headless` flag through `runBuild` / `runBuildStep`.
- `internal/plugin/update.go`: passes `headless=false`.
- `internal/peersync/plugins.go`: `applyPlugins` uses `InstallHeadless`.
- `cmd/catway/plugins.go`: `seedDefaultPlugins` (log per outcome) and
  `outputTail`. `cmd/catway/main.go`: `go seedDefaultPlugins()` before
  `s.Run()`.
- Tests (`internal/plugin/defaults_test.go`, new): fresh install (plus a
  headless build env check, the state file staying out of `List`, and an
  uninstall not coming back), existing install, opt-out (writes no state),
  retry then give up, already present, and `TestInstallIsNotHeadless`.
- Docs: `docs/subsystems/plugins.md` gets "Default plugins on a fresh
  install" (with a mermaid flow), `CATS_PLUGIN_BUILD_HEADLESS` in the
  build-step env table, a "Headless installs" note, and a line in the cats-todo
  section. `docs/reference/configuration.md` gets `CATS_NO_DEFAULT_PLUGINS`.
  The README's cats-todo paragraph now says fresh installs get it
  automatically.

**cats-todo** (`56c5c4d`, committed, not pushed)
- `init.go`: `hostHeadlessEnvVar`, `hostBuildIsHeadless`, and the guard at
  the top of `runInstallOffer`.
- `init_test.go`: `TestInstallOfferWaitsOutAHeadlessInstall`.
- `cats-plugin.toml`: the build-step comment now describes the host's current
  env (it still said the host never passes a terminal).

## 4. Verification

- cats: `make fmt-check vet`, `go test ./...`, and ghostty-tagged
  `go test` / `go vet` for `cmd/catway`, `internal/plugin` and
  `internal/peersync`. All green.
- cats-todo: `go vet .`, `go test .`. All green.
- End-to-end seed against the real `rohanthewiz/cats-todo` from GitHub into
  a scratch root (`CATS_PLUGINS_DIR`, `CATS_BIN_DIR`,
  `CATS_TODO_CONFIG_DIR`), via a throwaway `cmd/_seedcheck` (deleted
  afterwards). It cloned, built and installed v0.42.0 with the
  `~/.cats/bin`-style link in about 3s. A second pass was a no-op and the
  state file read `{"pending": []}`.
- The real cats-todo binary: `CATS_PLUGIN_BUILD_HEADLESS=1 cats-todo init
  --post-install` printed nothing and created no config dir. The next run
  without the variable printed the hint and wrote `.install-offered`.
- Not run: a real catway start (or Cats.app) on a machine with no plugins
  root, so the goroutine, the log lines and the dialog showing the seeded
  plugin have not been seen end to end (N-045).

## Next

Closed: None. Declined: None. Raised: N-044, N-045, N-046.
Deferred: None. Promoted: None.
Updated: None. Full list: `ai_docs/todo/next-list.md`.
