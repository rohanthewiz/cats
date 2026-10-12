# N-053, N-052, N-039: narrow headings, readable state words, a dead pane's last screen

Session: `c911b2be-e5ee-44d0-ad48-dfc9e1d9516f` · 2026-10-11

Second batch of the unattended `/work-next-list` run (the first is
`2026-1011-2316-n044-n018-n045-seed-checked-live`).

## N-053: sidebar headings at 150px

- **Problem, reproduced:** a headless-Chrome render of a scratch catway with
  `ui.sidebar_width: 150` showed USAGE wrapping "3s ago" onto two lines and
  dropping ▼, and WORKSPACES dropping ▼. The sidebar also scrolled sideways.
- **Cause:** the title was a bare text node in a flex `h2`, which becomes an
  anonymous flex item that CSS can't make shrink or ellipsize.
- **Change:** `headingTitle` in `cmd/catway/web/sidebar.go` wraps it in
  `span.htitle` with the word as its tooltip. In `05-sections.css` the title
  shrinks first (flex-shrink 1000) down to a ~"US…" stub, and every other
  heading item holds its width. USAGE's controls alone fill the column, so
  past the stub its `.hage` stamp ellipsizes too (`06-usage.css`). The fold
  rule `> :not(.hctl)` became `> :not(.hctl):not(.htitle)`, since it relied
  on the title being the only text node.
- **Checked:** 150px reads "USA… ↻ ⊞ ⊟ ▼" and "WORKS… ⊞ ⊟ ▼". 180px reads
  "USA… 3s a… ↻ ⊞ ⊟ ▼". 278px is unchanged.

## N-052: idle / blocked / done as text

- New theme keys `ok-fg`, `done-fg`, `err-text` (derivations copy `ok`,
  `done`, `err`). Red is `err-text` because `err-fg` is the banner's ink on
  `err-bg`, and cats-green authors it as a pale pink `#ffd7d7`.
- `.st-idle` / `.st-blocked` / `.st-done` text uses them. A marker rule keeps
  the dots, tab marks and the workspace badge on the base hues, as
  `.st-working` already did.
- Authored on the three named themes, chosen by a scratch script that holds
  the hue and moves the lightness until the text is 4.5:1 on panel, panel2,
  chrome and chrome-focus:
  - solarized-light: ok `#486200` (nudged to hue 76 so "idle" stays apart
    from the `#694f00` "working"), done `#484eb3`, err `#ac1f1d`.
  - corporate: `#166a43`, `#0a6776`, `#ad2e2e`.
  - solarized-dark: `#b1cc00`, `#b6baff`, `#ffa9a8`. No full red reaches
    4.5:1 on the mid-teal focused header `#0f5666`, so it is a salmon.
- cats-green authors the three keys at its base values (TestDefaultFullyAuthored).
- `TestBuiltinStateWordContrast` checks those three themes on all four
  surfaces. Run over every built-in it found seven more themes short,
  cats-green among them: raised as **N-059** with the numbers. Changing the
  default theme's look was left to the user.
- Before/after headless render of the real stylesheets with each theme's
  variables: the dots are unchanged and the words darker (lighter on
  solarized-dark).
- Docs: a `docs/reference/configuration.md` row and a `:root` fallback.

## N-039: capture of an exited pane

- `Grid.ScreenText()` (`internal/browserproto/frame.go`) gives the mirror's
  screen as plain text: trailing blanks trimmed per row, trailing empty rows
  dropped, no final newline, like the daemon's trimmed plain capture. Also
  `Grid.ScrolledBack()`.
- `captureExited` (`cmd/catway/catway.go`):
  - the visible scope gets the screen;
  - the recent scope gets its tail (`lines` from the end, 0 = all). That is
    what `catctl capture <pane>` sends, so refusing it would have left the
    everyday verb empty-handed. The screen is the buffer's tail while the
    view sat at the bottom, so this returns the end of what was asked for,
    not different text;
  - `ansi`, `unwrap` and a recent capture of a view scrolled into history
    fail with "pane N has exited; only its last screen is kept, as plain
    text (no ansi or unwrap)";
  - an invalid grid (frame-gated off screen) keeps "pane N has exited".
- `read` is unchanged.
- Tests: `TestGridScreenText`, and `TestCaptureOfExitedPaneServesItsLastScreen`
  (ghostty tag).
- Live on the scratch catway: `catctl run 2 'printf "\npanic: boom\n  at
  main.go:42\n"; exit 3'`, then `catctl capture 2` returned the whole last
  screen and `catctl capture 2 1` returned `"  at main.go:42"`. `catctl read 2
  …` answered "pane 2 has exited".
- Known limits: soft wraps stay split, and a wide grapheme's spacer cell
  reads as a space.
- Found on the way: `catctl help capture` / `help read` advertise a raw
  `--params` form that catctl refuses for verbs named like their method.
  Raised as **N-060**.

## Verification

`go build ./...`, `go vet` on the touched packages, `go test` for
`internal/theme`, `internal/browserproto`, `cmd/catway` (untagged and
`-tags ghostty`) and `cmd/catctl`, and `make jstest`: all pass.

## Next

Closed: N-053, N-052, N-039. Declined: None. Raised: N-059, N-060.
Deferred: None. Promoted: None. Moved: None. Updated: None. Full list:
`ai_docs/todo/next-list.md`.
