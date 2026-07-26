# Session: agent-detection manifest cache + Rust unicode escape translation

Session ID: `8dadee3b-35f9-4b84-9699-0b76c2bb7922`
Date: 2026-07-26

## The report

cathost startup log showed a good cached manifest being thrown away every run:

```
cathost listening on /tmp/cats-cathost.sock (persistent, protocol v2)
detect: ignoring remote manifest for hermes: error parsing regexp: invalid escape sequence: `\u`
detect: skipping unknown remote manifest agent "devin"
detect: skipping unknown remote manifest agent "maki"
detect: agent manifests up to date
```

Ask: make sure the manifests we have that are good are cached locally, and
don't check them in.

## Root cause — `translatePattern` only knew one of four escape spellings

`internal/detect/manifest.go`. The remote hermes manifest fetched, validated,
and committed to the cache fine, but its patterns use Rust's **braced**
codepoint escape:

```toml
regex = ['^⚠[\u{fe0e}\u{fe0f}]?(?:\s|$)']
```

The translator was a single `regexp.ReplaceAllString` for `\uXXXX` (exactly 4
hex digits, no braces). `\u{fe0e}` didn't match, reached Go's RE2 verbatim, and
`compileGate` failed — so `loadManifests` logged "ignoring remote manifest" and
fell back to the older *embedded* manifest. Cached, valid, and unused.

The failure mode is the dangerous kind: detection keeps working, just on stale
rules, with one log line at startup as the only signal.

## Fix

Replaced the blind regex substitution with an escape-aware scanner covering all
four Rust codepoint spellings:

| Rust | Go |
|---|---|
| `\uXXXX` (4 hex) | `\x{XXXX}` |
| `\u{H…}` | `\x{H…}` |
| `\UXXXXXXXX` (8 hex) | `\x{XXXXXXXX}` |
| `\U{H…}` | `\x{H…}` |

plus the pre-existing `\p{Alphabetic}` → `\p{L}`. Two design points worth
keeping in mind:

- **Escape-aware walk, not a blind replace.** `\\` is consumed as a pair, so
  `\\u2026` (a literal backslash then the letter u) is left alone instead of
  being mistaken for a codepoint escape. A regex-based replace can't see that
  distinction.
- **Malformed payloads pass through untouched** (`\uZZZZ`, `\u{}`) rather than
  being half-rewritten — a bad manifest should fail loudly at compile, not get
  silently corrupted into something that compiles to the wrong thing.

New helpers: `unicodeEscapeDigits` (braced or fixed-width payload reader) and
`isHexRun`. `reRustUnicodeEscape` is gone.

## Tests (`internal/detect/update_test.go`)

- `TestTranslatePattern` — all four forms, `\p{Alphabetic}`, and the
  passthrough cases (`\\u2026`, `\uZZZZ`, `\u{}`, ordinary escapes).
- `TestLoadManifestsAdoptsRustUnicodeEscapes` — writes a braced-escape overlay
  and asserts by **rule count** that it replaced the bundled manifest. A
  presence check (`m["codex"] != nil`) would have passed under the old bug,
  since the fallback also yields a non-nil manifest. That's the whole point of
  the test.

`go build ./...`, `go vet ./internal/detect/`, `go test ./...` all pass.

## Verified against the live cache

A throwaway test walked the real cache dir and parsed + compiled every file
(deleted afterward — not committed). All 17 load and are adopted over their
embedded counterparts:

```
agy amp claude cline codex copilot cursor droid gemini
grok hermes kilo kimi kiro opencode pi qodercli   → all loaded=true
```

hermes is `v2026.07.24.1`, 9 rules — previously discarded on every startup.

## Cache location — nothing is checked in

`persist.DefaultDir()` → `$XDG_STATE_HOME/cats`, so the overlay lives at:

```
~/.local/state/cats/agent-detection/remote/<agent>.toml
~/.local/state/cats/agent-detection/status.json
```

Outside the repo entirely — git never sees it, so no `.gitignore` entry is
warranted (an ignore rule for a path that can't appear is just noise).
Confirmed no manifest cache exists anywhere under the repo. The tracked
`internal/detect/manifests/*.json` are the **embedded fallback sources**, not
cache — those stay committed.

## Left alone (deliberate)

`devin` and `maki` are skipped because this build has no embedded manifest for
them. `loadManifests` keys the overlay off the embedded set
(`for id := range m`), so caching an unknown agent's manifest would be inert —
nothing would ever read it. Adopting manifests for unknown agents would be a
real design change (the store would need to be catalog-driven rather than
embed-driven); flagged to the user, not done.

## Files touched

- `internal/detect/manifest.go` — `translatePattern` rewrite + two helpers.
- `internal/detect/update_test.go` — two new tests.
