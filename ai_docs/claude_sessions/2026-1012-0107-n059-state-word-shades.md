# N-059: state-word shades for every built-in theme

Session: `163ece60-0c11-4185-94ca-99de9f2c605c` · 2026-10-12

The user pasted N-059 from the backlog. It is a follow-on to N-052, which
added the `ok-fg` / `done-fg` / `err-text` keys and gave shades to three
themes. Code commit: `dc5abd1`.

## What changed

- `internal/theme/builtin.go`: every failing built-in now has its own
  state-word shades, each with a comment saying why.
  - cats-green (the default): ok `#71c780`, done `#6ebbff`, err `#f39e9e`.
    This is the one change to the default look. The `:root` fallback in
    `cmd/catway/web/css/01-theme.css` mirrors it.
  - darcula: `#94d584` / `#a5cae6` / `#ffb1af`.
  - cool-blue: ok `#aed88b`, done `#dac3eb`, err `#f2c0b8`.
  - err only: tokyo-night `#fb9dae`, super-warm `#f19181`, dark-game
    `#ff5778`, dark-city `#ff7171`.
- `TestBuiltinStateWordContrast` now holds every built-in, and the `held` set
  is gone. The derivation comment in `theme.go` says the same.
- The dots, tab marks and badge keep the base hues. No change there.

## How the shades were chosen

- I wrote a scratch test (since deleted) that holds the HSL hue and raises
  the lightness until a colour is ≥4.55:1 on panel, panel2, chrome and
  chrome-focus. It also tried saturation at three levels: kept, 30% of the
  way toward full, and full.
- Keeping the saturation turned muted hues (darcula's green and blue, cool-blue's
  aurora colours, cats-green's red) into grey or dusty pink, so those took the
  30% step. Full saturation was too loud for the muted palettes.
- cool-blue's chrome-focus `#4c566a` is a mid slate. Lifted at their own hues,
  the purple (311) and the red (354) both became the same pale pink. done
  went to a lavender (hue 275) and err to a salmon (hue 8).
- I compared the candidates on a swatch sheet of each theme's chrome-focus
  and panel2, rendered in headless Chrome, and picked by eye.

## Not done

- Not looked at in the running app. Only the swatch sheet and the contrast
  test were checked.

## Next

Closed: N-059. Declined: None. Raised: None.
Deferred: None. Promoted: None. Moved: None.
Updated: None. Full list: `ai_docs/todo/next-list.md`.
