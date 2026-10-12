# N-043, N-008, and the end of the work-next-list run

Session: `c911b2be-e5ee-44d0-ad48-dfc9e1d9516f` · 2026-10-11

Last batch of the unattended `/work-next-list` run. The earlier batches are
`2026-1011-2316-…`, `2026-1011-2327-…`, `2026-1011-2335-…` and
`2026-1011-2344-…`.

## N-043: linked checkouts get a "has moved on" hint

- Done without the fetch that had kept it out. `linkedBehind`
  (`internal/plugin/check.go`):
  - takes the branch from `symbolic-ref`, and its upstream from
    `branch.<b>.remote` and `.merge`;
  - reads the tip with `git ls-remote <remote> <merge>`, which writes
    nothing to the repo;
  - gives no hint when the tip equals HEAD, or is in the object store and an
    ancestor of HEAD;
  - otherwise appends "origin/main has moved on (sha)" to the skip reason.
- The status stays skipped: no toolbar badge and no update button.
- catway: linked checks now go through `checkPluginCached` (timeout and
  cache, keyed by HEAD) rather than an untimed `context.Background()`.
- Dialog (`30-plugins.js`): `linkedMovedOn` pulls the hint out of the
  reason. It shows in amber BEFORE the path, because the path line
  ellipsizes at the end and the first attempt (trailing) was invisible
  in the live render.
- **Bug found live:** `runCheck` returned early when every plugin was linked
  ("nothing checkable"), so a dialog whose plugins were all linked never
  asked. It now skips only when every plugin is broken.
- `TestCheckUpdateLinkedBehindHint` uses a real upstream repo and a clone:
  level, upstream ahead (and the clone's `origin/main` unmoved, proving no
  fetch), local commit ahead, and a branch with no upstream.
- Live: a scratch catway with a linked checkout one commit behind.
  `catctl plugin check` and the dialog both showed the hint, and the badge
  stayed empty.

## N-008: declined

`waitReady` stays. The item named Non-goals as the default, and the global
rule is not to remove code unless it is clearly in the way.

## The run as a whole

Done: N-044 (already shipped), N-018 (no longer reproduces), N-045 (checked
live), N-053, N-052, N-039, N-060 (raised and fixed in this run), N-040,
N-033, N-034 (cats-mobile), N-038, N-043.

Moved to Validate (fix landed, hand check needed): N-014 (dead keys) and
N-013 (IME preedit).

Declined: N-008.

Raised and still open: N-059 (state-word contrast on the other built-ins,
the default theme among them).

Skipped as needing the user: N-009, N-010, N-019, N-015, N-016, N-020, N-037,
N-055, N-001, N-012, N-042, N-054, N-056.

## Next

Closed: N-043. Declined: N-008. Raised: None. Deferred: None. Promoted: None.
Moved: None. Updated: None. Full list: `ai_docs/todo/next-list.md`.
