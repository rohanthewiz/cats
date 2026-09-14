# ~/.cats/bin put to work: cats-todo, gonotes and ced on PATH

*`~/.cats/bin` was wired up (on PATH in every pane) but empty, so every
plugin tool typed by name resolved to a stale hand-installed copy in `~/bin` —
cats-todo was running 0.22.0 while the plugin had built 0.31.2. All three
plugins now declare `bin`, the host links them into `~/.cats/bin`, the `~/bin`
copies are gone, and `~/.zshrc` evals `catctl shellinit zsh`. ced's manifest
reversed yesterday's deliberate "no bin" choice because the cats plugin is ced's
official install; its README now says so, and a `make install` doc mismatch was
fixed on the way out.*

Session: https://claude.ai/code/session_017JuGQ823Vnw61jMoV3avZV
Date: 2026-09-14
Repos: `~/projs/go/cats` (branch `main`, this doc only), `~/projs/go/cats-todo`
(`main`), `~/projs/go/gonotes` (`master`), `~/projs/go/ced` (`main`); plus
`~/.zshrc`
Previous doc: `2026-0914-0158-catway-restart-cats-todo-release.md`

## Request

> Cats has a bin directory at `~/.cats/bin`. Is that even in use?

Follow-ups, in order:

> You make no mention of the plugins bin dir and executable, for example: `~/.config/cats/plugins/rohanthewiz.cats-todo/bin/cats-todo`

> yes, do cats-todo and gonotes. Also ensure ~/.cats/bin is in the PATH

> What about the ced plugin can we do similarly for it too?

> The official install of CEd is through Cats as a plugin, not homebrew

> fix the make install mismatch in ced's README

## 1. What `~/.cats/bin` is, and why it was empty

### The machinery (all in cats, unchanged this session)

- **Symlink farm** — `internal/plugin/bin.go` `SyncBinLinks` reconciles
  `~/.cats/bin/<name> -> <plugins-root>/<id>/<declared bin path>` from a
  manifest's top-level `bin = [...]`. Run after install, link and update.
  Ownership is a prefix test on the link target, so a link pointing into a
  plugin's dir that its manifest does not list is removed as stale on that
  plugin's next update — a hand-made link there does not survive.
- **Panes** — `internal/shellenv/shellenv.go` `Merge` puts the dir first on
  PATH for everything catway spawns, gated only on the directory existing.
  That is why it led PATH inside cats even while empty.
- **User shells** — `catctl shellinit zsh` emits a guarded prepend
  (`case ":$PATH:" in *":$HOME/.cats/bin:"*) ...`). It was not in any rc file.
- **Precedence claim** — the cats Makefile keeps cats's own binaries
  (`catway`, `cathost`, `catctl`) in `~/bin`; `~/.cats/bin` is plugin-only.

### Why nothing was linked

None of the three installed plugins declared `bin`:

| Plugin | `bin` | Why |
|---|---|---|
| cats-todo | none | never had one (`git log -S` on its manifest) |
| gonotes | none | never had one |
| ced | none | deliberate, `f179657` (2026-09-13): feared shadowing a Homebrew / install.sh ced |

cdx, the plugin the dir was created for on 2026-08-24, is no longer installed.

### The actual problem: two builds per name

Plugin **actions** exec `./bin/<tool>` relative to the plugin root, so the
palette always ran the fresh build. Everything that looked the name up on PATH
(shells, agents, cats's `editor.command` spawn of `ced`) fell through the empty
`~/.cats/bin` to `~/bin`:

| Tool | Plugin build | `~/bin` copy (what PATH found) |
|---|---|---|
| cats-todo | 2026-09-14, v0.31.2 | 2026-09-01, v0.22.0 |
| ced | 2026-09-14, v0.3.0 | 2026-09-10, v0.2.0 |
| gonotes | 2026-09-02 | 2026-08-26 (bytes differ; no `--version`) |

## 2. cats-todo and gonotes

- **Manifests** — added a top-level `bin` (above the `[[build]]` tables, as
  TOML requires) with a comment explaining the two-builds-per-name problem.
  - cats-todo `ed6eb59` — `feat(plugin): expose cats-todo on PATH via the cats bin dir`
  - gonotes `1dbc677` — `Expose gonotes on PATH through the cats bin dir`
- **Updates** — both `.cats-plugin-source.json` records carry no `ref`, so
  `catctl plugin update <id>` tracks the default branch; both picked up the new
  commits and created the links. The gonotes update also brought in
  `dd346b0` (advanced search), which was on `master` but not yet installed.
- **Removed** `~/bin/cats-todo` and `~/bin/gonotes` after checking: no
  references in LaunchAgents, rc files, Claude settings or cats config; no
  running process from those paths (the running cats-todo was the plugin
  build); no Makefile or script in either repo installs to `~/bin`.

## 3. PATH for terminals outside cats (`~/.zshrc`)

Appended at the very end, after `catctl completion zsh`:

```sh
eval "$(catctl shellinit zsh)"
```

It has to be last: `~/.zshrc` reassigns PATH wholesale on line 31, and later
lines prepend more. Chose this over `catctl integration install shell zsh`
because that also installs OSC 133 prompt/command marks — more than was asked.

## 4. ced

- **Asked first** (it reverses a deliberate decision). Options offered: a
  local-only `~/bin/ced` symlink to the plugin build, adding `bin` to the
  manifest, or leaving it. Chosen: add `bin` to the manifest.
- `9103b61` — `feat(cats): expose ced on PATH via the cats bin dir`. Updated
  the plugin, verified `~/.cats/bin/ced` → v0.3.0, removed `~/bin/ced` (0.2.0,
  not running).
- **Mid-turn correction from the user:** the cats plugin is ced's *official*
  install, not Homebrew. That made the manifest comment's "trade-off against a
  Homebrew copy" framing wrong.
- `95da6ea` — `docs(install): the cats plugin is the official install`:
  - Manifest comment reworded: the plugin build is meant to be `ced`
    everywhere; other builds are shadowed, call them by path.
  - README: "Inside cats (as a plugin) — the official install" now leads the
    Install section (with `plugin update` and the `shellinit` line). The old
    plugin section further down, which claimed the plugin never puts ced on
    PATH, was removed. Homebrew, install script, prebuilt binaries and from
    source sections kept, under "The sections below cover running ced without
    cats."
- `dd361f2` — `docs: make install copies to /usr/local/bin, not $GOPATH/bin`.
  The Makefile's `install: build` copies `bin/ced` to `/usr/local/bin`; both
  README ("From source") and ced's `CLAUDE.md` build section said `go install`
  to `$GOPATH/bin`. Fixed both, noted sudo, and pointed at `make build` for
  `./bin/ced` alone.

## Verification

- `ls -la ~/.cats/bin` — three links, each into its plugin's `bin/`.
- `~/.cats/bin/cats-todo --version` → `0.31.2`; `~/.cats/bin/ced --version` → `0.3.0`.
- Fresh shell: `env -i HOME=$HOME TERM=xterm zsh -i -c 'type -a cats-todo gonotes ced'`
  → only `~/.cats/bin/...` for each; `${path[1]}` is `~/.cats/bin`.
- This session's own PATH resolves the same.
- Not verified: clicking a path in cats to confirm the editor spawn now opens
  ced 0.3.0 (follows from `shellenv.Merge`, not observed).

## Files touched

| Where | File | Change |
|---|---|---|
| cats-todo | `cats-plugin.toml` | `bin = ["./bin/cats-todo"]` + comment |
| gonotes | `cats-plugin.toml` | `bin = ["./bin/gonotes"]` + comment |
| ced | `cats-plugin.toml` | `bin = ["./bin/ced"]`; "NO [bin] ENTRY" comment replaced |
| ced | `README.md` | plugin install first; stale PATH claim removed; `make install` target fixed |
| ced | `CLAUDE.md` | `make install` line fixed |
| home | `~/.zshrc` | `eval "$(catctl shellinit zsh)"` at the end |
| home | `~/bin/{cats-todo,gonotes,ced}` | deleted |

## Open / next

- Already-open terminals outside cats need `exec zsh` to get the new PATH.
- `catctl integration install shell zsh` (adds OSC 133 marks for the command
  ledger) was not run; the plain `shellinit` eval covers PATH only.
- ced's README still documents Homebrew / install.sh installs; now positioned
  as "without cats". Whether they should stay at all is the user's call.
- ced's Makefile `alt` target installs to `~/bin/ce` and its comment still
  talks about "a brew-installed ced"; untouched.
