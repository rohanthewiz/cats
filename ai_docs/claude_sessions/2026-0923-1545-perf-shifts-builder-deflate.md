# Session: performance, round two — shifts, backpressure, builder, deflate

Session ID: e471085f-30f2-4086-82f6-3ec7a897366d
Date: 2026-09-23
Commits (cats, all on main, pushed): `3559d33`, `a6db343`, `93e8fa7`,
`1d26235`, `cc8e7fa`, `3f94fe0`, `7c8ea3d`, `af3cb1b`
Commit (rweb, pushed, tagged v0.1.31): `cb8e038`

## 1. The ask

"Continue with the remaining items in `ai_docs/plans/perf-improve-plan.md`.
Commit in-between each item." That meant plan items 3–9 (1 and 2 were done in
`2026-0923-1437-perf-canvas-sparse-frames-and-frame-gate`). The plan now has a
"(done)" section for each, with its design, numbers and tests. This doc keeps
to what the plan does not: order, decisions, and what went wrong on the way.

## 2. Order

3 → 4 → 5 → 6 → 7 → 9 → (3b + 8). Compression (3b) and write batching (8)
both live in rweb, a separate public repo, so they went last. Publishing them
needed a push and a tag there. I asked first, and the user picked "push and
tag v0.1.31".

## 3. What each item became

| Item | Commit | Shape | Result |
|------|--------|-------|--------|
| 3 scroll op | `3559d33` | cathost detects a whole-grid scroll (row-hash votes, kept only if it saves ≥ 2 rows of cells) → `Frame.Shift{rows, fill}`. Client feature `shift_frames` (only with `sparse_frames`). catway `Grid.Apply` shifts. Browser: `Init.Features: ["pane_shift"]` → `PaneDiff.Shift`; others get a full frame | one-line scroll on 200×50: 133 KB → 2.2 KB |
| 4 slow clients | `a6db343` | `client.queued` bytes. Above 4 MB: skip frames, reset translator, mark stale. Under 1 MB: `catchUp` sends `Grid.FullView()` (the Grid now keeps cursor, scroll, links) | no more drop at 512 |
| 5 encoding | `93e8fa7` | `MarshalFrame` hand-written appender, byte-exact vs `encoding/json`. `ViewEncoder` shares an encoding per translator state. Not moved off the loop: not worth a typed queue now that it is cheap | full frame 562 → 100 µs |
| 6 snapshot/diff | `1d26235` | emulator row cache (clean rows reused from the last snapshot). `FrameBuilder` replaces `p.prev`. Diff built outside `emuMu`, and a new `frameMu` orders snapshot → emit | 1.4 ms → 40 µs per one-cell tick |
| 7 empty frames | `cc8e7fa` | `Diff` returns nil when there is no cell, cursor or scroll change. The flusher sends the throttled `pane_activity` instead (`reportActivity`, now every tick) | — |
| 9 front end | `3f94fe0` | `everyVisible` tickers, palette `select()`, `updateAutoclose`, `hostBadgeKey` | — |
| 3b + 8 | rweb `cb8e038`, cats `7c8ea3d` | rweb: permessage-deflate (`WebSocketWithOptions`), one `Write` per frame, `WriteMessages`. catway: `/ws` compressed; the writer drains up to 64 msgs / 1 MB per write | diffs ~7×, full frames ~8× smaller |

## 4. Bugs found on the way

- **Stale links after links leave the screen.** The replay property test's new
  scroll steps exposed it. A diff compares cells without links, so a cell that
  lost its link but kept its text was skipped, and the receiver kept the old
  link. The first frame after a link-bearing one is now full as well
  (`FrameFromSnapshot`, `FrameBuilder.Diff`).
- **Resync vs flusher ordering.** Pre-existing: a resync's full frame could be
  emitted before a diff the flusher had taken against the old base. Fixed by
  `frameMu` spanning snapshot → emit in both paths.
- **rweb Close without the write lock.** `Close` and the close-handshake reply
  wrote frames without `writeMutex`. That was a latent interleave, and with the
  new shared frame buffer it would have been a data race. Both take the lock now.

## 5. Decisions worth remembering

- **flate level 2, not BestSpeed.** Go's level 1 Huffman-codes any flush under
  128 bytes without matching, and resets its history for it (`encSpeed`,
  verified in the stdlib source). Small diffs got nothing from context
  takeover: 3,870 B → 3,462 B in the test, and 716 B at level 2. The measured
  table is in the plan §3b and in rweb's `defaultCompressionLevel` comment.
- **Client → server is `client_no_context_takeover`.** The server needs no
  per-connection inflate window. Offers asking for `server_max_window_bits` < 15
  are declined, because compress/flate has only the 32 KB window.
- **Shift fill differs per hop.** β's fill is the terminal's default colours.
  The browser's blank is the translator's `def_fg/def_bg` (the dominant
  colours), so `translateDiff` re-checks the vacated rows per connection and
  does not trust `Changed` there.
- **Snapshot rows are now shared between snapshots.** "Immutable" in
  `terminal.Snapshot`'s doc is a rule readers must keep. `FrameBuilder` relies
  on slice identity (`sharedRow`) to skip unchanged rows, and only when the
  default colours did not change.
- **The rweb bump also brings v0.1.29–30** (Host-header validation, a
  keep-alive conn fix, StaticFilesAbs). The cats suite passes on them.

## 6. Verification

- Go: the full ghostty suite; `-race` on catway, orchestration and terminal;
  plain `go vet ./...`. JS: all jstest suites. rweb: the full suite with `-race`.
- Equivalence tests that were run against a deliberately broken cache and
  failed there:
  - `TestSnapshotRowCacheMatchesAFreshRead` (3,000 random VT steps);
  - `TestFrameBuilderMatchesTheReference` (four builders, one per feature
    combination, against the stateless reference, empties included).
- The replay property test now runs a third pass with shifted frames, for a
  shift-capable and a plain translator sharing one view.
- rweb compression end to end against Node 22's `WebSocket` (undici) and
  Chrome, from a throwaway server in the scratchpad. Both negotiated
  `permessage-deflate; client_no_context_takeover` and round-tripped a 60 KB
  message and a 200-message `WriteMessages` batch.
- **Not run in a live Cats.app.** The β changes need a new cathost as well as
  catway, and restarting a persistent cathost ends its shells. The checklist
  is folded into N-001.

## 7. Gotchas hit

- The Write tool turned `�` and ` ` in Go source into the literal
  characters. The encoder test caught the first; the `' '` rune literals
  still compiled but were unreadable. Write such escapes via a Python edit with
  doubled backslashes, then grep for non-ASCII.
- gofmt folds an indented diagram that follows a numbered list into the last
  list item. Put a plain paragraph line before the diagram.
- `browserproto/wire_aliases.go` says "generated by aliasgen", but the
  generator is gone. New code imports `wire` directly (`wire.FeaturePaneShift`).
- The next-list gained N-031..N-033 (the settings session) after it was loaded
  at the start. A scripted edit asserting the old **Next ID** failed halfway,
  after the commit command had already run on the next line. That is why the
  fix-up commit `af3cb1b` exists; the new item is N-034.
- catway tests with `c.out` but no writer never call `wrote`, so `queued`
  only grows there. That is harmless under the 4 MB watermark. The
  backpressure tests shrink the watermarks and play the writer (`writeAll`).

## Next

Closed: N-027, N-028, N-029, N-030. Declined: None. Raised: N-034.
Deferred: None. Promoted: None.
Updated: N-001. Full list: `ai_docs/todo/next-list.md`.
