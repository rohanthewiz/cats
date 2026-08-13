# Session: ⌘ chords reach an editor pane, and only an editor pane

- Session id: `ed2482f7-91e8-4d6d-a0aa-1600345c3302`
- Date: 2026-08-13
- Branch: `main`, commit `77285f9` (+ this doc)
- Companion work in ced (`~/projs/go/ced`, commits `684c95d`, `4d10f88`):
  the editor side of this — its ⌘ accelerator table, plan Phase 5.2. This
  change is what lets those chords arrive.
- Downstream: `cats-mobile fdd7b1e` re-pins its generated Dart wire layer
  at this commit (a protocol field was added, so `tool/regen.sh` had to
  run; `dart test` green, 72).

## What was asked

ced's Phase 5.2 (a ⌘ accelerator table) found that **cats forwards no
Command chord but ⌘C and ⌘Z**, so the whole layer was dark inside cats.
Asked to make the cats-side change.

## The problem, exactly

`onKey`, one line:

```js
if (e.metaKey && !e.ctrlKey && e.code !== "KeyC" && e.code !== "KeyZ") return;  // leave other Cmd shortcuts to the browser
```

Everything below that line already worked: `mods()` sends meta as bit 8,
`browserproto`/`inputenc` map it to ghostty's `ModSuper`, and the encoder
emits `\x1b[<cp>;9u` for a kitty-protocol pane (pinned by
`encoder_ghostty_test.go`) and nothing at all for a legacy one. The gate
was the only thing in the way.

## What landed

```
internal/browserproto/down.go     + test   PaneModes.Kitty (omitempty)
internal/browserproto/frame.go            ModesFrom carries the flags
cmd/catway/catway.go                      both pane_modes emit sites
cmd/catway/web/index.html                 CMD_TO_PANE + cmdGoesToPane + help row
cmd/catgen-dart/testdata/golden           regenerated (new field)
ai_docs/phase-c-ws9-protocol.md           §3 pane_modes documents kitty
```

`make test` and `make race-ghostty` green.

### The rule: forward by CHORD, but only to a pane that speaks kitty

Two decisions, and the second is the one that keeps this from being a
regression.

**Which chords.** A curated set, matched on `e.code` so a non-QWERTY
layout still gets the physical key (the rule ⌘K/⌘B already follow):

```js
const CMD_TO_PANE = new Set(["KeyS", "KeyP", "KeyF", "KeyD", "KeyG", "Slash"]);
```

Chosen by **what the browser loses**, not by what the pane wants. ⌘W ⌘T
⌘L ⌘R ⌘N ⌘Q are the window's own and stay with it — swallowing "close the
tab" to give a pane a shortcut is a bad trade, and a user who can't close
a tab blames cats, correctly. The six above cost a page that draws its
panes into a **canvas** almost nothing: ⌘F's find bar cannot see canvas
text, ⌘P would print a screenshot of a terminal, ⌘S saves this page's
HTML, ⌘D bookmarks the only URL there is. On the other side of the wire
they are save, find, go-to-line, comment and duplicate-line.

One real cost, noted in the source: `e.code` ignores Shift, so ⌘P takes
⌘⇧P with it — Firefox's private-window chord — while a kitty pane has
focus. Shift is deliberately not part of the key: the pane receives the
modifiers and decides, so ⌘F and ⌘⇧F are one entry here and two chords
there.

**Which panes.** Only one that turned the kitty keyboard protocol ON.
A legacy pane *cannot receive* a super chord — the encoder emits no bytes
for one — so forwarding there would eat the user's browser shortcut and
send nothing in its place. That is strictly worse than today, and it is
the trap this change had to avoid.

The browser could not answer that question: `pane_modes` mirrored only
`{mouse, alt_screen}`. So `PaneModes` gains `kitty` — the raw flags, not
a bool, because bit 2 (report-event-types) already decides elsewhere
whether key releases are worth sending. `omitempty` keeps the legacy case
off the wire entirely, which is also exactly what an old client sees:
absent → 0 → nothing forwarded → today's behavior. That is the
back-compat story in one field tag.

So: **an editor that asked for the protocol gets its accelerator; a shell
keeps the browser's.** The help overlay says so in a row of its own,
because "⌘S stopped opening the save dialog" needs somewhere to be
explained.

## Verification

- `make test`, `make race-ghostty` green; `node --check` on the page's
  script.
- The shipped `cmdGoesToPane` was **extracted from index.html and run in
  node** against a stub pane map: a kitty pane takes ⌘S/⌘⇧P/⌘/, the same
  pane does NOT take ⌘W/⌘T/⌘R, a legacy pane takes none of them, and an
  unknown or absent focus forwards nothing. (Kept as a scratch harness —
  the repo has no JS test rig, and adding one for nine lines is not the
  trade.)
- `TestModesFromCarriesKittyFlags` pins both halves of the wire claim:
  flags survive `ModesFrom`, and a legacy pane's message has no `kitty`
  key at all.
- **Not verified live end to end.** The running catway is the old binary
  and restarting it would kill every pane in this session, including the
  one this work happened in. What is unverified is the last hop — a real
  ⌘S in a browser reaching a real editor pane. Everything it depends on
  is either tested or read: tcell asks for the protocol at startup
  (`CSI > 1 u`), the emulator mirrors the flags through `pane_modes`, and
  the encoder's super output is pinned by cats' own test.

## For the mac app

Probably free, and worth checking rather than assuming. Cocoa resolves ⌘
**menu key equivalents** before the WebView — but the app's menus claim
only ⌘H ⌥⌘H ⌘Q, Edit's ⌘Z ⇧⌘Z ⌘X ⌘C ⌘V ⌘A, and View's ⌘+ ⌘= ⌘- ⌘0.
**None of `CMD_TO_PANE` collides**, so those chords should fall through
the responder chain to the WebView and hit this same handler. If that
holds, the "⌘ passthrough needs native menu routing" ask shrinks to "only
for chords that collide with a menu item" — which today is none of them.

## Next

- ced is already armed for this (its table fires at Tier 1), so the two
  halves meet with no further ced change.
- The obvious follow-up if this feels good: ⌘E, once ced grows the
  recent-files picker it would open.
- Two hand-checks worth doing after the next catway restart, both listed
  above: the chords end to end in a browser over a ced pane, and whether
  the mac app got this for free.
