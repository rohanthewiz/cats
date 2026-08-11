# Session: every agent said idle, because the spinner changed shape

- **Session ID:** `62f8950b-d4e5-4abf-9a40-bad2b2971b81`
- **Date:** 2026-08-11
- **Branch:** main (`ddc1dde` → this commit)
- **Repos:** `cats`

A one-line report with a screenshot attached:

> I seem to have lost agent status. Even though agents were in various states.

The sidebar's AGENTS panel showed two claude rows, both `idle`, one stale by two
hours. Nothing in the UI was broken. Detection had quietly stopped producing
`working` at all, and the reason it went unnoticed for so long is that the
failure mode of the rules engine is *to report idle*.

## Finding it

The decisive move was to stop reading code and ask the running daemon, mid-turn,
what it thought of the pane doing the asking:

```
$ bin/catctl panes
277 'claude' idle  '◑ Investigate lost agent status'   ← this session, actively working
265 'claude' idle  '✳ Fix API authentication error…'
276 'claude' idle  '✳ Reorganize todo app UI heading…'
```

Pane 277 was working and reporting idle, with the evidence sitting in its own
title. Claude's *only* `working` signal in the manifest is the OSC title
spinner:

```toml
[[rules]]
id = "osc_title_working"
region = "osc_title"
regex = ['^[\x{2800}-\x{28FF}] ']    # braille ⠋⠙⠹…
```

Claude Code has since moved its title spinner to the half-filled circles
`◐◓◑◒` (U+25D0–25D3). The rule stopped matching, no other rule claims `working`,
and `Detect` fell through to its "known agent, no rule matched" branch — which
returns `idle`. The `✳` idle glyph still matched, which is exactly why the
symptom looked like *lost* status rather than *broken* status: idle was right,
and only idle was ever reported.

That fallback is the design decision worth remembering. A rules engine whose
no-match answer is a *valid state* cannot signal its own obsolescence. When an
agent's vocabulary drifts, detection does not fail loudly — it degrades to a
plausible lie.

## The second bug, which would have eaten the first fix

Detection was not using the manifest in this repo at all.
`~/.local/state/cats/agent-detection/remote/claude.toml` (v2026.08.04.1, fetched
from the herdr.dev catalog) **wholly replaced** the bundled `claude.json` —
unconditionally, with no version comparison. `loadManifests` took any overlay
that parsed.

So the obvious fix — correct the regex in `manifests/claude.json`, rebuild —
would have changed nothing, forever, with no error anywhere. The overlay is
meant to be a hotfix channel between releases; as written it was a permanent
override that only the remote catalog could ever lift.

## The fix

**`manifests/claude.json`**

- Working regex broadened to
  `[\x{2800}-\x{28FF}\x{25D0}-\x{25D3}\x{25F4}-\x{25F7}]` — braille retained for
  older builds, half circles `◐◓◑◒` for current, quadrant circles `◴◵◶◷` added
  because they also appear in the CLI binary and are the other conventional
  circle spinner set.
- Version bumped `2026.06.10.3` → `2026.08.11.1` so it outranks the cached
  overlay.
- Ported the two rules that existed **only** in the overlay — `btw_overlay_working`
  and the `enter to confirm` variant of `live_blocked_form` — so promoting the
  bundled manifest does not silently regress them. This is the trap in making
  the bundled side win: the overlay had been ahead for months.

**`loadManifests`**

An overlay now replaces the bundled manifest only when its version is ≥ the
bundled one; otherwise it is skipped with a log naming both versions. Either
channel can lead. Bundled manifests with an unparseable version stay out of the
comparison, preserving the old behaviour for them.

**Tests** — both spinner glyph families, and version precedence in each
direction (older overlay ignored, newer overlay wins), using a marker rule
absent from the bundled codex manifest so "which one is in force" is directly
observable. `go test ./...` passes.

## Files

| path | state |
|---|---|
| `internal/detect/manifests/claude.json` | working regex broadened, version bumped, two overlay-only rules ported in |
| `internal/detect/manifest.go` | `loadManifests` compares versions before letting an overlay win |
| `internal/detect/manifest_test.go` | glyph cases + overlay precedence both ways |

## Open, and deliberately not guessed

**`osc_title_working` sits at priority 1100, above the blocked rules (980/850).**
If Claude Code keeps spinning the title while a permission prompt is up, a
blocked pane will now report `working`. That ordering came from upstream cats
and was **unreachable for claude** the whole time the braille regex was dead —
this fix makes it live for the first time. It wants checking against a real
permission prompt, not reasoning about.

**Nothing takes effect until cathost restarts.** Manifests are embedded in the
binary; the running daemon was 4.5h old and holding every pane. Worth noting
what this session did *not* find: `/Applications/Cats.app/Contents/MacOS/{cathost,catway}`
hashed byte-identical to `bin/`, so for once this was not a stale bundle.

## What I would tell the next session

- **Ask the running system.** `catctl panes` on the live daemon, with this
  session's own pane as the specimen, answered in one call what the manifest
  source could only have suggested. The pane doing the debugging is a free
  test case that is guaranteed to be in the `working` state.
- **A silent default is a silent failure.** Anywhere detection falls back to a
  real state rather than an unknown one, drift in the thing being detected is
  invisible. Agent vendors change their spinners; nothing here notices.
- **Before promoting a bundled artefact over a cached remote one, diff them.**
  The overlay was ahead by two rules. Winning the version comparison would have
  quietly thrown those away.
