# The note and the follow-up get drawn instead of spelled

*Three passes: swap the note's glyph off the rename pencil, discover the
sidebar was never the thing constrained to one text cell, then draw both marks
properly.*

Session: https://claude.ai/code/session_01Fzzn6WkEcpRHKiVwKdYZ87
Date: 2026-09-11
Repo: `~/projs/go/cats` (branch `main`)
Commits: `a3b1cd3` (glyph swap + colour lift), `d33372d` (the note as a drawn
sticky note), `17c39bb` (the follow-up as a drawn flag)

## Request

> When a workspace is flagged with a note, the pencil icon does not standout.
> Can we use an icon more closely related to a note, like a notebook page or
> scroll?

then, after the first pass landed:

> Hmm, isn't the todo icon (paw icon) occupying two cols, so why can't we do
> that for note? This is much clearer as a note

then:

> The icon is too plain. Can we have something with some color and like
> notebook page image I shared?

and finally:

> Looks great. Commit. Then do similarly for the flag icon. Give me some options.

## Where the note glyph lived

One table, mirrored into the browser and checked by a test:

| place | what it is |
| --- | --- |
| `internal/flags/flags.go` | `defs` — the vocabulary itself, six `Def{Kind, Glyph, Label, Meaning}` |
| `cmd/catway/web/js/07-workspaces.js` | `FLAG_DEFS` — a hand-kept mirror, so the browser can draw a menu before any flag exists |
| `cmd/catway/web/web_test.go` | `TestWebFlagVocabularyMatchesGo` — fails the build if the two drift |
| `cmd/catctl/flaglist.go` | `dispWidth`, which assumes every named glyph is one column |
| `docs/reference/cli.md`, `docs/protocols/control-api.md` | the documented table |

## Pass 1 — the glyph, and a wrong reason

`✎` went to `▤` (U+25A4, a ruled square). The real problem with the pencil was
never that it was faint: **a pencil is the rename affordance** in the pane
chrome (`02-color.js:25`) and in the help sheet (`32-help.js:80`), so the same
shape was saying two different things one column apart.

Also lifted `--flag-note` off `var(--muted)` — the dimmest token in the theme —
to `#c3d0c6`. That was half of why the mark disappeared.

The reasoning offered for *not* using a notepad emoji was half wrong, and the
user caught it in the next message.

## Pass 2 — the correction that mattered

> isn't the todo icon (paw icon) occupying two cols?

Yes. The sidebar already draws three SVGs in those rows:

| mark | source | size |
| --- | --- | --- |
| todo paw | `todoMark`, `07-workspaces.js:181` | 14px |
| padlock | `lockMark` | 12px |
| sleep moon | `sleepMark` | — |

Nothing in that row is column-constrained. The two-column limit is
**`catctl`'s**, and it applies to `dispWidth` laying out a terminal listing —
a different client entirely.

And `internal/flags` had already written down the way out, in `Def`'s own doc
comment:

> Glyph is the fallback rendering — a client with a real icon for the kind may
> use it instead, but every client must be able to fall back here, because the
> custom-glyph case has nothing else to draw.

So the split is: **`flagMark` (a DOM node, 4 call sites) can draw an icon;
`flagGlyph` (a string, 8 call sites) cannot and must not.**

```
flagMark  → 07-workspaces.js:527, 17-agentlist.js:105,201, 08-panelist.js:219
            ── sidebar + list rows, DOM, room for an icon

flagGlyph → 06-chrome.js:45 (pane header chip), 09-hovercard.js ×3,
            28-ctxmenu.js:158, 27-dialogs.js:299,309
            ── menus, toasts, tooltips, dialog titles: strings, glyph only
```

`FLAG_ICONS` is a **lookup, not a branch**, so kinds without an icon and every
custom glyph the user invents take the path they always did. A missing icon is
a miss, not a special case.

## Pass 3 — the first icon failed, for a structural reason

The first note icon was a stroked page with a dog-ear and two rules. It was
drawn correctly and it was unreadable: at 13px its outline, its fold and its
rules were all **the same 1px grey line**, so the whole thing averaged into a
smudge the size of a character.

The fix is the lesson `todoMark` already records a hundred lines up in the same
file — its toe pads are filled, not stroked, "solid shapes are what keep a paw
print readable". Small marks need **fields, not lines**.

## The two icons

Four colour candidates were rendered into a real sidebar mock and opened in a
browser; the user picked from the page. Both winners are filled, both carry
their own colours.

**note — a sticky note**

```
┌────────┐
│ ▬▬▬▬▬▬ │   three rules, the last short, so the field
│ ▬▬▬▬▬▬ │   reads as written-on rather than as a swatch
│ ▬▬▬  ╱▓│
└─────┘▓▓┘   corner turned up, in the shade the fold casts
```

field `#f2d45f`, fold `#cfa92f`, rules `#9c7f22`. The yellow sits a step warmer
and deeper than the `★` important flag (`#f2c14e`) and the todo paw
(`--todo #f0dfa0`) — the two nearest things in hue *and* row position. Hue alone
would not separate them; shape does.

**followup — a waving flag**

```
┃▅▅▅▅▅▀▀▀
┃████████   field hangs from the staff: top and bottom
┃▀▀▅▅▅▅▅    edges are mirrored cubics
┃
┃           staff runs full height, so the mark keeps the
┃           optical centre of the glyphs in the rows above
```

field `#e05c5c`, staff `#b9c2ba`. The wave is load-bearing — a straight-edged
field at 14px is a red rectangle parked beside a line. The staff is the one
stroke in either icon (1.5 units, ~1.3px) and was kept deliberately: without it
the field reads as a ribbon, and the `⚑` that `catctl` still prints has one.

Rejected: a swallowtail pennant (notch closes up small), a bookmark ribbon
(reads "bookmark", furthest from the printed glyph), a two-tone pennant (the
tones merge into one mid-red at 14px, losing the effect they pay for).

## The cost, accepted on purpose

A coloured icon is artwork, not a tinted silhouette, so these two kinds **stop
following `--flag-followup` / `--flag-note`** and stop responding to a theme.
Taken knowingly: a note has no urgency, so it loses every contest for attention
that colour alone decides. Being *drawn* is how it gets noticed without claiming
to outrank a follow-up — a claim colour would have made for it.

The `fk-*` class still goes on the span regardless, so a kind that later swaps
in a tinted icon gets its colour without touching `flagMark`. And
`--flag-note`'s lift still earns its keep: the pane header chip draws `▤` as
text in that colour.

## Files

| file | change |
| --- | --- |
| `internal/flags/flags.go` | `✎` → `▤`; comment on why the glyph stays a BMP character and what a client with a real icon should do |
| `cmd/catway/web/js/07-workspaces.js` | `FLAG_ICONS` + `drawIcon`; `flagMark` looks the kind up and falls through to text |
| `cmd/catway/web/css/28-flags.css` | `.flag-icon` layout, `.ficon` at 14px |
| `cmd/catway/web/css/01-theme.css` | `--flag-note` off `--muted` → `#c3d0c6` |
| `cmd/catctl/flaglist.go` | `dispWidth`'s glyph list |
| `docs/protocols/control-api.md` | the table, plus a note that the glyph column is the *fallback* |
| `docs/reference/cli.md` | the table |

## Unchanged

Every string context. `flagGlyph` still returns `⚑` and `▤`, so menus, toasts,
hovercards, the pane header chip and `catctl`'s aligned columns render exactly
as before — which is why the glyphs still have to be legible on their own, and
why an emoji would have been wrong there regardless of the sidebar.

## Verifying without the app

Sessions run inside Cats.app, so the live app is off limits. Two things worked:

- `qlmanage -t -s 240 -o <dir> icon.svg` renders an SVG to a PNG big enough to
  read — good enough to catch a broken path before showing anyone. Its
  *small*-size thumbnails are useless (it draws the icon into a corner of a
  padded canvas).
- A standalone preview page in the scratchpad — real theme tokens, real row
  metrics, candidates side by side at 13/14/16/22px plus a 6× zoom — opened
  with `open <file>`. `open` goes through LaunchServices, not AppleScript, so
  it is not blocked. This is what the icon choices were actually made from.

## Next, if wanted

The other four kinds (`?` `★` `⚠` `✓`) are still tinted text. `FLAG_ICONS` is
already the place they would go, and `drawIcon` already takes the table they
would be written as. The user's call at the time was note-only, then follow-up
— so the set is deliberately half drawn, half text.
