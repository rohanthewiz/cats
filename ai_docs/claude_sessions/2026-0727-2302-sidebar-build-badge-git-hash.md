# Session: Sidebar Build Badge — Short Git Hash with Commit-Subject Popup

- **Session ID:** `1c6b1a76-b7c5-4708-b530-22dfe57dd844`
- **Date:** 2026-07-27
- **Branch:** main

## Request

> In the top left to the right of "cats" please show the current short git hash
> with a pop-up of the short commit message as done recently in this project
> `~/cbre_projs/edp_dataflow`.

## The referenced precedent doesn't exist

edp_dataflow has no git hash, no commit subject and no tooltip on its version
indicator — verified across all branches (400 commits) and its ~50 session docs.
What it actually has is a Jenkins `BUILD_NUMBER` → `-ldflags` → plain
`<span class="version-label">v0.0.123</span>` (`web/header_component.go:68`),
with no popup at all. Its *unrelated* `.id-wrapper`/`.id-popup` CSS hover card is
the closest thing to a popup pattern there.

The nearest real hash capture in the wider tree is `go_notes`
(`Makefile` → `GIT_COMMIT=$(shell git rev-parse --short HEAD)` → `version.go`),
but it too has no commit message and no popup.

So this was built from cats' own patterns rather than ported.

## Design

Three decisions worth recording.

### Where the data comes from: link-time stamp, with a free fallback

A runtime `git rev-parse` was rejected outright — catway in an app bundle has no
repo to ask, and its cwd is arbitrary. The hash must describe *the binary*, not
whatever directory it happens to sit in.

`debug.ReadBuildInfo()`'s `vcs.revision` is stamped automatically by the
toolchain (confirmed: `go version -m` on a plain `go build` of `./cmd/catway`
shows `vcs.revision`, `vcs.modified=true` on a dirty tree, and `-trimpath` does
not strip it). But build info carries **no commit subject**, so ldflags are still
required for the popup text. The two compose: ldflags supply hash + subject,
build info supplies the dirty flag always and the hash when unstamped — so
`go run` / `go build ./...` still show a correct hash, just no subject.

### Why the subject is base64-encoded

A commit subject is free to contain spaces, single quotes (`don't`), double
quotes, `` ` `` and `$`. It has to survive: make variable expansion → the recipe's
shell → a double-quoted `-ldflags` string → go's own ldflags tokeniser. Stripping
the offending characters with `tr -d` is lossy and the quoting is unreadable;
base64's alphabet (`A-Za-z0-9+/=`) is inert in every one of those layers and
lossless. Cost is one `base64.StdEncoding.DecodeString` at startup.

(Make specifics that make this safe: `:=` expands `$(shell …)` once at parse
time, and make does **not** rescan the resulting text, so a `$` or `#` in a
subject is never re-expanded or treated as a comment.)

### Where it renders

`#brand small` already existed in the stylesheet — muted, `margin-left:6px` —
and was entirely unused. The badge drops straight into that slot.

For the popup, `#panetip` (the pane-list hover card) already had the right look
and the viewport-clamping logic, so it was generalised rather than duplicated.

## What was done

### `internal/buildinfo/` — new package

`Get() Info` where `Info{Hash, Subject string; Dirty bool}`, resolved once via
`sync.Once`. Stamped vars `hash` / `subjectB64` are unexported so `Get` is the
only reader. `resolve()` decodes the subject, keeps only its first line (a
hand-set value shouldn't be able to smuggle a second row into the display), then
consults `debug.ReadBuildInfo` for the fallback hash and the dirty flag. Empty
fields mean "unknown" — callers omit the UI rather than show a placeholder.

### `Makefile` — the `STAMP` variable

```make
STAMP_PKG := github.com/rohanthewiz/cats/internal/buildinfo
GIT_HASH  := $(shell git rev-parse --short HEAD 2>/dev/null)
GIT_SUBJ  := $(shell git log -1 --pretty=%s 2>/dev/null | base64 | tr -d '\n')
STAMP     := -ldflags "-X $(STAMP_PKG).hash=$(GIT_HASH) -X $(STAMP_PKG).subjectB64=$(GIT_SUBJ)"
```

Applied in `binaries` and `local`. `macapp` inherits it for free — it depends on
`binaries` and `scripts/build-macapp.sh` only *copies* `bin/catway`. The `-X` for
a package cathost/catctl don't import is silently ignored by the linker, so the
one-line `$(foreach …)` recipes stay as they are.

### `cmd/catway/page.go`

`buildScript()` emits
`<script id="cats-build">window.__catsBuild={…}</script>`, appended to the
existing `renderPage` injection beside `themeStyle` / `keybindingsScript`.
`json.Marshal`'s default HTML escaping keeps a `</script>` in a subject inert —
the same argument the keybindings block already relies on. An unknown hash emits
nothing.

### `cmd/catway/web/index.html`

- **Tooltip generalised.** `showPaneTip` was one function doing three jobs. Split
  into `showTip(e, items)` — takes `[label, value, valueCls]` rows, drops empty
  values, owns the cursor positioning and viewport clamp — plus `hideTip()`, with
  `showPaneTip` reduced to building its row array. One gotcha: the row array
  cannot be named `rows`; that shadows the global terminal-rows variable the
  `Window` row reads.
- **`initBuildBadge` IIFE** appends `<small class="hash">` to `#brand` when
  `window.__catsBuild.hash` is present, text `e251787` (`*` suffix when dirty),
  wired to `showTip` on mouseenter/mousemove and `hideTip` on mouseleave. Rows:
  `Build` (hash + `· modified tree`) and `Commit` (the subject).
- CSS: `#brand small.hash { cursor:help; }` + an `--accent` hover colour.

### Tests

- `internal/buildinfo/buildinfo_test.go` — a `stamp()` helper swaps the link-time
  vars and calls `resolve()` directly (`Get`'s `sync.Once` has usually already
  fired), restoring via `t.Cleanup`. Covers: subject round-trip through the
  characters that motivated base64 (`don't "break" on $vars & <angles>`),
  first-line-only, undecodable base64 degrading to no subject, and the unstamped
  fallback asserted by *shape* (empty or 7 chars) since VCS stamping depends on
  how the test binary was built.
- `cmd/catway/page_test.go` — `TestRenderPageBuildScript` asserts both branches
  (badge present with a non-empty hash and before `</head>`; nothing injected
  when the build is unknown), and `TestBuildScriptEscapesSubject` for the
  `</script>` case.

## Verification

- `make check` — exit 0, 37 packages including the ghostty-tagged race tests.
- `make -n binaries` — confirmed the `-X` pair expands correctly into all three
  build lines.
- `go test -ldflags "<the STAMP>" ./cmd/catway/` — drives the *stamped* branch of
  `TestRenderPageBuildScript`, which the unstamped `make check` run cannot reach.
  A throwaway test printed the real injection:
  ```html
  <script id="cats-build">window.__catsBuild={"hash":"e251787","subject":"feat(ui): name the workspace in pane references outside the pane list","dirty":false};</script>
  ```
- `go version -m` on a freshly built catway — `vcs.revision=e2517874…`,
  `vcs.modified=true`, proving the unstamped fallback and the dirty flag.
- `node --check` on the page's extracted inline script — parses clean.
- Not visually confirmed in a running app.

## Left to the user

`/Applications/Cats.app` carries the pre-change catway, so **no badge appears at
all until `make macapp` (or `make local`) plus a restart** — which is itself the
stale-install signal the badge exists to give.

## Notes / possible follow-ups

- The badge is catway-only. `internal/buildinfo` is deliberately shared, so
  `catctl --version` or a cathost startup log line are now one call away.
- `scripts/build-macapp.sh` builds `cmd/catapp` with its own
  `-ldflags "-X main.defaultMode=…"` and was left alone — catapp is the launcher
  and serves no page. If a bundle should ever report *its own* build, that's the
  third place to add `$(STAMP)`.
- `showTip` is now generic; the statusbar spans and tab close buttons still use
  native `title=` attributes and could move over if richer hints are ever wanted.
