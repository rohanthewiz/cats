# Local-path plugins in the dialog — link / rebuild / unlink

- **Session ID:** `20872547-512b-401c-afb6-e9565ce5675d`
- **Date:** 2026-07-25 01:36
- **Branch:** `main`
- **Scope:** `cmd/catway/web/index.html`, `internal/plugin/install.go`,
  `internal/plugin/plugin_test.go`, `README.md`

## Request

Started as "how do I start the server for quick testing", then: the cats-todo
plugin lives at a local path in this repo — how do I install it from the
plugins dialog? Answer at the time: you couldn't. Then "add local path support
to the dialog (list, linking and unlinking)", followed by "also support
`../dir`".

## Why the dialog couldn't do it

The dialog's install prompt always spawned `catctl plugin install <input>`, and
`install` means `git clone` (`internal/plugin/install.go`). A local path does
survive `resolveSourceURL` untouched, but `./cmd/cats-todo` isn't a git repo,
and cloning the cats repo root leaves `cats-plugin.toml` at `cmd/cats-todo/`
rather than the clone root, so `LoadManifest` fails. The local-path verb is
`link`, which the dialog never exposed.

## Design

- **One prompt, two routes.** The footer button is now **add…**; the prompt
  dispatches on the *shape* of the input — path-shaped → `catctl plugin link`,
  anything else → `catctl plugin install` (unchanged). Two separate buttons
  would have made the user classify their own input; the host already draws
  that line, so the dialog follows it. `pluginSourceIsPath` (`/^[.~/]/`)
  deliberately mirrors the Go `resolveSourceURL` heuristic so the dialog's
  choice always equals what catctl would have done with the same string.
- **Relative paths anchor on the focused pane's cwd.** The link tab is spawned
  with `tab.create`'s existing `cwd` param, filled from the focused pane's live
  `pane_cwd` state. Someone typing `./cmd/cats-todo` means the directory they
  are looking at — not wherever the server was started. Same precedent as the
  worktree dialogs (anchor on the focused pane's repo).
- **Tilde expands host-side, in `internal/plugin`.** `tab.create` execs argv
  directly with no shell, so `~/src/thing` would be read as a literal directory
  named `~`. Putting `expandTilde` in `Link` + `resolveSourceURL`'s path branch
  (rather than in catctl's CLI layer) means every caller — CLI, dialog-spawned
  catctl, any future host command — gets it for free with no new API surface.
  This also fixed `catctl plugin link ~/src/thing`, which was broken before.
  `~user` is left alone (needs the user database; nothing wants it).
- **`rebuild` is the linked analogue of `update`.** `plugin link` on the same
  checkout is idempotent and re-runs the manifest's build steps — exactly how a
  developer picks up edits. `update` refuses on linked plugins by design (no
  remote to pull from), so linked rows swap one button for the other. Closes
  the dev-iteration follow-up from the plugin-manager-dialog session.
- **Unlink needed no work**: `plugin.Uninstall` already removes only the
  symlink and reports "checkout left in place at …".

## What changed

- `cmd/catway/web/index.html`: `pluginSourceIsPath`, `focusedPaneCwd`;
  `pluginCatctlTab` gained an optional `cwd`; `pluginInstallDialog` retitled
  "add plugin" and branches to `link` (rejecting `--ref`, which is meaningless
  there); linked rows render their checkout path inline (new `.pal .row .sub`
  CSS — shrinks before the label so deep paths truncate instead of pushing the
  buttons off) and get a `rebuild` button; row tooltip now carries the dir;
  footer button `install…` → `add…`.
- `internal/plugin/install.go`: `expandTilde`, applied in `Link` and
  `resolveSourceURL`.
- `internal/plugin/plugin_test.go`: `TestExpandTilde`, `TestLinkTildePath`,
  `TestLinkRelativeParentPath` (links from a *sibling* dir so `..` is the only
  route — a broken resolution can't pass by accident), plus a
  `../sibling/repo` case in `TestResolveSourceURL`.
- `README.md`: plugins section documents the add… prompt, pane-cwd anchoring,
  linked-row path, rebuild vs update, and the `./` gotcha.

## The `./` gotcha (documented in both code and README)

A bare `cmd/cats-todo` is **not** a path — two segments is the `owner/repo`
GitHub shorthand, so it would try to clone `github.com/cmd/cats-todo`. The
prefix has to be required rather than inferred from whether the directory
exists, since the browser cannot stat the host filesystem.

`../dir` needed no code change (leading `.` already matched both heuristics);
the work there was proving the resolution lands correctly and naming the form
in the hint text + README.

## Verification

- `make check` clean end-to-end; the four link/path tests pass by name.
- Inline JS syntax-checked via `new Function` (index.html has no build step).
- `make local` reinstalled `~/bin/{catway,cathost,catctl}` — index.html is
  `go:embed`ed, so a browser reload alone would keep serving the old dialog.
- Not exercised in a live browser: the dialog changes are verified by syntax
  check + code reading only.

## Quick-testing recipe (from the top of the session)

```bash
cathost -socket /tmp/cats-cathost.sock -persistent &   # once; panes survive catway restarts
make local && catway --addr :8421 --auth none --state-dir /tmp/cats-test-state
```

Then gear menu → plugins → **add…**, with a pane in the cats repo:
`./cmd/cats-todo`.

## Follow-ups

- Live browser pass over the new dialog (link a checkout, rebuild it, unlink).
- `update --all` and a dialog-wide "update everything" button if plugins
  multiply (carried over).
- `min_cats_version` enforcement still pending a real server version constant
  (carried over).
