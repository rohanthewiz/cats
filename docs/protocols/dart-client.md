# Dart client generation

`cmd/catgen-dart` emits the mobile client's wire layer from the Go definitions
that already exist in this repo. The output lands in
[cats-mobile](https://github.com/rohanthewiz/cats-mobile)'s `catsproto`
package; a byte-identical copy is committed here and diffed by `make check`.

The point is not to save typing. The protocol is 39 message shapes and 47
commands with their params and results — hand-porting that is ~2500 lines
nobody would keep current, and the failure mode of a stale copy is not a crash.
It is a decode that *succeeds* with a missing value, which surfaces as a blank
row on a phone screen weeks later.

## Hybrid: reflection plus `go/ast`

Neither half suffices alone.

| Source | What it knows |
|--------|---------------|
| `reflect` | field set, JSON tags, `omitempty`, embedded-struct promotion, **type aliases**, Go kinds |
| `go/ast` | doc comments, const-block names and values |

`internal/browserproto/cmd.go` is ~50 type *aliases* into `internal/app`, which
a pure AST walk would have to chase by hand; reflection dissolves them for free.
And the doc comments — the best documentation in this repo — are invisible to
reflection, so they arrive as `///` on the Dart side through the AST.

The unexported case settles it: `internal/orchestration`'s ratatui attribute
bits are lowercase, so reflection cannot see them at all. The alternative to
parsing them is retyping six bit patterns and hoping — and a misplaced bit is
not a crash, it is italics where there should be underline, forever.

## What it emits

| File | Contents |
|------|----------|
| `wire.g.dart` | every message class both directions, the nested types, the `t` constants, `decodeDown`/`decodeUp` |
| `commands.g.dart` | the params/result classes, `CmdName`, `kCommandSpecs`, and `CatsCommands` — 47 typed methods |
| `attrs.g.dart` | the cell attribute bitmask, parsed out of unexported Go consts |
| `codec.g.dart` | the total-decoding helpers every `fromJson` calls |
| `keys.g.dart` | USB HID usage → W3C `KeyboardEvent.code` |

Shape decisions worth knowing:

* **`[]byte` → `Uint8List`** via base64, which is what `encoding/json` does.
* **`json.RawMessage` → `Object?`**, decoded by the caller against the
  command's result type.
* **`Rect [4]uint16` → a real class** with `x`/`y`/`w`/`h`. `panes[i].rect[2]`
  does not say "width", and `anchor[0]` is worse — one is a row, one a column,
  and neither index says which. `[2]uint32` becomes `RowCol` for the same reason.
  Both serialize back to the bare array, so the wire is untouched.
* **Embedded structs are flattened**, exactly as `encoding/json` promotes them —
  `DiffCell{I; Cell}` is `i, s, f, b, m, h` on the wire — and then handed back
  as a group through a generated getter (`Cell get cell`), because Go's
  embedding is a wire-level flattening, not a modelling decision.
* **The `t` discriminator is not a field.** It is a `static const` on the class:
  a caller who could set it could set it wrong.
* **Every `fromJson` is total.** A missing or wrong-typed key yields the zero,
  never a throw. catway may add fields within a protocol version — §0.3 was
  deliberately additive for exactly this reason — and a phone that threw on one
  unexpected shape would drop the socket and every pane with it.

### Discovery is derived, not listed

The message roots are not a hand-written table. The generator walks
`browserproto`'s `Type` const block and asks the **real decoders** what each
value decodes to:

```
MsgWelcome → {"t":"welcome"} → DecodeDown → *browserproto.Welcome
```

So a message type added to the block and wired into `DecodeDown` reaches Dart
with no edit to the generator — and one added to the block but never routed
fails generation. That is the same bidirectional check `TestCommandSpecsRouted`
makes for the command table.

Commands come from `app.CommandSpecs()` the same way, so `ReplyRequired` and
`ParamsRequired` arrive as method signatures: a reply-gated command's Dart
method documents that it always correlates, and an optional-params command takes
`[XParams? params]`.

### Name collisions fail the build

Two Go types cannot silently land on one Dart name, and no generated class may
shadow `dart:core`. `browserproto.Error` would shadow `Error`;
`browserproto.WorkspaceInfo` and `app.WorkspaceInfo` are different types with
one name. Each is renamed through an explicit table, and anything *not* in that
table fails generation with the fix in the message.

`Key`, `Theme`, `Title` and `Image` collide with Flutter widgets instead — left
alone deliberately, because those are the server's names, and the app imports
the package with a prefix.

## Regenerating

```sh
go run ./cmd/catgen-dart -out cmd/catgen-dart/testdata/golden
go run ./cmd/catgen-dart -out ../cats-mobile/packages/catsproto/lib/src/generated
```

Both directories get the same bytes. The files carry `// dart format off`, so
the golden and the shipped copy stay identical — otherwise there would be two
artefacts of one generation and the app repo's drift gate could only compare the
formatted one, which is a transformation away from what actually drifted.

The key table needs a Flutter SDK and is regenerated only with one:

```sh
go run ./cmd/catgen-dart -out <dir> -flutter-root "$FLUTTER_ROOT"
```

## Three drift gates

1. **Here.** `TestGoldenIsUpToDate` regenerates in memory and diffs the
   committed output, so adding a field to a wire struct without regenerating
   fails **cats's** `make check`, in the repo where the change happened.
   `TestGeneratorIsDeterministic` runs the pipeline four times, because a
   missed sort over a Go map would turn the golden diff into a coin flip.
2. **In cats-mobile.** CI regenerates at the pinned `CATS_REV` and fails on
   `git diff`.
3. **At runtime.** A `welcome.v` mismatch shows "app update required" rather
   than a stream of decode failures — catway requires exact equality and closes
   the socket, so there is no partial-compatibility mode to guess at.

## The key table

A Flutter client receives a raw key as `PhysicalKeyboardKey.usbHidUsage`; catway
wants a W3C `KeyboardEvent.code`, because `internal/inputenc` converts exactly
that into libghostty's key names. The bridge is 269 entries.

It is not hand-written. Flutter generates its own `PhysicalKeyboardKey`
constants from `dev/tools/gen_keycodes/data/physical_key_data.g.json`, where
`names.name` **is** the W3C code and `scanCodes.usb` **is** `usbHidUsage`. The
generator reads that file, so the table cannot disagree with the Flutter side.

`internal/inputenc`'s `TestGeneratedKeyCodesParse` closes the loop from the
other end, on every `make check`: it reads the committed table and puts all 269
codes through the real `w3cKeyName` → `libghostty.ParseKey`. 162 reach the
encoder; the other 107 are gamepad buttons, USB error sentinels, brightness and
media keys — the whole HID keyboard page, which is much larger than "keys a
terminal can encode". Those are listed explicitly rather than skipped, so the
partition can only move by an edit: a code that *stops* parsing is a regression
in `w3cKeyName`, and one that *starts* means ghostty grew support.

They are not filtered out of the generated map. The phone should report honestly
which key was pressed, and catway's `KeyUnidentified` fallback is the correct
answer for a key with no VT sequence.
