# Session: plugin types, a PLUGINS section, and a release train

Session ID: faf51bc9-7ce7-479d-9777-a1f3ed838d14
Date: 2026-09-22
Driven from: cats (with cats-todo, ced, gonotes and cats-mobile alongside)

## 1. The ask

"ced is not an agent — just an editor plugin." Separate LLM agents from
plugins, give plugins a type (`agent` — future-proofing, `editor`,
`todos_mgr`, `notes_mgr`), and put plugins in a new **PLUGINS** sidebar
section below AGENTS. Context: cats-todo's session doc
`2026-0922-1601-ced-not-a-drop-target.md`. cats-todo had hard-coded
`editorAgents = ["ced"]` because `pane.list` did not say which panes are
editors, and it listed "Wire an editor flag" under Next.

## 2. Design

```
cats-plugin.toml          launch env              catway pane state        consumers
type = "todos_mgr"  ──▶  CATS_PLUGIN_TYPE  ──▶  PaneState.PluginType  ─┬─▶ agents rollup: PluginPane.Type
(validated at load)      (plugin.LaunchEnv,      (durable, set with     │      → sidebar AGENTS / PLUGINS split
                          all 3 launchers)        PluginID, cleared on  ├─▶ pane.list: PaneMeta.plugin / plugin_type
                                                  every respawn)        └─▶ plugin.list / catctl plugin list: [type]
```

- **The dividing line is agent vs tool.** An agent takes turns, has a state
  worth watching, and can be handed a prompt. Everything else is a tool
  (PLUGINS, never a drop target). The finer types exist so a client can find
  "the notes manager" without knowing plugin ids.
- **The set of types is left open.** `wire.ValidPluginType` accepts any
  lowercase snake_case word, so an older cats still installs a newer plugin.
  An unknown type is treated like an unset one (a tool). A typo like
  `Todos-Mgr` is rejected at manifest load.
- **catway still reads no manifests.** The type rides the launch env like
  `CATS_PLUGIN_ID`. `plugin.LaunchEnv` is the single builder, used by the
  browser (via plugin.list), `catctl plugin run`, and the completion
  delegate, so a new variable can't reach one launcher and miss the others.
  The type is recorded only alongside an id.
- **`editor.agents` overrides the manifest.** `wire.EditorInfo.ResolvePluginType`
  returns `editor` for any configured editor label, so a ced typed into a
  shell (no manifest) and a ced whose manifest predates the key both work.
  The reverse holds too: a manifest that declares `editor` stays out of
  AGENTS even when its label isn't configured. Any other declared type
  passes through, and a pane an untyped or tool plugin launched that really
  runs an agent stays an agent row.
- **One rollup, two sections.** No new message: `Agents.Plugins` gains
  `Type`, and the browser splits on it. `type == "agent"` stays in AGENTS
  (as the old plugin block under the agents), and everything else goes to
  PLUGINS. PLUGINS is hidden while empty, like Hosts and Runbooks.
- **`pane.list` answers the drop question.** `PaneMeta` gains `plugin` and
  `plugin_type`, resolved exactly as the sidebar resolves them.
  `PaneMeta.IsDropAgent()` is true when an agent is detected and the type is
  `""` or `agent`, so clients stop copying `editor.agents`.

## 3. What changed, by repo

**cats**
- `61b4e6a` plugins: type + PLUGINS section.
  - wire: `PluginType*` constants, `ValidPluginType`, `IsAgentPluginType`,
    `EditorInfo.ResolvePluginType`, `PaneMeta.Plugin/PluginType`,
    `PaneMeta.IsDropAgent`, `PluginPane.Type`, `PluginInfo.Type`.
  - `internal/plugin`: `Manifest.Type` + validation, `TypeEnvVar`,
    `LaunchEnv`.
  - Session/workspace: `SetPanePlugin(id, plugin, typ)`,
    `PanePlugin → (plugin, typ)`, persisted `plugin_type`.
  - catway: records the type at `createPane`, classifies in `agentsMsg`
    and `PaneMeta`.
  - web: `sec-plugins` / `plugin-list`, `renderAgents` split
    (`isAgentPlugin`, `appendPluginBlocks`, `pluginTypeLabel` — `todos_mgr`
    shows as `todos`), CSS shared through `:is(#agent-list, #plugin-list)`,
    type in the plugins dialog.
  - `catctl plugin list` prints `[type]`.
  - Docs: plugins.md "Plugin types", the PLUGINS layout, env table;
    control-api `pane.list` fields; cli.md.
  - Tests: `wire/plugintype_test.go`, manifest cases + `TestLaunchEnv`,
    catway pluginpane tests (types on rows, editor typed `editor`, declared
    editor leaves AGENTS, `PaneMeta` / `IsDropAgent`, env round trip),
    jstest rewritten for the split (25 assertions).
  - Also gofmt'd a pre-existing misalignment in `web/assets.go`.
- `3b79d0d` docs: CI's `mkdocs build --strict` had failed on the last three
  pushes. `Triggers — \`on:\`` gave a GitHub-style anchor (`triggers--on`,
  which gkdocs uses) that mkdocs didn't produce (`triggers-on`), so the
  heading now uses brackets. `cli.md#peers` didn't exist, so the link now
  points to `#verbs`. Checked locally with a throwaway mkdocs install in
  `$TMPDIR`.
- **Tag v0.2.3.** The first product release since v0.2.1 (v0.2.2 only added
  the Intel build). 247 commits, and the annotated tag message summarises
  them. Tagged after CI was fully green on `3b79d0d`.

**cats-todo** — `636999a`: bumped the cats pin to `61b4e6a`. `isDropAgent` =
`p.IsDropAgent()` and not in `editorAgents`, with the list kept only as a
fallback for a cats that doesn't send `plugin_type`. `TestIsDropAgent`
covers the typed cases. Manifest `type = "todos_mgr"`. Release `99b6e01`,
**v0.33.1** (the release its previous doc planned).

**ced** — `c7964a1` manifest `type = "editor"` (plus the header comment,
which said AGENTS). Release `78bbcac` **v0.3.5**, following ced's current
practice (a hand-made "Release ced X" commit bumping `version.go` and the
manifest together, then a tag). The `release` branch/CI route in
`release.yml` hasn't been used since 0.2.0. The release also carried the
user's pending `832ebe1`.

**gonotes** — `d98450e` manifest `type = "notes_mgr"`. Release `e0c2a39`
**v0.2.0**, a minor bump because the 19 commits since v0.1.0 include
features (advanced search, summarize, sync relay). The tag message lists them.

**cats-mobile** — `0b38a3c` `wire: pin cats at v0.2.3`. Only `61b4e6a`
touched `wire` in between, and every change was an added field or helper.
`Session.Plugins` already keeps the whole `PluginPane`. No new down type or
command. vet, `test -race` and the wasm build are clean.

## 4. Things learned

- **The cats-mobile "regen" is gone.** cats-mobile is a Go client importing
  `wire`. Updating it means `go get cats@<tag>` plus its drift tests
  (`TestEveryDownTypeHasAnArm`, `viewer_mode_test.go`). The memory notes
  still described `tool/regen.sh` / `CATS_REV` / `dart test`, and were
  rewritten.
- **Release practice differs per repo.**
  - cats: an annotated `v*` tag triggers `release.yml` (four platform
    tarballs, generated notes; the tag message doesn't become the release
    body).
  - cats-todo: a `chore(release): vX` commit (`main.go` + manifest), then
    the tag.
  - ced: a hand-made release commit, then a tag.
  - gonotes: manifest version plus the tag.
- `go get` on a sha pushed seconds earlier: use `GOPROXY=direct`. The error
  "shallow file has changed since we read it" is a transient module-cache
  race, and a retry succeeds.
- cats' TOML loader ignores unknown keys, so adding a manifest key is safe
  against older hosts. That is why the plugin manifests could be updated
  before anyone upgrades cats.

## 5. Verification

- cats: `make vet`, `vet-ghostty`, `fmt-check`, `test`, `test-ghostty` and
  `jstest` pass. CI is fully green on `3b79d0d`.
- cats-todo `go test ./...`; ced full suite (see the flake below);
  gonotes `go test ./...`; cats-mobile `go test -race ./...` + wasm.
- **Not seen running:** the PLUGINS section hasn't been viewed in a live
  Cats.app (GUI driving is blocked in-app, and the bundle needs a rebuild).

## Next

- Look at PLUGINS in a rebuilt Cats.app. Relaunch cats-todo, ced and gonotes
  through the plugin host (`catctl plugin update` / relink first) so their
  panes pick up `CATS_PLUGIN_TYPE`. Check the row type labels and that
  AGENTS says "none" when only plugins are open.
- Optional: copy the v0.2.3 tag message into the GitHub release body
  (`gh release edit v0.2.3 --notes-file`), since only generated notes land
  there. The release itself is published: run `35790848738` was green on
  all four platforms, and all four tarballs are attached.
- ced: `TestThemeAfterSave_RepaintsLive` is flaky (~1 in 10): TempDir
  cleanup fails with `themes/` "directory not empty", which means something
  writes into it after the test ends. The assertion itself passes.
- ced: `release.yml` still describes the `release` branch + auto-bump
  route, which hasn't been used since 0.2.0. Either retire it or go back to
  it.
- cats-mobile: nothing draws `Session.Plugins` yet. If the phone gets a
  plugins list, split it by `Type` the way the desktop does.
- cats-todo: "Send info prompts to gonotes" (from its own Next) can now find
  the notes plugin by `plugin_type == "notes_mgr"` in `pane.list` instead
  of by id.
- `errPaneInputFull` emits one error event per refused message; rate-limit
  it if a flood into a dead pane ever spams them (carried).
- Legacy X10 / urxvt mouse encodings are never droppable. That's fine while
  the browser encoder emits SGR (carried).
- The peers dialog was syntax-checked, not clicked through. Open gear ›
  *peers / sync…* once and look (carried).
- `catctl attach-peer` takes a token file only. A `pair`-style grant for
  peers (short-lived, revocable) would avoid copying passwords (carried).
- Cross-OS home translation in peer sync is only unit-tested. The first
  Mac ↔ Linux sync is the real test (carried).
