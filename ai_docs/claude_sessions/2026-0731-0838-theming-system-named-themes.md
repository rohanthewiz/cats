# Session: Named Themes — Ten Palettes, Custom Theme Files, and a Live Broadcast

- **Session ID:** `999c308c-fe76-4b15-a442-6694cf81ad92`
- **Date:** 2026-07-31
- **Branch:** main
- **Repo:** `cats`

## Request

> Let's do a thorough take on theming. Take the current theme as the default and
> give me several other cool light and dark themes: darcula, tokyo night,
> solarized - light and dark, super-warm, cool-blue, dark-game, dark-city,
> corporate. Also provide a way for a user to create their own theme or modify an
> existing theme to make a custom one. Perhaps allow taking a theme in via the
> current or other plugin system.

## What shipped

A full named-theme system, built on the ground the 2026-0730 session mapped
(the "Go map wins the cascade" trap):

1. **`internal/theme` (new package)** — the single source of truth for color.
   - `Theme{Name, Label, Dark, Colors, Font, Source}`; ~31 canonical color keys.
   - **Derivation table**: only 8 core keys are required (`bg fg muted line
     accent ok warn err`); everything else derives in order (`panel ← bg`,
     `chrome ← panel2 ← panel`, `sel-fill ← rgba(accent, .30)`, …). Sparse
     user themes stay sparse on disk; `Normalize` fills them at resolve time.
   - **Ten built-ins** (presentation order): `cats-green` (default — fully
     hand-authored so the pre-themes rendering reproduces byte-for-byte),
     `darcula`, `tokyo-night`, `solarized-dark`, `solarized-light`,
     `super-warm`, `cool-blue` (Nord), `dark-game`, `dark-city`, `corporate`.
     Light themes: `solarized-light`, `corporate`.
   - **Registry precedence**: user (`~/.config/cats/themes/*.yaml`) > plugin
     (`<plugins-root>/<id>/themes/*.yaml`) > builtin; a name clash shadows *in
     place* so builtin ordering survives. Broken files are warnings, never
     failures. `dark:` auto-detects from bg luminance (Rec. 601) when absent.

2. **Config becomes choices-only** — `config.Theme` gained `Name`; `Colors` is
   now a *sparse overrides* map (Default() is empty), `Font` "" means inherit.
   `defaultColors`/`defaultFont` left `internal/config` for the theme package.
   Old configs with full color maps still work: they act as overrides with
   identical rendering.

3. **Resolution at render** — `resolveTheme` (`cmd/catway/page.go`) re-reads
   the registry on every `renderPage` (startup / config.set / reload — cheap,
   and it's what makes a freshly installed theme plugin appear without a
   restart). `themeStyle` now emits sorted keys (deterministic bytes).

4. **Wire + commands**
   - `config.get` result: `theme` = effective (name + resolved palette +
     font), `theme_overrides` = the raw sparse map, `themes[]` = full registry
     with normalized palettes (client previews without a round trip).
   - `config.set` theme semantics: **Name present ⇒ switch** (colors/font
     REPLACE overrides — empty means "the theme, clean"); Name absent ⇒
     legacy key-wise merge.
   - New `theme.list` / `theme.save` / `theme.delete` (Backend seam methods
     `ThemeList/ThemeSave/ThemeDelete` in `cmd/catway/settings.go`).
     `theme.save {activate:true}` = install-and-switch, clearing overrides.
     Deleting the active theme re-points config to the default.
   - New down message `theme` (`browserproto.Theme`): full resolved palette,
     broadcast on ConfigSet / ThemeSave / ThemeDelete / ReloadConfig — **every
     client restyles live**, not on next page load. Reply-then-broadcast
     ordering (the issuer's recvDown sees cmd_result first).
   - `catctl themes` / `catctl theme <name>` ergonomic verbs.

5. **index.html var-ification** — the ~30 hardcoded dark-only hexes became
   vars so light themes work: `--fg-strong/soft/bright` (text ladder),
   `--chrome-fg/-dim`, `--accent-fg` (text on accent), `--err-bg/fg` (link
   banner), `--hover`, and canvas colors `--term-fg/bg`, `--sel-fill`,
   `--cm-cursor`, `--scroll-thumb/-idle`. JS: `THEME_FG/BG`, `SEL_FILL`,
   `CM_CURSOR`, thumbs are now `let`s re-read from computed style by
   `readThemeVars()`; `applyThemeInline(colors, font)` paints inline `:root`
   props + repaints all pane canvases; the `theme` WS message calls it.

6. **Settings modal rework** — theme picker (optgroups built-in / custom /
   plugins, "(light)" markers) with instant preview; color rows against the
   picked theme; save sends only rows that *differ* from the theme (the sparse
   overrides); "save as" writes + activates a user theme file; delete button
   for user themes. Cancel rollback re-applies the config.get snapshot
   (not `removeProperty` — an earlier live push may sit in the inline props
   and the baked style underneath could predate it).

7. **Plugins ship themes by convention** — `themes/*.yaml` next to the
   manifest; *no manifest field*, preserving "the server never parses a
   manifest". Manifest validation relaxed: zero `[[actions]]` is now legal
   (asset-only plugins).

## Gotchas encountered

- `go build ./cmd/catway/` (single package) drops a `catway` binary in the
  repo root — deleted before commit.
- The settings tests read down-messages in order; the new broadcast made
  `recvDown` return `theme` where a `cmd_result` was expected → the
  reply-before-broadcast ordering, plus tests that consume the push explicitly.
- `#ccc` (wsgrp hover) and `#ddd` (tab hover) merged into one `--fg-soft:#ddd`
  — deliberate tiny visual delta.
- `--done` drift healed: it was stylesheet-only (`#00ccf5`) and invisible to
  the settings modal; it's now a first-class key (and the stale
  `config.example.yaml` value is gone with the example's color dump).

## Files

- New: `internal/theme/{theme,builtin,load}.go` + tests.
- Touched: `internal/config/config.go`, `internal/app/{command_vocab,commands}.go`,
  `internal/browserproto/{proto,down,cmd}.go`, `internal/plugin/manifest.go`,
  `cmd/catway/{page,settings,catway}.go`, `cmd/catway/web/index.html`,
  `cmd/catctl/subcommands.go`, tests alongside, docs
  (`configuration.md`, `plugins.md`, `cli.md`, both protocol docs, README
  theming note, `config.example.yaml`).

## Verification

- `go build ./...`, `go build -tags ghostty ./cmd/...` clean; `gofmt -l` clean.
- Full `go test ./...` + `go test -tags ghostty ./cmd/catway/` green, including
  new coverage: theme derivation/registry/round-trip, dispatcher routing,
  config sparse-override persistence, switch-clears-overrides,
  save/activate/delete flow, asset-only manifest.
- Palette sanity print for `corporate` / `solarized-light` / `dark-game`
  confirmed derivations (dark text washes on light bgs, accent-tinted canvas
  overlays).
- Not yet done: a visual in-browser pass. Try `catctl theme dark-city` with a
  running server — every open browser should flip instantly.

## Follow-up ideas

- The login page (`cmd/catway/auth.go`) and the macOS launcher pages
  (`cmd/catapp/pages.go`) still wear the old orphaned blue/charcoal palette —
  they predate theming and are served before/outside the themed page.
- Terminal cell colors remain the PTY's own (deliberate, unchanged); only the
  canvas *defaults* (`term-fg/bg`) theme.
- Settings modal could preview themes on hover (picker previews on change now).
