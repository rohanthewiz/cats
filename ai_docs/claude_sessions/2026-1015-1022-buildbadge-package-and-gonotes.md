# Session: build badge packaged to share, then added to gonotes

Session ID: 8e4550fa-566a-426b-b256-6316ed4cb0f1
Date: 2026-10-15

The user asked to package the build-badge popup from `804723d` ("Build badge
popup shows WHEN") as something another project could use, and then to add it
to gonotes.

## 1. `buildbadge/`: the shareable package (cats `80e336c`)

It is a top-level, public package that uses only the standard library and
imports nothing else from cats. Another project can import it or copy the
directory.

| File | What it holds |
|---|---|
| `buildinfo.go` | The link-time stamp (`hash`, `subjectB64`, `when`), `Info`, `Get()`. It moved here from `internal/buildinfo` with `git mv`, so history follows. Also new: `Info.String()` for `version` output (`abc1234* · subject · …Z`). |
| `html.go` | `JS`/`CSS` (embedded), `DataScript(global)`, `Head()`, `ScriptTag()`. Each returns `""` when the hash is unknown. `DataScript` refuses a global that isn't a plain JS identifier, because the name is spliced in unquoted. |
| `buildbadge.js` | A plain IIFE that defines `window.BuildBadge` (`mount`, `rows`, `fmtWhen`, `fmtAge`, `autoMount`) and auto-mounts into `[data-build-badge]`. `mount` takes `show`/`hide` hooks, so a host can route the card through its own hover-card system. The built-in card is used only when no hooks are given. |
| `buildbadge.css` | `.build-badge` and `.bb-tip`. All colours are `--bb-*` custom properties, with a dark fallback through `prefers-color-scheme`. |
| `stamp.sh` | Prints the `-ldflags` for builds that don't go through the cats Makefile. Its argument is the package path, for projects that copied the package. |
| `README.md`, tests | Go tests (the moved resolve tests, plus `DataScript`/`Head`/escaping/`String`) and `buildbadge.test.mjs`, which runs under plain `node` with no cats harness. |

How cats uses it:

- **Makefile:** `STAMP_PKG` now points at `buildbadge`, and `make jstest` also
  runs `buildbadge/*.test.mjs`.
- **`page.go`:** `buildScript()` = `DataScript("__catsBuild")` + `ScriptTag()`.
  It deliberately does not use `Head()`: the badge keeps the `#brand
  small.hash` styling, and the card is the sidebar's shared hover card.
- **`js/10-buildbadge.js`:** shrank to a single `BuildBadge.mount(...)` call
  with `armTip`/`showTip` and `hideTip` as hooks, so the badge keeps the
  sidebar's dwell and warm-window timing. `fmtBuildWhen` moved into the
  package as `fmtWhen`.
- **Visible change:** the data `<script>` is now marked `data-buildbadge`
  instead of `id="cats-build"`, and `page_test.go` was updated to match.
- **Docs:** `docs/reference/build-and-packaging.md` now names the package and
  `stamp.sh`.

Verified with `go test ./...`, `go vet ./...` and `make jstest`, and the page
tests also ran against a build stamped through `stamp.sh`. The badge has not
been looked at in a real catway (N-061).

## 2. gonotes (gonotes `619126f`, on master)

- **Dependency:** `go get github.com/rohanthewiz/cats/buildbadge@80e336c`.
  Both repos are public. Through MVS this also raised gonotes'
  **`rweb` v0.1.26 → v0.1.31**, and gonotes' full test suite passes on it. The
  cats module zip is about 33 MB (mostly `third_party`) and is downloaded once
  per machine; only the package is compiled in (see N-063).
- **`landing/page.go`:** `buildbadge.Head()` goes before the `app.css` link
  (bumped to `v=18`), so app.css's `:root` mapping of `--bb-*` onto the theme
  variables wins at equal specificity.
- **`landing/toolbar.go`:** `<span class="toolbar-build" data-build-badge>`
  goes right after the logo link, as a sibling rather than inside it, so a
  click on the hash doesn't navigate home.
- **`app.css`:** the `--bb-*` → `--text-primary` / `--bg-primary` / … mapping,
  the spacing in the toolbar, and `:empty { display:none }` for an unknown
  build.
- **`scripts/stamp.sh`:** delegates to the `stamp.sh` inside the pinned cats
  version, found with `go mod download -json`, so the variable names can't
  drift. It prints nothing and exits 0 when the version can't be resolved.
  `GO=` selects the toolchain.
- **Builds:** `cats-plugin.toml` and `mac-install.sh` (`-s -w` plus the stamp)
  now stamp their builds.

Verified by running a stamped binary on a scratch data dir and port 18444. The
page carried the data (hash, subject, time, and `dirty:true` for the
then-uncommitted tree). Headless Chrome renders of the toolbar with the card
hovered looked right in both the dark-green and light themes. The live login
redirect was bypassed by loading a local copy of the page with the app
scripts stripped.

### A misstep, recorded so it isn't repeated

`gonotes serve --help` is not a subcommand. It ran the default server against
the real `~/.gonotes`, which opened both bytdb files, failed to bind 8444
(the live server holds it) and closed again within a second. The data files
were not modified (mtimes still Oct 13/14), but both `.lock` sidecars were
rewritten. That revealed the live server (a `go run` build from Oct 12) holds
the databases **without** the lock, so a second process could open them
(N-062). For gonotes, use `gonotes -d <scratch> -p <port>` and nothing else
when testing.

## Next

Closed: None. Declined: None. Raised: N-061, N-062, N-063.
Deferred: None. Promoted: None. Moved: None.
Updated: None. Full list: `ai_docs/todo/next-list.md`.
