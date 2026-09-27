# Session: N-051 warn-fg for amber text across the app

Session ID: 9ce2fb76-019e-491b-ad67-6a0a516ee650
Date: 2026-09-27
Driven from: cats

This follows `2026-0927-0014-n050-warn-fg-theme-key`, which added the
`warn-fg` theme key and used it in the plugins dialog. That session raised
N-051 for the other amber text in the app. The user pasted N-051.

## 1. The change

All CSS, in `cmd/catway/web/css/`. No theme values changed: the three themes
that need a separate shade already author `warn-fg` (solarized-light
`#694f00`, corporate `#734e00`, solarized-dark `#f2b700`). Every other
built-in derives it from `warn`, so those themes are unchanged.

### Swapped to `--warn-fg` (twelve rules)

| File | Rules |
|---|---|
| `06-usage.css` | `li.ugrp.high .gsum`, `.gsum .gleft.soon`, `li.high .uval`, `.ureset.soon` |
| `11-agentlist.css` | `.aage.stale-idle` |
| `10-panelist.css` | `.st-working` |
| `12-main.css` | `.pane .chrome .info .agent`, `.info .mode` |
| `29-runbooks.css` | `li.trig-off .rtrig` |
| `24-toolbar.css` | `#recbtn.on.empty .n` |
| `30-peers.css` | `.modal .hint.warn`, `.peers-report .row.skipped .kind` |

The usage meter fill and sparkline, the toolbar "!" mark and the plugins
dialog's tints stay `--warn`: they are fills, not text.

### Markers keep `--warn`

`.st-working` is generic. `stClass()` in `js/07-workspaces.js` puts it on
text and on markers alike:

- text: a pane row's `.ag` agent label, and the pane header's `.astate` word
- markers: `.adot` (the agent-list "●"), `.tmark` (tab marks) and the
  `#ws-list` "●N" badge

A new rule in `10-panelist.css`,
`.adot.st-working, .tmark.st-working, #ws-list .st-working { color:var(--warn); }`,
keeps the markers on the base amber. These are the same three selectors the
`dotpulse` rule already names. The markers sit in a row beside the ok, err and
done markers, where the hue is the signal. On solarized-light, the dark
`#694f00` would drift toward that theme's `#859900` olive ok. This follows
N-050's split: text uses `warn-fg`, and fills and markers use `warn`.

### The pane header's agent colour

The item asked for a check before changing it. `renderChrome`
(`js/06-chrome.js`) emits `.agent` and then `.astate st-<state>`. The agent
name and the "working" word therefore shared `--warn`. They move together, so
they still read as one run. The strip's other hued fields are the bold
`--branch` orange, `--accent` for the handle, and `--ok`/`--err`/`--done` for
the other states. The branch stays clearly separate on all three themes:
burnt orange against dark brown on the light themes, orange against gold on
solarized-dark. `12-main.css` has a comment on `.agent` saying so.

## 2. Checking it

- **Contrast (WCAG):** computed in a scratch Python script on each surface
  the rules sit on:
  - `--panel`: the sidebar and the top bar
  - `--panel2`: modals
  - `--chrome`: the pane header
  - `--chrome-focus`: the focused header, and the sidebar's focused rows
  - the record button's 14% err tint over `--panel`

  | Theme | before (warn) | after (warn-fg) |
  |---|---|---|
  | solarized-light | 2.12–2.62 | 5.11–6.30 |
  | corporate | 2.70–3.33 | 5.30–6.53 |
  | solarized-dark | 2.57–4.05 | 4.54–7.15 |

  The item's premise was that `--panel` and `--chrome` sit at least as far
  from `warn-fg` as `--panel2`. That holds, but `--chrome-focus` does not.
  It is the worst surface, and solarized-dark's focused header lands at
  4.54:1. That still clears 4.5:1.
- **Headless-Chrome render:** a scratch page linked the real stylesheets from
  `git show HEAD` (before) and from the working tree (after). Theme vars came
  from `theme.Normalize` via a throwaway test file, which was deleted
  afterwards. The page showed:
  - pane headers, unfocused and focused, with the working, idle and blocked
    states and copy mode
  - a usage group and row
  - a runbook trigger, pane rows, agent rows, and a workspace badge
  - the peers-report hint and rows

  The page was rendered on corporate, solarized-light, solarized-dark and
  cats-green. The text went dark amber on the light themes and bright gold on
  solarized-dark. The dots and badge are unchanged. cats-green's before and
  after PNGs are byte-identical. The record button fell below the screenshot
  crop, so it is covered by the contrast figures only.
- `go build ./...`. Tests pass in `./internal/theme/` and `./cmd/catway/...`.
  The full app was not run.

## 3. Found along the way

- **Other state words:** "idle", "blocked" and "done" are just as faint as
  text on the same three themes, at 1.8 to 4.3:1. The worst is
  solarized-dark's "blocked" on a focused header, at 1.8:1. Raised as N-052.
- **solarized-light's folded usage group:** the `.high` reading is now
  `#694f00`, close to the `#6f5f2a` ws-heading beside it. It still stands
  apart from the grey resting state. Noted in N-052, not changed.
- **`--flag-question`** (`01-theme.css`) is also `var(--warn)`, but it colours
  a flag glyph (`.fk-question`), not running text. Left alone.

## 4. Gotchas

- **Theme vars for a scratch render:** `internal/theme` can't be imported
  from outside the module. A throwaway `zz_dump_test.go`, gated on a
  `DUMP_DIR` env var, wrote each built-in's normalized vars as a
  `:root{…}` file. It was deleted right after the run.
- **Screenshots:** headless Chrome again, not claude-in-chrome, which
  force-darkens pages. `--allow-file-access-from-files` let the scratch page
  link the stylesheets over `file://`.

## Next

Closed: N-051. Declined: None. Raised: N-052.
Deferred: None. Promoted: None.
Updated: None. Full list: `ai_docs/todo/next-list.md`.
