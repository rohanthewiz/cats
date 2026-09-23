# Session: one config.json and a tabbed Settings screen (⌘,)

Session ID: f69ccacf-3d48-4cb8-b477-e3c90def2ea1
Date: 2026-09-23
Branch: `todo/settings-ui-scrn-1521` (worktree), rebased onto main and
fast-forwarded in. Commit: `settings: one config.json and a tabbed Settings screen (⌘,)`

## 1. The ask

From a cats-todo item: "Consolidate options as JSON and add a settings UI scrn
accessible from the Mac menu and `CMD+,`".

A settings modal already existed, but it only covered the theme and copy-mode
keys. Options were spread over three places: `~/.config/cats/config.yaml`
(12 sections), the Mac app's `~/Library/Application Support/cats/app.json`, and
about 9 browser localStorage keys. Two decisions were put to the user:

- **JSON scope:** `config.json` **replaces** YAML, with a one-time migration.
  (The alternatives were keeping YAML and sending JSON over the wire, or
  reading both formats.)
- **UI coverage:** all config sections, **plus** folding `app.json` and the UI
  prefs into the one file.

## 2. What was built

### internal/config — the file

- Every field carries a `json:` tag identical to its `yaml:` tag, so both
  formats describe one schema.
- `DefaultPath()` is now `…/cats/config.json`; `LegacyPath()` is `config.yaml`;
  `ResolvePath()` is exported for the Mac app.
- `Load`: at the default location, `migrateLegacy` converts `config.yaml` to
  `config.json` once and leaves the YAML in place, logging that it is no longer
  read. If the JSON can't be written, Load falls back to reading the YAML (and
  Save follows the extension, so later saves go to the YAML). An explicit
  `--config x.yaml` is still read and written as YAML.
- `parse(data, asYAML)`: absent keys keep their defaults and unknown top-level
  keys are ignored (the file holds the app's `app` section).
- New file `jsonfile.go`, for two writers sharing one file:

  ```
                config.json
     ┌──────────────────────────────┐
     │ "server" … "ui"              │ ◄── catway: Save(cfg) rewrites every Config
     │                              │     key and copies unknown keys through
     │ "app": {…}                   │ ◄── catapp: WriteSection("app", …)
     └──────────────────────────────┘
  ```
  - `saveJSON` does a read-modify-write that keeps foreign keys. It never
    resurrects a known key that was omitted (removing the last host must
    stick).
  - `WriteSection` / `ReadSection` handle the Mac app's section.
    `WriteSection` refuses any key Config models and migrates the legacy YAML
    first. Without that, the app saving before catway had ever run would
    create a `config.json` holding only `app`, and the YAML would be stranded.
  - Key order is Config's field order, then foreign keys alphabetically (a map
    marshal would sort everything and make git diffs noisy).
  - Writes are atomic (temp file + rename). A new file is created 0600; an
    existing file keeps its mode.
  - The lock is a `flock` on a hidden sidecar, `.config.json.lock`, because a
    rename replaces the inode. Unix builds use `flock`; other platforms get a
    no-op.
- New `UI` section: `font_px` (9–32) and `sidebar_width` (≥150). Zero means
  "unset", so each browser decides.
- `config.example.json` is `Default()` saved; `TestExampleJSON` keeps it in
  sync (`-update-example` regenerates it).

### wire (backward-compatible)

- `ConfigGetResult.Options map[string]json.RawMessage`: the editable sections,
  exactly as they appear on disk.
- `ConfigGetResult.RestartSections`.
- `ConfigSetParams.Options`.
- Raw JSON rather than typed mirrors, so that a new knob doesn't need a wire
  release (other repos pin `wire`).

### catway

- `settings.go`:
  - `optionSections` = panes, persistence, worktrees, push, editor, ledger, ui.
  - Deliberately excluded: server/hosts/peers (restart-bound, or have their own
    dialogs), theme/keys (typed params), and runbooks (a runbook could
    otherwise re-enable its own triggers).
  - `applyOptions` decodes onto the current section with
    `DisallowUnknownFields`, and clones `Push.Priority` first (the decoder
    writes into maps, and a rejected save must not mutate the live config).
  - `applyLiveOptions` re-derives reap, autoclose and the worktree dir.
    persistence, push and ledger are `restartSections`.
- `page.go`: `uiPrefsScript` bakes `window.__catsUI` into the page (only the
  fields that are set).
- `internal/app/record_test.go`: `options` is classified plain.

### web

- `33-settings.js`: the modal is now a tabbed screen.
  - `tab()` starts a page and `sub()` adds an h3, so the existing theme code
    was kept almost as-is.
  - Tabs: appearance (theme + interface font/sidebar), keys, panes, editor,
    worktrees, notifications, persistence, ledger, server (read-only), and app
    (Mac only).
  - Generic tabs are rendered from the `OPTION_TABS` table (kinds: text,
    duration, int, bool, list, kinds, priority). The hints carry the
    documentation the YAML comments used to hold.
  - Only changed sections are sent. The screen reopens on the last tab.
  - Enter saves only from a text input.
- `persistUIPref` (debounced 800ms): ⌘+/⌘- (`01-bootstrap.js`) and the end of a
  sidebar drag or a double-click reset (`38-sidebar.js`) write back. The file
  value wins over localStorage at boot.
- `20-keys.js`: ⌘, / Ctrl+, opens settings.
- `window.catsOpenSettings` is the hook the Mac menu uses.
- CSS in `26-settings.css`: a 760px dialog with a tab column; the push
  kinds/priority controls sit in a wrapping `.chkgrp`; a narrow-screen layout
  moves the tabs on top.

### catapp (Mac)

- `config.go`: `appConfig` is now config.json's `app` section.
  `appConfigPath()` uses `CATS_CONFIG` when it names a JSON file, otherwise the
  default path. A legacy `app.json` is imported once and left in place.
- `menu_darwin.m`: Cats › **Settings…** ⌘, → `catappOpenSettings` →
  `catsEvalKeyWindow("window.catsOpenSettings && …")`. It falls back to the
  first window when none is key.
- `window_darwin.m`: a new `catsSettings` reply handler, and
  `window.catsAppSettingsGet/Set` in the bridge JS.
- `settings_darwin.go`: get returns `{mode (effective), presets}`. Set
  validates, re-reads the file, and lays mode and presets over it (Remote and
  Windows are kept). In remote mode it also updates `activeRemote.cfg` and
  redraws the Connect menu, so the client's next save doesn't write the stale
  preset list back.

### Docs

- Paths changed from `config.yaml` to `config.json` across the docs, README,
  flag help and the peers dialog text.
- `configuration.md` has a new intro covering migration, the shared `app`
  section and `ui`.
- `config.example.yaml` is re-headed as the annotated legacy reference.

## 3. Verification

- New tests:
  - `internal/config/jsonfile_test.go`: round-trip, partial documents keep
    defaults, migration, explicit YAML, foreign-section preservation both ways,
    removed-host not resurrected, WriteSection refusals and migrate-first, UI
    bounds, 0600 mode.
  - `cmd/catway/settings_test.go`: options persisted, applied live and echoed;
    six rejection cases, none of which mutate the file or live config;
    `uiPrefsScript`.
  - `cmd/catapp/settings_test.go`: app.json import, a settings save keeps
    Remote/Windows, catway's save keeps `app`.
- `make fmt-check vet vet-ghostty test-ghostty jstest` and
  `go test ./cmd/catapp` all green, before and after the rebase onto main.
- Live check against an isolated catway on `127.0.0.1:8497`, with temp
  XDG config and state dirs and its own cathost:
  - the YAML migrated on start;
  - Ctrl+, opened the screen (no app tab in a browser);
  - editing panes → save wrote `45m` and kept the theme;
  - ⌘+ twice wrote `ui.font_px: 16`, and a reload seeded it over localStorage;
  - the app tab, with a stubbed bridge, rendered and saved
    `{"mode":"remote","presets":[…"home mini"]}`;
  - the priority-row overflow found by screenshot was fixed with `.chkgrp`.
- **Not checked by hand:** the real Cats.app menu item and the native bridge
  (added to N-001).

## 4. Decisions worth remembering

- Front-end view state (sidebar folded, chat open, collapsed groups) stays in
  localStorage. Only real preferences (font, width) moved into the file.
- A font size set in the file is shared by every client, including a phone.
  That was the user's choice ("fold in UI prefs").
- `config.set` refuses unknown sections and keys instead of ignoring them, so a
  typo can't look like a save.

## Next

Closed: None. Declined: None. Raised: N-031, N-032, N-033.
Deferred: None. Promoted: None.
Updated: N-001 (Mac Settings… menu, app tab and app.json import to check by
hand). Full list: `ai_docs/todo/next-list.md`.
