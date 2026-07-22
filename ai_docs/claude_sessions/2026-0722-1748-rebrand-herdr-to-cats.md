# rebrand-herdr-to-cats

**Date:** 2026-0722-1748
**Session ID:** `2e6e649a-03f8-429d-ae15-84e32e38655c`
**Project:** `~/projs/go/herdr-web` (now targeting `github.com/rohanthewiz/cats`)

---

## Scope

Wholesale rebrand **herdr-web → Cats**, targeting a new blank repo the user
supplied (`github.com/rohanthewiz/cats.git`). Executable names chosen by the
user; runtime identifiers fully rebranded; the prompt-style Mac icon preserved.
The recent plan doc was updated, and a standing TODO to replicate `herdr.dev`
was added.

## Decisions (from clarifying Q&A)

The user pre-empted my 4-question ask and answered directly:

| Question | Choice |
|---|---|
| Binaries | `gateway`→`catway`, `termhost`→`cathost`, `herdrctl`→`catctl` (mac launcher `herdrapp`→`catapp` inferred to match the `cat*` pattern) |
| `herdr.dev` external URLs | **Keep** — still the live agent-detection manifest catalog + future relay base |
| Runtime IDs (env vars, dirs, sockets) | **Full rebrand** to `CATS_*` / `cats` |
| Docs depth | Plan doc + core docs (README, config.example.yaml, Makefile); **leave historical session notes as-is** |

Two mid-turn asks folded in: (1) update the recent plan doc, (2) add a TODO to
replicate `herdr.dev` under Cats/`cats.dev` in the future.

## Name mapping applied

- **Module:** `github.com/rohanthewiz/herdr-web` → `github.com/rohanthewiz/cats`
- **Binaries:** `catway` (server), `cathost` (terminal daemon), `catctl` (CLI)
- **Mac launcher:** `cmd/herdrapp`→`cmd/catapp`; products `Herdr.app`→`Cats.app`,
  `Herdr Client.app`→`Cats Client.app`; bundle IDs `dev.cats.app`/`dev.cats.client`
- **Brand token** herdr/Herdr/HERDR → cats/Cats/CATS: env `CATS_*`,
  `~/.config/cats`, `~/.local/state/cats`, `~/Library/Application Support/cats`,
  sockets `/tmp/cats-*.sock`, JS `__catsKeys`/`catsEntry`, agent hooks
  `cats-agent-state.*`
- **Icon:** design preserved — `AppIcon.icns` byte-identical; only the SVG
  source renamed `scripts/icon/herdr-icon.svg`→`cats-icon.svg`

## Deliberately kept

- **`herdr.dev`** external host (fetched by `internal/detect/update.go`
  `defaultCatalogURL = https://herdr.dev/agent-detection/index.toml`, and the
  `relay.herdr.dev` relay base in the plan §2c). Negative-lookahead in the
  replacement (`herdr(?!\.dev)`) protected it while still rebranding
  `dev.herdr.app`→`dev.cats.app`.
- **`internal/gwauth` / `internal/gwtls`** — `gw` is an opaque abbreviation, not
  a literal `gateway`/`herdr` token; renaming unrequested internal packages was
  judged over-reach. (Flagged to user; `gw`→`cw` available on request.)

## How it was done

1. `git mv` renames: 4 `cmd/` dirs, `cmd/catway/gateway.go`→`catway.go`, 17
   `herdr-agent-state.*` hook assets → `cats-agent-state.*`, the icon SVG.
2. A reviewable perl script (`scratchpad/rebrand.sh`) — ordered, case-sensitive
   substitutions, longest-token-first, `herdr.dev` protected — over a scoped
   file set (`cmd`, `internal`, `scripts` text, root build files,
   `.github/workflows`, the one plan doc). 192 files processed.
3. Pre-flight safety check: confirmed no third-party dependency import path
   contains `gateway`/`termhost`/`herdr`, so the global replace can't corrupt
   imports.
4. `gofmt -w` fixed alignment shifts from token-length changes (catway is one
   char shorter than gateway, etc.).
5. Plan doc gained a **Rebrand note** + **replicate-herdr.dev TODO** at the top.

## Verification (all green)

- `gofmt` clean; `go vet` untagged **and** `-tags ghostty`.
- Full test suite passes — untagged (`go test ./...`) and ghostty-tagged.
- `catway`/`cathost`/`catctl` build (`-tags ghostty`, static libghostty-vt).
- **Both** app variants build: `make macapp` → `dist/Cats.app` (self-contained,
  bundles all four binaries), `make macapp-client` → `dist/Cats Client.app`.
  cgo export `catappCleanup` + `CatsMenuTarget` consistent across `.go`/`.m`.
- `Info.plist` verified: CFBundleName `Cats`, id `dev.cats.app`, exec `catapp`,
  icon `AppIcon`.
- Residual scan: the only remaining herdr/gateway/termhost strings are the
  intentional old→new mapping lines in the plan-doc rebrand note.

## Repo / git

- `git remote set-url origin https://github.com/rohanthewiz/cats.git`.
- Commit `d8897c1` (`rebrand: herdr-web → Cats …`) — 147 paths (58 renames,
  88 content edits). `git push -u origin main` → new `main` on `cats.git`.

## Memory written

Project memory `rebrand-herdr-to-cats` (+ `MEMORY.md` index) so a future
session knows: historical `claude_sessions/` notes (loaded by `/sess-load`)
still use the **pre-rebrand** names — translate old→new when acting on them.

## Open items

- **TODO:** stand up a `cats.dev` equivalent of the agent-detection manifest
  catalog, then flip `defaultCatalogURL` + `relay.herdr.dev` over.
- Local checkout dir was `~/projs/go/herdr-web` at the time this was written —
  ~~unchanged~~ **superseded, see Correction below.** Rebuild on the Linux
  mini-PC after `git pull`.
- Optional: `internal/gwauth`/`gwtls` → `cwauth`/`cwtls` if full consistency
  wanted.

## Correction (2026-07-22, appended)

The "checkout dir still `~/projs/go/herdr-web`" open item above is **out of
date.** Later the same session the local checkout was renamed to match the repo:

- `~/projs/go/herdr-web` → **`~/projs/go/cats`** (our checkout — start future
  Claude Code sessions from here).
- Name collision handled first: `~/projs/go/cats` already held an unrelated,
  non-git "cat shop" Go project, so it was moved aside to
  **`~/projs/go/catshop`** (intact; still not under git — its only safety net).
- Verified from the new path: `origin` = `github.com/rohanthewiz/cats.git`,
  module `github.com/rohanthewiz/cats`, working tree clean, `go build ./...` OK.
- Claude Code keys memory by directory path, so the project memory was copied to
  the new key `~/.claude/projects/-Users-ro-projs-go-cats/memory/`.
- This was a local filesystem rename only — not tracked by git, nothing to push.
