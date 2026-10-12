# N-034, N-038, N-014, N-013: mobile shifts, a tools tab, dead keys, IME preedit

Session: `c911b2be-e5ee-44d0-ad48-dfc9e1d9516f` · 2026-10-11

Fourth batch of the unattended `/work-next-list` run.

## N-034: cats-mobile applies pane_shift (sibling repo)

- cats-mobile `a228479` pins cats at **v0.4.0**, the first tag with
  `wire.FeaturePaneShift` (v0.2.3 had none). The coverage test then flagged
  `plugin_notice`, which went on `ignoredDownTypes` because the phone draws
  no plugins list.
- `8c050f3`: `PaneGrid.shiftUp` scrolls the five column slices, blanks the
  vacated rows (" ", frame defaults), marks every row dirty, and runs before
  the diff's cells. A shift of the full height or more blanks the grid. The
  handshake lists `pane_shift`.
- **Live:** a throwaway test (not committed) dialled a scratch catway with
  catsclient while pane 1 printed random base64 lines. 171 of 194 diffs were
  shifts, no full frames were sent during the run, and the grid matched
  `catctl capture` row for row.
- With `echo line $i` output catway sent plain diffs instead. Consecutive
  rows differ by a digit, and a shift has to save two rows of cells
  (`minShiftGainRows`).
- Not watched on a phone. Painting goes through the same dirty-row path as
  full frames.

## N-038: tools.types on the settings screen

- A new **tools** tab, with a `map` kind in OPTION_TABS and `mapControl`
  in `33-settings.js`: label box, type box and × per row, plus **+ add**.
  Empty lines are ignored. A value without a label, or a label given twice
  (case-insensitively), is an error on the save line.
- `tools` was added to `optionSections`. `applyToolTypes` (`settings.go`)
  makes a `types` object replace the map, because decoding onto a map can
  only add. A default left out is saved as `""`, because the load-time
  merge over the defaults would otherwise restore it.
- `TestConfigSetToolTypesReplacesAndOptsOutDefaults`.
- Live in Chrome: removing dbc and adding `roman → http_client` wrote
  `{"dbc":"","gonotes":"notes_mgr","roman":"http_client"}`. A duplicate
  showed its error.
- Docs: configuration.md.

## N-014: dead keys (→ Validate)

- `onKey` returns on `e.key === "Dead"` without calling preventDefault, so
  the host composes into the text sink.
- `composedCodes` swallows the keyup of a key whose keydown went to a
  composition. Chrome ends the composition on the keydown, so that keyup is
  no longer `isComposing`.
- `deadkeys.test.mjs`. A real ⌥e e has to be checked by hand.

## N-013: IME preedit (→ Validate)

- `showPreedit`: while composing, the sink textarea itself is shown on the
  cursor cell, so the host's own marked-text rendering (clause underlines,
  caret) matches the editing. It uses the terminal font scaled by the pane's
  `gs`, `--term-fg` on `--term-bg`, and an accent underline.
  `sizePreedit` refits the width on each revision, and `compositionend`
  restores the 1px transparent style.
- `placeTextSink` now anchors on the cursor row's top edge (it was the row
  below), so the box covers the cell. Caret-anchored host UI still lands
  under the row.
- Chrome, synthetic events only: "かんじ" drew right after the prompt at
  the pane's size, and on end the overlay hid and "漢字" reached the pane.
  The scratch zsh ran in the C locale (`LANG=""`), so it showed the second
  character as bytes. That was the environment, not the paste path.

## Next

Closed: N-034, N-038. Declined: None. Raised: None. Deferred: None.
Promoted: None. Moved: N-014 → Validate, N-013 → Validate. Updated: None.
Full list: `ai_docs/todo/next-list.md`.
