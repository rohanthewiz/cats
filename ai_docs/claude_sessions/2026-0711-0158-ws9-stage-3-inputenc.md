# WS9 Stage 3 — structured input encoding (`internal/inputenc`)

**Session id:** `0b0f1ccc-8494-4e48-a6a2-7ce27e92d267` (same session as stages 1–2, doc
`2026-0711-0136-ws9-stage-1-2-browserproto.md`; this doc covers the Stage 3 continuation)
**Date:** 2026-0711-0158 · **Repo:** this repo only (branch `roh/phase-b`); herdr (Rust)
read for reference, untouched.
**Continues:** `2026-0711-0136-ws9-stage-1-2-browserproto.md`.

> **Implementation session.** Executed WS9 Stage 3 (D4: structured input, server-side VT
> encoding) from `ai_docs/phase-c-ws9-tasks.md` — spike (3.1), encoder package (3.2);
> 3.3 (`raw` escape hatch) was already satisfied by Stage 2. Stage 3 checked off.

---

## Commits

- herdr-web `2d6c583` **feat(inputenc): structured input encoding via go-libghostty
  (WS9 stage 3)** — 5 files, +1,015 lines.

## The spike (3.1): wrap, don't port

go-libghostty (pinned `v0.0.0-20260528200934-790a3ff6e9f6`) exposes ghostty's real
encoders as CGO wrappers over `ghostty/vt.h`:

- **`KeyEncoder`** — options for all five kitty flag bits (disambiguate=1,
  report-events=2, report-alternates=4, report-all=8, report-associated=16),
  `ModifyOtherKeysState2`, `CursorKeyApplication` (DECCKM), keypad application,
  `AltEscPrefix` (mode 1036), backarrow, macOS option-as-alt. `KeyEvent`: W3C physical
  key enum + mods + press/repeat/release + UTF-8 text + unshifted codepoint +
  consumed mods.
- **`MouseEncoder`** — tracking modes none/X10/normal(1000)/button(1002)/any(1003),
  formats X10/UTF-8(1005)/SGR(1006)/URxvt/SGR-Pixels, surface-space px positions with a
  size context, any-button-pressed, motion dedup. Wheel = press of buttons four–seven
  (→ 64–67 on the wire; pinned by the vendored `mouse_encode.zig` test). X10 *mode*
  strips modifiers; legacy release encodes as button 3 (`buttonCode`, mouse_encode.zig:200).
- **`PasteEncode`** — bracketed-paste wrap + sanitization (unsafe control bytes → spaces;
  `\n`→`\r` when unbracketed). Richer than Rust's plain wrap; kept.

All encoder symbols verified present in the **vendored** static lib
(`nm herdr/vendor/libghostty-vt/zig-out/lib/libghostty-vt.a`). Decision rule from the
task doc ("if yes, wrap it") applied → **no Rust port** (an Explore inventory sized the
port at ~1,100–1,200 Go lines; report also confirmed the Rust differential matrix
deliberately omitted kitty bits 2/8 — the C encoder is strictly more capable).

**CGO implication (recorded):** the server binary builds with `-tags ghostty` +
`PKG_CONFIG_PATH=<herdr>/vendor/libghostty-vt/zig-out/share/pkgconfig` — the same
prebuilt-lib prerequisite as the termhost daemon; no Zig at Go-build time. Encoder and
emulator come from the same library, so they cannot drift.

## The package (3.2): `internal/inputenc`

- **`inputenc.go`** (pure, untagged): `w3cKeyName` — mechanical W3C
  `KeyboardEvent.code` → libghostty snake_case (`KeyA`→`key_a`, `Digit0`→`digit_0`,
  `LaunchApp1`→`launch_app_1`; F-keys keep digits attached: `F12`→`f12`) for
  `libghostty.ParseKey`. `keyText` (single printable rune = produced text; named keys
  none). `unshiftedCodepoint(code, key, altHeld)` — **layout-aware**: letters from key
  text lowercased ("й" on Russian layout) UNLESS alt held (option-compose: "å" must
  resolve to physical 'a' so option-as-alt emits ESC-a); digits/punct from physical code
  (US-layout assumption for punct, advisory only). `AlternateScrollActive` +
  `EncodeAlternateScroll` — ghostty Surface rule byte-for-byte (Surface.zig:3506): alt
  screen + mode 1007 + no mouse reporting ⇒ wheel → `\x1b[A/B` (or SS3 `\x1bOA/OB`
  under DECCKM), one per line.
- **`encoder.go`** (`//go:build ghostty`): per-pane `Encoder` (not concurrent-safe;
  server serializes pane input). `SetModes(terminal.InputModes)` maps the β pane_modes
  mirror onto encoder options (kitty flags, mOK state 2, DECCKM, tracking mode, format).
  `SetGrid` bounds mouse coords (1px cells ⇒ cell coords pass through). `Key` sets
  action/key/mods/utf8/unshifted/consumed-shift, `runtime.KeepAlive` for the uncopied
  utf8 string, clears the C-side pointer after. `Mouse` tracks a pressed-button bitmask
  for any-button-pressed; wheel emits per-line presses (four/five vertical, six/seven
  horizontal), with the alternate-scroll/viewport-scroll (`nil`) fallbacks when
  reporting is off. `Paste` → `PasteEncode(text, modes.BracketedPaste)`.
- **Two defaults pinned** where the standalone encoder differs from a live terminal
  (both found by failing goldens): `OptionAsAltTrue`, and `AltEscPrefix=true` — DEC mode
  1036 defaults ON in the terminal (`modes.zig:289 .default = true`) but OFF in the
  encoder (`key_encode.zig:31`). β doesn't mirror 1036 changes — acceptable: herdr's
  Rust encoder unconditionally ESC-prefixed alt too.

### Tests (all green, tagged + untagged, full repo both ways)

- Pure: `w3cKeyName` (25 codes incl. F/Fn/digit-run edges), `keyText`,
  `unshiftedCodepoint` (layout + alt cases), alternate-scroll active/encode.
- Tagged goldens keyed to the WS0-B2 matrix dimensions **plus the previously-degraded
  cases**: legacy (C0 ctrl map, `\x1b[Z` BackTab, modified specials `\x1b[1;2A`/
  `\x1b[3;5~`/`\x1b[15;6~`, alt→ESC-a incl. macOS "å"), DECCKM SS3 (and
  kitty-ignores-DECCKM), modifyOtherKeys `\x1b[27;5;13~`/`\x1b[27;2;13~` (retires the
  Rust XTMODKEYS Enter special-case), kitty: `\x1b[27u` bare Esc, `\x1b[99;5u`,
  event-type suffixes `:2`/`:3` (bit 2), report-all `\x1b[97u`/`\x1b[13u` (bit 8),
  alternates `\x1b[97:65;2u` (bit 4). Mouse: SGR press/release/drag/motion/mods,
  X10-bytes format + release-as-3 + X10-mode mod-stripping, UTF-8 wide coord
  (`é` = 233), wheel 64–67 + multi-line, gating by tracking mode. Paste both modes.
  Mode-switch-mid-stream.
- Run tagged: `PKG_CONFIG_PATH=<herdr>/vendor/libghostty-vt/zig-out/share/pkgconfig
  go test -tags ghostty ./internal/inputenc/`.

## Next

- **Stage 4** (last): proof harness — gateway spike speaking browserproto to the
  browser, panes directly from the termhost daemon over β, two-pane split from
  `internal/layout`, minimal JS (per-pane canvases, packed-u32 resolve, structured
  key/mouse senders through `inputenc`), acceptance incl. htop mouse/kitty keys +
  daemon-restart resync. All three inputs (stages 1–3) ready.
- Still flagged, not done: live_handoff harness fix (leaked attached `herdr server`
  clients defeat the daemon idle reaper).
