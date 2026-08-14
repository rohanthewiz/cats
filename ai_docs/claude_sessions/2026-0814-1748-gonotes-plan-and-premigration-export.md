# Session: a plan for gonotes, and the export that has to come first

- Session id: `c50c7335-da82-4843-8a74-c93e3d26a149`
- Date: 2026-08-14
- Branch: `main`; this session's cats commits are docs only
- Plan/record written: `ai_docs/cats-gonotes-intg.md`
- Models for it: `ai_docs/dbc-intg-plan.md` and
  `~/projs/go/ced/ai_docs/cats-native-plan.md`
- Subject repo: `~/projs/go/gonotes` (no commits there yet — Phase 0 only
  read from it and built a throwaway binary)

## What this session was

"Plan gonotes as an agent the way we did ced and dbc, and feel free to
revamp its TUI to conform." Then, mid-planning, the thing that reorders
everything: a forgotten remote branch, `origin/migrate-to-bytdb`, moves the
whole data layer off DuckDB — and the user's real concern is not the
integration at all, it is that the notes survive the move. So the plan grew
two phases at the front, and this session executed the first of them.

Nothing about the cats integration was implemented. What exists now is a
nine-phase plan and a verified backup.

## The plan, in one paragraph

gonotes becomes the third cats-native client on the established contract —
hand-copied stdlib transport mirrors, hook reporter, host identity, host
theme, agent collaboration, ⌘ layer, Tier 0 loses nothing. Four decisions
were the user's: a **Bubble Tea v2 upgrade** (v1 has no kitty keyboard
support, so ⌘ chords could never arrive), **markdown export/import** as the
data mover across the bytdb merge, **hybrid data access** (speak HTTP when a
gonotes server is up, open the embedded DB when it is not), and shipping a
**`cats-plugin.toml`** alongside the native client. The cats-side diff
remains one line in `AGENT_HUE`, same as dbc.

## Three findings from planning that changed the shape

**The ⌘ layer is free again, for a different reason.** dbc's windfall was
that tcell already pushes the kitty protocol. bubbletea v1 does not and
never will — but v2's renderer engages kitty *unconditionally* at startup
(`cursed_renderer.go:134-140`, minimum flag 1 regardless of what the app
asks for), which is exactly the gate `cmdGoesToPane` checks. So the chords
arrive for free once the app is on v2, and `View.KeyboardEnhancements` is
not needed to get them. This is what made the v2 upgrade worth doing rather
than a fallback of hand-emitting CSI-u.

**The v2 import paths are `charm.land/*`, not `github.com/charmbracelet/*`.**
The github proxy paths serve zips whose go.mod declares `charm.land`, so the
familiar import fails at build time with a module-path mismatch. Easy hours
to lose. The whole v2 stack is stable, not beta: bubbletea v2.0.8, bubbles
v2.1.1, lipgloss v2.0.6, glamour v2.0.1, and a teatest/v2.

**The embedded store stays single-writer, so the hybrid mode is not a
nicety.** The concern that motivated it was DuckDB's lock, and bytdb does
not remove it. A TUI in a cats pane cannot share a data directory with the
running desktop app. gonotes already has the full JSON API the TUI would
need, so the escape hatch is a store interface with two implementations
rather than anything new on the server.

## Phase 0, and why the order of its steps is the whole trick

Export every note to markdown before the merge, and keep a raw copy of the
database. It sounds like two commands. Four things made it not that:

1. **The process holding the database was the stale binary.** `~/bin/gonotes`
   is an April build whose `--help` lists no subcommands at all — no
   `export-md`, no `tui` — and it had been serving since 24 July. The export
   required building a current binary from master first. The installed one
   could not have done the job it was blocking.
2. **The lock is absolute and the error names its holder.** `duckdb
   -readonly` refuses with `Conflicting lock is held in ~/bin/gonotes (PID
   61161)`. Stopping the server is step one, not a courtesy — and the tool
   call to do it was blocked by the permission classifier, so the user ran
   the `kill` themselves.
3. **SIGTERM checkpointed the WAL, which is the only reason the backup is
   worth having.** The database went 20,983,808 → 22,556,672 bytes on
   shutdown and the 172 KB `.wal` vanished into it. Copying first — the
   obvious instinct, "back up before you touch anything" — would have
   captured a torn mid-write file *and* missed the WAL entirely. Copy after
   the clean stop; the copy then verifies byte-identical by SHA-256.
4. **Encryption was never enabled**, so the "export with the key set" step in
   the plan was moot. No `GONOTES_ENCRYPTION_KEY` in the server's
   environment, the app plist, LaunchAgents, shell profiles, and no config
   `.env` — the directory does not exist. Private bodies were already
   plaintext; the export exposes nothing storage did not already hold.

Result: 30 live notes (5 private), 0 errored, 19 category folders, 404 KB.
The one soft-deleted note is correctly absent.

## The verification trap, kept because it looked exactly like data loss

Comparing DuckDB's `length(body)` against `wc -c` on the exported files
reported 14 of 30 notes as mismatched, one of them wildly — 6,089 against
10,815. That reads as a truncating exporter.

It is measurement error twice over. `length()` counts **characters** and
`wc -c` counts **bytes**, and these notes are full of em dashes and arrows;
and the extraction pipeline was stripping trailing whitespace, which
produced negative deltas in the other direction. The byte-accurate function
is `strlen()` — `octet_length` rejects VARCHAR outright, and casting a
non-ASCII body to BLOB throws a conversion error that prints the offending
note into the terminal, which is its own small lesson about diagnostics on
private data.

Measured correctly, all 30 bodies differ by exactly +1 or +2 bytes: the
newline the exporter appends. The GUID sets are identical between database
and export. That is the check worth keeping — counting files would have
passed either way.

## Where things stand

The server was restarted exactly as found, same stale binary, health
answering. The export and the raw backup live under `~/.gonotes/` and stay
out of git — this repo is public.

Phase 1 is next and needs the server stopped again: merge the bytdb branch
(one commit, near-clean — only `.gitignore` overlaps), import the markdown
into fresh databases, and cross-check against the branch's own
`scripts/migrate` run into a scratch directory. After that push,
`mac-install.sh` hard-resets its own checkout to origin/master, so the
desktop app rebuilds itself onto bytdb on next run.
