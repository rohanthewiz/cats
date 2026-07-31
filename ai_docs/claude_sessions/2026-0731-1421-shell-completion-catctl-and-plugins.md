# Session: Shell Completion for catctl and Its Plugins

- **Session ID:** `32e99ac7-db83-49e3-afb5-a7f62da91c65`
- **Date:** 2026-07-31
- **Branch:** main
- **Repos:** `cats` **and** `cats-todo` (both touched)

## Request

> I would like shell integration (autocomplete and help) for Cats and its plugins

Two scoping questions were asked up front, and answered:

- **Plugin depth** → not just completing plugin ids/actions, but *plugin
  binaries complete themselves* (a manifest surface + a cats-todo change).
- **Binaries** → `catctl` only. `catway`/`cathost` are daemons launched by the
  app or a script, not typed interactively.

## What shipped

### 1. `catctl __complete` — the engine (`cmd/catctl/complete.go`, new)

A hidden verb holding the entire completion vocabulary in Go, so it can never
drift from a hand-maintained shell script.

**Protocol** (deliberately shell-agnostic):

```
catctl __complete [--for <binary>] [--] <word>...
```

Words are the command line after the program name, the last being the
(possibly empty) word under the cursor. Output:

```
value<TAB>description     # description optional
:nofiles                  # terminating directive — or :files / :dirs
```

A directive **always** terminates the output, so a shell parses it without
knowing anything about catctl.

**What it knows:**

| Where | Candidates |
|-------|------------|
| first word | ergonomic verbs + families; raw §7 methods *once a prefix is typed* |
| `<pane>` | live pane ids from `pane.list`, described by handle / agent+state / title |
| `<num>` | live tab numbers from `tab.list` |
| `<id>` / `[workspace]` | live workspace ids from `workspace.list` |
| `theme <name>` | `theme.list`, active one marked |
| directions | `left\|right\|up\|down`, `h\|v`, `next\|prev` |
| `integration install\|uninstall` | targets, each labelled with its **install state** (reuses `InstalledIntegrationStatuses`) |
| `plugin run\|update\|uninstall` | installed ids (name + version), then that plugin's action ids (titles) |
| `plugin link` | `:dirs` |
| flags | global flags, plus each family's own (`--ref`, probe's `--url`…) |

Raw §7 methods are gated on a non-empty prefix: a bare `<TAB>` shows the
curated verb list, typing anything opens the full table. ~40 candidates instead
of ~80 on the first keystroke.

**Live lookups** are capped at **500 ms** and fail silently — a stopped server
gives an empty menu, never a pause at the prompt. A `--socket` already typed on
the line being completed is honoured, so completion queries the same server the
command will reach.

### 2. `catctl completion <bash|zsh|fish>` (`cmd/catctl/completion.go`, new)

One shared helper per shell plus one registration per command. The scripts know
*only* the wire format.

- **bash** — `COMPREPLY` from already-prefix-filtered candidates; `compopt -o
  default/dirnames` for the file directives, guarded for bash 3.2 (macOS
  system bash has no `compopt`); `complete -o nosort` attempted with a fallback.
- **zsh** — builds `value:description` entries and calls
  `_describe -V -t catctl`; `_files` / `_files -/` on the directives. Colons in
  *values* are escaped (`_describe` splits at the first one).
- **fish** — reads `value<TAB>description` natively;
  `__fish_complete_path` / `__fish_complete_directories` on the directives.

### 3. Plugins complete themselves — `[[completions]]` (`internal/plugin`)

```toml
[[completions]]
binary  = "cats-todo"                        # the name typed in the shell
command = ["./bin/cats-todo", "__complete"]  # plugin-root-relative, like an action
```

`catctl completion <shell>` reads every installed manifest and emits a
registration per declared binary, routing that command's Tab back through
`catctl __complete --for <binary>`. **One protocol in the shell, either style in
the plugin:**

- **Dynamic** (`command`) — catctl execs the plugin's completer and passes its
  reply through. Bounded at **2 s / 64 KiB**, output normalised to whole lines
  with exactly one directive. Gets `CATS_PLUGIN_ID`/`CATS_PLUGIN_DIR`; inherits
  the user's cwd so it can answer for the project they stand in.
- **Static** (`subcommands` / `flags`) — catctl serves them itself; zero
  completion code for a shell-script plugin.

New API: `Completion`, `Manifest.FindCompletion`, `plugin.CompletionArgv`,
`plugin.Completions()`, `plugin.LookupCompletion`, `BinaryCompletion`.
Validation bounds `binary` to `[A-Za-z0-9._-]` — it is pasted into generated
shell scripts as a word, so it is rejected at load rather than escaped at emit.

### 4. `catctl help <verb>` (`cmd/catctl/help.go`, new)

A page per verb: synopsis, summary, the §7 method it builds, what each argument
completes to, and — for the ~12 verbs whose shape a one-liner cannot carry —
the notes that were previously only in the source (`help wait`, `help send`,
`help capture`, `help read`, `help theme`, `help new-ws`…). Also accepts a
family name or a raw method (which points back at the ergonomic verb).

`help` dispatches **before** the global flag re-parse, so `catctl help --json`
describes the flag instead of setting it.

### 5. `argKind` on the subcommand table (`cmd/catctl/subcommands.go`)

Each verb declares what each positional slot completes, sitting immediately
beside `synopsis` on purpose — the two describe the same argument list, so an
author editing one has the other in view. `TestArgKindsMatchSynopsis` enforces
the pairing both ways.

### 6. cats-todo (separate repo)

`complete.go` implements the protocol; `main.go` routes the hidden
`__complete`; `cats-plugin.toml` declares `[[completions]]`. This is what lets
`-i` offer image files while `-t` offers nothing — the payoff of the dynamic
form over a static list.

## Gotchas encountered

- **`%%` in a `fmt.Fprintf` format string.** The shell bodies are full of
  `${line%%$'\t'*}`; printf would have silently turned `%%` into `%` and changed
  longest-match to shortest-match trimming. Switched to raw constants with an
  `@EXE@` placeholder + `strings.ReplaceAll`.
- **Backticks inside a Go raw string.** A comment mentioning `` `complete -a` ``
  inside the fish body terminated the literal. Prose in a raw-string shell
  template must not use backticks.
- **fish `complete -a` quoting.** The path *must* stay inside single quotes so
  fish defers the substitution to completion time; double quotes would run it
  once at load. Inlining a quoted path breaks the outer quoting, so the path now
  goes through `set -g __cats_exe` and the registration references the variable
  — which also survives a path with spaces.
- **fish drops empty variable expansions.** An empty current token is an empty
  *list*, so `$cur` would vanish from the argv and `__complete` would read the
  previous word as the one being completed. Forced to a one-element list holding
  `""`.
- **`PaneInfo.Focused` is per-*tab*.** In a session of single-pane tabs it is
  true for nearly every pane, so the first cut labelled all 14 "focused".
  `Focused && Visible` picks out the one globally focused pane.
- **A real bug caught by its own test.** `cappedWriter.Write` reassigned
  `p = p[:room]` and then returned `len(p)` — reporting a short write on
  truncation, which would make a child die noisily. Now captures the length
  first.
- **Family flags vs. global stripping.** Slot counting originally handed
  families a tail already stripped with the *global* flag table, which drops
  `--ref` but leaves `main` behind to be miscounted as an operand. Families now
  get the **raw** tail and re-scan with their own table (`firstPositional`).

## Files

- **New:** `cmd/catctl/{complete,completion,help,complete_test}.go`
- **cats touched:** `cmd/catctl/{main,subcommands}.go`,
  `internal/plugin/{manifest,plugin,plugin_test}.go`, `README.md`,
  `docs/getting-started.md`, `docs/reference/cli.md`,
  `docs/subsystems/plugins.md`
- **cats-todo:** `complete.go` (new), `main.go`, `cats-plugin.toml`, `README.md`

## Verification

- `make check` passes (fmt, vet, build, test, vet-ghostty, race-ghostty).
- catctl tests green **both** with a live server and with
  `CATS_CONTROL_SOCKET` pointed at nothing — no test passes silently because
  nothing answered.
- **bash verified end to end**: sourced the generated script, drove `_catctl`
  and `_catctl_for_cats_todo` with real `COMP_WORDS`/`COMP_CWORD`.
- **zsh verified in parts**: the function builds correct `value:description`
  items (stubbed `_describe`/`_files`), and `-V` confirmed a real `_describe`
  flag by reading zsh 5.9's `getopts "oOt:12JVx"`. A stable pty harness to drive
  a live Tab could not be got working — this is the one gap.
- **fish untested** — not installed on this machine; written defensively around
  the two traps noted above.
- Plugin path exercised end to end against a scratch `CATS_PLUGINS_DIR`
  symlinked to the cats-todo checkout, so the user's real plugin install was
  never touched.

## Discovered, not fixed

**The running server does not support `theme.list`** — `catctl themes` returns
*"command not supported yet (WS2 in progress)"*, so theme completion is
silently empty against it. The command exists in this tree, so the installed
MacApp build predates it. A rebuild/reinstall (`make macapp`) lights it up.
This is the stale-bundle pattern again.

## Follow-up ideas

- A pty-based zsh completion test would close the one verification gap; the
  `zsh/zpty` attempts here fought echo and timing.
- `resize <border>` completes nothing — border ids come from the pane tree and
  are not exposed by any §7 query. A `pane.borders` query would make it
  completable.
- `plugin install <owner/repo>` could complete from a registry, if one ever
  exists.
- The generated script's plugin list is fixed at generation time, so a plugin
  installed today is completable in the *next* shell. Documented rather than
  solved; a lazy per-command lookup would remove the caveat at the cost of a
  fork per Tab on unknown commands.
