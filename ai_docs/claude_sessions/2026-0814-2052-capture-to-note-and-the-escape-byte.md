# Session: capture-to-note, and the escape byte that wasn't

- Session id: `58699acb-d36b-4a96-83b7-d26c94fefdac`
- Date: 2026-08-14
- Branch: `main` (cats); gonotes on `master`
- Subject repo: `~/projs/go/gonotes` — commit `cf353b1`, **pushed**
- cats: `e4970e5` (plan record), **pushed**
- Plan/record updated: `ai_docs/cats-gonotes-intg.md` (Phase 7 marked done)
- Predecessor: `2026-0814-2011-phase-5-cats-transport-and-hooks.md`
  (Phase 6 landed in the same session as its record; this follows it)

## What this session was

Phase 7 of the gonotes plan: capture-to-note — the inverse of every other cats
feature in the integration. ctrl+g in the note list reads the text out of a
sibling agent's pane and opens a note prefilled with it.

`tui/capture.go` (413) and `tui/capture_test.go` (472) new; 78 insertions / 20
deletions across `tui/cats_glue.go`, `tui/tui.go`, `tui/browse.go`,
`tui/keymap.go`, `tui/form.go` and three existing test files. `go build`,
`go vet`, `go test -race ./...` green; TUI suite stable over three runs.

Landed as specified. Three things the plan did not anticipate, in the order they
would bite someone else.

## Stripping an escape BYTE is worse than leaving it

The plan said "ansi:false + residual control strip", and the obvious reading —
drop C0 and DEL, keep tab — is what the first draft did. It fails in a way that
is visible rather than subtle:

```
in    "before\x1b[2Kafter"
out   "before[2Kafter"        ← the ESC went; its payload stayed
```

An invisible sequence became visible text *in the note*. So
`stripEscapeSequences` removes sequences whole. It is a scanner rather than a
regexp for one reason worth stating: the terminating byte of a CSI is defined by
its **range** (0x40-0x7E, after any run of parameter and intermediate bytes) and
an OSC's by either BEL or ST — so "what ends this" differs per introducer, which
a scanner expresses directly and a pattern only approximates.

Deliberately not a full VT parser: DCS and the other string-terminated
introducers lose two bytes rather than their payload. They cannot reach here
anyway — cats answers `ansi:false` by stripping styling itself, and this is the
second line of defense, not the first.

The test table is where the reasoning lives: a bare BEL, a CSI whole, a CSI with
parameters, an OSC ending at BEL, an OSC ending at ST, a two-byte escape, and a
truncated sequence at end-of-string that must not run off the end.

## Tier-1-up had to start speaking, reversing Phase 5

Phase 5 chose silence on a successful probe, reasoning that a notice displacing
the login screen's own feedback would be a regression for the common case. That
was right *then*. Phase 7 changes the calculus: Tier 1 acquired a **door**, and a
door nothing advertises is a door nobody opens.

It could not go in the browse footer. Footers render in every terminal, and a
permanent "ctrl+g capture" row would advertise a key whose only answer in a plain
shell is that the feature is unavailable — this codebase's footers are generated
from the same bindings the switch dispatches on precisely so they cannot lie.

So: one status line at Tier-1-up, where the claim is true by construction. The
Phase 5 concern turns out not to bind — the status bar is cleared by the first
keypress, and login feedback only exists *after* the user has typed. Confirmed on
the pty run at byte 372: on the login screen, gone the moment typing started.

## captureDone is the one data message not routed to the active screen

Every other result in the package lands on whatever screen asked for it. A
capture is different: it takes seconds (cats forwards it to the cathost daemon)
and the user is free to open a note or a category meanwhile. Delivering to the
top of the stack would mean a capture the user *explicitly asked for* vanishing
because they navigated while it was in flight. It is handled at the root, which
pushes the form regardless of what is showing.

## Smaller decisions, each of which had an alternative

- **The three pane events are handled identically.** `pane_agent`, `pane_added`
  and `pane_removed` all answer one question — the cached layout is now wrong —
  so which arrived changes nothing, and `pollPanes`' 2s rate limit is what keeps
  a tab opening four panes at once from costing four round trips.
  `focus_changed` is subscribed but deliberately not acted on: the picker does
  not render which pane is focused.
- **`CaptureRecent` (scope 1), 200 lines.** The visible viewport alone loses the
  top of a long answer, which is the thing being captured; the whole buffer
  buries it in the conversation that led there. The plan's original
  `unwrap:true` did not survive Phase 5's contact with "a note stores markdown",
  and stayed off.
- **The form opens UNSAVED** — the same rule the outbound half keeps, where
  `pane.send_input` stages text without pressing Enter.
- **No hook span for the capture.** Consistent with the save: cats turns a
  working→idle edge into a toast and a phone push, and a five-second action the
  user is watching does not qualify.
- **No picker when there is nothing to pick.** At Tier 1 with no sibling agent,
  ctrl+g answers with a status line rather than an empty modal.
- **The picker is hand-rendered**, and that is a simplification: three or four
  rows, no filter, no pagination, so unlike the two bubbles/list screens it
  stores nothing derived from the palette and needs no `restyle()` at all.
  `confirmScreen` is the precedent.

## Two test-harness notes worth keeping

**`tea.Batch` is not transparent.** It returns a `BatchMsg` carrying the
sub-commands for the runtime to run, so a test that calls `cmd()` gets the
envelope rather than the result — hence `drainCmd`, which flattens it.
`tea.Sequence`'s message type is *unexported*, so there is no equivalent, which
is why the enter-to-capture path is exercised through a real program instead.
That is also why `captureDone` returns a Batch rather than a Sequence: the push
and the status line are independent, so the opaque form bought nothing.

**The self-exclusion is tested twice** — by resolved pane id and by handle
fallback — because GoNotes reports *itself* to cats as the agent "gonotes" and so
appears in `pane.list` looking exactly like a capture target. Without it the
picker would offer to capture the note list into a note.

## Verified on a real pty

A scripted cats answering `ping` / `pane.list` / `capture` / `events.subscribe`,
with the fake buffer deliberately hostile: row padding, an ANSI color run, and
blank rows top and bottom. The real binary registered, pressed ctrl+g, Enter.

```
Capture from an agent pane

  codex           blocked · w1:p4
  claude          idle · w1:p9

  ↑/↓ move • enter capture • esc back
```

| | |
|---|---|
| picker rows | blocked ranked first; the plain shell (`w1:p3`) and our own pane absent |
| wire | `capture {"pane":4,"scope":1,"lines":200}` — no `ansi`, no `unwrap` |
| form | title `Capture: codex — 2026-08-14 20:48`, tags `capture` |
| body | 3 lines: padding, blank edge rows and `\x1b[38;5;204m` gone, interior blank line kept |
| hooks | `idle` claim → `release`, nothing between |
| `pane.list` calls | 3 — resolve, prime at Tier-1-up, refresh on picker open |

One trap re-encountered rather than discovered: the harness' socket directory has
to be **short**. `AF_UNIX path too long` from a scratchpad path is the same
104-byte macOS `sun_path` limit the suite's `catsSockPath` works around, and it
reads like a permissions failure.

## Where this leaves the plan

Phases 0-7 done. Remaining:

- **Phase 8** — ⌘ accelerators (`tui/metakeys.go`). ⌘G maps onto the
  `keys.Capture` binding this session added; the table is ⌘S/⌘E/⌘F/⌘D/⌘G/⌘/,
  all on cats' `CMD_TO_PANE` allowlist, none ⌘-only, swallow every other armed
  chord.
- **Phase 9** — `gonotes/cats-plugin.toml` on the cats-todo template, plus
  `gonotes: 4,` in cats' `AGENT_HUE` (`cmd/catway/web/index.html` ~:1709).

Still open from earlier phases: the kitty **set**-form registration question
(needs gonotes in a real cats pane; a fake control socket cannot answer it), and
`GONOTES_JWT_SECRET` being unset — every token this instance issues is signed
with the publicly known development constant, which becomes load-bearing now that
hooks and phone push carry gonotes activity off the machine.
