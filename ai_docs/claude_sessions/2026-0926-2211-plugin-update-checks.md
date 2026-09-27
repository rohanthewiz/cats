# Session: plugin update checks and a restyled plugins dialog

Session ID: 4f3d0400-2917-432d-9647-ed29145747ae
Date: 2026-09-26

The user asked:

> The plugins dialog could use a bit of spicy up.
> For one thing, let me know when an update is available.

The dialog now knows when upstream has moved. A count on the toolbar's
**⧉ plugins** button says how many plugins have an update waiting, and the
dialog was restyled into two-line rows.

## 1. What was there

- `cmd/catway/web/js/30-plugins.js`: one-line palette rows
  (`id vX [type] — name`) with run / run all / update / rebuild / uninstall.
  **update** spawned `catctl plugin update <id>` in a tab, but nothing ever
  said whether there was anything to update.
- `internal/plugin/update.go`: `Update` fetches the recorded ref
  (`fetch --depth 1` + `reset --hard FETCH_HEAD`) and rebuilds. The install
  keeps `.git` and `.cats-plugin-source.json` (source URL + ref), so the check
  had everything it needed.
- The server had only `plugin.list` / `plugin.uninstall`. install and update
  run as tabs on purpose, because their output is worth watching.

## 2. The check: `internal/plugin/check.go`

`CheckUpdate(ctx, inst) UpdateCheck` is the read-only twin of `Update`, with
the same addressing (the pinned ref, else the remote's `HEAD`):

1. `git ls-remote origin <ref> <ref>^{}`. The output is filtered to exact
   refnames, because ls-remote patterns are tail-matched. The peeled `^{}`
   line wins for annotated tags, since the tag ref names the tag object and
   not the commit a clone checks out. Equal to local `HEAD` → `current`.
2. Otherwise `git fetch --quiet --depth 1 --no-write-fetch-head origin <ref>`,
   then `git show <sha>:cats-plugin.toml` (decoded loosely, just `version`)
   and `git log -1 --format=%s <sha>`. This is only there so the row can name
   the version and the commit subject, so a failure here leaves `available`
   standing with those fields empty. No ref, index or working-tree file moves.
   `--no-write-fetch-head` keeps it from clobbering a concurrent `Update`'s
   FETCH_HEAD.

`gitQuiet` turns off every prompt: `GIT_TERMINAL_PROMPT=0`, an empty askpass,
and `GIT_SSH_COMMAND=ssh -o BatchMode=yes` unless the user already set their
own. It also sets `WaitDelay`, so a killed git's ssh child can't hold the pipe
open past the deadline. The statuses (`available` / `current` / `skipped` /
`error`) alias the wire constants. Linked, broken and no-`.git` entries come
back `skipped`. `HeadCommit(inst)` was exported from `update.go` to use as the
cache key.

## 3. Wire + server

- `wire/vocab.go`: `CmdPluginCheckUpdates = "plugin.check_updates"`,
  `ReplyRequired` and not `Recorded` (it is a query). Params are
  `{ids?, force?}`. The result is `{plugins: [PluginUpdateInfo], available}`,
  where each entry carries status, reason, current/latest version, short
  commits, `latest_subject` and `checked_at` (ms). Aliases were added in
  `internal/app` and `internal/browserproto`. This is an additive change, so
  cats-todo and cats-mobile need no re-pin.
- `internal/app/commands.go`: new `Backend.StartPluginCheckUpdates`. The
  dispatch drops the command without a reply channel and decodes optional
  params. `fakeBackend` and `TestDispatchPluginCheckUpdates` were added.
- `cmd/catway/plugins.go`: the checks run 4 workers wide with a 20s timeout
  per plugin. The package-level cache keeps a verdict for 30 minutes (2 for
  errors), and an entry only counts while the installed commit and ref still
  match. So an update that moves `HEAD` makes the next ask miss the cache,
  and the badge clears without any explicit invalidation. `force` bypasses
  the cache.

## 4. catctl

- `catctl plugin update <id>...` now takes several ids. They run in sequence
  (so build logs don't interleave), a failure moves on to the next one (Update
  already rolls back), and the exit code is 1 if any failed. **update all**
  uses this.
- `catctl plugin check [id...]` gives the same report straight against the
  remotes, with no server and no cache. Completion was added for both.

## 5. Front end

- `mainarea.go`: `#pluginsbtn` is hand-rendered with an empty `.n` span, the
  same pattern as `#recbtn`. `24-toolbar.css` styles it as an accent count
  chip, hidden while `:empty`.
- `30-plugins.js`: new state `pluginUpdates` / `pluginUpdatesAt`.
  `checkPluginUpdates(force, done)` is single-flight: waiters join a check
  already in flight, and a 90s guard stops `busy` from latching if the socket
  dies. `schedulePluginUpdateChecks()` is called from `ws.onopen` in
  `37-session.js`: the first check after 8s, then hourly, and a reconnect
  restarts the chain. `pluginRecheckSoon()` fires at +30s, +90s and +4m after
  an update tab starts. `paintPluginBadge()` sets the count and the tooltip.
  An uninstall drops the plugin's entry.
- `openPluginsDialog` was rewritten around a `paint()` that rebuilds the
  header, rows and footer, so a check landing later repaints in place. The
  first paint uses the last known answer, then `runCheck(false)`. The header
  has a count chip, a summary or spinner, and **↻** (forced). Each row has a
  monogram tile (one of the `--agent-1..6` hues, hashed from the id), a bold
  display name, pills for version, type and status plus `↑ v0.4.0` or
  `↑ <commit>` when the version string didn't change, the trailing id
  (truncated first), and a second line (update commits + subject / linked
  path / error / description). Update rows get an accent inset edge and tint,
  and their **update** button is `.hot`. **update all (N)** appears once
  N ≥ 2. All the existing behavior (run / run all picker, rebuild,
  unlink/uninstall confirm, add…) is kept, with its comments.
- `21-plugins.css` was rewritten: a 680px card layout, pills tinted with
  `color-mix` from theme tokens, a spinner that respects reduced motion, and
  a muted uninstall until hover.

## 6. Verification

- `internal/plugin/check_test.go`: default branch (current → available with
  version 0.2.0 and subject `v2`, installed tree untouched, then Update still
  works and the check reads current); an annotated-tag pin (main moving is
  ignored, re-pointing the tag is caught); linked/broken skipped; a vanished
  remote gives `error` with a reason.
- `make check` passes (fmt, vet, tests, jstest, ghostty vet + race).
- `catctl plugin check` on the real plugins: cats-todo is behind
  (78c85ac → 9bfe73e, the "fix(form)… N-068" commit, same v0.41.1), ced is
  skipped as linked, and dbc and gonotes are current.
- A dev catway on :8531 from the scratchpad, driven in Chrome: the badge
  showed `1`, the dialog rendered as designed (light theme), and ↻ showed the
  spinner and the disabled button, then the result. The instance was stopped
  afterwards (only its own pid).
- Not run: a real update from the dialog, update all, the dark theme, and
  Cats.app (N-042).

## Next

Closed: None. Declined: None. Raised: N-042, N-043.
Deferred: None. Promoted: None.
Updated: None. Full list: `ai_docs/todo/next-list.md`.
