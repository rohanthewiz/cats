# Session: A Persistent Annotated Flag on a Workspace or an Agent

- **Session ID:** `ec1bdaf7-0e72-4f09-acd9-ab972f1efe5b`
- **Date:** 2026-08-27
- **Branch:** main
- **Repo:** `cats`
- **Landed as:** `534bc11` — *flags: a persistent annotated mark on a workspace or a pane*

## Request

> Allow me to add a persistent annotated flag to a workspace or listed agent.
> Think of flag as some icon with meaning, like a red follow-up flag to which I
> can add a note

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

## Known limits (stated, not hidden)

- **There is no "show me everything flagged" view yet.** The mark is drawn in
  four lists and the data is on every `pane.list` / `workspace.list` row, so a
  cross-session listing is a query away — but nothing renders one today, and
  `catctl` has no `flags` verb. That is the obvious next increment.
- **The note is single-line and capped at 500 characters.** It is drawn in a
  sidebar tooltip and a pane header; a paragraph has nowhere to go.
- **The vocabulary is duplicated in Go and JS.** Guarded by a test, not by
  construction. Serving it would be a message for six compile-time constants.
- **cats-mobile is not regenerated.** The Dart golden in this repo is up to
  date, but `../cats-mobile` pins a `CATS_REV`; per the usual flow that is
  `tool/regen.sh` after this lands and is pushed.

## Open at the end of the session

- **Push + cats-mobile.** The Dart golden in this repo is regenerated and
  committed, but `../cats-mobile` pins a `CATS_REV`, so its `tool/regen.sh` run
  is a separate step once this is on the remote.
- **`cats-todo.zip`** sits untracked at the repo root and predates this work. It
  was deliberately left out of both commits — a stray 443 KB archive is not part
  of this change.

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
