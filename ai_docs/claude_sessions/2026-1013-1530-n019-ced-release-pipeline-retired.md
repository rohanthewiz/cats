# N-019: ced's release pipeline retired

Session: `9912fa41-2f6c-47a8-8567-394eeb33ccad` · 2026-10-13

The user pasted N-019 from the backlog: ced's `release.yml` still described
the `release` branch with automatic version bumps, unused since 0.2.0.
"Retire it or go back to it." The work landed in the ced repo. ced commits:
`2176e90` (the retirement) and `ea2e834` (a session-doc fix), both pushed.
ced's own session doc is
`ced/ai_docs/claude_sessions/2026-1013-1525-retire-release-pipeline.md`.

## What checking found

- ced has 15 tags since v0.2.0, and not one GitHub Release. The Releases
  page's latest is still v0.2.0 (2026-07-29), and the only workflow runs
  date from then. `install.sh` fetches the latest release, so it was
  installing a binary two and a half months old.
- Hand releases (`Release ced x.y.z` plus an annotated tag on main) are
  documented in ced's CLAUDE.md. ced's own next-list parked the pipeline as
  its N-001 on 2026-09-21.
- `origin/release` (`0409317 Release ced 0.2.0`) was fully contained in
  main.

## Decisions (the user's)

- **Retire it entirely**, not move it to a tag trigger and not go back to
  the branch: no workflow, no goreleaser, no install script, no binaries.
  The user added "Don't go back to release.yml".
- **Push ced main**, and **delete the `release` branch**.
- **Keep the v0.2.0 GitHub Release**. Deleting it was offered and not
  chosen.
- The backfill question (publish v0.4.3) only applied if binaries were
  kept, so it didn't apply.

## What changed in ced

- Deleted `.github/workflows/release.yml`, `.goreleaser.yml` and
  `install.sh`.
- README:
  - The install-script and manual-binary sections are gone. Without cats,
    "build from source".
  - **Releases** describes only the hand flow, and notes that the Releases
    page stops at v0.2.0.
  - The goreleaser row is gone from the layout tree, and the feature
    bullet now says "builds for" rather than "cross-compiled".
- CLAUDE.md: no install script or release binaries, and the pipeline
  retired on 2026-10-13 must not come back. The parked-pipeline notes are
  gone. The "don't add windows" rule now points at `cats-plugin.toml`'s
  `platforms`.
- AGENTS.md: the release-packaging line and the `[skip ci]` automation
  advice now describe the hand flow.
- Comments only: `cats-plugin.toml`, `internal/version/version.go`,
  `version_test.go` and `test.yml`. Both version tests stay and pass.
- ced's next-list: N-001 (the parked pipeline) moved to Non-goals as
  declined, and N-009 (a download-summary note) closed as moot.
- The `release` branch is deleted on origin and locally (`git branch -d`,
  after confirming it was merged into main).

## In cats

- N-019 closed in `ai_docs/todo/next-list.md`.
- N-010 updated. Its `install.sh` half went with the retirement, so only
  ced's Makefile `alt` target is left to decide.
- Nothing in cats's docs or code referred to ced's install script or
  releases.

## Next

Closed: N-019. Declined: None. Raised: None. Deferred: None. Promoted: None.
Moved: None. Updated: N-010. Full list: `ai_docs/todo/next-list.md`.
