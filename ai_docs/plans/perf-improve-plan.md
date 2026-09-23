# Performance improvement plan

Audit date: 2026-09-23. Scope: the whole path a keystroke's echo travels —
cathost PTY → VT snapshot → β frame → catway → browser `pane_frame`/`pane_diff`
→ canvas — plus the sidebar DOM.

## Measurements (200×50 grid = 10k cells)

Taken with a throwaway `go test -overlay` harness; nothing in the repo.

| What | Size | Cost |
|------|------|------|
| β `pane_frame`, ONE cell changed | 850,141 B | 3.3 ms encode (cathost) + 12 ms decode (catway) |
| browser `pane_frame` (full) | 118,980 B (~11.9 B/cell) | 0.9 ms marshal |
| browser `pane_diff`, 1 cell | 97 B | — |
| browser diff, 60% changed | 62,558 B | — |

The β number is the headline: a skipped (unchanged) cell still serializes every
field, `{"symbol":" ","fg":33…,"bg":33…,"modifier":0,"skip":true,"hyperlink":null}`,
~85 B, so a one-character echo ships the whole grid. At 60 Hz per busy pane that
is roughly a core spent on JSON alone.

---

## Done — front end (commit "perf: incremental canvas painting …")

1. **Row-band canvas repaint** (`18-render.js`). A `pane_diff` repaints only the
   rows it touched: a clipped band of the dirty rows plus one cleared neighbour
   each side, with one more row each side painted underneath for overhang.
   Full draw is kept for frames, resizes, theme, selection, focus, and diffs that
   touch > ½ the rows or move the scroll.
2. **No redundant canvas state writes.** Memoized `css()`/`dimCss()` strings;
   `ctx.font` / `fillStyle` written only on change (was: two CSS parses per glyph).
3. **No canvas reallocation on every layout** (`placePane` guards width/height).
4. **`pane_title` stops re-querying `pane.list`**, patches the snapshot entry in
   place instead (named panes still query).
5. **Pushed Workspaces redraws coalesced** into the inventory rAF
   (`renderWorkspacesSoon`: clients, ws_git, hosts, agents).
6. **Hover card keyed** — no rebuild + forced layout per mousemove when unchanged.

Guarded by `jstest/render.test.mjs` (incremental == from-scratch full draw, pixel
for pixel, over random diffs) and the retargeted `TestPagePaintsBackgroundsBeforeGlyphs`.

---

## 1. Sparse β diffs  (done)

### Problem
`FrameFromSnapshot` emits every cell, marking unchanged ones `Skip`. catway
depends on that density: `FrameTranslator.Translate` builds a *full* browser
frame out of a *diff* β frame whenever a connection needs one (translator reset
because the pane just entered its viewport, or > 60% changed). So the skipped
cells' contents are load-bearing — they cannot simply be blanked.

### Design
- **Negotiation.** `Hello` gains `Features []string` (client capabilities). A
  catway that can apply sparse diffs sends `"sparse_frames"`. An older cathost
  ignores the unknown hello field and keeps sending dense frames; an older
  catway never sends it, so a newer cathost keeps sending dense frames to it.
  Persistent cathosts outlive catway restarts, so both skews are real and both
  keep working.
- **Wire shape.** `Frame` gains `Runs []CellRun` (`{"at":i,"cells":[…]}`,
  contiguous runs of changed cells, row-major index). A sparse diff has
  `Full=false`, `Cells=nil`, `Runs` set. Full frames are unchanged in shape.
- **Leaner cells everywhere.** `Cell.Modifier`, `Skip`, `Hyperlink` get
  `omitempty`. Safe for every receiver because each decode targets a fresh
  value (`var ev orchestration.PaneFrame` per message), so an omitted field reads
  as its zero value — exactly what was sent before.
- **catway keeps the grid.** Each pane runtime holds the last resolved grid
  (`browserproto.Grid`). Every β frame is applied to it on the loop — full or
  dense replaces it, sparse patches it — and translation reads the grid plus the
  list of changed indices. The resolved view is built once per frame and shared
  by every connection's translator (which also removes the per-connection
  re-scan of all cells).
- **Invalidation.** A frame for a pane nobody is viewing is dropped (as today),
  and marks the grid invalid. A sparse diff against an invalid grid is dropped
  too; the pane's full resync (always requested when it enters a viewport) makes
  it valid again. Dense diffs rebuild the grid outright, so an old cathost never
  hits this path.
- **Cheap type peek.** `ReadMessage` unmarshalled the whole payload just to read
  `type`, then dispatch unmarshalled it again. Every message is marshalled from a
  struct whose first field is `type`, so a prefix scan (`{"type":"…"`) reads it,
  with the full unmarshal kept as the fallback.

### Result
`TestSparseDiffIsSmall`: a one-cell diff on 200×50 plain text is 550,123 B
dense (with the leaner cells; 850 KB before them) and 226 B sparse. catway's
decode of it drops from ~12 ms to microseconds.

Tests: `sparse_test.go` (Sparsify, lean cells, hello features, type peek,
codec), `TestHostSparseFramesFollowTheHello` (ghostty: negotiated both ways),
the replay property test run a second time down the sparse path through JSON,
`TestGridRefusesWhatItCannotResolve`, and catway's `frames_test.go` (sparse
diffs to a window, a reset translator served from the grid, stale grids refusing
until a full frame).

---

## 2. Stop streaming hidden panes  (done)

### Problem
cathost snapshots, diffs, encodes and ships every dirty pane every 16 ms, and
catway decodes it — then throws it away if nobody is looking, keeping only
`rt.histDirty = true`. An agent streaming in a background tab costs the full
price of §1 at 60 Hz.

### Design
- **Feature + request.** cathost advertises `FeatureFrameGate` (`"frame_gate"`)
  in its welcome. catway, when the feature is present, sends
  `set_frame_panes {"panes":[…]}` — the union of panes any window is showing —
  whenever that set changes, and once after every (re)connect. Until a client
  sends it, cathost streams everything (old catways, `catctl probe`, tests).
- **cathost.** The gate is per session (reset on detach, like the stats and
  ledger subscriptions). In `flushDirty` a gated pane is not snapshotted. Its
  dirty flag is consumed, its input modes are still checked (input encoding for
  `send_input` to a hidden pane depends on them), and a tiny `pane_activity`
  event is emitted at most once per 2 s while it keeps producing output.
- **catway.** `pane_activity` sets `histDirty` — the only thing hidden frames
  were used for. Entering a viewport already triggers `RequestResync`
  (`resyncViews`), which re-baselines cathost's `p.prev` and sends a full frame,
  so a pane coming back into view is exact. The gate is sent before the resync
  on the same connection, so ordering holds.
- **Grid invalidation.** A pane leaving the viewport union has its catway grid
  invalidated (`syncFrameGates`), so a diff that beats the resync's full frame
  is dropped rather than applied to a base the host no longer diffs against.

### Result
An off-screen pane costs an atomic swap, an input-modes read when it has
output, and one small event per 2 s — no snapshot, diff, encode or decode.

Tests: `TestHostFrameGate` (ghostty: a gated pane is never framed, reports
activity, and replays everything it printed while hidden on resync),
`TestFrameGateFollowsTheViewport` (list follows zoom/unzoom, is sent before the
returning pane's resync, is not re-sent when unchanged, hidden grid refuses
diffs), `TestFrameGateNeedsTheFeature`, `TestPaneActivityMarksHistoryDirty`.
Documented in `docs/protocols/orchestration-seam.md` (frame gate, client
features, sparse frame shape).

---

## Later (not started)

3. **Scroll op + compression.** Scrolling output shifts every row, so it
   always falls back to full frames (~119 KB/tick/pane to the browser). A
   `scroll_up n` diff op (row-hash match of prev vs cur) turns `cat` into ~one
   row per line. rweb v0.1.28 has no permessage-deflate; adding it shrinks
   terminal JSON 10–20×.
4. **Coalesce slow browser clients** instead of dropping at 512 queued messages:
   track queued bytes, stop translating above a threshold, `Reset()` the
   translators, send one fresh full frame per visible pane once drained.
5. **Encode once per pane, off the loop.** Marshal on the loop is per
   connection; cache by (pane, frame seq, translator state) or move to the
   reader goroutine. Hand-written appender instead of reflective JSON.
6. **Snapshot cost.** ~3 cgo calls per cell; libghostty's per-row dirty tracking
   (`RenderState.Dirty`, row `Dirty`) is unused; `FrameFromSnapshot` runs under
   `emuMu`, stalling `feed`; per-tick allocation of the whole grid.
7. **Suppress empty frames** (no cell, cursor or scroll change).
8. **WebSocket write batching** in rweb (header + payload in one write; drain
   the queue per wakeup).
9. **Front end, low:** 5 s / 10 s sidebar tickers ignore `document.hidden`;
   the command palette re-renders its list on hover; `renderChrome` rebuilds the
   whole header on the 500 ms autoclose tick; `hosts` pong latency re-renders
   every header.
