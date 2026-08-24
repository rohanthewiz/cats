# Session: cdx Back on Mission, then a Cats Plugin — and ~/.cats/bin Is Born

> Session: https://claude.ai/code/session_019fRMMPc8Kzwr8JWnDyr4uv
> Date: 2026-08-24
> Repos: cats (`3fbd633`), cdx (`8ed791a`, `4f44c65`) — all on main, pushed

## The prompts

1. cdx (`../cdx`) strayed from its mission: a simple call prints the chosen
   path instead of changing directory. Use a flag (or better idea) so
   programmatic callers get the path and humans get the cd.
2. Commit/push cdx; print the updated `cdx()` for another machine.
3. **What would it take to make cdx a non-agentic Cats plugin** — a tool Cats
   installs and manages? And the larger picture: **should Cats standardize on
   `~/.cats/bin`** for all Cats-related tools? (Planned, approved, built.)

## Part 1 — cdx: three endings, chosen by stdout

Finding first: Cats never shells out to cdx. `internal/pathpick` reads cdx's
state file (`os.UserConfigDir()/cdx/state.json`) — "no subprocess, nothing to
install" is the documented integration. The print-on-stdout design was cdx's
own initial commit, because **no child process can cd its parent shell**; the
sourced `cdx()` zsh function capturing stdout is the only in-place mechanism.
The "stray" only bites where that function isn't loaded.

The fix (cdx `8ed791a`, v0.2.0), decided by who's listening:

- `--print` / `-p` — explicit path-on-stdout for programs driving cdx
  (required when the driver allocates a PTY, where stdout looks like a tty).
- Bare binary, stdout **captured** (a pipe) → still prints. Every existing
  wrapper keeps working, no flag needed — the capture *is* the signal.
- Bare binary, stdout **on the terminal** → chdir + `exec $SHELL`: a subshell
  in the picked directory (`exit` returns), with a stderr note pointing at the
  in-place setup. Detection is `os.Stdout.Stat()` ModeCharDevice — no new dep.

`cdx()` and `cdwith()` now pass `--print` explicitly (old wrappers still work
via pipe detection; new wrapper needs the new binary — binary first when
updating another machine).

## Part 2 — The plugin + bin-dir design (explored, planned, approved)

Two Explore agents mapped the ground; the load-bearing facts:

- A Cats plugin is a **directory with `cats-plugin.toml`** under
  `~/.config/cats/plugins/<id>` (env `CATS_PLUGINS_DIR`), git-cloned by
  `catctl plugin install`, built by `[[build]]` steps, actions launched by
  **absolute path** (`ActionArgv` anchors to the plugin dir). Zero actions is
  legal (asset-only plugins; themes discover by convention).
- Two gaps for a plain shell tool: plugin binaries never reach PATH, and no
  hook existed for code that must be *sourced* (only sourced code can cd the
  user's shell).
- `~/.cats` already existed with one tenant (`worktrees/`) — user-visible,
  neither config (`~/.config/cats`) nor state (`~/.local/state/cats`). A bin
  dir fits that taxonomy exactly.
- Everything PATH funnels through `shellenv.Merge` (catapp hydration + the
  per-pane `Lookup` backstop) — one prepend covers panes and daemons. The one
  subtle trap: `Lookup` computed envPATH only when the login-shell probe
  succeeded.
- go-toml ignores unknown manifest keys → rollout is non-breaking, but cats
  must land **before** the cdx manifest means anything.

User decisions: `~/.cats/bin` holds **plugin tools only** for now (`make
local` stays at `~/bin`; `LOCAL_BIN` became `?=` overridable); cdx **does**
declare a tab action — with the new subshell ending, "run cdx in a tab" =
"open a tab whose shell lands in the picked directory".

## Part 3 — What landed in cats (`3fbd633`)

- **Manifest** (`internal/plugin/manifest.go`): `bin = ["./bin/tool"]` and
  `[shell] zsh = "shell/tool.zsh"`. Both validated via new `localRel` (clean,
  relative, `filepath.IsLocal`); bin base names bounded by `binaryPattern` and
  deduped; shell keys limited to bash|zsh|fish.
- **Symlink farm** (`internal/plugin/bin.go`): `BinDir()` = `$CATS_BIN_DIR` >
  `~/.cats/bin`. `SyncBinLinks` reconciles; links target the **stable**
  `<root>/<id>/<rel>` path (through the dev-link symlink, not into the
  checkout) so ownership is a readlink-prefix test, rebuilds never re-link,
  and `RemoveBinLinks` works with a broken manifest. Foreign entries: warn +
  skip, never overwrite; farm problems never fail an install. Hooks: Install
  (after the atomic rename, via `loadAndSyncBin`), Link (both paths), Update
  (**unconditionally**, even no-op — heals pre-feature installs), Uninstall
  (links first, then RemoveAll).
- **shellenv**: `CatsBin()`/`catsBinEntry()` (existence-gated, stat per call);
  `Merge` leads with it; `Lookup` restructured to merge unconditionally so a
  failed login probe no longer suppresses the injection. Tests pin
  `CATS_BIN_DIR` (they'd otherwise depend on whether the real farm exists).
- **`catctl shellinit <bash|zsh|fish>`** (`cmd/catctl/shellinit.go`): guarded
  PATH prepend (spelled `$HOME/.cats/bin` so the emitted text is
  machine-portable; an explicit `$CATS_BIN_DIR` override is emitted literally
  with **double-quote escaping** — `dqEscape`, fish variant differs on
  backtick; caught a bug where `shellQuote` inside a double-quoted context
  would have put literal apostrophes into PATH) + one guarded source line per
  installed plugin snippet (anchored to `inst.Dir`, so linked checkouts source
  live). Dispatches before flag.Parse like `completion`; wired into usage,
  help, families, and completion (`case "completion", "shellinit"`).
- **shellint v2**: assets gain the PATH bootstrap + `catctl shellinit` eval
  after the interactive guard; `Version = 2` (v1 installs report Outdated);
  package doc widened from "prompt marks" to general shell setup. One test
  fixed to rewrite the version stamp generically.
- Docs: plugins.md ("Binaries on PATH", "Shell hooks" sections + manifest
  table), cli.md (`catctl shellinit` section, widened shell target), README,
  getting-started state table.

## Part 4 — cdx as the worked example (`4f44c65`)

`cats-plugin.toml`: id `rohanthewiz.cdx`, `bin = ["./bin/cdx"]`,
`[shell] zsh = "shell/cdx.zsh"`, build `go build -trimpath -o ./bin/cdx .`,
action "Pick a directory and open a shell there", static flag completions.
Plus `/bin/` gitignored and README/cdx.zsh headers documenting the plugin
install path.

## Verified on this machine

`make check` clean. Local-path install → farm link created → `shellinit zsh`
emits both lines → `rm ~/bin/cdx` (stale hand copy retired) →
`catctl integration install shell zsh` (guarded block now in `~/.zshrc`) →
fresh interactive zsh has `~/.cats/bin` **first** in PATH, `cdx` as function,
binary resolving through the farm. Uninstall removes link + emitted line;
reinstall restores; deleting the link by hand and running a **no-op update**
heals it. Then pushed and reinstalled from GitHub (`catctl plugin install
rohanthewiz/cdx`) — same results.

## Loose ends / next

- `catctl plugin run rohanthewiz.cdx` (the tab action) untested — no Cats
  server was running. Try when one is up.
- cats-todo and gonotes gain nothing until their manifests declare `bin`
  (and optionally `[shell]`) — natural follow-ups; keep the cats-todo wire
  vocab lockstep rule in mind.
- Flipping `LOCAL_BIN` default to `~/.cats/bin` deliberately deferred
  (stale-copy shadowing hazard while `~/bin` copies sit earlier in PATH).
- Old catctl silently ignores the new manifest fields (go-toml leniency) —
  always `make local` before testing manifest changes.
