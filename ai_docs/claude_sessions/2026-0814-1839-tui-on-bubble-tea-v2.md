# Session: the TUI on Bubble Tea v2, and the palette the port forced

- Session id: `a5ed4034-634e-4a61-b6fa-b99f13004739`
- Date: 2026-08-14
- Branch: `main` (cats); gonotes on `master`
- Subject repo: `~/projs/go/gonotes` — commit `c00daee`, **not pushed**
- cats: `c198f9c` (plan doc), **not pushed**
- Plan/record updated: `ai_docs/cats-gonotes-intg.md` (Phase 2 marked done)
- Predecessor: `2026-0814-1810-gonotes-lives-on-bytdb.md`

## What this session was

Phase 2 of the gonotes plan: move all 9 `tui/` files from the charmbracelet
v1 libraries to their v2 successors. The plan called it "mechanical,
behavior-identical". Most of it was. The part that wasn't is the whole
interest of the session, and it was mis-scheduled in the plan.

Landed: bubbletea v2.0.8, bubbles v2.1.1, lipgloss v2.0.6, glamour v2.0.1,
teatest `v2.0.0-20260813141921`. 578 insertions / 159 deletions including the
package's first tests. `go build`, `go vet`, all four test packages green;
the TUI suite stable over 5 consecutive runs.

## The import-path trap runs both ways

The plan carried one known gotcha forward: v2 imports are `charm.land/*`, not
`github.com/charmbracelet/*`, because the GitHub proxy serves zips whose
`go.mod` declares `charm.land`. True for all four libraries.

**teatest is the exception.** `go get charm.land/x/exp/teatest/v2@latest`
fails with `module declares its path as: github.com/charmbracelet/x/exp/teatest/v2`
— the mirror image of the documented trap. Each module's own `go.mod` is the
only authority; assuming either convention is uniform costs a resolution
error either way.

## The four API changes, ordered by how quietly they fail

1. **`" "` → `"space"`.** Nothing catches this. Not the compiler, not vet —
   the private-note checkbox in `form.go` would simply have stopped
   toggling. The mechanism is worth knowing: `Key.String()` returns `Text`
   when non-empty, *except* that space is the one printable character with
   an invisible literal form, so it falls through to `Keystroke()`, which
   names it. Every other binding the six screens use — letters, `esc`,
   `enter`, `tab`, `shift+tab`, `ctrl+s`, `ctrl+e`, shifted `Y`/`N` —
   stringifies exactly as before.
2. **`View() string` → `View() tea.View`, root model only.** Screens still
   return plain strings. `tea.WithAltScreen()` is gone from the program
   options; `AltScreen` is a field on the returned view. Get that wrong and
   you have a program that runs happily and paints nothing.
3. **`KeyMsg` → `KeyPressMsg`.** `KeyMsg` survives in v2 as an *interface*
   over press and release, so `case tea.KeyMsg:` still compiles — and would
   double-fire every binding the moment release reporting is enabled. Which
   is exactly what Phase 5's kitty keyboard work would turn on.
4. viewport `New(WithWidth, WithHeight)` + `SetWidth`/`SetHeight`; textinput
   `SetWidth`. Genuinely mechanical.

## The plan's one wrong call

Phase 2 was specified as `glamour.WithStyles(styles.DarkStyleConfig)` as an
interim, palette work deferred to Phase 3. That doesn't hold: **lipgloss v2
deleted `AdaptiveColor`**, so the palette question lands in Phase 2 whether
or not it is scheduled there. Hardcoding dark is a live regression on light
terminals, and glamour v2 also dropped `WithAutoStyle()`. So `styles.go`
grew `setPalette(dark bool)` and every style became a var assigned there.
(Both glamour configs do exist: `styles.DarkStyleConfig` /
`LightStyleConfig`. Only Dracula and TokyoNight are exported as `go doc`
top-level vars, which briefly suggested otherwise.)

Two related v2 facts: `lipgloss.Color` is now a *function* returning
`color.Color`, not a type — so the palette vars are declared against
`image/color`. And `lipgloss.LightDark(isDark)` returns the chooser that
replaces `AdaptiveColor`.

### Where the detection goes, and why the two obvious spots are wrong

- `lipgloss.HasDarkBackground` writes an OSC 11 query and blocks on the
  reply with a **2s timeout — per fd, and `BackgroundColor` tries stdin then
  stdout**. So a terminal that ignores OSC 11 costs ~4s of black screen at
  startup. The first pty smoke test caught this directly: two OSC 11 queries
  in the capture and nothing after them.
- A package-level var initializer would also run it during `gonotes serve`,
  because `main` imports `tui` unconditionally.

The answer is v2's own mechanism, which lipgloss's docs point Bubble Tea
users at explicitly: `tea.RequestBackgroundColor` from `Init()`, `setPalette`
on `tea.BackgroundColorMsg`. Non-blocking; a silent terminal just stays dark.

**It is passed uncalled.** Its signature is `func() tea.Msg`, i.e. already a
`tea.Cmd`. Its own doc comment shows it invoked — `return m, RequestBackgroundColor()`
— which does not compile.

### bubbles v2 compounds it

`list.NewDefaultDelegate()`, `textinput.New()` and `textarea.New()` all bake
in a hardcoded **dark** style set and copy it into the model at
construction. This is not incidental; the list source says so:

```go
// XXX: Let the user choose between light and dark colors. We've
// temporarily hardcoded the dark colors here.
Styles: NewDefaultItemStyles(true),
```

So every construction site re-applies `DefaultStyles(isDark)`, and
`loginScreen` — built before the program starts, hence the only screen that
can be on the stack when the reply lands — implements a one-method
`restyler` interface. Phase 3 inherits both seams (`setPalette`, `restyler`)
and broadcasts to the whole stack.

## Tests — the package's first (249 lines)

- **A key-name table** pinning every binding string the six screens dispatch
  on, so an upstream rename fails here instead of silently disabling a
  feature. This is the direct answer to failure mode #1 above.
- teatest boot flows: login renders (including the single-user prefill,
  which exercises a command result reaching `Update`), and a seeded browse
  list.
- An altscreen-requested assertion, for failure mode #2.

### Two harness gotchas worth keeping

- **`tm.Output()` is one consumable stream.** A second `WaitFor` accumulates
  from wherever the first one stopped reading, so text already pulled into
  the first call's buffer is simply gone. Three tests failed this way and it
  reads *exactly* like a rendering bug — the debugging round only ended by
  dumping raw bytes and finding the content present all along. One `WaitFor`
  per program, with a condition over all the wanted substrings.
- `WithInitialTermSize` races any message injected at startup. The browse
  test re-sends `WindowSizeMsg` after `loggedInMsg`, or the list sizes
  itself 0x0 and draws nothing.

## Verification past the suite

Driving the built binary on a real pty, which is where the startup-stall
problem surfaced in the first place.

**`script -q /dev/null` is useless for this.** It inherits a 0x0 winsize
when the parent isn't a tty, so the program correctly renders nothing and
looks broken. That produced a 93-byte capture that read like a total
rendering failure and was nothing of the kind.

A small python `pty.fork` + `TIOCSWINSZ` harness — which also *answers* the
OSC 11 query the way a real emulator would — showed the full login card. The
decisive run flipped the answer from `rgb:1c1c…` to `rgb:ffff…`:

| | occurrences | first byte | last byte |
|---|---|---|---|
| dark primary `#7D79F6` | 42 | 104 | 2063 |
| light primary `#5A56E0` | 42 | 2237 | 4114 |

Clean separation, no interleaving: the first frame renders on the dark
default, the background reply arrives, and every frame after it is light.
The async palette swap working end to end, in the shipped binary.

## Where things stand

gonotes `master` is on Bubble Tea v2 at `c00daee`; cats has the plan record
at `c198f9c`. **Neither is pushed** — this session's wrap does that.

Deliberately not done: `~/bin/gonotes` and the MacApp bundle were **not**
reinstalled. The binary carries the server as well as the TUI, so swapping
it is a live-service change and outside "start phase 2". Phase 1's warning
still applies whenever that happens — a stale bundle silently serves the
wrong thing.

Phase 3 is next: the conformance revamp. It now starts with two seams
already in place (`setPalette`, `restyler`) rather than building them from
scratch, and its `tui/palette.go` work is partly pre-paid. Its remaining
scope is the `key.Binding` keymap replacing the string switches, the cached
glamour renderer, the two-pane wide layout, and the goldens.
