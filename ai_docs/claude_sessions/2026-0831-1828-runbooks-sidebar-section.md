# Session: A Runbooks Sidebar Section

- **Session ID:** `3b3c5110-adc3-4b16-9506-1cbc67ea33ac`
- **Date:** 2026-08-31
- **Branch:** main
- **Repo:** `cats`
- **Follows:** `2026-0831-1801-a-record-button-for-the-macro-recorder.md`, whose
  closing note named this as the natural next surface.

## Request

> add a Runbooks sidebar section

Nothing more, so the scope came from the previous session's own line: a Runbooks
section, *list / run / edit*.

## What was already there

`runbook.list` and `runbook.run` have existed since the runbook phase and were
reachable from the browser the whole time — `handleCmd` has **no allowlist**, so
every §7 command is one `sendCmdAwait` away — but nothing in `web/` had ever
called either. `grep -rn runbook cmd/catway/web/js/` returned only
`40-record.js`. So the recorder could write `~/.config/cats/runbooks/deploy.yaml`
and the browser then had no way to see, run or fix it. This closes that loop.

## The design decisions

### Where it goes: between AGENTS and HISTORY

`sidebar.go`'s existing comment says the order is "the session's coordinate
system, outermost first", with History last as "the only section that looks
BACKWARD". Runbooks fits neither half of that rule, which is the argument for
where it lands: **every section above it is a reading OF the session; a runbook
is a file that does something TO one**. So the column reads down through the
session's own structure (machine → workspace → tab → pane → agent) and then
leaves it for the two sections about acting — what can be run, then what already
was. History stays last, which the old comment asked for.

### It is a QUERY section, and that is the whole shape of the file

The other six sections are fed by pushes. This one cannot be: a runbook is a
file, an editor rewrites one without the session ever knowing, and
`runbook.Load` deliberately caches nothing (its own comment: a cache would make
"edit, run" silently execute the previous version). So the section asks, on the
four moments that can have changed it:

| trigger | why |
|---|---|
| `welcome` | a reconnecting window has been away and cannot know |
| a recording **ending** | the one moment this UI itself writes a runbook |
| a run finishing | the files are unchanged, but `trigger_status` is not |
| the heading's `⟳` | an edit in `$EDITOR`, a hand-dropped file, a delete |

The recording edge is watched in `applyRecord` — in **every** window, on the
`true → false` transition — rather than in `stopRecording`'s success callback.
The recorder is session state, the window that saves is often not the only one
open, and `catctl record stop` has no callback here at all. A *cancel* takes the
same path and finds nothing new; that costs one readdir on a transition that
happens a handful of times a day, against putting the outcome on the wire for no
other reason.

### A click runs — through a gate

Every other clickable row in that column moves the viewport. This one splits
panes and types into shells, with no undo. So the click opens a dialog first:

- **vars declared** → `dialogFields`, one field per var. Only the fields the user
  filled in are sent: an empty field means "keep the declared default", and
  sending `""` would *override* that default with an empty string — a
  substitution that types nothing into a step that needed something and fails
  several steps later.
- **no vars** → `dialogConfirm` naming the runbook and its step count.

Two dialogs rather than one because `dialogFields` with an empty field list is a
prompt with nothing to type into and nowhere to put focus (`focusField(inputs[0])`
would deref undefined).

### `pane.open_file` names the local host explicitly

Runbooks live under **catway's own** config directory. `pane.open_file` otherwise
resolves the host from the anchor pane, which in a multi-host session is very
often another machine — where, as `OpenFileParams`' own comment says, "a path is
only half an identity: the same string on two machines is two files". That needed
a `localHostId()` in `14-hosts.js`, beside the existing `hostLabel`/`hostUp`
roster lookups. `""` (roster not landed) omits the param and falls back to the
anchor, which is right in the single-host case that is the only way `""` happens.

### The gap left open, deliberately

A run started by `catctl`, a plugin, an `on:` trigger or another window does
**not** mark a row — only runs this window is awaiting. Closing it means a
per-step down-message, which would feed `emitEvent` → `fireRunbookTriggers` and
hand a runbook an event that its own steps produce. That is the same loop
`broadcastRecord` documents, one phase later. The `⚡` mark and the `⟳` cover the
part that matters — whether the triggers are armed — without inventing it. The
reasoning is written into `runRunbook`'s comment so it is not re-proposed.

## What shipped

### 1. The section (`web/sidebar.go`, `web/css/29-runbooks.css`)

`section(b, "sec-runbooks", "Runbooks", "rb-hctl", "rb-list", true)` — hidden by
default, like Hosts and History.

```
RUNBOOKS                    ⟳ ▼
  ▸ deploy               ⚡  5
  ▸ morning                  3
  ✕ oops
```

- `.rdot` is the **affordance, not a status**: `▸` "this runs" is the one thing a
  reader needs before clicking. `✕` (broken) and `●` (running) are the
  exceptions.
- `.rtrig` `⚡` marks an `on:` clause — accent while armed, `--warn` under
  `li.trig-off` when `trigger_status` is set. That status is suspended-after-a-
  runaway / already-running / switched-off-in-config, all invisible daemon state,
  and this is the only place in the UI it appears at all.
- `li.running .rdot` reuses `dotpulse` from `10-panelist.css` rather than adding a
  third identical keyframe — one blinking mark means one thing across the column.
  `prefers-reduced-motion` drops it.
- `#rb-hctl .refresh` sizing is **repeated** rather than shared: `.refresh` is
  scoped per heading in `06-usage.css`, and a bare `.refresh` selector would be
  the one rule in the sidebar reaching across sections.

### 2. The front end (`web/js/41-runbooks.js`, new)

`41-boot.js` → **`42-boot.js`** so boot stays last and the numbering stays
monotonic; `assets.go`'s `jsFiles` and `cssFiles` updated for both.

- `refreshRunbooks(spin)` — single-flight with a trailing re-run, the shape
  `refreshPaneList` uses, because callers arrive in bursts and the answer costs
  the server a readdir plus a parse of every file. `spin` lights the heading
  control and **only the button passes it**: a mark that turns on its own every
  time a recording stops reads as the section doing work rather than being
  current.
- A failed list leaves the last good rows on screen. Its only failure is "no
  config directory is resolvable", a property of the machine rather than of the
  call — retrying says the same thing, and wiping the section to report it takes
  away what the user was reading.
- `runbookError(rb)` trims the leading file path a load error carries. `Parse`
  prefixes every message with its file because a CLI listing has no other way to
  say which one it means; a row's title has already given the path its own line,
  and repeating an absolute path pushes *which step, and what about it* off the
  readable end. **Only the title trims** — "copy error" keeps the original,
  because a message pasted into an issue has lost the row it came from.
- No step count on a broken row: a `0` there would read as "an empty runbook",
  which is a different and valid thing.

### 3. Wiring

`01-bootstrap.js` (`rbSecEl`, `rbListEl`), `19-messages.js` (the `welcome`
refresh), `40-record.js` (the recording-ended edge), `14-hosts.js`
(`localHostId`), `31-palette.js` (one `run runbook: <name>…` entry per runbook —
broken files excluded, same rule the recorder's palette entry follows: a palette
that lists what it knows will be refused is a palette you stop trusting).

### 4. Docs

`control-api.md` gained a **From the browser** block under *Runbooks*, parallel
to the recorder's, covering the row vocabulary, the run gate, the local-host
rule, and why the section is a query rather than a broadcast.

## Checks

- `make check`'s full sequence run piecewise: `gofmt -l cmd internal` clean,
  `go vet ./...`, `go build ./...`, `go test ./...`, `go vet -tags ghostty ./...`,
  `go test -tags ghostty ./...` — all pass.
- `go test -race -tags ghostty ./cmd/catway/... ./internal/runbook/...` — ok
- `node --check` over the concatenated 42-file bundle, a strict-mode
  `new Function` parse, and a duplicate-declaration sweep over all seventeen new
  identifiers — clean.
- Served stylesheet: braces balance (410/410), all six new selectors present, and
  the block lands after the flags block it follows in `cssFiles`.
- `web_test.go` gained `sec-runbooks` / `rb-hctl` / `rb-list` to the id list and
  `sec-runbooks` to the hidden-by-default table.

### Verified against a live instance, over the browser's own socket

A throwaway RFC6455 client (`scratchpad/wsclient.py`, ~60 lines: handshake,
masked text frames, a frame reader) speaks `/ws` and sends the same
`{t:"cmd", id, name, params}` `sendCmdAwait` sends — so the browser dispatcher
path was exercised, not just the control socket. The init message is
`{t:"init", v:1, cols, rows, dpr, cell_w_px, cell_h_px}`; a wrong first message
yields a `welcome` and then silence.

Four fixture runbooks under an isolated `XDG_CONFIG_HOME`:

| case | result |
|---|---|
| `runbook.list` | all three row shapes — vars, triggers, parse error |
| run with vars | `{steps:[…]}` no `failed` → "2 steps ok" |
| run failing mid-way | `failed:true`, step 2 errored, step 3 `skipped` |
| run a broken one | `ok:false` carrying the load error |
| `host.list` | `local:true` present, so `localHostId()` resolves |

Then `renderRunbooks` itself was run in node against that captured payload
(`scratchpad/rendertest.mjs`: evaluates `41-runbooks.js` alone via `new Function`
with a DOM stub and stubs for the closure's externals, returning the two
functions under test). It checked row classes, glyphs, the hidden-when-empty
flip, and every title string, plus a synthetic `trigger_status` row for the
`trig-off` state.

**That found a real flaw:** the broken row's tooltip printed the absolute path
twice. Fixed by `runbookError` above.

**Not verified on screen.** Chrome rendered an error page for both
`127.0.0.1:8498` and `localhost:8498` — the same failure as the previous session,
almost certainly the extension's site permissions not covering localhost. The
instance was left running:

```bash
# built to the scratchpad, isolated config+state, its own cathost socket
cathost -socket /tmp/cats-rbtest.sock
XDG_CONFIG_HOME=$SP/cfg XDG_STATE_HOME=$SP/state $SP/catway \
  --addr 127.0.0.1:8498 --socket /tmp/cats-rbtest.sock --auth none \
  --state-dir $SP/state/cats --control-socket /tmp/cats-rbtest-ctl.sock
```

## Known limits / next levers

- **The gap above**: rows only light for runs this window started.
- **No keybinding.** The palette is the keyboard route; `20-keys.js` and
  `32-help.js` are untouched, same as the recorder.
- **A runbook run from a browser window acts on the PRIMARY view**, not the
  issuing window: `handleCmd` builds a `NewDispatcherFor` with the view backend,
  but `RunbookRun` then dispatches its steps through a plain
  `app.NewDispatcher(o.session, o)`. Pre-existing, not touched here, but it is
  now reachable by a click rather than only by `catctl`, so it is worth knowing.
- **No delete, no rename, no new-from-scratch.** Recording is how runbooks get
  made and `$EDITOR` is how they get changed; a browser-side YAML editor would be
  a second, worse validator over what the server already checks at load.
- **The section does not show what a runbook will do** before it runs — the vars
  dialog names the count, not the steps. `runbook.list` does not carry them and
  should not; a preview wants the file, which "open in editor" already gives.

## Notes for next time

- `handleCmd` has **no allowlist** — the browser reaches the entire §7
  vocabulary. A new browser feature over an existing command needs no server
  change at all, which is why this whole session is front-end plus one doc block.
- The browser's `/ws` first message is `{t:"init", v:1, …}`. Getting it wrong
  fails *silently*: `welcome` still arrives, commands are simply never answered.
- Adding a sidebar section costs: `sidebar.go`, a css file + `cssFiles`, a js
  file + `jsFiles`, the `01-bootstrap.js` element handles, and **both** lists in
  `web_test.go`. `TestAssetListsMatchDirectories` catches a file added to the
  directory but not to the list.
- A new js file before boot means renaming `NN-boot.js` upward. Second time now;
  it is the established convention rather than an accident.
- Evaluating one js file alone with `new Function(...stubNames, src + "; return {…}")`
  is a cheap way to unit-test front-end render logic without a DOM library — the
  closure's externals become parameters.
- A unix socket under the session scratchpad is still too long for macOS's
  104-char limit; test sockets live in `/tmp`.
