# work-next-list run: final report

Session: `c911b2be-e5ee-44d0-ad48-dfc9e1d9516f` · 2026-10-12

This is the closing note for the unattended `/work-next-list` run. The work
itself, and the list changes, are in the five batch docs:
`2026-1011-2316-n044-n018-n045-seed-checked-live`,
`2026-1011-2327-n053-n052-n039-headings-state-words-exited-capture`,
`2026-1011-2335-n060-n040-n033-raw-params-unpair-ui-push`,
`2026-1011-2344-n034-n038-n014-n013-mobile-shift-tools-tab-ime` and
`2026-1011-2349-n043-n008-linked-hint-and-run-end`. Nothing in code or the
list changed after the last one.

## Done

| ID | What changed | Commit |
|---|---|---|
| N-044 | Already shipped in v0.42.1; closed | ce9131f |
| N-018 | ced's theme-test flake no longer reproduces (800+ runs) | 6828d2b |
| N-045 | Default-plugin seed checked live | 6828d2b |
| N-053 | Narrow sidebar headings: the title ellipsizes and the controls keep their width | 7bc90c2 |
| N-052 | `ok-fg` / `done-fg` / `err-text` theme keys; state words ≥4.5:1 on the three named themes | c10c7a7 |
| N-039 | `catctl capture` of an exited pane returns its last screen | 942c41e |
| N-060 | `catctl capture --params …` works as documented (raised and fixed this run) | 73837fa |
| N-040 | `detach-peer` revokes the grant on the other catway (`/peer/v1/unpair`) | d1005fb |
| N-033 | `ui_prefs` push: font size and sidebar width reach other open windows | 08e882a |
| N-034 | cats-mobile applies `pane_shift`, pinned to cats v0.4.0 | cats-mobile a228479, 8c050f3; list 4f0fa34 |
| N-038 | Settings **tools** tab edits `tools.types` | 05e5a3c |
| N-043 | Linked plugin behind its upstream gets a quiet hint, without fetching | cbe6c25 |

## Moved to Validate

- N-014 (5c92851): dead keys compose. Needs ⌥e e, ⌥u u and ⌥e space in
  Cats.app and Chrome, at a shell and in ced.
- N-013 (b05ca8d): the IME preedit shows at the cursor. Needs a real
  Japanese or Pinyin IME, and dictation, in Cats.app.

## Declined

- N-008 (d8c177b): `waitReady` stays. The item named Non-goals as its
  default, and the code is not removed without a reason.

## Needs the user

- N-059 (raised): seven more built-in themes, cats-green (the default) among
  them, have state words below 4.5:1. Fixing it changes the default look.
- N-009: run `catctl integration install shell zsh`.
- N-010: whether ced's README keeps the Homebrew and `install.sh` paths and the
  Makefile's `alt` target.
- N-019: retire ced's `release.yml` branch route, or go back to it.
- N-037: allow reading `~/.copilot/session-state`.
- N-055: codex isn't installed, so its history format is unknown.
- N-015, N-016, N-020: contingent, deferred by design.
- N-001, N-012, N-042, N-054, N-056: hand checks in Cats.app, on a Linux
  peer, or with a real claude session.

Nothing failed or was left half-done.

## Next

Closed: None. Declined: None. Raised: None. Deferred: None. Promoted: None.
Moved: None. Updated: None. Full list: `ai_docs/todo/next-list.md`.
