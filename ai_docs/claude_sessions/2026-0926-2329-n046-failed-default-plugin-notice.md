# Session: N-046, a notice for default plugins that failed to install

Session ID: 2d0d9b1b-d6e4-4814-b66e-03e292915a18
Date: 2026-09-26
Driven from: cats

The user pasted next-list item N-046:

> A default plugin that failed to seed is only reported in the daemon log. A
> fresh-install user without Go never learns why cats-todo is missing. The
> plugins dialog could show a "cats-todo could not be installed: …" line from
> `.cats-defaults.json`'s `last_error`, with the install button next to it.

## 1. What the item missed

`last_error` was only kept while a default was still **pending**. On the last
attempt (`maxSeedAttempts` = 3) the seed dropped the entry, and the error went
with it. So the Go-less machine, which fails all three times, was left with
nothing to show. The error alone was also thin. A build failure reads
`build step 1 (sh -c …): exit status 127`, and the actual reason
(`go: command not found`) is in the build output, which only went to the log.

## 2. Design

- **The state file keeps give-ups.** `defaultsState` gains
  `Failed []pendingDefault` (`"failed"`, omitempty). A default the seed gives
  up on moves there with its error. The seed never reads `Failed` again. It
  exists only for the notice.
- **Each failure records its output tail.** `pendingDefault.LastOutput`
  (`last_output`) holds the last 6 lines of the attempt's clone/build output.
  `SeedDefaults` tees each attempt into its own buffer (with `io.MultiWriter`
  when the caller passed a writer) and keeps the tail via `tailLines`.
- **`plugin.FailedDefaults()`** reads the state with no lock and returns both
  kinds of failure: still pending after at least one attempt (`GaveUp`
  false) and given up (`GaveUp` true). It leaves out anything whose id is
  present in the plugins root, so a successful install hides the notice by
  itself. It returns nothing when there is no state file or the opt-out is set.
- **`plugin.DismissDefault(id)`** removes the id from both lists. It is "I
  don't want this one", so it also stops pending retries. A dismiss of an
  unknown id succeeds, so two windows can both dismiss the same notice.
- **`Uninstall` forgets the default too** (`forgetDefault`, best effort). This
  also fixes an older gap: a default installed by hand while still pending,
  then uninstalled before the next catway start, was seeded straight back in.
- **`defaultsMu`** serializes read-modify-write passes in one process. The seed
  holds it for its whole pass, installs included. A dismiss sent during the
  first-start seed therefore waits rather than having its write overwritten by
  the seed's final one. catctl is another process and is not covered; its only
  write (forget on uninstall) can at worst leave one stale notice, which the
  presence filter mostly hides.

Protocol (`wire/vocab.go`, additive):

- `PluginListResult.FailedDefaults []PluginFailedDefault`
  (`failed_defaults`: `id`, `source`, `attempts`, `gave_up`, `error`,
  `output`).
- New command `plugin.dismiss_default {id}` (`PluginDismissDefaultParams`,
  `ParamsRequired`, **not** `Recorded`: it is housekeeping on this machine's
  seed state, not a runbook step).

Front-end (`cmd/catway/web/js/30-plugins.js`, `css/21-plugins.css`):

```
┌──┐ cats-todo  not installed · gave up          rohanthewiz.cats-todo
│ !│ cats-todo could not be installed: build step 1 (sh -c …): exit…
└──┘ sh: go: command not found                        [install][dismiss]
```

`failedDefaultRow` uses the installed-row anatomy, with the `--warn` edge
treatment instead of the update accent. Nothing is broken, it is just news.
Rows go above the installed plugins and are not counted in the header. The
tooltip carries the full error, the output tail and the source. **install**
runs `catctl plugin install <source>` in a tab (the same path as add…).
**dismiss** sends `plugin.dismiss_default` and re-opens the dialog.

`catctl plugin list` prints the same notice after the listing, and also when
the list is empty, since that is the likely reason it is empty (`defer
printFailedDefaults()`).

## 3. Files

- `internal/plugin/defaults.go`: `Failed`, `LastOutput`, `defaultsMu`,
  `seedOutputTailLines`, the output tee in `SeedDefaults`, `FailedDefault`,
  `FailedDefaults`, `DismissDefault`, `forgetDefault`/`forgetDefaultLocked`,
  `dropDefault`, `tailLines`.
- `internal/plugin/plugin.go`: `Uninstall` calls `forgetDefault` after both
  the unlink and the remove paths.
- `wire/vocab.go`: `CmdPluginDismissDefault`, the vocab entry,
  `PluginListResult.FailedDefaults`, `PluginFailedDefault`,
  `PluginDismissDefaultParams`.
- `internal/app/commands.go`: `Backend.StartPluginDismissDefault`, dispatch
  case (id required).
- `internal/app/wire_aliases.go`: `PluginDismissDefaultParams` and
  `CmdPluginDismissDefault` aliases, added by hand (see §5).
- `cmd/catway/plugins.go`: `StartPluginList` appends `FailedDefaults`
  (a read error is logged, not fatal); `StartPluginDismissDefault` runs off
  the loop.
- `cmd/catctl/plugin.go`: `printFailedDefaults`.
- `cmd/catway/web/js/30-plugins.js`: `failedDefaultRow`, the layout diagram
  (it had shown cats-todo both installed and failed; now uses cats-git for the
  update row, and the footer no longer shows `update all` with one update).
- `cmd/catway/web/css/21-plugins.css`: `.row.plg.dflt`, `.pill.st.missing`.
- Docs: `docs/subsystems/plugins.md` (a "Failures are shown" bullet, and the
  command in the dialog diagram), `docs/protocols/control-api.md` (table row
  plus a paragraph), `README.md`.

## 4. Verification

- New tests in `internal/plugin/defaults_test.go`:
  - `TestSeedDefaultsRetriesThenGivesUp` now also checks the `Failed` entry
    after the last attempt and `FailedDefaults` on every pass (`GaveUp`,
    `Attempts`, and a non-empty output tail with git's own complaint).
  - `TestDismissDefault`: one pending, one given up; double dismiss; the next
    seed pass does nothing.
  - `TestFailedDefaultInstalledThenUninstalled`: the presence filter, then
    uninstall forgets the default.
  - `TestFailedDefaultsNoState`.
- `TestDispatchPluginDismissDefault` in `internal/app/commands_test.go`.
- `go test ./...`, `make test-ghostty` and `make jstest` all green.
- **Live check:** a scratch catway and cathost from the scratchpad, on
  `127.0.0.1:18517`, with `CATS_PLUGINS_DIR`, `XDG_*` and `-state-dir` in the
  scratchpad and a hand-written `failed` entry. The row rendered as in the
  diagram, and dismiss emptied `.cats-defaults.json`. **install** was not
  clicked. The seed itself was not run live (still N-045).

## 5. Gotchas

- **The alias files are "generated", but the generator is gone** (noted in
  `2026-0923-1545`). A longer name added in the middle of a block makes gofmt
  re-align ~1000 lines. The new aliases sit after a blank line with a comment,
  which is its own gofmt alignment section. Only `internal/app` needs them:
  `TestCommandSpecsRouted` requires every `Dispatch` case to be a `Cmd*`
  identifier, so `wire.CmdPluginDismissDefault` there fails the test.
  `browserproto` was left alone, and `cmd/catway` uses `wire.*` directly.
- **A scratch catway needs a short cathost socket path.** A socket path under
  the scratchpad is over the 104-byte `sun_path` limit (`invalid argument`),
  and without cathost the page's commands never answer, so the dialog does
  not open. A relative `-socket h.sock`, with both processes started in the
  scratch dir, works. Port 8499 was already taken by another server.
- In the Chrome tab, coordinate clicks on the toolbar's plugins button did
  not open the dialog, but `document.getElementById("pluginsbtn").click()`
  did. Not looked into.
- The state file's embedded `DefaultPlugin` has no json tags, so its keys are
  `"ID"` / `"Source"` next to snake_case ones. That was already the case, and
  a hand-written state file has to match it.

## Next

Closed: N-046. Declined: None. Raised: N-048.
Deferred: None. Promoted: None.
Updated: N-045. Full list: `ai_docs/todo/next-list.md`.
