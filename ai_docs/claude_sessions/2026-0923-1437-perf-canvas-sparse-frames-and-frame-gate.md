# Session: performance — incremental canvas, sparse β diffs, frame gate

Session ID: da4b0484-3f63-4037-bdb6-85c14ef3d863
Date: 2026-09-23
Commits: `7f3474d`, `825374c`, `d3c96ce` (all on main, pushed)

## 1. The ask

"Take a deep look at performance, particularly UI performance and see what can
be improved." After the first round, which covered the front end: "save the
plan under ai_docs/plans/perf-improve-plan.md and start on 1 and 2 committing
between each". Items 1 and 2 were the two top server-side findings.

## 2. The audit

Two background agents covered the parts I wasn't reading myself. One took
the Go frame pipeline, the other the sidebar DOM; I took the canvas painter.
Numbers are for a 200×50 grid (10k cells), from a throwaway
`go test -overlay` harness:

| What | Size | Cost |
|------|------|------|
| β `pane_frame`, ONE cell changed | 850,141 B | 3.3 ms encode + 12 ms decode (twice: `ReadMessage` peeked the type with a full unmarshal) |
| browser `pane_frame` | 118,980 B | 0.9 ms marshal |
| browser 1-cell `pane_diff` | 97 B | — |

The pipeline: cathost `readPump` → `feed` sets `dirty` → a 16 ms ticker runs
`flushDirty` → `takeFrame` (`Snapshot` + `FrameFromSnapshot` under `emuMu`) →
JSON → catway `dispatch` → one `Translate` + `Marshal` per connection on the
loop → WebSocket.

Everything found is written up, ranked, in `ai_docs/plans/perf-improve-plan.md`.

## 3. Front end (`7f3474d`)

- **Canvas painting, `18-render.js`.** `draw()` used to repaint every cell
  on every `pane_diff`, and set `ctx.font` plus a freshly built `fillStyle`
  string for each glyph. The browser parses both of those on every
  assignment. The painter is now split:
  - `scheduleDraw` does a full draw. Every existing caller keeps it.
  - `markRow` + `requestPaint` are the diff path. `paint` falls back to a
    full draw when more than half the rows changed or the scroll moved.
  - `drawBand` clears the dirty rows plus one neighbour row on each side,
    and repaints them with one more row each side painted underneath the
    clip. Glyphs that overhang their cell (box-drawing, emoji) leave ink in
    the neighbour rows, which is why the clip has to include them.
  - `paintCells` holds the two-pass bg-then-glyph order. `drawOverlays`
    draws the cursor, selection, copy cursor and scrollbar.
  - Colours are memoized (`css`, `dimCss` in `02-color.js`), and canvas state
    is only written when it changes.
- **`placePane`** no longer reassigns `canvas.width/height` when the size is
  unchanged. Any assignment reallocates and clears the canvas, and this ran
  on every layout message.
- **Sidebar.**
  - `pane_title` no longer calls `refreshPaneList`, which did a `pane.list`
    round trip plus two full rebuilds on every agent spinner tick. It now
    patches the snapshot entry in place. The workspace todo marks read titles
    from that snapshot, and a pane with a custom name still re-queries.
  - Pushed Workspaces redraws (clients, ws_git, hosts, agents) go through
    `renderWorkspacesSoon`, i.e. the inventory rAF.
  - The hover card is keyed by its rows (`tipItemsKey`): when the rows are
    unchanged, a move only repositions it.
- **Tests.**
  - `jstest/render.test.mjs` runs the real painter and the real `pane_diff`
    handler against a fake raster that records paint layering. After random
    diffs it checks the result against a from-scratch full draw, pixel for
    pixel. It caught two real bugs: stale overhang in neighbour rows, and a
    cursor moving along its own row leaving its old block behind (now tracked
    with `curCol`).
  - `TestPagePaintsBackgroundsBeforeGlyphs` now checks `paintCells`.

## 4. Sparse β diffs (`825374c`)

A dense diff's skipped cells can't simply be blanked. `Translate` builds
*full* browser frames from diff frames whenever a connection needs one: after
a translator reset, or when more than 60% of cells changed. So:

- **Negotiation.** `Hello.Features` is new: capabilities the client can
  accept, the mirror of `welcome.features`. catway sends `sparse_frames`.
  cathost stores it in `sparseFrames` at hello, clears it on detach, and
  calls `takeFrame` → `Frame.Sparsify()`, which turns the changed cells into
  `Runs []CellRun{At, Cells}` and sets `Sparse=true`.
- **catway keeps the screen.** `paneRuntime.grid browserproto.Grid.Apply`
  returns a `FrameView` (whole grid plus `Changed` indices). It is built once
  per frame and shared by all connections through `TranslateView`;
  `Translate` stays as the dense shorthand.
- **Invalidation.** The grid is invalidated when a frame for a hidden pane is
  dropped, at reconcile (daemon reconnect) and on `rehomePane`. A sparse diff
  against an invalid grid is dropped until a full frame arrives. A dense diff
  rebuilds the grid, so an old cathost always works.
- **Smaller wire.**
  - `Cell` now omits `modifier`, `skip` and `hyperlink` when they are zero.
    Every decode targets a fresh value, so older readers see the same cells.
  - `peekType` reads the type off the `{"type":"` prefix. Anything else
    falls back to the full unmarshal.
- **Result.** A one-cell diff on 200×50 is **226 B** sparse, against 550 KB
  dense (850 KB before the omitempty change).
- **Tests.**
  - `sparse_test.go`.
  - `TestHostSparseFramesFollowTheHello` (ghostty; checks both negotiation
    directions).
  - The replay property test now also runs down the sparse path through
    JSON.
  - `TestGridRefusesWhatItCannotResolve`.
  - catway `frames_test.go`.

## 5. Frame gate (`d3c96ce`)

- **cathost** advertises `frame_gate`. catway sends `set_frame_panes` with
  the viewport union split by host. `syncFrameGates` is called from
  `refreshViewport`; it sends only on change, and `sendFramePanes` re-sends
  on reconnect.
- **The flusher** skips panes outside the gate (`flushGated`). For those it
  consumes `dirty`, still runs `emitModeChanges` (input reaches hidden panes
  too), and emits `pane_activity` at most once every 2 s. catway turns that
  into `histDirty`, which was the only use it had for off-screen frames.
- **Ordering.** A pane coming back into view already gets `request_resync`.
  The widened gate goes out ahead of it on the same connection, so the full
  frame does get taken. Panes leaving the union have their grid invalidated.
- **Scope.** The gate is per session and opt-in, so older catways and probes
  still get every frame.
- **Tests.**
  - `TestHostFrameGate` (ghostty: a gated pane is never framed, reports
    activity, and its resync frame holds `TWOMORE`, the output printed while
    it was hidden).
  - `TestFrameGateFollowsTheViewport` (zoom and unzoom; checks the gate goes
    out before the resync).
  - `TestFrameGateNeedsTheFeature`.
  - `TestPaneActivityMarksHistoryDirty`.
- **Docs.** `docs/protocols/orchestration-seam.md` gained sections on client
  features, the frame gate and the sparse frame shape. The item-1 docs
  landed in this commit too.

## 6. Gotchas hit

- catway's `.go` files and most of its tests are `//go:build ghostty`. A
  plain `go build ./cmd/catway` compiles almost nothing. Use
  `PKG_CONFIG_PATH=$PWD/third_party/libghostty-vt/zig-out/share/pkgconfig
  go test -tags ghostty ./...` (or `make test-ghostty`).
- `mcOrch` tests run no loop goroutine; the test goroutine *is* the loop. So
  `syncPost` deadlocks there. Call loop code directly and pump posted work
  with `waitFor`.
- `sliceConst` in the jstest harness can't lift a single-line const with a
  trailing comment. Pass those consts in through `env`.
- A JS LCG with a plain multiply overflows a double's 53-bit mantissa and
  can fall into a short cycle. Use `Math.imul`.

## 7. Not verified

Nothing here was run in a live Cats.app. Getting items 1 and 2 needs a *new
cathost*, and restarting a persistent cathost ends its shells. A mixed
new-catway / old-cathost setup is safe, but gets neither feature.
Folded into N-001.

## Next

Closed: None. Declined: None. Raised: N-027, N-028, N-029, N-030.
Deferred: None. Promoted: None.
Updated: N-001. Full list: `ai_docs/todo/next-list.md`.
