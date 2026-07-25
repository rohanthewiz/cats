# Plugin manager dialog — web UI plugins menu over §7 + catctl tabs

- **Session ID:** `db659f9c-ad9c-47cd-a628-438f6816dcda`
- **Date:** 2026-07-24 22:24
- **Branch:** `main`
- **Scope:** `internal/app` (vocab + dispatcher + tests), `cmd/catway/plugins.go`
  (new), `cmd/catway/web/index.html`, `internal/browserproto/cmd.go`, Makefile,
  README

## Request

"Make plugin management more intuitive" — a menu listing plugins with
install/uninstall/update, or possibly a custom pane. Explored three shapes and
the user picked the **hybrid**: a browser dialog for the instant verbs, spawned
`catctl` tabs for the streaming ones.

## Design

The deciding constraint: browser commands get exactly one `cmd_result` (no
progress channel), while `plugin.Install/Update` run git + a build whose live
output matters. A pane is the streaming surface the app already has — and an
exited pane keeps its output on screen (`pane_exited` marks chrome; the pane
stays). So:

- **Instant verbs become §7 commands** — `plugin.list` / `plugin.uninstall`
  registered once in `internal/app` (vocab consts + `CommandNames()` +
  dispatch cases), which lands them on both surfaces at once: browser WS and
  the catctl control socket. `TestCommandNamesAllRouted` guards the drift.
- **Streaming verbs stay in catctl** — the dialog's update/install buttons
  send `tab.create` with `command: [catctl, "plugin", "update", id]` etc., so
  the git/build output streams in a fresh tab and failures stay readable.
  The server never runs the subprocesses.
- **Backend seam, worktree-shaped**: `StartPluginList(r)` /
  `StartPluginUninstall(r, p)` on `app.Backend`, implemented in a new
  ghostty-tagged `cmd/catway/plugins.go` — disk work on its own goroutine,
  `o.post` resolves the responder back on the loop. Keeps `internal/app` free
  of the `internal/plugin` dependency (fakes stay trivial).
- **Wire-ready list result**: `PluginInfo` carries action argv already
  resolved via `plugin.ActionArgv` (root-relative `./bin/tool` anchored to the
  install dir) plus the identity env map (`CATS_PLUGIN_ID`/`CATS_PLUGIN_DIR`),
  so the front-end composes `tab.create` params without knowing manifest
  conventions. Broken entries list with `broken: <err>` (uninstallable, not
  launchable).
- **`catctlPath()` resolution** for the spawned tabs (the browser can't
  resolve host paths): `CATS_CATCTL` env override → `catctl` on PATH →
  sibling of the server executable → bare name (so a failed spawn names what
  was missing).

## What changed

- `internal/app/command_vocab.go`: `CmdPluginList`/`CmdPluginUninstall`,
  `PluginInfo`, `PluginActionInfo`, `PluginListResult`,
  `PluginUninstallParams/Result`; names added to `CommandNames()`.
- `internal/app/commands.go`: two Backend methods + dispatch cases
  (`plugin.list` gates on `WantsReply`, `plugin.uninstall` requires id).
- `internal/app/commands_test.go`: fakeBackend recorders + routing tests
  (`TestDispatchPluginList`, `TestDispatchPluginUninstall`).
- `cmd/catway/plugins.go` (new): `StartPluginList`, `StartPluginUninstall`,
  `catctlPath`.
- `internal/browserproto/cmd.go`: alias re-exports.
- `cmd/catway/web/index.html`: plugins dialog (`openPluginsDialog` — palette
  list chrome, inert rows with per-row `run`/`update`/`uninstall|unlink`
  buttons, multi-action rows open a ctx-menu picker at the button; install
  prompt accepts `owner/repo|url [--ref x]`); gear menu + ⌘K palette entries;
  small `.acts` button CSS. Uninstall confirms (danger only for real installs
  — unlink leaves the checkout), then reopens the dialog to refresh.
- Makefile: `LOCAL_MAP` de-aliased to canonical names (`hway/thost/hctl` →
  `catway/cathost/catctl`), and **cats-todo dropped from `make local`** — the
  plugin host builds it now (`catctl plugin link ./cmd/cats-todo`); `BINS`/
  dist still ship it. A transient `hctl` PATH fallback in `catctlPath` was
  added then removed once the alias died.
- README: plugins section documents the dialog + `CATS_CATCTL`; corrected the
  now-stale "the server never reads a manifest" claim (plugin.list reads it
  host-side).

## Verification

- `make check` clean end-to-end (fmt, vet, untagged + ghostty builds, tests,
  race).
- Inline JS syntax-checked via `node -e 'new Function(script)'` (index.html
  has no build step to catch typos).
- `make local` reinstalled `~/bin/{catway,cathost,catctl}` (fresh catway
  embeds the dialog — index.html is `go:embed`ed, a reload alone won't do).

## Follow-ups

- Linked plugins refuse update by design, so a linked cats-todo rebuilds only
  on re-`plugin link` — dev iteration note, not a bug.
- If plugins multiply: `update --all` (from the update-verb session) and
  perhaps a dialog-wide "update everything" button on top of it.
- `min_cats_version` enforcement still pending a real server version constant
  (unchanged).
