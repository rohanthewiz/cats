# Session: the ⌘ layer stopped being a claim and became a measurement

- Session id: `73f924c0-9c58-489f-bd7c-397ef4e4186e`
- Date: 2026-08-14
- Branch: `main`, cats `b5e5b04` → `cb8e518` → `cdb7e5c`
- Companion work in ced (`~/projs/go/ced`): `9013d5c`, `5dfa5d9`, `f6807ba`
- Predecessor: `2026-0813-1304-cmd-chords-reach-editor-panes.md`, whose two
  "hand-check after the next restart" items are both closed here.

## What this session actually was

It began as "let's do ⌘E" and found ⌘E already shipped. Everything after
that is the same small piece of knowledge being corrected twice, in five
files across two repos, as the user supplied facts no amount of reading
could have produced. **No behavior changed anywhere in this session.**
Every commit is a comment or a doc, and that is the point: the code was
right and every note around it was wrong.

## Round 0 — ⌘E was already done

`ed4962c` (2026-08-13 16:17) had added `KeyE` to `CMD_TO_PANE` a few hours
after the allowlist landed. What it missed was the help overlay: the row
that exists so a user can find out why ⌘S stopped opening the save dialog
listed every forwarded chord **except** ⌘E — the one whose browser meaning
("use selection for find") a user is least likely to name unaided, and so
the one most likely to read as cats having simply eaten a key.

→ cats `b5e5b04`, one array entry, ordered to mirror `CMD_TO_PANE` itself.

## Round 1 — three stale notes in ced

⌘E was bound in ced *after* cats' allowlist had closed, so every note
written around it recorded the same true-at-the-time fact: live in
kitty/Ghostty/WezTerm, dead in browser-cats. cats fixed that the same
afternoon and none of the notes caught up.

```
metakeys.go:68    header's allowlist roster missing ⌘E outright
metakeys.go:75    ⌘E filed under "what is still the HOST's to decide",
                  next to ⌘W ⌘T ⌘L — chords that really are still the browser's
metakeys.go:146   the row's own comment, the one that was asked about
cats-native-plan.md:118
                  §2 said "deliberately NOT on the allowlist yet" while §5's
                  follow-up note already recorded it shipped — the doc
                  contradicted itself
```

→ ced `9013d5c`. Also dropped the "a day after" framing rather than
propagating it: cats `77285f9` 13:05, ced `374e0d0` 15:17, cats `ed4962c`
16:17 are all one afternoon. The commit subject that says "a day" is
history and was left alone.

## Round 2 — the mac app is free, and Chrome is not

User-supplied, and neither half was derivable from the source:

- **The mac app passes the ⌘ layer through.** Predicted in the last
  session's "For the mac app" section and now confirmed. Cocoa resolves ⌘
  *menu key equivalents* before the WebView, catapp's menus collide with
  none of `CMD_TO_PANE`, so the chords fall through the responder chain
  and hit the same handler the browser front end uses. **The plan's §5
  upstream ask — "⌘ passthrough needs native menu routing" — is
  withdrawn**, shrinking to "only for a chord that collides with a menu
  item", which today is none of them.
- **⌘E never arrives in Chrome**, allowlist or no allowlist.

Verified first that this was not a stale bundle — the standing hazard in
this project. It was not:

```
$ strings -a /Applications/Cats.app/Contents/MacOS/catway | grep CMD_TO_PANE
const CMD_TO_PANE = new Set(["KeyS","KeyP","KeyE","KeyF","KeyD","KeyG","Slash"])
```

Binary built 19:42 on the 13th, running since 19:44 — well after
`ed4962c` at 16:17. `index.html` is `go:embed`ed (`cmd/catway/main.go:83`),
so the running page genuinely does forward ⌘E and Chrome is taking it
before `keydown` is dispatched. cats cannot `preventDefault` an event it
is never offered.

**This inverted the reasoning in `ed4962c`.** That commit picked ⌘E by
*what the browser loses* — a find-selection asked of a canvas that has no
text selection, so nothing measurable. True, and beside the point: the
question that decides delivery is what the host will **hand over**.

→ cats `cb8e518`, ced `5dfa5d9`. Help overlay grew a `†` on ⌘E (the
section already had a `*` footnote convention for the paste row, so a
second marker rather than overloading the first).

## Round 3 — the correction to the correction

Reasoning from ⌘E alone, the diagnosis was "a native menu binding blocks
delivery", which implicates ⌘S ⌘P ⌘F ⌘D ⌘G — all Chrome menu items (Save
Page As, Print, Find, Bookmark, Find Next). Flagged as worth six
keystrokes. The user pressed them: **all five arrive, in Chrome and in the
mac app.**

So the diagnosis was wrong and the docs written in Round 2 were
over-broad — "the rest of the set is unverified per browser" reads as five
more ⌘E's waiting to be found. Chrome dispatches the keydown to the page
*before* resolving its menu and honours `preventDefault`; that is the same
courtesy that lets any web editor claim ⌘S. **⌘E is an exception to that
rule, not an instance of it.**

→ cats `cdb7e5c`, ced `f6807ba`.

### Where the layer stands, measured

| host | status |
|---|---|
| kitty, Ghostty, WezTerm | full set |
| cats mac app | full set — confirmed by hand |
| Chrome | full set **but ⌘E**; `Esc B` is the way in there |

`⌘/` is the only entry with no menu binding in any host and was never in
doubt.

### The rule that survived

The set was curated by *what the browser loses*, which is a **proxy** for
the question that decides delivery — *will the host hand it over*. The
proxy held for every chord but one. Press a new entry in a real host
rather than reasoning it in: the failure is benign (a chord the host keeps
is inert in that host, never a chord that does the wrong thing) but
**silent**, and silent is how ⌘E sat wrong for a day.

## Also answered, no code

- **ced's command palette**: `Esc k`, `⌘⇧P`, or ≡ → first row. It fuzzy-
  searches actions *and* project files in one list (`palette.go`), so it
  doubles as the file finder; `⌘P` / `Esc p` is the files-only version.
- **Not the same as a help overlay.** Three distinct surfaces: ced's
  palette *runs* things; ced's **which-key** overlay (`whichkey.go` — tap
  `Esc`, hesitate ~350ms) documents the leader table and is passive and
  clickable; cats' **help overlay** (`⌘K → keyboard shortcuts`) documents
  cats' own keys and which ⌘ chords reach a pane, and knows nothing about
  ced's actions.

## Verification

- cats: `make test` green (29 packages, no failures); `node --check` on the
  script extracted from `index.html` after every edit.
- ced: `go build ./...`, `gofmt -l` clean, `go test -run Meta` green.
- gopls reports every ced symbol undefined while the LSP workspace is
  rooted at cats. Noise, not findings — the module builds.

## Next

- Nothing outstanding on the ⌘ layer. Both of the predecessor's hand-checks
  are closed and its upstream ask is withdrawn.
- Untested by anyone: Safari and Firefox. Only matters if either becomes a
  host worth supporting; the mac app and Chrome are the ones in use.
- If a chord is ever added to `CMD_TO_PANE`, the standing rule above is now
  written next to the set itself.
