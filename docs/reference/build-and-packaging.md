# Build and packaging

## The two build worlds

```mermaid
flowchart TB
  subgraph plain["Untagged — plain go build ./..."]
    P1["catctl"]
    P2["orchestration/protocol.go — the wire contract"]
    P3["terminal/terminal.go — the Emulator interface"]
    P4["app · layout · workspace · config · persist<br/>ctlproto · browserproto · detect · integration<br/>plugin · gwauth · gwtls · worktree · startdir · buildinfo"]
  end

  subgraph tagged["-tags ghostty + PKG_CONFIG_PATH"]
    T1["catway"]
    T2["cathost"]
    T3["orchestration/host.go — the pane runtime"]
    T4["terminal/ghostty.go — the emulator"]
    T5["inputenc/encoder.go — the key/mouse encoders"]
  end

  subgraph cgoonly["cgo, but no ghostty tag"]
    C1["catapp — WebKit windows + a native menu"]
  end

  VT["third_party/libghostty-vt<br/>vendored Zig source"]
  VT -->|"libghostty-vt.a, static"| tagged
```

This split is the reason most of the tree is cheap to test: the domain packages,
both protocol contracts, and `catctl` all build and test with `go build ./...` and
`go test ./...` — no Zig, no CGO, no `PKG_CONFIG_PATH`.

## Building the VT engine

```bash
make vt          # or: scripts/build-libghostty-vt.sh
```

What it does:

1. Downloads **Zig 0.15.2** for this platform into `.tools/` (gitignored), verified
   against a pinned SHA-256. No system changes.
2. Builds `third_party/libghostty-vt` into a static `libghostty-vt.a` plus a
   pkg-config file at `third_party/libghostty-vt/zig-out/share/pkgconfig`.
3. Prints the `PKG_CONFIG_PATH` a tagged build needs. The Makefile wires this for
   you.

Supported platforms: `Darwin/arm64`, `Darwin/x86_64`, `Linux/x86_64`,
`Linux/aarch64`. The script is idempotent — re-running reuses an existing Zig
download and patched SDK.

Zig package dependencies land in the Zig global cache. `uucode` is pre-vendored in
`zig-pkg/`; a few others are fetched from `deps.files.ghostty.org` on a cold build,
which CI caches keyed on `build.zig.zon`.

### The macOS 26 SDK workaround

Worth understanding, because it looks alarming and is not:

```mermaid
flowchart TD
  P["libghostty-vt pins Zig 0.15.x"]
  S["macOS 26.5 SDK dropped the plain arm64-macos slice<br/>from its system .tbd stubs — only arm64e-macos remains"]
  F["Zig 0.15.2 does not fall back arm64 -> arm64e<br/>so its build-runner fails to link libSystem"]
  W["Workaround: build a patched COPY of the SDK<br/>with arm64-macos re-injected into every .tbd,<br/>and point Zig at it via an xcrun shim"]
  N["Idempotent, and harmless on older SDKs<br/>that still carry the slice"]

  P --> F
  S --> F
  F --> W --> N
```

Zig 0.16 handles SDK 26.5 fine, but libghostty-vt requires 0.15.x. The patch
touches a **copy** — your real SDK is not modified. On Linux the whole step is
skipped, which is why the Linux build path is simpler.

## Building the binaries

```bash
make binaries    # catway + cathost + catctl into bin/
make local       # the same three into ~/bin
```

Both pass `-tags ghostty -trimpath` plus the build stamp.

### The build stamp

`internal/buildinfo` carries the git identity that `catway` serves to the sidebar
brand, so a running instance names the commit it was built from — the quickest way
to spot a stale install.

```
-ldflags "-X ...buildinfo.hash=<short sha> -X ...buildinfo.subjectB64=<base64 subject>"
```

The commit **subject is base64-encoded** deliberately: it is free to contain
spaces, quotes and `$`, none of which survive a Make recipe's `-ldflags` string
intact, while base64's alphabet is inert throughout.

An unstamped build (plain `go build`) still shows a hash — `buildinfo` falls back
to the Go toolchain's own VCS stamping.

## Testing

```bash
make check       # exactly what CI runs, in order
```

which is:

```mermaid
flowchart TD
  A["fmt-check<br/>gofmt -l cmd internal"]
  B["vet<br/>untagged"]
  C["build<br/>untagged"]
  D["test<br/>untagged"]
  J["jstest<br/>cmd/catway/web/jstest/*.test.mjs"]
  E["vet-ghostty"]
  F["race-ghostty<br/>go test -tags ghostty -race ./..."]

  A --> B --> C --> D --> J --> E --> F
```

`jstest` is the front end's own suite. `cmd/catway/web/js/` ships as one closure
with no exports (see the note atop `assets.go`), so nothing there can be
imported: the harness lifts a function's source out of its part file and
evaluates it against stubs the test supplies, which is enough for the string- and
list-building half of the UI and needs no DOM. It also compiles the whole
concatenated bundle, which is the only check that catches a `const` declared in
two part files — a syntax error that would reach the browser as a blank page. It
runs on `node` and skips itself, with a message, where there is none.

CI (`.github/workflows/ci.yml`) runs the untagged quick checks on Linux first for a
fast signal, then the ghostty-tagged race tests on **both** Linux and macOS.

A `v*` tag triggers `release.yml`, which attaches per-platform tarballs to the
GitHub release.

## Distribution tarball

```bash
make dist
```

Produces `dist/cats_<version>_<goos>_<goarch>.tar.gz` containing `catway`,
`cathost`, `catctl`, `config.example.yaml` and `README.md`. Version comes from
`git describe --tags --always --dirty`.

## macOS app bundles

Both variants come from the **one** `cmd/catapp` codebase.
`scripts/build-macapp.sh` does the assembly; the Makefile target chooses the
variant and the baked-in default mode.

```mermaid
flowchart TD
  SRC["cmd/catapp"]
  V{"variant"}

  SELF["make macapp<br/>build-macapp.sh self"]
  CLIENT["make macapp-client<br/>build-macapp.sh client"]

  A1["dist/Cats.app<br/>dev.cats.app<br/>defaultMode=local"]
  A2["dist/Cats Client.app<br/>dev.cats.client<br/>defaultMode=remote"]

  B1["MacOS/catapp + catway + cathost + catctl<br/>needs 'make binaries' first"]
  B2["MacOS/catapp only<br/>no ghostty/Zig toolchain needed"]

  SRC --> V
  V --> SELF --> A1 --> B1
  V --> CLIENT --> A2 --> B2
```

`make macapp` depends on `binaries` for the daemons; the script **copies** them
rather than building them, so `make vt` must have run first.

`catapp` itself is always built here with plain `go build` — cgo on for WebKit,
**no** `-tags ghostty`, and `-ldflags "-X main.defaultMode=…"`.

Two Objective-C files are in the cgo set, both picked up automatically by their
`_darwin` suffix (no Makefile entry to keep in step):

| File | What it is |
|---|---|
| `cmd/catapp/menu_darwin.m` | the menu bar — App, Edit, View (⌘+/⌘-/⌘0), Window (New Window ⌘N), and the thin client's Connect list |
| `cmd/catapp/window_darwin.m` | the multi-window shell — `NSWindow` + `WKWebView` per window, the app delegate, the pasteboard and connect-form bridges |

They link `-framework Cocoa -framework WebKit`.

Bundle layout:

```
dist/<AppName>.app/Contents/
  Info.plist                  bundle id, name, version from git describe,
                              NSHighResolutionCapable, min-system
  MacOS/catapp                CFBundleExecutable
  MacOS/{catway,cathost,catctl}   self variant only
  Resources/AppIcon.icns
```

No dylibs to copy and no `@rpath` fixups: the daemons link libghostty-vt
statically, so `otool -L` shows only system frameworks. The launcher finds its
siblings via `os.Executable()` → same directory.

The build starts by removing any existing bundle, so a rebuild never leaves a
stale binary behind.

### Gatekeeper

The bundles are **unsigned** — this is a personal-use path, with no Apple Developer
signing or notarization. On the Mac that built it, it just opens. On another Mac it
needs a one-time **right-click → Open**.

## Cross-compilation

| Target | How |
|--------|-----|
| Same OS/arch | `make binaries` |
| macOS → Linux, tagged | **avoid**. CGO cross-compilation needs a Linux cross-toolchain *and* libghostty built for the Linux target. Build on the Linux host (`make vt && make binaries`) or pull the release tarball |
| Anything → `catctl` | trivial, it is pure Go |
| Anything → `catapp` | macOS only (WebKit windows + a native menu via cgo, with a `darwin` build constraint on every file in the package) |

On Linux, CGO links glibc dynamically, so build on the same distro family you run
on.

## Editing the web UI

The page is assembled by the `cmd/catway/web` package — element components for
the markup, `css/*.css` for the stylesheet, `js/*.js` for the front-end — and
compiled into the binary with `//go:embed`.

```mermaid
flowchart LR
  E["edit a component,<br/>css/ or js/"]
  B["rebuild catway"]
  R["restart catway"]
  L["reload the browser"]
  X["a browser reload alone<br/>keeps serving the old page"]

  E --> B --> R --> L
  E -.->|"skipping the rebuild"| X
```

Theme and keybinding changes are the exception — they are injected at render time,
so `catctl reload` applies them with no rebuild and no restart.

## The icon

`scripts/gen-icon.sh` produces `scripts/AppIcon.icns` from the sources in
`scripts/icon/`. Both bundles copy it into `Resources/`.

Those sources paint their background across the whole 1024 canvas, square, with
no corner radius, and they must stay that way. macOS 26 composites a legacy
`.icns` over a light plate and masks the result to the system's own shape, so any
transparency in the artwork comes back as a white ring around a shrunken icon —
the rounding is the system's to apply, not the artwork's to bake in. The two
drawings and the rest of the reasoning are in `scripts/icon/gen-trace/README.md`.
