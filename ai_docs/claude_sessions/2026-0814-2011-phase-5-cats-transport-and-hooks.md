# Session: the cats transport, and the deadlock it dodged

- Session id: `1e17efe6-2164-4b52-8419-5b01f72d81c7`
- Date: 2026-08-14
- Branch: `main` (cats); gonotes on `master`
- Subject repo: `~/projs/go/gonotes` — commit `319bdaf`, **pushed**
- cats: `c979a92` (plan record), **pushed**
- Plan/record updated: `ai_docs/cats-gonotes-intg.md` (Phase 5 marked done)
- Predecessor: `2026-0814-1938-the-store-seam-and-http-mode.md`

## What this session was

Phase 5 of the gonotes plan: the cats transport, hook reporting, and host
identity. A new `cats/` package hand-copied from `../dbc/cats/` (four source
files, four ported test files), `tui/cats_glue.go`, two new TUI test files, and
three existing files rewired.

1,885 lines of `cats/`, 505 lines of glue, 746 lines of TUI tests, 113
insertions / 6 deletions across `tui.go`, `commands.go`, `form.go`. `go build`,
`go vet`, `go test -race ./...` green; TUI suite stable over three consecutive
runs.

Landed as specified except in one place, where the spec was wrong. What follows
is that, and what the spec left open.

## The spec said "probe/subscribe goroutine". That deadlocks.

`Program.Send` **blocks while the program has not started yet** and is a no-op
only once it has *terminated*. Those are different states, and the gap between
them is where a pre-`Run` goroutine lives.

The failure is not the probe itself — it is what the probe leads to. `close()`
calls `stream.Close()`, which waits for its reader goroutine to be gone. That
reader posts through `Program.Send`. So on a TUI that failed to start:

```
Run() errors  →  close()  →  stream.Close()  →  waits for reader
                                                      ↓
                                        reader blocked in Send, forever
```

The process never exits, and the trigger is the one case where the user most
wants it to.

The fix is that the probe is a `tea.Cmd` issued from `Init()`. A Cmd cannot run
until the event loop already is, so nothing downstream of it — including the
subscription, which is opened from `ready()` on the loop — can ever reach the
blocking window. `cs.send = p.Send` is still assigned before `p.Run()` exactly
as planned; only the trigger moved.

This is also why `close()` runs *after* `p.Run()` returns and *before* the error
return: `init()` claimed the pane before the program existed, so the release is
owed on the failure path too.

## Only the external editor is a reported span

cats turns a working→idle edge into a badge, a toast, and a phone push. A span
is therefore a claim that the user might reasonably have walked away during it.

The form's save is one bytdb write, or one POST. It resolves faster than the
badge would render, and a notification for it is how a channel earns being
muted. The editor — where the user is in another program for as long as they
like — is the one that qualifies. `form.go` carries the reasoning at the point
where the busy flag is set, so the next person to add a span has the rule.

The span opens **inside** `openEditorCmd`, not at its call site, because only
that function knows an editor is going to run: the temp-file write can fail,
and a pane badged "editing" for an editor that never launched stays badged
until the next transition. `TestNoEditorSpanWhenTheTempFileFails` pins it by
pointing `TMPDIR` at a path that cannot hold a file.

The closing edge fires on **both** outcomes of `editorFinishedMsg`. An editor
that exited with an error is still an editor that is no longer running.

## Events are subscribed to, and dropped

`frame()` has no consumers until Phase 6 (theme) and Phase 7 (pane cache).
Subscribing now is deliberate: the subscription is what puts the handshake and
the shutdown ordering under test *before* two more phases are built on top of
them. The planned handshake test's "prime cache" step is correspondingly absent
— there is no picker yet — so it pins probe → resolve → subscribe → release.

The filter names events and never a pane. That is load-bearing for Phase 6:
`theme_changed` is session-scoped and cats emits it against pane 0, so a
pane-narrowed subscription would silently never see it.
`TestSubscribeFilterNamesEventsAndNoPane` asserts the absence of the key.

## The capture verb landed ahead of its consumer

dbc omits `capture` on the grounds that a mirror struct with no caller is a
wire contract nobody is checking. The plan called for it here anyway, and it
carries two choices worth keeping:

- `ansi` and `unwrap` are both off, and both are `omitempty`, so neither key is
  sent. A note stores markdown — VT styling would arrive as escape noise, and
  unwrapping would rewrap prose to whatever width the pane happened to be.
- The timeout is 5s rather than the 3s default. Capture is not a local answer:
  cats forwards it to the cathost daemon and resolves the reply when that comes
  back, so the round trip includes a process hop the other verbs do not have.

`CaptureVisible` is scope 0, which `omitempty` drops — the *absence* of the key
is the visible scope, and a test pins that rather than letting someone "fix" it
into being spelled out.

## Tier-0 inertness is proved by absence

`newAppModel`'s signature did not change. It constructs an inert `catsState`
whose zero value is "not in cats, nothing connected", and detection happens
only when `Run` calls `init` on it. Every pre-existing test therefore builds the
model with no host at all — and all of them passed untouched, which is the
proof the plan asked for and cost nothing to obtain.

The one assertion worth stating out loud: a *nil* `*catsState` also survives
every method, because the alternative is a nil check at each of the call sites
the glue exists to keep clean.

## A test that measured its own decoder, twice

Both traps are inherited from dbc and both were live here:

- **Short socket dirs.** `t.TempDir()` embeds the test name, `sun_path` is 104
  bytes on macOS, and the overrun reports as `bind: invalid argument` — which
  reads like a permissions problem. `os.MkdirTemp("", "g")` throughout. The pty
  smoke script hit this too, from the scratchpad path alone.
- **`Decoder.UseNumber`.** The hook seq is seeded from `UnixNano` (~1.8e18),
  past float64's 53-bit mantissa; decoded as a float, two consecutive seq
  values compare *equal*.

And one earned here: `TestOnlyTransitionsAreReported` first asserted report
order and failed, because each report rides its own goroutine — which is the
entire reason the wire carries a seq. It now counts rather than indexes, and
says so. The ordering is pinned in the `cats` package, where the seq is.

## Verified past the suite, on a real pty

A scripted cats host: a fake control socket answering ping / pane.list /
events.subscribe, and a fake hook socket recording every report. Real binary,
`pty.fork` + `TIOCSWINSZ`, answering OSC 11.

| run | control methods | hook reports |
|---|---|---|
| launch, sit at login, ctrl+c | `ping`, `pane.list`, `events.subscribe` | `idle` claim → `release` |
| register → new note → ctrl+e (3s editor) → ctrl+c | same | `idle` → `working "editing: Recipes"` → `idle` (+3.26s) → `release` |

The 3.26s gap matches the scripted editor's 3s sleep. The window title tracked
it live: `GoNotes` → `editing: Recipes — GoNotes` → `GoNotes`. OSC 7 was
emitted once, before the alternate screen. A `theme_changed` frame and a
deliberately unknown frame were both delivered on the stream and dropped
without incident — the Phase 5 no-consumer path, exercised.

### The kitty risk is half-resolved

The pty capture shows `ESC[>4m ESC[=0;1u ESC[>4;2m ESC[=1;1u` at startup: the
**set** form, flags 1, paired with modifyOtherKeys. The encoding is now
observed rather than assumed. What is still open is only whether it registers
host-side, and the code path says it should — cats does not parse this itself,
it reads `e.term.KittyKeyboardFlags()` from ghostty-vt
(`internal/terminal/ghostty.go:270`). Confirming needs a real cats pane; a fake
socket cannot answer it.

## Two pre-existing traps the smoke pass cost

Neither is a Phase 5 bug. Both reproduce on the Phase 4 binary, which is how
they were classified rather than chased.

1. **An empty `GONOTES_URL` is not "no server".** `ServerURL()` falls back to
   `DefaultServerURL`, so the first two smoke runs came up in **HTTP mode
   against the live MacApp server on 8444**. The symptom was baffling until it
   wasn't: a fresh data dir never showed the first-run registration screen,
   because `ErrNoUserList` — correctly — does not enter registering mode. Force
   local mode with a dead port: `GONOTES_URL=http://127.0.0.1:9`.
2. **`gonotes -d <dir> tui` silently ignores `-d`.** The `tui` command declares
   its own `--dir` with the same default, and command-level flags win on
   lookup, so the global form resolves to the command flag's *default*. The
   working form is `gonotes tui -d <dir>`. This is the sibling of Phase 4's
   `gonotes serve` trap, and between them the rule is: check which level owns
   the flag before trusting that `-d` did anything.

## Where things stand

Both repos pushed. **The stale-build caveat carried since Phase 2 is closed**:
`~/bin/gonotes` is a current build (lists the `tui` subcommand, unlike the Apr
22 one that could not have run Phase 0's export), and `~/.gonotes-src` is at
`319bdaf`, so the MacApp rebuilds onto Phase 5 on its next run.

Phase 6 is next: `tui/catstheme.go` — host colors → `Palette` (hex gate,
fallbacks, sel blend), the synchronous startup fetch pre-first-frame when
`DetectEnv().InCats`, and `theme_changed` → `catsThemeMsg` → same-palette
early-return → `setPalette` + broadcast over the Phase 3 machinery. ~250 lines.
Two seams are already in place for it: `frame()` is where the event lands, and
`paletteChangedMsg` already broadcasts to the whole stack.
