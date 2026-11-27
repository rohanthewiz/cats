# 2026-11-27 08:49 — ⌘-click plain-text URLs in a pane

Session: `d363e509-e68a-4baf-ad79-61a3a9997ca5`

## Request

"Is it possible to make URLs in a Cats terminal clickable, perhaps with a
modifier key" (screenshot: `gr serve` for rosync printing
`http://127.0.0.1:7420` and a `?token=…` sign-in URL as ordinary text).

## What was already there

- OSC 8 hyperlinks already worked end to end. libghostty's `HyperlinkURI` →
  `terminal.Cell.Link` → the frame's `Hyperlinks` table → the browser's
  `p.links` and each cell's `c.h` (1-based index). A **plain** click with no
  drag on such a cell calls `followLink` from the window mouseup handler
  (`24-upload.js`). `followLink` (`19-messages.js`) gives `file://` to
  `pane.open_file` and passes everything else to `window.open`.
- Plain-text URLs were not detected anywhere.
- Latent bug in the Mac app: `createWebViewWithConfiguration:` sent **every**
  `window.open` to `catappOpenWindowURL`, so an OSC 8 `http://` link opened
  *inside a new Cats window* instead of the browser.

## What changed

### Detection (`cmd/catway/web/js/19-messages.js`, after `fileLinkTarget`)

- `linkModHeld(ev)`: ⌘ on a Mac, Ctrl elsewhere, never both. On a Mac,
  Ctrl-click is a right click and must keep opening the pane menu.
- `plainUrlAt(p, x, y)`: client-side scan of `p.cells`, with no server round
  trip, so hover stays free.
  - Logical line: a row whose last cell is non-blank is taken to continue on
    the next row, up to `URL_MAX_ROWS = 8` rows each way. The frame does not
    carry soft-wrap flags, so this is the usual terminal heuristic.
  - Text is built per UTF-16 code unit, with `at[k]` mapping back to the cell
    index. A wide glyph's empty spacer cell adds nothing, and a missing cell
    reads as a blank.
  - Regex (built with `new RegExp` so `jstest`'s brace-scanning `slice` can
    lift it): schemes `https?|ftp|file`. The match stops at whitespace,
    `<>"'` and backticks, and box drawing U+2500–U+257F (TUI borders).
  - Returns `{ url, a, b }`, the first and last cell indexes, for the
    underline.
- `trimUrlTail(u)`: drops trailing `.,;:!?'"*` and an unbalanced
  `)`/`]`/`}`. A balanced pair stays (`Go_(programming_language)`).
- `linkAt(p, x, y)`: an OSC 8 link on the cell wins; otherwise
  `plainUrlAt`.

### Mouse wiring (`cmd/catway/web/js/24-upload.js`)

- mousedown: after the scrollbar check and **before** the mouse-capture
  branch, a left click with the link modifier over a link calls
  `followLink` and returns. That makes ⌘-click work inside vim, htop and
  Claude Code too, as iTerm2 and Ghostty do. It fires on press: neither
  `p.sel` nor `p.pressed` is set, so the window mouseup has nothing to finish
  and the app never sees a stray button-up. With no link under the pointer,
  the click falls through unchanged.
- Hover affordance: `linkHover` remembers the last pointer position over a
  pane. `trackLinkHover` runs on every canvas mousemove and is suppressed
  while the left button is held (a drag). `setLinkHover` repaints only when
  the span changes and sets `canvas.style.cursor = "pointer"`. Window
  keydown/keyup on bare `Meta`/`Control` replays the last position, so
  pressing ⌘ without moving the mouse shows the hover. `blur` clears it,
  since ⌘-Tab sends ⌘'s keyup elsewhere. `mouseleave` clears it too.
- The pane is looked up again in `panes` before a replay, because
  `applyLayout` rebuilds panes on a tab switch.

### Rendering (`cmd/catway/web/js/18-render.js`)

- `drawOverlays` → `drawLinkHover`: a 1px underline in the default
  foreground across cells `a..b`. A wrapped URL runs to the right edge and
  picks up again at column 0. An index past `W*H` (a resize since the hover
  was computed) is skipped until the next move.

### Mac app (`cmd/catapp/window_darwin.m`)

- `createWebViewWithConfiguration:` now splits on origin. If the URL has the
  same scheme, host and port as `webView.URL` (the page's own
  `?ws=<id>` "open in new window"), it goes to `catappOpenWindowURL` as
  before. Anything else goes to `[[NSWorkspace sharedWorkspace] openURL:]`,
  the default browser.
- `static BOOL catsSameOrigin(NSURL*, NSURL*)` is forward-declared above
  the method. Ports default to 443/80 by scheme. A nil or host-less page URL
  (a literal-HTML window such as the connect form) has no origin, so all of
  its links go to the browser.

## Tests

- New `cmd/catway/web/jstest/urlclick.test.mjs`, 33 assertions:
  - the screenshot's single-row case;
  - a URL wrapped across rows, clicked on either row, and its span;
  - prose punctuation and parens (balanced ones kept);
  - quotes, `<…>` and `│` borders;
  - only the listed schemes, and a bare `http://` is not a link;
  - two URLs on one row;
  - an emoji plus its spacer before a URL;
  - an OSC 8 link beating text scanning;
  - `linkModHeld` on Mac and Linux platforms.
- These pass: `make jstest` (all suites), `go test ./cmd/catway/...`,
  `go vet ./cmd/catapp/`, a `go build ./cmd/catapp/` (stray binary removed),
  and a syntax check of the concatenated bundle.
- Not run live in a browser or a rebuilt Cats.app (N-072).

## Notes

- Known limitation of the wrap heuristic: a hard line that happens to fill
  its row joins the next one. That only matters when a URL ends exactly at
  the right edge and the next line starts with characters a URL may contain.
  The underline shows the span before the click.
- The hover underline does not follow new output under a still pointer.
  The next mouse move or modifier change recomputes it.

## Next

Closed: None. Declined: None. Raised: N-072. Deferred: None.
Promoted: None. Moved: None. Updated: None.
Full list: `ai_docs/todo/next-list.md`.
