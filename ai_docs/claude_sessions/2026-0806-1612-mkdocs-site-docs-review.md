# Session: mkdocs site docs review — admonitions, push config, orphan pages, renderer config

Session id: `fd2f6c88-c4e7-4ade-a465-ff51cf14edb3`

## Task

"Are there any improvements needed to the site (mkdocs) docs?" — review, then
apply all four findings.

## Method

Reviewed `docs/` against **both** renderers and against the code, rather than
reading prose. Three checks did the real work:

1. **Link/anchor validator** (throwaway python, not kept): walks every `.md`,
   slugifies headings, resolves every relative link + `#anchor`, and reports
   orphans (files no page links to). All 54 links resolved before and after.
2. **Rendered-output diff**: ran the docs through gkdocs (`~/projs/go/gkdocs`,
   `MKDOCS_CONFIG=... PORT=... go run ./cmd/gkdocs`) *and* plain `mkdocs build`,
   then grepped the HTML. This is what caught the admonition bug — the source
   looks fine, only the output shows it.
3. **Code-vs-docs drift**: extracted `yaml:"..."` tags from `internal/config`,
   `flag.*` names per `cmd/*/main.go`, and `CATS_[A-Z_]+` from all Go, then
   diffed each against the reference pages.

## Findings and fixes (all applied)

### 1. Admonitions rendered as literal text — 8 blocks

`!!! note` / `!!! warning` are a python-markdown extension. gkdocs **does not
implement them** (its own session notes list admonitions under "Not
implemented"), and `mkdocs.yml` declared no `markdown_extensions`, so neither
renderer handled them. Output was a literal paragraph:

```html
<p>!!! note &quot;The web UI is embedded&quot; ...</p>
```

The affected blocks were mostly the security warnings (relay trust model, no
`X-Forwarded-*` trust ×2, password not in config file, install hooks on the
cathost host). Rewrote all 8 as blockquotes with a bold lead
(`> **Warning — relay trust model**`), which renders correctly in gkdocs,
mkdocs, and GitHub's markdown view alike. Verified 0 literal `!!!` remain in
rendered HTML.

### 2. Push notifications were undocumented site-wide

`push:` shipped in `config.example.yaml` and `internal/config` but appeared
nowhere in `docs/`. Added:

- **`reference/configuration.md`** — new `## push` section: YAML shape,
  key/flag/default table for all six keys, accepted ntfy priorities, and the
  reasoning behind the two deliberate defaults (attention-only kinds; capped at
  `high` not `urgent` because urgent bypasses Android DND). The capability
  warning about the topic URL + `CATS_PUSH_TOKEN` is now prose, not a YAML
  comment.
- `CATS_PUSH_TOKEN` row in the env-var table.
- **`reference/cli.md`** — `--push-url` in the `catway` synopsis and flag table,
  noting passing it is itself the opt-in and `""` forces the bridge off.
- **`docs/index.md`** — feature-tour bullet.

Also fixed a stale claim at `configuration.md:26`: it said `server.*` were "the
only settings with flags", but `--persist`/`--state-dir` reach `persistence.*`
and `--push-url` reaches `push.url`. That line was wrong before push existed.

Everything else diffed clean — all `catway`/`cathost` flags, all other config
keys, `catgen-dart`.

### 3. `mkdocs.yml` was bare — `mkdocs build` produced a broken site

No `theme`, no `markdown_extensions`, no `plugins`. `mkdocs build` *succeeded
silently* while emitting all 72 mermaid diagrams (24 of 26 pages) as raw
`<code class="language-mermaid">` blobs.

**The risk checked first:** Material's mermaid support needs a
`!!python/name:pymdownx.superfences.fence_code_format` YAML tag, which yaml.v3
normally errors on — it could have broken gkdocs. Read
`gkdocs/internal/mkdocs/config.go`: it strips `!!python/` tags with a regex
before parsing and decodes non-strictly. So a full Material config is inert
there, and this was not a tradeoff.

Added `theme: material` (light/dark palette toggle), `plugins: [search]`,
`repo_url`/`edit_uri`, and `markdown_extensions` (admonition, attr_list,
def_list, md_in_html, tables, toc permalinks, pymdownx.details/highlight/
inlinehilite, superfences with the mermaid custom fence). A comment in the file
records the constraint: **content must render under both renderers, never one or
the other.**

Support files, because the new README instructions create the need:
`requirements-docs.txt` (pinned; header explains the gkdocs path needs none of
it) and `.gitignore` entries for `/site/` + `.venv/`.

### 4. `reference/troubleshooting.md` was orphaned

Reachable only from the nav — no page linked it. Linked from
`getting-started.md` (new "When something does not work" close),
`subsystems/auth-and-tls.md` (deep-linked to the two symptoms that subsystem
produces), and `index.md`. Same defect applied to the home page never linking
Getting started or Concepts — fixed too.

## Verification

| Check | Result |
|---|---|
| `mkdocs build --strict` | passes, 0 warnings |
| mermaid under mkdocs | 72 `<pre class="mermaid">` — exactly matching 72 source fences |
| `language-mermaid` raw blocks | 0 remaining (was 72) |
| gkdocs, all 26 nav pages | all HTTP 200 |
| gkdocs mermaid + new anchors | native, unchanged |
| link/anchor validator | 0 broken, troubleshooting no longer orphaned |
| literal `!!!` in rendered HTML | 0 |
| clean venv from `requirements-docs.txt` | installs + builds correctly |

Anchors targeted by the new cross-links (`#push`, `#where-to-look-first`,
`#websocket-connects-then-immediately-fails`,
`#i-have-to-log-in-again-after-every-restart`) were each confirmed present in
the rendered HTML, not just in the source headings.

## 5. Docs build in CI (`88332b5`, added after the four above)

The docs had no CI at all — which is how eight admonitions rendered as literal
text and nobody noticed. New `docs` job in `.github/workflows/ci.yml`: ~30s, no
Go and no Zig, running unconditionally rather than behind a `paths:` filter,
since a page can break from a link in a page that did not change.

**`mkdocs build --strict` alone would not have caught the link rot.** mkdocs
does not validate relative links or `#anchors` by default — it renders a dead
cross-reference without complaint. So `mkdocs.yml` gained a `validation:` block
(`omitted_files`, `absolute_links`, `unrecognized_links`, `anchors` → `warn`,
needs mkdocs >= 1.6), and `--strict` promotes those warnings to errors. Neither
half works alone. Verified by planting a dead link and a dead `#anchor` in
`concepts.md`:

```
WARNING - ... contains a link 'reference/nope.md', but the target is not found ...
WARNING - ... the doc 'reference/cli.md' does not contain an anchor '#no-such-anchor'.
Aborted with 2 warnings in strict mode!      → exit 1
```

**Admonitions need a separate grep guard**, and this is the interesting part:
mkdocs renders `!!!` *correctly*, gkdocs does not. So the strict build is
structurally blind to the exact bug that motivated the job — the check that
guards the real renderer cannot come from the other one. Guard verified both
ways (passes clean, exit 1 on a planted `!!! note`).

## Verification status of the CI job

Locally proven; **not yet confirmed green on GitHub**. No Actions run was
created for any of the three commits this session. That is a GitHub-side
outage, not a bad workflow file — the run for `ef57af9` (the commit *before*
this session) failed the same way:

```
##[error]Failed to resolve action download info. Error: Service Unavailable
```

i.e. the runner could not download `actions/checkout`. `gh workflow list` shows
ci active, the commit is on `origin/main`, and the YAML parses. **When Actions
recovers, confirm the `docs` job goes green** — that is the one open item from
this session.

## Follow-ups

- **Confirm the new `docs` CI job passes** once GitHub Actions recovers (see
  above). Nothing else is pending.
- **Admonitions in gkdocs** remain unimplemented upstream. The blockquote
  rewrite sidesteps it; if gkdocs ever gains `!!!` support the docs need no
  change, and the CI grep guard could then be dropped.
- The mkdocs path is a compatibility fallback, not the primary — gkdocs stays
  the served renderer per the README. The CI job checks the content the two
  share, which is why it is worth running despite mkdocs not being the
  production renderer.

## Commits (all on main, pushed)

| | |
|---|---|
| `054d098` | `docs:` the four content fixes — 12 files, +195/−40 |
| `219b52f` | `docs(session):` this doc |
| `88332b5` | `ci:` docs build + link-rot and admonition guards |
