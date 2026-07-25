# Plugin update verb — catctl plugin update, in-place fetch + rebuild

- **Session ID:** `64e10df3-9c7b-4f51-b2fb-d85a11758b1f`
- **Date:** 2026-07-24 21:25
- **Branch:** `main`
- **Scope:** `internal/plugin/update.go` (new), `internal/plugin/plugin_test.go`,
  `cmd/catctl/plugin.go`, README

## Request

"Add a plugin update verb" — the follow-up called out in the phase-3 plugin
host session: Install's provenance JSON + kept `.git` were left behind
precisely so update could be a small add.

## Design

`Update(id, out) (Installed, updated bool, error)` in a new
`internal/plugin/update.go`, cashing in the provenance Install recorded:

- **fetch + hard-reset, not `git pull`**: the refresh is
  `git fetch --depth 1 origin <ref>` then `git reset --hard FETCH_HEAD`.
  Pull breaks on the detached HEAD a tag-pinned clone leaves behind, and a
  merge would trip over force-pushed release branches; fetch+reset handles
  branches, tags, and rewritten history identically. The installed copy is a
  cache of upstream, not a working tree anyone edits, so discarding local
  state is correct.
- **Empty ref → fetch the remote's `HEAD`** — the default branch, whatever
  it's named, with no local `symbolic-ref` resolution needed.
- **No-op detection**: `git rev-parse HEAD` before/after; same sha → skip the
  build entirely, return `updated=false` (keeps a future "update everything"
  loop cheap). New `gitHead` helper captures output, unlike streaming
  `runStep`.
- **Linked plugins refused** (checked before anything touches the tree): the
  linked dir is the developer's own checkout — a hard reset could destroy
  uncommitted work. Error points at updating the checkout + re-linking.
  A dir without `.git` (stripped install) also gets a clear "reinstall" error.
- **Rollback on bad releases**: after a real fetch, the new tree gets
  fresh-install scrutiny — manifest must parse/validate, id must still match
  the install dir (dir is keyed by id; a changed id would make the entry lie),
  platform must still be supported, build must succeed. Any failure
  hard-resets back to the old sha and reports "previous version restored".
  Build artifacts and the provenance JSON are untracked, so they survive the
  resets in both directions.

## What changed

- `internal/plugin/update.go` (new): `Update`, `gitHead`.
- `cmd/catctl/plugin.go`: `update` case in the dispatch, `pluginUpdate`
  (usage/exit codes 0/1/2 like the rest of the family; prints
  "already up to date (vX)" vs "updated <id> to vY (dir)"), help line
  (kept backtick-free — the raw-string usage lesson), family doc comment now
  notes update reaches the git remote but never the cats server.
- Tests: `gitIn` helper extracted from `TestInstallFromLocalRepo`'s inline
  closure (identity via `-c`, gitconfig-independent); `TestUpdate` covers
  no-op → real 0.2.0 release (new build step artifact lands, provenance
  survives the reset) → upstream id-change refused with prior version intact;
  `TestUpdateLinkedRefused` covers the linked guard. Both skip without git.
- README Plugins section: `catctl plugin update` line.

## Verification

- `go test ./internal/plugin/ ./cmd/catctl/` then full `make check` — clean.
- CLI smoke with a scratch upstream repo + `CATS_PLUGINS_DIR` root:
  install → update (already up to date, no rebuild) → bump upstream to
  0.2.0 → update ("updated acme.smoke to v0.2.0") → update of a missing id
  errors exit 1.
- The smoke also proved shallow fetch works over local-path remotes, so
  `--depth 1` needs no local-upstream special case (clone prints a
  "--depth ignored in local clones" warning but fetch honors it).

## Follow-ups

- `min_cats_version` enforcement still pending a real server version constant
  (unchanged from phase 3).
- If plugins multiply, an `update --all` loop over `List()` is trivial on top
  of the no-op detection.
