# Session: Mirror the generated wire layer into cats-mobile

- **Session ID:** `4c30345f-b26e-4e30-a215-a1ddaee6aa32`
- **Date:** 2026-08-03
- **Branch:** main (cats at `52d63cd`, unchanged by this session)
- **Repos:** `cats` (read-only here) → `cats-mobile` (cloned to `~/projs/go/cats-mobile`)

Picks up the open item left by
`2026-0803-1026-copilot-cli-support-in-cats.md`.

## Request

> cats-mobile is now available at https://github.com/rohanthewiz/cats-mobile.
> Please push up the needed file.

…followed by *install dart and run the tests*.

## The premise that did not survive

The prior session logged the remaining work as "`wire.g.dart` needs mirroring —
doc-comment only, so nothing functional drifts meanwhile." That was true of the
**copilot** change in isolation, but wrong about the checkout: cats-mobile was
pinned at `CATS_REV = 8c3738b`, several commits behind. Regenerating showed two
files stale, not one, and the drift was functional:

| File | Drift |
|---|---|
| `wire.g.dart` | `AgentItem.model`; `Usage.memory` + `Usage.read_at`; the `PaneAgent` doc-comment correction |
| `commands.g.dart` | the `usage.refresh` command |
| `CATS_REV` | `8c3738b` → `52d63cd` |

`attrs.g.dart` and `codec.g.dart` were already current. `keys.g.dart` untouched.

## Method

The repo ships `tool/regen.sh`, which does the generate **and** re-pins
`CATS_REV` from the cats checkout's `git rev-parse HEAD`. Used it rather than
copying files by hand, so the pin cannot silently disagree with the bytes.

Two checks before generating, so the pin is honest:

1. `git status` on cats is clean — HEAD really describes the working tree.
2. Generator output is byte-identical to `cmd/catgen-dart/testdata/golden/`.
   The golden test guards cats's side; comparing to it proves cats itself is not
   mid-drift.

`FLUTTER_ROOT` is unset, so `keys.g.dart` was left as committed — the script
says so on stderr, and its input (the SDK's `physical_key_data.g.json`) only
moves on an SDK upgrade.

## The one thing that was not mechanical

`packages/catsproto/lib/src/session.dart:75` rebuilds an `AgentItem` when a
`pane_agent` arrives:

```dart
case final PaneAgent m:
  paneAgents[m.pane] = m;
  // Patch the rollup in place. The server re-sends the whole rollup on
  // any change too, but this arrives first and the roster should not lag
  // a round trip behind the pane it is describing.
```

`model` is new and defaults to `''`, so the rebuild compiled fine and silently
blanked the label until the next full rollup — reintroducing the exact lag the
patch exists to remove, for the field most likely to have just changed. A
generated-code mirror that also needed a hand edit; nothing in the diff flags
it, because a defaulted field is precisely what does not break the build.

Fixed by taking it from the message rather than carrying it over from the old
item. That direction is deliberate and matches the web client
(`cmd/catway/web/index.html:2073`, `p.agentModel = msg.model || ""`): an agent
whose model stops resolving reports it **absent**, and a stale model must not
outlive the report it came from.

## Verification

No Dart SDK existed on the machine, so the first push went out analyzed-by-eye
and explicitly reported as unrun. Then, on request:

- `brew install dart-sdk` → 3.12.2, symlinked into `/opt/homebrew/bin`, so it is
  on the default PATH with no shell config change. Satisfies the workspace's
  `sdk: ^3.10.0`.
- `dart pub get` — resolves the workspace natively (the root pubspec's stated
  reason for having no melos).
- `dart analyze` — no issues, whole workspace.
- `dart test` — 70/70 in `packages/catsproto`.
- **The new test earns its place**: reverted `model: m.model`, re-ran, watched it
  fail, restored. Tree then clean against the pushed commits, so the remote is
  exactly what was tested.

`pubspec.lock` and `.dart_tool/` were already gitignored — `pub get` left
nothing untracked.

## Pushed

`rohanthewiz/cats-mobile` main, `8e2fd64..f5bf391`:

| Commit | Change |
|---|---|
| `6cccc5f` | `chore(catsproto): regenerate the wire layer against cats 52d63cd` |
| `f5bf391` | `fix(session): the pane_agent rollup patch carries the model` (+ test) |

## Notes

- **cats itself was not modified this session.** Its golden testdata was already
  in sync; only the mobile mirror was behind.
- **`make macapp` still not run** — carried over, untouched here. The installed
  app remains on the old build.
- **Usage/quota for copilot still deferred**, unchanged from the prior session.
  Worth noting the mirrored `Usage` now carries `memory` and `read_at`, but is
  still single-account — the `browserproto.Usage`-carries-two-accounts blocker
  stands.
- **Regen is a two-repo ritual with no CI enforcing it.** `CATS_REV` is the only
  record of how far behind the mirror is, and nothing fails when it drifts —
  which is how a "doc-comment only" item turned out to be three changes.
