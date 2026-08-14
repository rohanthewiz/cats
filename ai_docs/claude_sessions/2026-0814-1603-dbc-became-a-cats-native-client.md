# Session: dbc became a cats-native client

- Session id: `15ed73e0-1889-42ff-b9f3-35584bda0789`
- Date: 2026-08-14
- Branch: `main`, cats `613e623` → `96b9dc7` (3 commits)
- Companion work in dbc (`~/projs/go/dbc`): `cd60130` → `ebbb0a7` (8 commits)
- Plan/record: `ai_docs/dbc-intg-plan.md` (written this session, in cats at
  the user's direction rather than in dbc)
- Model for the whole thing: `~/projs/go/ced/ai_docs/cats-native-plan.md`

## What this session was

"Integrate DBC into cats the same way we integrated ced." Planning found
that dbc had **zero** environment awareness — no env sniffing, no OSC
emissions, no sockets, one `os.Getenv("DBC_DEMO")` in the whole tree — so
this is the ced roadmap re-run from nothing, at full scope, in one sitting:
seven phases, one commit each, plus one fix for a bug the phases
introduced.

The flagship is the same as ced's and it is the reason to do any of it: a
four-minute query's `working → idle` edge becomes a cats badge, toast and
phone push. dbc had no way to tell anyone anything once you looked away.

## The one architectural decision everything else follows from

ced owns a raw tcell event loop, so its integration invents a `catsEvent`
type with ten kinds and adds one case to the main switch. **dbc has no
event loop** — `tview.Application.Run()` owns it, and background→UI handoff
is `QueueUpdateDraw(closure)`.

So no event type was invented. Closures already carry what ced's `kind`
enum had to encode, and routing them through `QueueEvent` would only mean
arriving at the same handler by a longer road. ced's iron EVENTS ONLY rule
became one sentence at the top of `ui/cats_glue.go`:

> a goroutine started here may touch only the values it was handed and
> `a.catsPost`. Everything that reads or writes an App field happens inside
> the closure `catsPost` hands to the UI goroutine.

`catsPost` checks a `stopping` channel before posting, so a late probe
result cannot park on tview's update queue after the loop stops draining
it.

## What landed

| Phase | dbc commit | What |
|---|---|---|
| 1 | `c4be386` | `cats/` transport (detect/client/hooks) + `ui/cats_glue.go`; hook reporter on beginRun/endRun |
| 2 | `3e0db4b` | `ui/hostident.go` — title follows the run, OSC 7 cwd |
| 3 | `ad3d249` | `cats/events.go` stream, `theme/palette.go`, host theme live + at startup |
| 4 | `4cc0311` | `ui/clip.go` — OSC 52 fallback so copying works over SSH |
| 5 | `681bd8a` | `ui/catsagents.go` — ^G sends the statement to a sibling agent |
| 6 | `0682e3b` | `ui/metakeys.go` — ⌘E/⌘P/⌘G |
| — | `0eea876` | **fix**: take tview's screen instead of handing it one |
| — | `ebbb0a7` | test: pin the whole Tier-1 startup conversation |
| 7 | cats `137976d` | `dbc: 3` in `AGENT_HUE` |

The verb set is seven: `ping`, `pane.list` (+`ResolvePane`), `pane.focus`,
`pane.send_input`, `chat.send`, `config.get`, `events.subscribe`. Not
ported, with the growth path written down: `capture` + `wait_for_output`
(read an agent's answer back), `pane.split` (open a result in a sibling
dbc), `clipboard.read` (unneeded — dbc is write-only to the clipboard).

## The findings, which are the point

Nine things the plan got wrong or did not know, in the order they cost
time. Most were cheap once seen and expensive while unseen.

**1. The ⌘ layer needed no enabling work at all.** tcell v2.13 already
emits the kitty CSI-u push for XTermLike terminals (`tscreen.go:332`), and
cats sets `TERM=xterm-256color`, so every dbc pane was already registering
non-zero kitty flags with cats' emulator — the exact gate `cmdGoesToPane`
checks. The chords had been arriving all along; nothing was reading them.
The plan had budgeted for a possible tcell feature flag.

**2. hostident shrank to a third of ced's.** tcell saves and restores the
terminal's own title itself (`\x1b[22;2t` / `\x1b[23;2t`), and tview's
`Application.SetTitle` reaches tcell's OSC 2 emitter and re-applies on any
new screen. Emitting either again would push a second copy onto that stack
and leave one behind. OSC 7 is the only sequence dbc spells out.

**3. A clock-seeded seq cannot be read back through `float64`.**
`UnixNano()` is ~1.8e18, past float64's 53-bit mantissa, so two consecutive
seq values decode **equal** through `map[string]any`. The test harness
needs `Decoder.UseNumber`. The real server parses uint64 and was never
affected — this was a test measuring its own decoder, and it looked exactly
like a bug in the reporter.

**4. `t.TempDir()` cannot hold a unix socket on macOS.** It embeds the test
name, and `sun_path` is 104 bytes; a descriptive test name plus the system
TMPDIR overruns it and fails with `bind: invalid argument`, which reads
like a permissions problem. Tests use a short `os.MkdirTemp("", "d")`.

**5. Making the palette a runtime value broke `go vet`.** A concatenated
color tag stopped being a constant, so the seven `logf` calls that passed a
*finished* message became "non-constant format string in call to logf".
They go to a new non-formatting `log()` rather than growing `"%s"` noise at
each site.

**6. Two places kept colors that no longer followed the palette**, and only
one was obvious. The layout containers between the panes were expected. The
other was not: a `TextView` keeps a **second** style for its text area,
captured from `tview.Styles` at construction, and `pane()` never reaches it
because it takes a `*Box`. The log pane's interior had therefore *always*
been the deep surface rather than the panel its border wears — a latent
inconsistency the theme work made visible. It is now set explicitly to
exactly that color: nothing looks different, everything follows a theme.

**7. `QueueUpdateDraw` runs the draw AFTER the closure returns.** A test
that reads cells as soon as its closure finishes is looking at the frame
*before* the change. It needs a second sync queued behind the draw.

**8. `Release()` blocks on the write, not on the server's bookkeeping.** A
test asserting the request had been *recorded* by the time Release returned
was asserting something Release does not promise.

**9. The `AGENT_HUE` seating chart was already full** (18 labels, 6 slots),
so `dbc: 3` shares with codex, as every entry past the first six shares
with something. Verified rather than assumed: FNV would have put `dbc` on
5, beside cursor. Seating it explicitly puts the three house tools that
actually share this sidebar — claude, ced, dbc — on 1, 2, 3.

## The bug the test suite could not have caught

Phase 4 needed a screen handle for OSC 52, so `Run` created the screen and
handed it to tview. That turned a clean failure into a crash: tview's
`SetScreen` calls `Init()` and **discards its error**, so on any terminal
dbc cannot drive — no controlling tty, an unusable TERM — the next call,
`EnableMouse`, wrote to a nil writer and panicked with a stack trace where
`Run` used to return "cannot open terminal".

Every test passed, and structurally had to: a `SimulationScreen` is usable
the instant it exists, so `EnableMouse` on an uninitialized one is harmless
there and fatal on a real one.

It was found by launching the **real binary** on a pty inside a fake cats
environment (scripted hook and control sockets standing in for catway). The
fix takes the screen from `SetBeforeDrawFunc`, which tview hands the live
screen on every draw: startup is back to what it was, the error path is
tview's again, and the handle is always the *current* screen rather than
one captured once.

The same run confirmed the thing it was written for — **the pane is claimed
at startup and released on exit even when dbc fails to start**, which is
the case that would otherwise leave a stale `dbc` badge on a pane forever.

## Design calls worth not reversing later

- **`blocked` has no honest source in dbc today, and that is a reading of
  the word rather than an omission.** Blocked means a question dbc raised
  that the user did *not* ask for; every modal dbc opens was opened by the
  keystroke just pressed, and paging someone about a dialog they summoned
  is how a notification channel earns being muted. `catsAsking` exists
  ahead of its first caller so a future *interrupting* modal (a dropped
  connection asking whether to reconnect) is one line to wire.
- **Sending to an agent pane focuses that pane.** The text is staged and
  unsubmitted, so it is inert until somebody presses Enter there; sending
  without focusing would leave the user hunting for where their question
  went.
- **Only agent panes are offered as destinations.** A fenced SQL block
  typed at a plain shell prompt is junk. cats' own chat panel is the other
  destination and the one that remains when there are no sibling agents.
- **The question carries the connection's driver.** It is the part an agent
  cannot infer and most needs — the same statement means different things
  to Postgres and SQLite — and the last error rides *after* the query
  rather than instead of it, because "why did this fail" is a question
  about both halves.
- **The HTML exporter still reads the theme constants, not the active
  palette.** An exported report outlives the session and travels to people
  who are not sitting in this terminal, so it should look like dbc rather
  than like whatever theme the author's multiplexer was wearing.
- **Env-only, no `[cats]` config section.** Same as ced: the tiers are
  silent, so there is nothing to configure and nothing to misconfigure.

## Verification

`go vet` and `go test -race ./...` green in both repos; dbc's pre-existing
78 tests untouched, which is itself the proof that the zero-value
`catsState` is inert Tier 0. Headless query mode re-run by hand.

`TestCatsInitCompletesTheTier1Handshake` pins the whole startup
conversation (probe → resolve our pane → subscribe → prime the cache)
against one fake socket, because the per-piece tests each mocked the step
before them.

**Still open, and only a running catway can answer it:** the sidebar
actually showing `dbc`, a real "finished" push on the working→idle edge, a
real `theme_changed` repaint, and the ⌘ chords arriving from browser-cats
and from a bare kitty/Ghostty. The pty harness cannot substitute — under
`script` the startup conversation stopped after `pane.list`, which the Go
test proves is the harness rather than dbc, and chasing it further would be
measuring the harness.

## Not done, deliberately

cats-mobile was **not** regenerated. `tool/regen.sh` exists for changes to
the command vocabulary, and this session changed none of it — only
`web/index.html`'s hue map and docs — so a regen would move the `CATS_REV`
pin and produce an identical client.
