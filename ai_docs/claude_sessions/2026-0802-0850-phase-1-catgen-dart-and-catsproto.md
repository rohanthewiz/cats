# Session: Phase 1 — `cmd/catgen-dart` and the `catsproto` package

- **Session ID:** `e22f6ab6-4b97-4153-aae0-119bb9f771fe`
- **Date:** 2026-08-02
- **Branch:** main (from `8c3738b`)
- **Repos:** `cats` (`1c455af`), **`cats-mobile` (new, `8e2fd64`)**

## Request

> next do phase 1

Phase 1 of the Flutter mobile client plan
(`~/.claude/plans/what-would-it-take-peaceful-treehouse.md`) is ~23 days across
two repos. Asked for the slice; answer was **generator + scaffold `cats-mobile`
with `packages/catsproto`, no Flutter UI yet**.

---

# Part 1 — `cmd/catgen-dart` (in `cats`)

## Why generated

39 message shapes, 47 commands with their params and results. Hand-porting is
~2500 lines nobody keeps current — and a stale copy does not crash. It decodes
*successfully* with a missing value, which surfaces as a blank row on a phone
screen weeks later.

## Why hybrid, and why go/ast is not optional

| Source | What it knows |
|---|---|
| `reflect` | field set, JSON tags, `omitempty`, embedding, **aliases**, Go kinds |
| `go/ast` | doc comments, const-block names and values |

`internal/browserproto/cmd.go` is ~50 type *aliases* into `internal/app`, which a
pure AST walk chases by hand and reflection dissolves for free. The doc comments
— the best documentation in this repo — are invisible to reflection.

The tiebreaker: `internal/orchestration`'s ratatui attribute bits are
**unexported**. Reflection cannot see them at all, so the alternative to parsing
the source is retyping six bit patterns and hoping. A misplaced bit is not a
crash; it is italics where there should be underline, forever.

## Nothing is enumerated by hand

The message roots are *derived*: walk `browserproto`'s `Type` const block, then
ask the real decoders what each value decodes to.

```
MsgWelcome → {"t":"welcome"} → DecodeDown → *browserproto.Welcome
```

A type added to the block and wired into `DecodeDown` reaches Dart with no edit
to the generator; one added but never routed **fails generation**. That is the
same bidirectional check `TestCommandSpecsRouted` makes for commands — which is
itself where the 47 typed methods come from, so `ReplyRequired` and
`ParamsRequired` arrive as method signatures rather than as prose.

## Shape decisions

- **`Rect [4]uint16` → a real class.** `panes[i].rect[2]` does not say "width",
  and `anchor[0]` is worse — one is a row, one a column. `[2]uint32` becomes
  `RowCol` for the same reason. Both serialize back to the bare array.
- **Embedded structs flatten** exactly as `encoding/json` promotes them
  (`DiffCell{I; Cell}` → `i, s, f, b, m, h`), then are handed back through a
  generated getter (`Cell get cell`) — Go's embedding is a wire-level
  flattening, not a modelling decision.
- **The `t` discriminator is a `static const`, not a field.** A caller who could
  set it could set it wrong.
- **Every `fromJson` is total.** Missing or wrong-typed → the zero, never a
  throw. catway may add fields within a version (§0.3 was deliberately
  additive), and a phone that threw on one unexpected shape would drop the
  socket and every pane with it.
- **No `==`/`hashCode`.** Deep equality over the List/Map fields needs
  `package:collection`, and this package takes no runtime deps; round-trip tests
  compare `jsonEncode(toJson())`, which is the thing that has to match anyway.

## Two guards that fail the build, not the app

- **Name collisions.** `browserproto.Error` would shadow `dart:core`'s;
  `browserproto.WorkspaceInfo` and `app.WorkspaceInfo` are different types with
  one name. Each is renamed through an explicit table; anything *not* in it stops
  generation with the fix in the message. `Key`/`Theme`/`Title`/`Image` collide
  with Flutter widgets instead — left alone, because those are the server's
  names, and the app imports with a prefix.
- **`// dart format off`** on every emitted file, so the golden in `cats` and the
  copy `cats-mobile` ships are the **same bytes**. Otherwise there are two
  artefacts of one generation and the app repo's gate could only compare a
  transformation of what actually drifted. Confirmed: `dart format` reports
  0 changed.

## The USB-HID key table

A Flutter client receives `PhysicalKeyboardKey.usbHidUsage`; catway wants a W3C
`KeyboardEvent.code`. 269 entries, transcribed **by a program** from
`$FLUTTER_ROOT/dev/tools/gen_keycodes/data/physical_key_data.g.json` — the same
file Flutter generates its own constants from, so the two cannot disagree
(`names.name` *is* the W3C code, `scanCodes.usb` *is* `usbHidUsage`).

Regenerating needs `-flutter-root`. That input changes only on an SDK upgrade,
so making cats's build depend on a Flutter checkout would buy no protection —
what runs every build is the check on the **committed** table.

---

# Part 2 — `cats-mobile` / `packages/catsproto` (new repo)

Dart 3.10 pub workspace, zero runtime dependencies, `dart test` in ~1 s.

| File | What |
|---|---|
| `lib/src/generated/*.g.dart` | emitted by `catgen-dart`, byte-identical to the golden |
| `connection.dart` | handshake, `invoke` correlation, timeouts, `Backoff` |
| `endpoint.dart` | pairing URI, DER cert pinning, `TrustStore` |
| `grid.dart` | typed-array cell store, diff apply, dirty rows |
| `session.dart` | the down-message fold; roster ordering |
| `sha256.dart` | FIPS 180-4, ~90 lines |

**SHA-256 is hand-rolled** rather than `package:crypto`. It is needed for exactly
one thing — hashing a certificate's DER against the pin learned at pairing — and
90 lines of a test-vectored algorithm is a smaller liability than a dependency
in the package that has to keep working when everything else is stale. Same
argument the in-repo QR encoder made last session.

## The four layers that stop the phone resizing the desktop

catway takes the session grid from the *first* connection that declares one and
shares it with every other, so a phone honestly reporting 40×20 reflows every
pane at the desk. This is the one failure mode where a bug in the phone breaks
the user's *desktop*, which is what the belt-and-braces buys.

1. `CatsConnection` builds the `Init` itself — hardcoded zeros, `viewer: true`.
   App code cannot supply one.
2. `send()` refuses a `Resize` (and a second `Init`).
3. The generator marks `Resize` `@Deprecated`; `analysis_options.yaml` promotes
   `deprecated_member_use` to an **error**.
4. `viewer_mode_test.dart` reads the source and fails on any `Resize(` or
   non-zero `cols:`/`rows:` under `lib/`.

Both cell-metric guards are asserted, not just the grid: `registerConn` has two
separate `> 0` checks and clearing one is the easy mistake.

## `dart format` moves an `// ignore:` off its target

The formatter reflowed `expect(() => conn.send(const Resize(...)), …)` onto a
later line, silently detaching the `// ignore: deprecated_member_use_from_same_package`
above it. Fixed by giving the construction its own statement. Worth knowing: a
silenced lint that drifts off its target stops silencing without saying so.

---

## Verification

`make check` fully green including the race suite. `dart analyze` clean,
`dart format --set-exit-if-changed` clean, **69 Dart tests**.

### Neutering

| neutered | failure |
|---|---|
| added a field to `browserproto.Clients` | `wire.g.dart is stale. first difference at line 347` |
| broke `w3cKeyName`'s F-key case | `"F3" → "f_3" no longer reaches ghostty` (+F2, F4, F13) |
| put `cols: 120` in `lib/src/session.dart` | `session.dart:165: declares cols: 120` |

### Live, against a real catway

`--tls` on `:18477`, paired with a real `catctl pair` QR link:

| Step | Result |
|---|---|
| parse the real `cats://pair` URI | **caught a bug — see below** |
| our SHA-256 over the served DER vs the advertised fingerprint | **`pinned`** — identical |
| redeem the single-use grant → `POST /login`, `Accept: application/json` | session |
| WSS + `Authorization: Bearer` | `welcome v1 caps=[viewer, key.pane, clients]` |
| `pane.list`, `session.get` via the generated typed methods | answered |
| `pane.send_input{submit:true}` then `capture{unwrap:true}` | **saw its own echo** |
| `session.get` before/after everything | `active_workspace`/`focused_pane` **unchanged** |

**The assertion that matters most.** With a 200×60 "desktop" already attached:

```
sizer alone:      total=1 sizers=1 grid=200x60
phone attaches:   total=2 sizers=1 grid=200x60   ← neither sized nor moved it
phone detaches:   total=1 sizers=1 grid=200x60
```

### The bug the live run caught

`Endpoint.parsePairUri` read `url` / `token` / `fp`. `catctl`'s `pairURI` mints
**`u` / `t` / `f`**. The short keys are not arbitrary — the URI has to fit a
scannable QR, and `TestPairURIFitsAQRCode` pins a typical one to version 8.

`PairGrant.expiresAt` went too, for the same reason it is absent from the URI:
the grant is five minutes and single use, and the honest signal that it ran out
is the 401 from redeeming it — a path the app handles regardless. A second
source of truth for that fact is only a way to disagree with the server about
whether the code still works.

`tool/live_probe.dart` now walks the whole first-launch path in one command, so
the next mismatch of this kind is found by running it rather than by a user.

---

## Deliberately not done

- **No Flutter app.** `packages/cats_mobile` is commented out of the workspace.
  The five screens (Roster, Pane detail, Compose, Inbox, Settings) are the next
  slice.
- **No endpoint racing.** `EndpointKind.relay` exists as a type; the LAN/relay
  race and per-network caching wait for the relay to exist (Phase 2).
- **No reconnect driver.** `Backoff` is built and tested; the loop that owns a
  socket's lifetime belongs with the app's lifecycle observers.
- **`tool/{record,bench}.dart` not written.** `bench.dart` is a named week-one
  risk (full-frame parse cost) and should land before the Phase 3 painter is
  built on an estimate.
- **No `TranscriptConnection`.** The seam is there — `CatsSocket` is an
  interface and `FakeSocket` already drives the real fold — but no recorded
  JSONL yet.

## Two limits, stated

- **107 of 269 key codes do not reach ghostty.** Gamepad buttons, USB error
  sentinels, brightness/media, IME language keys — the whole HID keyboard page
  is much larger than "keys a terminal can encode". They are *not* filtered out
  of the generated map: the phone should report honestly which key was pressed,
  and `KeyUnidentified` is the correct answer for a key with no VT sequence.
- **The generated classes have no value equality.** Riverpod `select` and
  rebuild-avoidance will notice. It is a generator change, not a rewrite, and
  the plan already puts the render path outside state management.

## Where this leaves the plan

Phase 0 complete. **Phase 1's protocol half is done**: the wire layer is
generated, gated three ways, and proven against a live server end to end.

Next is the Flutter app itself — `packages/cats_mobile`, the five screens, and
the deps the plan names (`flutter_riverpod`, `flutter_secure_storage`,
`connectivity_plus`, `app_links`, `flutter_local_notifications`). Screen A
(Roster) is the one that answers "is anything blocked?" and is the ~week-5
checkpoint.

## Files

**New in `cats`**
- `cmd/catgen-dart/{main,source,types,emit,keys}.go` — the generator
- `cmd/catgen-dart/golden_test.go` — the drift gate
- `cmd/catgen-dart/testdata/golden/*.g.dart` — the committed output
- `internal/inputenc/keycodes_ghostty_test.go` — the key-chain test
- `docs/protocols/dart-client.md`

**Modified in `cats`**
- `mkdocs.yml`, `docs/index.md`, `docs/protocols/index.md`

**New repo: `cats-mobile`** (`~/projs/go/cats-mobile`)
- `pubspec.yaml`, `CATS_REV`, `README.md`, `.gitignore`, `tool/regen.sh`
- `packages/catsproto/` — the package above, plus 5 test files and
  `tool/live_probe.dart`
