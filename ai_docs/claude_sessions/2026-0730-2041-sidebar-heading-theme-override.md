# Session: The Headings Stayed Yellow Because Go Had the Last Word

- **Session ID:** `51fb1f88-dd53-4c6f-a7a4-35d1f3fb06f1`
- **Date:** 2026-07-30
- **Branch:** main
- **Repo:** `cats`

## Request

> Hmm, the sidebar headings are still yellow even though this worktree commit tried
> to make them medium green: b5741a0 `style(ui): the sidebar headings turn green,
> the todo mark keeps its yellow`

Then, after the diagnosis: **"Make a note of this trap in the README, then /sess-wrap"**.

## The symptom, and the three things it wasn't

`b5741a0` changed `--heading:#f0dfa0` → `#6cbf8d` in `cmd/catway/web/index.html`.
The headings rendered yellow anyway. Four candidates, ruled out in order:

| suspect | verdict |
|---|---|
| the merge dropped it | no — `1823ea9` is a clean two-parent merge; its diff vs *each* parent carries the other parent's changes intact |
| the working tree lost it | no — `--heading:#6cbf8d` is in `index.html` at line 18 |
| a stale MacApp bundle | no — `strings` on the *installed* `/Applications/Cats.app/Contents/MacOS/catway` shows `--heading:#6cbf8d` already baked in (built 20:10, after the 19:32 commit) |
| a user config pinning the colour | no — no `~/.config/cats/config.yaml` exists |

Checking the installed binary is what turned this from a guess into a fact. The
green *was* being served. Something downstream was overriding it.

## The trap: two sources of truth, and the Go one wins

The page's CSS custom properties are declared in **two** places:

- the `:root` block in `cmd/catway/web/index.html`
- the `defaultColors` map in `internal/config/config.go`

`renderPage` (`cmd/catway/page.go:24`) injects the config theme as a *second*
`:root{…}` block, spliced in just before `</head>` — i.e. **after** the
stylesheet:

```go
inject := themeStyle(cfg.Theme) + keybindingsScript(cfg.Keybindings) + buildScript()
if i := strings.LastIndex(html, "</head>"); i >= 0 {
    return []byte(html[:i] + inject + html[i:])
}
```

Same specificity, later in the document — so for any var named in both, the Go
map wins outright. `defaultColors` still held `"heading": "#f0dfa0"`, so every
page load quietly repainted the headings yellow no matter what the stylesheet said.

The map's own comment had already called this out and been overlooked:

```go
// defaultColors are the served page's :root CSS custom properties (index.html).
// Keep in sync with the stylesheet's fallback values.
```

`--todo` was untouched by the bug for the mirror-image reason: `defaultColors`
has no `todo` key, so nothing overrode it.

## The fix

`internal/config/config.go`:

```go
"chrome": "#2b322c", "chrome-focus": "#3a4a3f", "heading": "#6cbf8d",
"todo": "#f0dfa0",
```

Two changes, not one. `heading` matches the stylesheet so the override stops
fighting it; `todo` is added so the new var is operator-overridable from
`theme.colors` like its neighbours — and so the map stays in sync as its comment
requires, rather than being re-synced only for whichever var is currently broken.

`go build ./...` clean; `internal/config` and `cmd/catway` tests pass.

## The symmetric failure mode

The override cuts both ways, and both halves are silent:

- var in **both** → the Go map wins, editing `index.html` does nothing visible
- var in **stylesheet only** → renders correctly, but `theme.colors` can't touch it

`--done:#00ccf5` is currently in the second category. Left alone — it renders
right and nobody has asked to theme it — but flagged: a test asserting the two
lists agree would catch the whole class instead of one instance at a time.

Documented in `README.md` as a **Theming note (two sources of truth)**, placed
beside the existing macOS toolchain note so the gotchas sit together.

## Note for next time

The MacApp embeds `index.html` via `go:embed`, so a UI change needs a rebuild and
reinstall to be visible. That's the usual suspect for "my change didn't take" here
— but this time the bundle was current, and believing the binary over the habit is
what found the real cause.
