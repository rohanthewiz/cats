# Session: A Persistent Annotated Flag on a Workspace or an Agent

- **Session ID:** `ec1bdaf7-0e72-4f09-acd9-ab972f1efe5b`
- **Date:** 2026-08-27
- **Branch:** main
- **Repo:** `cats`
- **Landed as:** `534bc11` — *flags: a persistent annotated mark on a workspace or a pane*
- **Then:** `64f6be2` — *flags: flag.list and `catctl flags` — every mark in one listing*

## Request

> Allow me to add a persistent annotated flag to a workspace or listed agent.
> Think of flag as some icon with meaning, like a red follow-up flag to which I
> can add a note

and, after the session was reloaded:

> Do the catctl flags verb

— which is the first of the known limits below, and closes it.

## The two scoping questions

**Icon vocabulary.** Three options were offered — a fixed named set, free emoji,
or both. Answer: **fixed set with a custom-glyph fallback**. That shape is what
the rest of the design falls out of: `kind` is one string holding *either* a
name we know or a glyph the user invented, and every client renders it through
one path (`flags.Glyph` / `flagGlyph`). Named kinds get a colour and a
documented meaning; a custom glyph gets plain foreground, because nothing here
knows what the user meant by 🍕.

The two shapes are kept **disjoint** by `flags.ParseKind`: a bare ASCII word
(letters, digits, `-`, `_`) must be a name we know. Without that rule, a
mistyped `folloup` silently becomes a flag that renders the word "folloup" in
the sidebar — the failure nobody notices until they go looking for their
follow-ups and one is missing.

**Entry points.** All four were asked for and all four shipped: sidebar context
menus, the command palette, `catctl`, and a clickable affordance in the pane
header.

## One decision made without asking

A pane's flag is stored on the **pane**, not on the detected agent, even though
the AGENTS list is where most of them will be set. An agent is not an
addressable thing — it is a process the runtime recognised, it goes away on a
`/clear` or a crash, and it has no identity that survives a restart. The pane
does. So "come back to this agent" is still true after the agent is restarted in
place, and a plain shell can be flagged as a free consequence.

## What shipped

### 1. The vocabulary (`internal/flags/`, new)

`Kind`, `Flag{Kind, Note, AtMs}`, the six-entry table, `ParseKind`, `CleanNote`,
`New`, `Equal`, `Clone`, `Glyph`, `Describe`.

| kind | glyph | means | colour token |
|---|---|---|---|
| `followup` | ⚑ | come back to this | `--flag-followup` → `--err` |
| `question` | ? | waiting on an answer | `--flag-question` → `--warn` |
| `star` | ★ | worth finding again | `--flag-star` |
| `warn` | ⚠ | something is wrong here | `--flag-warn` |
| `done` | ✓ | handled — nothing left to do | `--flag-done` → `--ok` |
| `note` | ✎ | just a note | `--flag-note` → `--muted` |

Six because each needs a distinct *shape* and a distinct *colour* at 12px; two
flags that read alike at that size are one flag with extra steps
(`TestVocabularyIsDistinct` enforces both).

Named `flags` rather than `flag` so it does not shadow stdlib `flag` in `catctl`
and `catway`, both of which import it.

Validation asymmetry, on purpose:

- A **glyph** is *refused* if it holds whitespace or a control character, or runs
  past 8 code points (enough for a ZWJ emoji sequence). It is one visible mark
  or it is nothing, and stripping would hand the user a flag they did not ask
  for.
- A **note** is *sanitized*: line breaks and tabs become spaces, other control
  characters are dropped, runs collapse, and it is capped at 500 runes. It is
  prose someone typed or pasted; a stray newline is a formatting accident, not
  something to fail a command over. It must come out single-line because it is
  drawn in a one-line row.

`AtMs` is Unix milliseconds, not a `time.Time`, for two reasons pointing the
same way: `cmd/catgen-dart` reflects the wire structs into Dart and has no
mapping for `time.Time`, and a persisted integer cannot acquire a timezone or a
format between releases.

`Equal` deliberately compares `AtMs`, so re-flagging with the same kind and note
**is** a change — it is a "still true, as of now", and the timestamp is the only
record of it. Only a genuine no-op (clearing something already unflagged) skips
the broadcast.

### 2. Model + persistence (`internal/workspace/`)

`Workspace.Flag` and `PaneState.Flag`, both `*flags.Flag`, with `SetFlag` /
`Tab.SetFlag`. Carried through `Snapshot` / `Restore` as `flag,omitempty`, so a
session with no flags in it serialises byte-identically to one written before
flags existed. Stored through `Clone` at every boundary: a snapshot is
marshalled after the fact, and a flag edited in between must not rewrite what is
being written out.

`buildWorkspace` in `persist_test.go` now sets both a named kind on the
workspace and a **custom glyph** on a pane, so the round trip covers both halves.

### 3. Session mutations (`internal/app/session.go`)

```go
func (s *Session) SetWorkspaceFlag(id string, f *flags.Flag) (bool, error) // "" = active
func (s *Session) SetPaneFlag(id layout.PaneID, f *flags.Flag) (bool, error)
func (s *Session) PaneFlag(id layout.PaneID) *flags.Flag
```

Both report whether anything changed, the `SetWorkspaceLock` contract.

### 4. The commands (`internal/app/`)

`pane.flag` / `workspace.flag`, params `{pane|id, kind, note}`. `kind: ""`
clears, spelled the same way an empty name clears a custom title. A bad kind
comes back as **bad params**, not a failed effect — it is a malformed request,
and it is refused before anything mutates.

The clock is read in the dispatcher (`time.Now().UnixMilli()`), not in the
Session: the domain model stays clock-free, and a test constructs flags directly.

`FlagInfo{Flag, FlagNote, FlagAtMs}` is a new **embedded** struct on
`app.WorkspaceInfo`, `app.PaneInfo`, `browserproto.WorkspaceInfo`,
`browserproto.PaneRectInfo` and `browserproto.AgentItem`. Flat scalars rather
than a nested object, so a client reads one more optional field beside `locked`
and `host` rather than learning a sub-object; embedded rather than repeated five
times, so the doc comment and the conversion live in one place — and catgen-dart
flattens it onto each class while still offering a `flagInfo` getter.

### 5. A new Backend method (`BroadcastFlags`)

Not `BroadcastLayout`, because a flag lands in one place the layout does not
reach: the **AGENTS rollup**, which is its own message and the only one that
spans every workspace. Not `ApplyModel` either — nothing structural changed, so
there are no PTYs to reconcile and no viewport to recompute.

```go
func (o *orch) BroadcastFlags() {
    o.broadcastLayouts()          // workspace rows + the active tab's pane headers
    o.broadcast(o.agentsMsg())    // the global AGENTS list
    o.saveSoon()                  // durable state
}
```

The sidebar's PANES rows need nothing here: the browser re-queries `pane.list`
whenever a layout arrives.

### 6. CLI (`cmd/catctl/`)

```
flag <pane> <kind> [note...]      unflag <pane>
flag-ws <id> <kind> [note...]     unflag-ws [id]
```

Two verbs over one command each, the `lock-ws`/`unlock-ws` pattern. The id is
**required** on `flag-ws` and optional on `unflag-ws`, which looks asymmetric and
is the only unambiguous spelling: with both optional, `flag-ws followup` and
`flag-ws w2` are the same shape meaning different things. Clearing has one
argument and no such collision.

The kind is *not* validated CLI-side. `flags.ParseKind` on the server is what
refuses a typo, so the CLI and the browser produce byte-identical errors and the
vocabulary is written down once. New `argFlagKind` completion kind offers the six
names with their meanings (static — the server could not tell us anything it does
not already know).

### 7. Front end (`cmd/catway/web/`)

No new JS file — each piece landed with its peers, which is also why the numbered
load order needed no renumbering:

| where | what |
|---|---|
| `07-workspaces.js` | `FLAG_DEFS`, `flagOf`, `flagGlyph/Label/Title`, `flagMark` — beside `lockMark`/`todoMark`, the other shared sidebar marks. Plus the WORKSPACES row. |
| `17-agentlist.js` | the mark on AGENTS rows, and a `contextmenu` handler those rows never had |
| `08-panelist.js` | the mark on PANES rows |
| `09-hovercard.js` | Flag + Note rows — the one surface with room for the note in full |
| `06-chrome.js` | the pane-header chip: glyph **and note inline**, clickable |
| `02-color.js` | the always-present ⚑ toolbar button; `mkBtn` now passes the click event through |
| `27-dialogs.js` | `sendFlag`, `editFlagNote`, `openFlagDialog` |
| `28-ctxmenu.js` | flag targets, `flagMenuItems`, wiring into the pane and workspace menus |
| `31-palette.js` | "flag focused pane…" / "flag workspace…" |
| `css/28-flags.css` | new; the mark, the six colours, the header chip, the menu icon |

`flagMark` is **text**, not SVG — unlike the padlock and the paw print it sits
beside. A custom glyph is a character the user typed and has no SVG to draw, so a
text mark is the one shape both halves of the vocabulary can share.

Three UI decisions worth naming:

- **Picking a kind from the menu is one click and keeps the existing note.** The
  common motion is "mark this, I'll come back"; making that cost a dialog means
  it does not get used. `flag with a note…` is a separate row for when the note
  *is* the point, and it is also where a custom glyph is entered (its field stays
  hidden until the custom choice is picked).
- **The flag is exempt from the locked-workspace dim**, alongside the padlock.
  Setting a workspace aside does not make the reminder pinned to it less true.
  `#ws-list li.ws.locked > :not(.lock-mark):not(.flag-mark)`.
- **`buildCtx` grew an optional `icon`.** The colour is half of what a flag mark
  means, and a menu that teaches only the shape leaves the reader to induct the
  palette from the sidebar. One aligned gutter, one coloured glyph per row;
  every other menu omits it and renders exactly as before.

The vocabulary is written down twice (Go for validation, JS for drawing) because
the browser must render the menu before any flag exists. Two tests in
`web_test.go` fail the build if they drift: `TestWebFlagVocabularyMatchesGo`
parses `FLAG_DEFS` and compares it field by field *and in order*, and
`TestEveryFlagKindHasAColour` checks `28-flags.css` has an `.fk-*` rule per kind
plus the `.fk-custom` fallback.

## What shipped in the follow-on (`64f6be2`)

The first known limit below said the marks were drawn in four lists but never
collected. `catctl flags` collects them.

### 8. The choice that shaped it: a server command, not two client calls

The obvious cheap route was to have `catctl` call `workspace.list` and
`pane.list` and merge. Rejected, for reasons that all point the same way:

- Every ergonomic verb in `catctl` is **one** request. A verb that is a loop has
  to own its runner the way `cp` and `probe` do — which then means its own entry
  in `families`, its own completion case, its own help page, and its own
  invented `--json` shape, because the global `--json` prints *a* response and
  there would be two.
- As a table verb backed by one method, all four of those come **free**:
  `argFlagKind` completion already existed, `printVerbHelp` already renders the
  page, and `--json` is the raw payload exactly as with `panes`.
- Two round trips are two snapshots. Flags change rarely, so the race is small
  — but it is a race that produces a listing describing a state that never
  existed, and there was no reason to buy one.
- `flags.Def.Meaning` was already documented as being "for tooltips and
  `catctl flags`". The verb was anticipated; a §7 query is the shape the rest of
  the vocabulary is in.

The counter-argument — "don't add wire surface" — is the one this repo keeps
making, and it does not apply. That rule was about *serving six compile-time
constants*; this serves **data**, which is what a §7 read query is for. The
browser will not call it (it already holds the whole model, which is how the
AGENTS rollup is built), but `catgen-dart` reflects it onto the phone for free.

### 9. `flag.list` (`internal/app/`)

```go
type FlagListParams struct{ Kind string }                 // "" = every flag
type FlagListResult struct {
    Workspaces []WorkspaceInfo
    Panes      []PaneInfo
}
```

The **flagged subset** of `workspace.list` and `pane.list`: the same row structs,
in those lists' own order, with the same `PaneMeta` merge applied to the panes it
returns. Three decisions worth naming:

- **Two lists, not one merged one, and no third row type.** The fields worth
  showing beside the mark differ by scope — a workspace has a tab count, a pane
  has a handle and an agent — so a merged row is half empty either way. Reusing
  the existing rows means a client that can draw a workspace row already knows
  how to draw this.
- **`ListFlaggedIn` filters the two full listings rather than walking the model
  again.** That costs one pass over rows that are cheap to build, and buys the
  guarantee that a pane described here is described *identically* in `panes`.
  Two builders would be two descriptions to reconcile.
- **An unknown kind is refused, not answered with an empty list.** "Nothing
  matched" and "you misspelled it" render the same way, and that is the one
  wrong answer a listing can give — it looks exactly like a session with nothing
  flagged. `flags.ParseKind` in the dispatcher, so the error is byte-identical
  to the setters'. A custom glyph filters like any other kind, because both
  halves of the vocabulary are one string field.

Both slices are non-nil when empty, so a client loops without a nil check.

### 10. `catctl flags [kind]`

```
w1  ⚠ problem    cats                 2h ago  flaky tests in here
1   ⚑ follow-up  w1:p1 claude · idle  5m ago  waiting on the API review
3   🍕           w2:p1                1d ago  lunch build
```

The one query rendered as a **table** instead of pretty JSON — a glance across
every workspace is the entire reason the verb exists, and JSON is not one.
`--json` still prints the raw payload, so the scripting path is unchanged and the
renderer is free to drop fields.

The first column is deliberately **not the same shape on both kinds of row**: it
is the argument the mutating verbs take — `w1` for `flag-ws`/`unflag-ws`, the
internal pane number for `flag`/`unflag` (`parsePane` takes the number, not the
handle). So a row can be acted on without a second lookup, and the two shapes
tell you which verb — the distinction the verb pair already makes. The handle
moves into the "where" column, where it sits beside the most specific thing known
about the pane: a custom name, else the agent and its state, else the live title.

Smaller calls:

- **Plural verb for the listing** (`flags` vs `flag`), the `themes`/`theme` and
  `runbooks`/`runbook` pair — one verb taking an optional argument would make
  "did I just change something?" depend on argument count.
- **`catctl help flags` generates its kind table from `flags.Defs()`.** That is
  what `Def.Meaning` was reserved for, and a second copy of the vocabulary in a
  help string is a copy that goes stale silently.
- **`dispWidth` is a documented three-case estimate**, not a full
  East-Asian-width table: joiners and variation selectors are zero (so a ZWJ
  emoji sequence measures as one glyph, not several), emoji and CJK are two,
  everything else is one. A dependency was not worth buying — the only thing
  riding on it is whether one row sits a column off, and every named kind's
  glyph (⚑ ? ★ ⚠ ✓ ✎) measures one.
- **An empty listing goes to stderr and exits 0**, naming the filter when there
  was one (`nothing flagged "done"`). Nothing is flagged is not an error, and
  stdout stays clean for `catctl flags | grep`.
- `argFlagKind` gained an `argHints` entry, so `catctl help flag` now says its
  second argument completes.

## Verification

Full stack, not just the suite. A throwaway `cathost` + `catway` on
`127.0.0.1:8499` with `--auth none`, its own short `/tmp/ctsv-f*.sock` sockets
(the 104-char `sun_path` limit again — the scratchpad path is too long) and its
own `--state-dir`, so the running MacApp was never touched.

**`catctl`:**

| command | result |
|---|---|
| `flag 1 followup "waiting on the API review"` | ok; `panes` reports `flag`/`flag_note`/`flag_at_ms` |
| `flag-ws w1 warn flaky tests in here` | ok; `workspaces` reports the same three |
| `flag 1 🍕 "lunch build"` | ok, stored verbatim |
| `flag 1 folloup` | refused — `unknown flag kind "folloup" (one of: done, followup, note, question, star, warn, or a single glyph)` |
| `flag 1 "a b"` | refused — `contains whitespace or a control character` |
| `flag 1 $'\x1b[31m'` | refused — same |
| `flag 1 note $'two\nlines   here'` | ok; stored as `two lines here` |
| `unflag 1` / `unflag-ws` | ok; the fields vanish |

**Durability:** `session.json` held both flags nested under the workspace and its
pane; catway was killed and restarted against the same state dir and both came
back with their notes and timestamps intact.

**Browser:** headless Chrome driven over CDP by a zero-dependency node script
(node 22 has a global `WebSocket`), asserting the rendered DOM rather than only
capturing a picture. Note that the front end is **one closure** — nothing is on
`window` — so every interaction had to go through real DOM events, which is the
honest way to test it anyway.

```
workspaces  ● cats ★   fk-star     rgb(242,193,78)  "★ important — the one that matters · flagged 53s ago"
panes       cats:p1 ⚑  fk-followup rgb(229,115,115)
agents      ●claude opus 5 ⚑ cats:p1 · 35s ago · idle
header      "⚑ still here after a restart?"  pflag fk-followup
flagBtn     true
```

Menus and dialog, driven live:

- pane menu → `flag: ⚑ follow-up…` → submenu with six kinds, `(current)` on the
  active one, plus `flag with a note…` / `edit note…` / `clear flag`
- the dialog opens with the current kind and note prefilled and the glyph row
  **hidden**; picking `custom glyph…` reveals it; Escape closes
- the header's ⚑ button opens the identical menu
- clicking `★ important` updated the header chip, the AGENTS mark and the PANES
  mark within one round trip, and toasted
  `cats:p1 flagged ★ important — still here after a restart?`
- on a locked workspace: name opacity `0.5`, flag opacity `1`, lock opacity `1`

The AGENTS row needed a detected agent, so a stand-in `claude` script (a `sleep`)
was put on `PATH` — process-name detection is all that path needs, and nothing
touched a network.

`make check` (fmt-check, vet, build, test, vet-ghostty, race-ghostty) clean.
`node --check` on the concatenated front end clean.

### The follow-on's own run

Same shape, a fresh throwaway pair on `127.0.0.1:8499` (`/tmp/ctsv-fl-*.sock`,
its own `--state-dir`), so the MacApp was again never touched. Two notes for the
next person standing one up: `catway` **exits** if `--config` names a file that
does not exist — the log line reads like a warning and is not — and `cathost`
needs `-control-socket -` / `-hook-socket -` to keep it from opening relays.

| command | result |
|---|---|
| `flags` (nothing set) | `catctl: nothing flagged` on **stderr**, exit 0, stdout empty |
| `flags` (2 workspaces, 3 panes, 5 flags) | all five rows, workspaces first, columns aligned |
| `flags followup` | the two follow-ups only, across both workspaces |
| `flags 🍕` | the one custom-glyph row |
| `flags done` | `catctl: nothing flagged "done"`, exit 0 |
| `flags folloup` | `error: bad params: unknown flag kind "folloup" (one of: …)`, exit 1 |
| `--json flags followup` | the raw payload, panes carrying `cwd`/`host` — the `PaneMeta` merge |
| `__complete flags ''` | the six kinds with their meanings |
| `catctl help` | the verb listed in the table |

The agent column was exercised the way the first half was: a stand-in `claude`
(a `sleep`) on `PATH`, run in a flagged pane, after which the row read
`1  ⚑ follow-up  w1:p1 claude · idle  23s ago  waiting on the API review` —
which also put a non-zero age on screen.

New tests: three dispatcher tests in `internal/app/query_test.go` (filtering,
the unknown-kind refusal, custom-glyph filtering) and six in
`cmd/catctl/flaglist_test.go` (`flagMark`, `paneWhere`, `humanAge` including the
backwards-clock case, `dispWidth` across all six glyphs, `nothingFlagged`, the
builder). `make check` clean; the catgen-dart golden regenerated and
`TestEveryCommandReachesDart` satisfied.

## Known limits (stated, not hidden)

- ~~**There is no "show me everything flagged" view yet.**~~ Closed by
  `64f6be2` for the CLI: `flag.list` + `catctl flags`. **The browser still has
  no flagged view** — the marks are drawn in place in four lists, and nothing
  collects them. It would not call `flag.list` either; the front end already
  holds the whole model, which is how the AGENTS rollup is built, so it is a
  rendering job rather than a protocol one.
- **The listing is not sorted by recency.** Both halves come back in the
  underlying lists' order — the sidebar's own top-to-bottom order — so a listing
  run twice reads the same way and "did I clear that one" is answered by a
  glance at the same position. A `--recent` would be a small addition if the
  ordering ever turns out to be the wrong default.
- **The note is single-line and capped at 500 characters.** It is drawn in a
  sidebar tooltip and a pane header; a paragraph has nowhere to go.
- **The vocabulary is duplicated in Go and JS.** Guarded by a test, not by
  construction. Serving it would be a message for six compile-time constants.
- **cats-mobile is not regenerated.** The Dart golden in this repo is up to
  date, but `../cats-mobile` pins a `CATS_REV`; per the usual flow that is
  `tool/regen.sh` after this lands and is pushed.

## Open at the end of the session

- **cats-mobile.** The Dart golden in this repo is regenerated and committed —
  now including `flag.list` — but `../cats-mobile` pins a `CATS_REV`, so its
  `tool/regen.sh` run is still a separate step, and it now has two commits to
  catch up on rather than one.
- **`cats-todo.zip`** sits untracked at the repo root and predates this work. It
  was deliberately left out of all three commits — a stray 443 KB archive is not
  part of this change.

## Notes for next time

- `CommandNames()` is now *derived* from `commandSpecs`, not hand-maintained —
  the old session doc's warning is stale. What still has to be edited by hand is
  `recordedParamClasses` in `record_test.go`: every `Recorded: true` command must
  classify its params or `TestRecordedParamsAreClassified` fails. That is the
  intended tripwire.
- `cmd/catgen-dart` parses only `internal/{browserproto,app,orchestration}`
  (`sourceDirs`). A wire struct built from a type in some *other* package would
  need that list extended — which is the main reason `FlagInfo` lives in
  `internal/app` and `internal/flags` stays off the wire.
- `TestArgKindsMatchSynopsis` in `cmd/catctl` needs a `placeholder` entry for
  every new `argKind`, keyed by the operand name in the synopsis.
- The CDP script (`shot.js` + a probe expression) is genuinely cheap and is the
  only way to test this front end honestly. Chrome plus node's global
  `WebSocket` is the whole dependency list. Remember the closure: no globals, so
  drive it with real events.
- Adding a §7 command is cheaper than it looks, and the tripwires are all
  automatic: a `commandSpecs` entry (name + params + result), a `Dispatch` case,
  and `go run ./cmd/catgen-dart -out cmd/catgen-dart/testdata/golden`.
  `TestEveryCommandReachesDart` fails until the golden is regenerated, and
  `record_test.go`'s hand-maintained `recordedParamClasses` only bites commands
  marked `Recorded: true` — a read-only query needs nothing there.
- An ergonomic `catctl` verb backed by ONE method gets completion, its help page
  and `--json` for nothing. A verb that needs two round trips does not, and pays
  for all three by hand (see `cp`). That asymmetry is worth weighing *before*
  deciding where a new listing is computed.
- Rendering a payload as something other than JSON is a `case` in `run()`'s
  output switch beside `clipboard.read` and `ledger.output` — and it must sit
  *after* the `*rawJSON` case, or `--json` stops working.
