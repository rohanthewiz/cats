# Session: ⌘ accelerators, and the twin that types

- Session id: `b1179dd7-c4b9-4c29-b8be-687ad1f7e661`
- Date: 2026-08-14
- Branch: `main` (cats); gonotes on `master`
- Subject repo: `~/projs/go/gonotes` — commit `05bc968`, **pushed**
- cats: `a829893` (seat + plan record), **pushed**
- Plan/record updated: `ai_docs/cats-gonotes-intg.md` (Phases 8 and 9 marked done)
- Predecessor: `2026-0814-2052-capture-to-note-and-the-escape-byte.md`

## What this session was

The last two phases of the gonotes plan, done together: Phase 8's ⌘ accelerator
layer and Phase 9's plugin manifest plus the one-line cats-side seat.

`tui/metakeys.go` (232) and `tui/metakeys_test.go` (347) new, plus
`gonotes/cats-plugin.toml`; 47 insertions across `tui/tui.go`, `tui/browse.go`,
`tui/categories.go`, `tui/form.go`, `tui/login.go`, `tui/confirm.go` and
`.gitignore`. `go build`, `go vet`, `go test -race ./...` green in gonotes;
cats builds and its internal suite passes.

Both phases landed as specified in shape, and in both the specified *detail* was
wrong in a way worth writing down.

## One twin per chord is not enough

The plan said "translate claimed `super+`/`meta+` chords to their existing twins
and re-dispatch", which reads as one keystroke per chord. It cannot be. GoNotes
spends unmodified letters on commands in the list screens (`e` edits, `f` flags,
`d` deletes) and on content everywhere text is entered, so the same verb has two
keys at two depths:

```
⌘E on the note list  →  "e"        opens the form
⌘E on the form       →  "ctrl+e"   opens $EDITOR on the body
```

So each row carries two twins, and the second is allowed to be **empty** — which
is the instruction to swallow rather than translate. Which one applies is decided
per keystroke by a new `texter` interface (`takingText() bool`), sibling of
`refresher` and `restyler` in the same architecture: the form, login and prompt
screens answer true unconditionally; the two list screens answer true only while
their fuzzy filter prompt is up (the same condition their own `Update` already
checks before its switch).

Not implementing `texter` means command mode, and that default is the safe one: a
new screen has to opt IN to having its keystrokes protected, and forgetting costs
a chord that does nothing rather than one that types.

| chord | command mode | taking text |
|---|---|---|
| ⌘S | `ctrl+s` | `ctrl+s` |
| ⌘G | `ctrl+g` | `ctrl+g` |
| ⌘E | `e` | `ctrl+e` |
| ⌘F | `f` | swallow |
| ⌘D | `d` | swallow |
| ⌘/ | `/` | swallow |

⌘/'s twin binding comes from `list.DefaultKeyMap().Filter` — the fuzzy filter
belongs to bubbles, not to our keymap, and the invariant test needs a real
binding to check against.

## The hazard is in the translation, which reverses dbc's reasoning

dbc's version of this file swallows unclaimed ⌘ chords because a chord that fell
through "would reach the focused widget as a bare letter and type it". Checked
rather than inherited, and under v2 that is no longer true:

- `key.Matches` compares `Key.String()`, which is `"super+f"`, and that matches
  no binding.
- Every text widget inserts `Key.Text` (bubbles textinput.go:647), and the CSI-u
  decoder deliberately leaves `Text` **empty** for any modifier above shift —
  its own comment: *"we need to clear the text if we have a modifier key other
  than a ModShift key"*. The Windows console path guards it the same way.

So an unclaimed chord is already inert. What is **not** inert is the twin this
layer synthesizes: a real printable keystroke, `Text` and all, because it has to
be indistinguishable from the user pressing the key. ⌘D translated to a bare `d`
without the mode check does not do nothing during a search — it appends a `d` to
the search box. That is the one reachable bug here, and the typing column is what
prevents it.

Confirmed by mutation rather than assertion: deleting the `takingText` branch
from `metaTranslate` fails two tests with

```
⌘f typed into the title field: "f"
⌘d typed into the search box: "ald"
```

The swallow stays anyway, demoted from repair to guarantee: it makes "a ⌘ chord
stops here" a property of this file rather than a coincidence of three upstream
implementation details.

## No arming gate, and now for a reason

dbc needed one because a terminal set to Option-as-Meta produced the same tcell
`ModMeta` as ⌘, so `⌥e` could have fired an accelerator — and no environment
variable separates the two configurations. v2 has no such ambiguity:
Option-as-Meta arrives ESC-prefixed and decodes to `ModAlt`, while `ModMeta` and
`ModSuper` come only from the kitty modifier bits (32 and 8, and ultraviolet maps
them explicitly in `fromKittyMod`). A chord can therefore only arrive from a host
that speaks the protocol and chose to forward it. Both mods are matched anyway —
the two names for the same physical key differ by terminal.

`metaChord` prefers `BaseCode` over `Code`, matching `Key.Keystroke`'s own
preference: it is the PC-101 physical key, which is what cats matched on
(`KeyboardEvent.code`) when it decided to forward the chord, so an AZERTY
keyboard resolves the same row. Shifted forms are claimed but not translated.

## Verified on a real pty

Tier 0, against the built binary, sending kitty CSI-u at modifier 9 (1 + the
super bit) — the exact bytes cats' input encoder and Ghostty emit. Register →
create a note → the six chords.

| | |
|---|---|
| ⌘E | opened `Edit: Alpha` |
| ⌘F ⌘D ⌘P on the form | drew nothing; title still `Alpha` |
| ⌘G | `Capturing an agent pane needs cats — GoNotes is running standalone` |
| ⌘F on the list | `⚑ Alpha` |
| ⌘/ | `Filter:` |
| ⌘D during that search | nothing; a following `z` left the box reading `z`, not `dz` |

10/10. The trap re-encountered, and it cost two runs: **a pty smoke test must
force local mode**. The MacApp's embedded server answers on `localhost:8444`, so
`gonotes tui -d <tmpdir>` probes, finds it, and comes up in HTTP mode against the
*real* notes database — the temp directory is silently irrelevant, and the first
run got a "Sign in" screen where a fresh database would have offered "Create your
account". `GONOTES_URL=http://127.0.0.1:1` is the fix.

## Phase 9: the flag the plan specified would have been a bug

The plan's action was `./bin/gonotes tui -d ~/.gonotes`, with the right reasoning
behind it — an action's `argv[0]` is anchored to the plugin root while the pane it
opens has the *user's* project as its cwd (`plugin.go:197-207`), so a
cwd-dependent data directory would put different notes behind every tab.

The flag is the wrong way to get it. This argv is exec'd directly, with no shell
to expand the tilde, so it would create a directory literally named `~`. It is
also unnecessary: `gonotes tui` already defaults `--dir` to `$HOME/.gonotes` via
`os.UserHomeDir` and chdirs into it before anything opens. Confirmed by asking the
binary:

```
--dir value, -d value  working directory for data and config (default: "~/.gonotes")
```

The manifest was validated through cats' own `plugin.LoadManifest` rather than by
eye (a throwaway test in `internal/plugin`, removed after), and the build step run
for real. Two actions, `tui` first so a bare `plugin run` opens the TUI, `serve`
second. `/bin/` added to gonotes' `.gitignore`.

## The seat, checked rather than assumed

The plan asked for the FNV fallback to be verified at implementation like dbc did,
and that check is what justifies the entry rather than merely permitting it:

```
gonotes → 1     dbc → 5     ced → 6
```

Slot 1 is claude's. A note-taking pane sits beside a claude pane more often than
beside anything else, since capturing that pane's output is what it is for — so
this is exactly the collision the seating chart exists to prevent. `gonotes: 4`,
continuing claude 1, ced 2, dbc 3.

## Where this leaves the plan

Phases 0-9 done — the gonotes integration is complete as specified.

Still open, unchanged from Phase 7:

- The kitty **set**-form registration question (needs gonotes in a real cats
  pane; a fake control socket cannot answer it). Phase 8 makes this cheaper to
  answer, since the chords are now the observable.
- `GONOTES_JWT_SECRET` unset — every token this instance issues is signed with
  the publicly known development constant, which is load-bearing now that hooks
  and phone push carry gonotes activity off the machine.
- Not run: `catctl plugin link .` from the gonotes checkout, which is what puts
  GoNotes in the ⌘K palette. Left to the user — it writes into
  `~/.config/cats/plugins`.
- The plan's live end-state checklist (§ Verification) is still worth walking
  once against the running catway.
