# Session: untitled panes are hidden from the workspace dropdowns

Session ID: 5f1c2618-845b-451d-9e92-a48e597a8a67
Date: 2026-10-08
Driven from: cats

The request came from the user's cats-todo backlog, with
`2026-1007-1436-panes-dropdown-in-workspace-rows` loaded as context:

> Make this configurable, but by default do not show a pane if it has no
> custom title. So in the attached screen shot, don't show `p3T`

The screenshot showed the `cats` workspace's dropdown with two rows: `p3T`
(no title at all) and `p4M  todo: cats (6)`.

## 1. What "custom title" was taken to mean

In the codebase "custom title" is the pane.rename name
(`Session.PaneCustomName`, `effectiveTitle` in `catway.go`). But p4M's
`todo: cats (6)` is cats-todo's own **OSC title**, not a rename, and the user
asked to hide only p3T. So the rule is "has **any** title": the merged
`row.title` that `wsPaneRows` already builds (the rename when set, else the
program's title). Hiding everything without a rename would have taken p4M out
too. If that stricter reading is ever wanted, it is a one-line change in
`paneRowShown`.

## 2. The change

```
  ● cats 🐾6                         ▼      tooltip: show 1 pane
      p4M  todo: cats (6)                    · 1 untitled not listed (Settings → interface)
                                             · Alt+click: every workspace
      (p3T, a bare shell, not drawn)
```

- **Config** (`internal/config/config.go`): `UI.ShowUntitledPanes`,
  `show_untitled_panes`, omitempty. Its zero value (false, meaning hide) is the
  default, which fits the `ui` section's "zero means unset" rule with no
  `Default()` entry, and existing config files change behaviour with no edit.
  It reaches the page through the existing `window.__catsUI`
  (`uiPrefsScript`), and is settable through `config.set {options:{ui:…}}`
  because `ui` is already an option section.
- **Filter** (`08-panelist.js`): `paneRowShown(row)` keeps a row if the
  setting is on, or it has a title, **or an agent, or a flag**. The last two
  were added without being asked for: each is a mark the eye scans this list
  for, and a flag is set deliberately to find the pane again. Agent panes
  are nearly always titled by the agent, so that exception mostly covers the
  frame before the title lands.
- **Filtered in `wsPaneRows`, not at draw time.** The caret, its count and
  the drawn rows then agree. A workspace whose panes are all untitled gets no
  caret, rather than one that opens onto nothing. Each bucket array carries
  `.hidden` (the count left out); an all-hidden bucket stays as an empty array
  so the count survives.
- **Caret tooltip** adds "· N untitled not listed (Settings → interface)".
  Without it a four-pane workspace with one row would look like a one-pane
  workspace.
- **Settings screen** (`33-settings.js`): a *list untitled panes* checkbox
  under appearance → interface. `uiChanges` takes it as a third control and
  sends all three `ui` keys once any changed (config.set decodes onto the
  file's value, so the unchanged two are no-ops). `applyUIPrefs` redraws the
  workspace list when the key is present. `showUntitledPanes()` reads
  `uiPrefsFile`, so a save takes effect in this window at once. **Other open
  windows** pick it up only on their next load, as with the font size:
  config.set re-renders the served page but pushes no `ui` to open pages.
- **No localStorage fallback**, unlike `font_px`/`sidebar_width`: it is a
  setting, not a gesture's resting place, so the file is its one home.
- Docs: `configuration.md` (`ui` bullet) and `config.example.yaml`.

## 3. Verification

- `make jstest` passes. `panedrop.test.mjs` went from 39 to 48 assertions.
  The bucketing world now takes `showAll` (true for the existing grouping
  cases). New cases cover: untitled hidden; OSC-titled, renamed, agent and
  flagged kept; `.hidden` counted; an all-untitled workspace keeps an empty
  bucket; the setting shows every pane; a live title beats an empty snapshot
  title; and the caret tooltip names the hidden count, and says nothing when
  there is none.
- `go test ./internal/config/` (the JSON round trip now carries the new
  field), `go test ./cmd/catway/...` with and without `-tags ghostty`,
  `go vet` and `gofmt -l` are all clean.
- **Not run in a browser or in Cats.app.** The hand check was added to
  N-001's pane-dropdown bullet. The Mac app needs a rebuilt and reinstalled
  catway to show any of it.

## 4. cats-mobile

No follow-up. The phone has no workspace dropdowns (see the previous
session's §3), and nothing in `wire/` or `internal/orchestration` changed.

## Next

Closed: None. Declined: None. Raised: None. Deferred: None. Promoted: None.
Moved: None. Updated: N-001. Full list: `ai_docs/todo/next-list.md`.
