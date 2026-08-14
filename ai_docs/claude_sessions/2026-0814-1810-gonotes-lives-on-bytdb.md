# Session: gonotes lives on bytdb, and the cross-check became the migration

- Session id: `6219fec1-5bc1-4b3d-a9ff-f1ffb3fa1f34`
- Date: 2026-08-14
- Branch: `main`; cats commits are docs only (`564877c`, `66827a5`)
- Plan/record updated: `ai_docs/cats-gonotes-intg.md` (Phase 1 marked done)
- Subject repo: `~/projs/go/gonotes` — merge commit `bdc0796`, pushed to
  master. This is the first session that changed gonotes.
- Predecessor: `2026-0814-1748-gonotes-plan-and-premigration-export.md`

## What this session was

Phase 1 of the gonotes plan: merge the forgotten `origin/migrate-to-bytdb`
branch, move the notes onto it, and prove nothing was lost. It ended with the
live database, the installed binary, and the MacApp bundle all on bytdb, and
with the user confirming their notes and their original password still work.

The merge was the easy part. The interesting part is that the plan had its two
data paths backwards, and only a byte-level comparison revealed it.

## The merge, which was uneventful and is worth one line

`.gitignore` was the only overlap and auto-merged. The six Monaco commits
master had gained since the branch never touch `models/`, so there were no
semantic conflicts either — `go build`, `go vet`, and all three test packages
passed on the first try. Committed as `bdc0796`.

## The inversion

The plan named `import-md` the migration and `scripts/migrate` the
cross-check. Both were run into scratch data dirs, then each was exported
*back* to markdown and byte-diffed against the Phase 0 export:

| | `scripts/migrate` | `import-md` |
|---|---|---|
| notes | 31 (incl. soft-deleted) | 30 |
| re-export vs Phase 0 export | **byte-identical, 30/30** | 6 files differ |
| private-note timestamps | preserved | **reset to import time** (5 notes) |
| trailing whitespace | preserved | trimmed (1 description, 2 bodies) |
| user identity | **original GUID + bcrypt hash** | new GUID, new password |
| categories / links | 28 / 42 | 28 / 42 |

Every one of the import path's losses is in the code, not accidental.
`md_import.go:196-205` routes private notes through `CreateNote` instead of
`CreateNoteWithTimestamps`, so their `created`/`updated` become the moment of
import. Body and description trimming costs exactly one trailing newline on
two notes — confirmed as whitespace, not content, by comparing the bodies with
trailing newlines stripped and by dumping the last eight bytes of each.

The decisive one is subtler. `import-md --user` requires the user to *already
exist*, and a fresh data dir has no way to provide one — the scratch run had
to register through the HTTP API first, minting a new user GUID. In a
hub-and-spoke sync design that GUID *is* identity. The import path silently
changes who you are.

So `scripts/migrate` produced the live database and the markdown export became
what it is actually good at: an independent witness. Its 30 files re-exported
byte-identical from the migrated database, which says far more than any count.

## Verification that beat counting

- The migrate output — 1 user, 28 categories, 31 notes, 42 links — matches the
  *old* DuckDB server's own startup log (`notes_count=31,
  categories_count=28, relationships_count=42`). Two independent readers of
  the same data agreeing.
- Password-hash preservation was checked by pulling the bcrypt string out of
  DuckDB and finding it inside `notes_public.bytdb`. **This needs `grep -aF`.**
  An unescaped `$2a$12$…` used as a regex quietly fails to match, and the
  first attempt read exactly like data loss. (Phase 0 had its own version of
  this trap with `length()` vs `wc -c`; the family resemblance is worth
  noticing.)
- Probing the login endpoint proves nothing: gonotes correctly returns the
  same `invalid credentials` for a wrong password and for a nonexistent user.

## The live file had drifted

After the clean stop, `~/.gonotes/data/notes.ddb` was the same 22,556,672
bytes as the 17:39 Phase 0 backup but a **different SHA-256**. Row counts and
`max(updated_at)` were unchanged, so it was DuckDB checkpoint churn rather
than edits — but the instinct to take a fresh copy at cutover time and migrate
from *that* was right, and cost nothing. Both copies are kept:
`backup-premigration-2026-0814` and `backup-cutover-2026-0814`.

## Cutover, in the order it has to happen

Stop the server → fresh copy of the `.ddb` → migrate from the copy → verify by
round-trip export → drop the two `.bytdb` files into `~/.gonotes/data` →
replace `~/bin/gonotes`, keeping the April DuckDB build as
`~/bin/gonotes-duckdb-2026-0422` → restart → `./mac-install.sh`.

Unlike Phase 0, the `kill` went through without the permission classifier
blocking it.

That last step is not housekeeping. `notes.ddb` is still sitting in the data
dir, and the MacApp only starts its bundled binary when nothing is already
healthy on 8444. A stale DuckDB binary in the bundle would therefore have
opened the old database and served notes that are no longer the source of
truth — with no error anywhere. The bundle now reports `bdc0796`.

## What the free session confirmed, and what it exposed

The MacApp showed all notes on first launch **without asking for sign-in**,
and a deliberate logout/login round trip then succeeded with the original
password. The skipped sign-in is itself evidence: the JWT subject is the user
GUID, not the row id (`models/token.go:31` — "Using UserGUID instead of ID
allows tokens to work across sync scenarios"), so the surviving WebKit session
only resolved because migrate preserved that GUID. The import path would have
invalidated it.

Chasing *why* it survived turned up something unrelated but real: `InitJWT`
falls back to the literal string
`development-only-secret-do-not-use-in-production` when `GONOTES_JWT_SECRET`
is unset (`models/token.go:42-49`), and it is unset here — no env var, and
`~/.gonotes/config/cfg_files/.env` does not exist. Exactly the same gap Phase
0 found for the encryption key. Every token this instance issues is signed
with a constant that lives in the public repo. Pre-existing, unrelated to the
migration, and mostly theoretical on a loopback-only server — but it becomes
load bearing at Phase 5, when hooks and phone push start carrying gonotes auth
off this machine.

## Also settled

The plan's **sync-hub caution** for Phase 1 is moot. `scripts/migrate` records
no change entries at all, sync is disabled, and `sync_state` is empty — there
are no peers. The 134 `note_changes` rows in the old database are inert local
history and were deliberately not carried across, along with `sync_state`,
`sync_conflicts`, and `invite_tokens`.

## Where things stand

gonotes master is on bytdb and pushed. The live server, `~/bin/gonotes`, and
the MacApp bundle are all `bdc0796`. The old `.ddb` is retained untouched in
the data dir alongside two backups.

Phase 2 is next: the Bubble Tea v2 migration, mechanical and
behavior-identical. The trap already identified is that the v2 import paths
are `charm.land/*`, not `github.com/charmbracelet/*` — the GitHub proxy serves
zips whose `go.mod` declares `charm.land`, so the familiar import fails at
build time with a module-path mismatch.
