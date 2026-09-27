# Session: N-049 contrast of the failed-default plugin row's "!" tile

Session ID: 5dd67798-3593-4e0d-8e8b-97d658ef378b
Date: 2026-09-26 (committed just after midnight, 2026-09-27)
Driven from: cats

The user pasted next-list item N-049. The plugins dialog's failed-default row
was never looked at on a light theme. It drew its "!" tile the way the N-048
toolbar chip first did, and that chip nearly vanished on a light toolbar.

## 1. The fix

Committed as `1f63d35`.

`.row.plg.dflt .av` in `cmd/catway/web/css/21-plugins.css` was --warn text on
an 18% --warn tint with a 1px --warn ring. It is now the toolbar mark's
treatment (`#pluginsbtn .w` in `24-toolbar.css`): solid `--warn` fill, ink
fixed at `#1b1606`.

- The ring was dropped. On a solid tile it matches the fill, so the base
  tile's bottom shade (`inset 0 -2px 0 rgba(0,0,0,.18)`) shows through
  instead.
- The row's --warn left edge, border and tint are unchanged. They frame the
  row, and the tile is what the eye lands on.
- There is no `--warn-fg` token for the tile ink. It has to be dark on every
  theme, and no existing token is: `--accent-fg` derives from `--bg` and is
  light on light themes. This is the reasoning of the N-048 toolbar rule.

### Contrast, measured

WCAG ratios computed from `internal/theme/builtin.go`. The modal surface is
`--panel2` and the tile sits on `--panel`.

| Theme | old tile | new tile | error line (unchanged) | "not installed" pill (unchanged) |
|---|---|---|---|---|
| solarized-light | 2.2 | 5.6 | 2.2 | 2.0 |
| corporate | 2.7 | 4.8 | 2.9 | 2.5 |
| solarized-dark | 3.3 | 5.6 | 3.1 | 2.7 |
| the other seven dark themes | ≥5.2 | ≥9.0 | ≥5.5 | ≥4.3 |

solarized-dark shares solarized-light's `#b58900` amber, so it is weak too,
which the item did not anticipate.

### The row's text: raised as N-050, not fixed

The error line (`.l2.warn`) and the pill (`.pill.st.missing`) are amber text
and cannot take a solid fill. Both classes, along with `.plg-head .chk.warn`,
also draw text on installed rows and in the dialog header, so this is a
decision for the whole dialog. One option was tried on paper: mix --warn toward
--fg-strong in CSS. At 50% it reaches about 4.5:1, but it turns
solarized-light's amber an olive (`#5e6021`) and the dark themes' a beige. The
likely fix is a `--warn-fg` theme key: derived from --warn, so the other
themes and sparse user themes are unchanged, and authored by the three
built-ins above.

## 2. Checking it visually

- **claude-in-chrome screenshots were unusable.** That browser force-darkens
  pages, so light themes came out dark and cats-green came out light.
  Computed styles were correct. Declaring `color-scheme: dark light` on the
  page did not stop it.
- **Headless Chrome renders true colours.** A scratch page linked the real
  `17-modal.css`, `20-palette.css` and `21-plugins.css` (and the pre-change
  copy from `git show HEAD:…`). It set each theme's vars inline on one cell
  apiece (corporate, solarized-light, cats-green), with a failed-default row
  and a normal row. It was served by `python3 -m http.server` on
  `127.0.0.1:18549` and captured with
  `Google Chrome --headless=new --force-device-scale-factor=2 --screenshot=…`.
  The new tile is clearly visible on all three. The old one was faint on both
  light themes.
- `go build ./cmd/catway` passes. The full app was not run: this is a
  one-rule CSS change, and the CSS is embedded, so seeing it live needs a
  rebuild and restart of the scratch setup from the N-048 session.

The headless-Chrome recipe is saved to auto-memory
(`chrome-force-dark-screenshots`).

## 3. Gotchas

- **Contrast claims need every theme checked.** The first draft of the CSS
  comment and of N-050 said the dark themes were all fine. The table showed
  solarized-dark was not, and both were corrected before the commit.
- **Stopping the preview server** was done by port, not by name:
  `lsof -ti tcp:18549 -sTCP:LISTEN | xargs -r kill`.

## Next

Closed: N-049. Declined: None. Raised: N-050.
Deferred: None. Promoted: None.
Updated: None. Full list: `ai_docs/todo/next-list.md`.
