# Session: A Record Button for the Macro Recorder

- **Session ID:** `b3eeaf0c-5da1-4279-bd52-ccdc7db7f9d2`
- **Date:** 2026-08-31
- **Branch:** main
- **Repo:** `cats`
- **Landed as:** `6459ef8` — *record: a record button for the macro recorder, lit in every window*

## Request

> Where would be a good place to add a record (macro) button?

and, after the recommendation:

> build it

So the session is two halves: a siting question answered from the existing code,
then the build.

## The siting question

`runbook.record start|stop|cancel|status` has existed since the recorder phase
(`cmd/catway/record.go`, `internal/app/record.go`) and was **catctl-only** —
`grep -r runbook cmd/catway/web/` returned nothing at all.

**Answer: the `#statusbar` cluster, between `chatbtn` and the `⚙`.**

The argument, in the order it was made:

1. **Recording is a mode, not a door.** Every other item in that cluster opens
   something and goes back to looking the way it did. The recorder is a state
   the whole session is in, and one you can walk away from — so the affordance
   that arms it has to be the one that keeps saying it is armed. A gear-menu
   item can *start* a recording; nothing in a menu can *show* one.
2. **The docs already name the failure.** `control-api.md`: "a recorder that
   silently captured nothing … is otherwise indistinguishable from one that is
   working". That is a visibility problem, and a toolbar slot is the fix.
3. **There is a precedent for earning the slot.** `mainarea.go` already writes
   down why plugins is top-level rather than in the gear menu: "installing and
   running plugins is routine work, not a settings excursion." Arming a recorder
   mid-task is the same shape.

Rejected sites, and why: a **pane header / context menu** (recording is
session-scoped — it *anchors* on the focused pane but is not about it); the
**gear menu alone** (starts fine, shows nothing); a **sidebar section** (a
Runbooks section is a real future thing, but the indicator has to survive
`setSidebarHidden(true)`).

## The one design change between the proposal and the build

The recommendation said to add a `record_changed` event to
`internal/app/events.go`. **It was not added, deliberately.**

`emitEvent` feeds `fireRunbookTriggers`, and the step counter ticks once per
captured command. A trigger on a per-step event would run a runbook whose own
steps are recorded — recording is *why* the steps are being counted — which is a
loop with a recording at the bottom of it.

It is a plain browser broadcast instead, and that is sufficient because **every
verb funnels through `RunbookRecord` on the loop goroutine no matter who
called it**: a browser click, `catctl record start`, a plugin, a relayed
command. So arming from the CLI lights the indicator in every open window with
no event vocabulary involved. Automation clients already have
`runbook.record {action:"status"}`, which answers exactly this question.

The reasoning is written into `broadcastRecord`'s own comment so the next reader
does not re-propose the event.

## What shipped

### 1. The wire (`internal/browserproto`)

`MsgRecord Type = "record"` + a `Record` struct
(`recording`, `steps`, `started_at`, `note`) + `NewRecord` + the `DecodeDown`
case. Added within protocol v1 — an old client ignores the type and draws the
UI it had before.

`steps` is on the wire because "armed" and "armed and actually capturing
something" are the two states that look identical from outside, which is the
whole reason `status` exists.

### 2. The recorder announces itself (`cmd/catway/record.go`)

| addition | why |
|---|---|
| `macroRecorder.notify func()` | wired to `o.broadcastRecord` in `Recorder()`. A callback, not a back-pointer to `orch`: the recorder's only power over the outside world is to say something changed |
| `(*macroRecorder).reset(next)` | the three `*m = macroRecorder{…}` assignments would each have silently dropped `notify` — freezing the indicator in every browser with nothing erroring. `reset` carries it across |
| `(*macroRecorder).changed()` | called on start / stop / cancel, on every `Commit`, and once when a ceiling is first hit |
| `o.recordMsg()` / `o.broadcastRecord()` | an idle recorder produces an ordinary "not recording" message, never silence |

`Commit` announces, not `Begin` — announcing on `Begin` would show a step that a
`Fail` is about to take away. The ceiling guard is `if m.full == ""` so a
recording that runs on past its limit does not re-toast once per command.

### 3. The connect burst (`cmd/catway/catway.go`)

`o.send(c, o.recordMsg())` — **unconditionally**, including when idle. A window
that reconnects was never reloaded and is still drawing whatever it last saw, so
"nothing to say" is exactly the case that leaves an indicator lit over a
recording stopped from somewhere else.

### 4. The button (`cmd/catway/web/mainarea.go`, `css/24-toolbar.css`)

```html
<span class="tbtn" id="recbtn" title="record a macro (runbook.record)">
  <span class="tmk"><span class="g">◦</span></span>rec<span class="n"></span>
</span>
```

- Idle: hollow `◦ rec`, muted, invisible to a session that never records.
- Armed (`.on`): the mark fills to `●`, takes `--err`, and pulses; the button
  takes a 14% `--err` ground; the label grows a live count — `● rec 12`.
- `.on.empty`: nothing captured yet colours the count `--warn`. That is the one
  failure the recorder cannot raise as an error — you are typing into a shell,
  and keystrokes are not vocabulary.
- The pulse is on the **glyph**, not the button: a background animating under a
  row of otherwise-still controls reads as a fault. `prefers-reduced-motion`
  drops it; the fill, tint and count still carry the state.
- `.g` mirrors the gear's own wrapper so the animation has a glyph-sized box
  rather than the padded hit target. `#recbtn .n:empty { display:none }` because
  an empty flex item still claims the row's 6px gap.

### 5. The front end (`js/40-record.js`, new)

`40-boot.js` → **`41-boot.js`** so boot stays last and the numbering stays
monotonic; `assets.go`'s `jsFiles` updated for both.

- `applyRecord(msg)` — the only writer of `recState`. Holds **no optimism**: it
  draws what it was last told, never what this window just clicked. The ceiling
  note toasts on the *edge* only, since the server re-sends it with every
  subsequent broadcast.
- `renderRecord()` — classes, glyph, count, and an armed `title` carrying
  "recording since 4:31:07 PM — 12 commands captured". The time is formatted
  client-side because that is the side that knows the reader's locale.
- Idle click arms directly (nothing exists yet, nothing to confirm); armed click
  opens a menu. Both ways **out** are gated: stop needs a name, cancel destroys
  work.
- `stopRecording()` handles the name collision by **letting the server refuse
  and offering a replace confirm**, rather than by carrying an overwrite
  checkbox. The browser has no listing of the runbook directory, so a checkbox
  would ask about a collision that usually is not there — and a failed stop
  leaves the recording armed by design, so declining costs nothing.
- The empty name is refused client-side, because the server's refusal for a
  missing name would be indistinguishable from its refusal for a taken one.

Wired in `01-bootstrap.js` (`recBtnEl`), `19-messages.js` (the `record` case),
`41-boot.js` (the click), and `31-palette.js` — which lists **only the verb that
applies**, since a palette that offers commands it knows will be refused is a
palette you stop trusting.

### 6. Dart + docs

`Record` collides with `dart:core`, and the generator hard-fails on that by
design. Added `browserproto.Record → RecordMsg` to `dartNameOverrides`, same as
`Error → ErrorMsg`, and regenerated: `wire.g.dart` only, +70 lines, additive.

`browser-protocol.md` gained the `record` row; `control-api.md` gained a
**From the browser** paragraph under *Recording one*.

## Checks

- `make check`'s full sequence, run piecewise: `gofmt -l cmd internal` clean,
  `go vet ./...`, `go build ./...`, `go test ./...`, `go vet -tags ghostty
  ./...`, `go test -tags ghostty ./...` — all pass.
- `go test -race -tags ghostty ./cmd/catway/... ./internal/browserproto/...` — ok
- `node --check` over the concatenated bundle, plus a grep for duplicate
  declarations of all eleven new identifiers — clean.
- Two new tests in `record_test.go`:
  - `TestRecordAnnouncesEveryChange` — the notifier fires on start / commit /
    stop / cancel, stays silent on a query, and **survives `reset`**.
  - `TestRecordReachesEveryWindow` — a real registered `client` (the
    `newConn`/`drain` harness from `multiclient_test.go`) gets the idle state in
    its connect burst, then start / one captured command / cancel.
- `web_test.go`'s id list gained `recbtn`.

**Not verified on screen.** An isolated catway was built into the scratchpad and
run against its own cathost — `curl` → 200 — but Chrome rendered an error page
for `localhost:8499` on three attempts, most likely the extension's site
permissions not covering localhost. The instance was left running for the user:

```bash
# built to the scratchpad, isolated state + its own cathost socket
cathost -socket /tmp/cats-rectest.sock
XDG_CONFIG_HOME=$SP/cfg XDG_STATE_HOME=$SP/state $SP/catway \
  --addr 127.0.0.1:8499 --socket /tmp/cats-rectest.sock --auth none \
  --state-dir $SP/state/cats --control-socket /tmp/cats-rectest-ctl.sock
```

## Known limits / next levers

- **No keybinding.** The palette is the keyboard route; `20-keys.js` and
  `32-help.js` are untouched.
- **The menu does not list what was captured.** `RunbookRecordResult.Commands`
  already carries it and `status` is one round trip away — the count plus the
  amber empty-state covers "is it working", but "what exactly am I about to
  save" still costs a `catctl record status`.
- A **Runbooks sidebar section** (list / run / edit) is the natural next surface;
  the recorder deliberately did not go there.

## Notes for next time

- A unix socket under the session scratchpad is **too long for macOS** (104-char
  limit) — `bind: invalid argument`. Test sockets have to live in `/tmp`.
- `cmd/catway/web/` embeds its assets, so any front-end change needs a rebuild
  and a server restart to look at. There is still no dev-serve flag.
- Adding a `browserproto` down-message costs four edits — the `Type` const, the
  struct + constructor, the `DecodeDown` case, and a golden regen
  (`go run ./cmd/catgen-dart -out cmd/catgen-dart/testdata/golden`). The
  generator catches a `dart:core` collision for you.
- `emitEvent` → `fireRunbookTriggers` means **every new event is a potential
  trigger**, and a high-frequency one is a potential loop. Worth asking of any
  future event before adding it.
