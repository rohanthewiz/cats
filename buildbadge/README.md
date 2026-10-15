# buildbadge

A small web badge that names the git commit a Go binary was built from.

The badge shows the short hash, with `*` if the working tree was modified. Hover it and a card shows the hash, the commit subject, and the commit time in the viewer's time zone with its age:

```
e251787*            ┌──────────────────────────────────────────┐
   ▲ hover ───────▶ │ BUILD   e251787 · modified tree          │
                    │ COMMIT  Build badge popup shows WHEN     │
                    │ WHEN    2026-10-15 09:41 · 3h ago        │
                    └──────────────────────────────────────────┘
```

It answers "is this install stale?" at a glance. The package uses only the standard library, embeds its JS and CSS, and needs no route or asset pipeline.

## Setup in three steps

**1. Stamp the build.**

```sh
go build -ldflags "$(sh path/to/buildbadge/stamp.sh)" ./cmd/yourapp
```

The script's argument is the package's import path. It defaults to `github.com/rohanthewiz/cats/buildbadge`, so pass your own path if you copied the package into your project. If you skip stamping, the hash and time still come from Go's own VCS info. Only the subject is lost.

**2. Put `Head()` in your page's `<head>`.**

```go
import "github.com/rohanthewiz/cats/buildbadge"

fmt.Fprintf(w, "<head>…%s</head>", buildbadge.Head())
```

**3. Mark where the badge goes.**

```html
<span class="brand">MyApp <small data-build-badge></small></span>
```

`Head()` returns `""` when the hash is unknown, so an unstamped dev build simply shows no badge.

## Pieces, for hosts that want control

| Go                         | Gives you                                                     |
|----------------------------|---------------------------------------------------------------|
| `Get() Info`               | `{Hash, Subject, When (RFC 3339 UTC), Dirty}`                 |
| `Info.String()`            | `abc1234* · subject · 2026-…Z`, handy for a `version` command |
| `DataScript(global)`       | `<script>window.<global>={…}</script>`                        |
| `ScriptTag()` / `JS`       | the library, with or without its tag                          |
| `CSS`                      | the badge and card styles, raw                                |

In the browser, `window.BuildBadge` provides:

- `mount(target, info?, {tag, className, show, hide})`: appends a badge to `target` and returns it, or `null` when there is no hash. Pass `show(e, rows)` and `hide()` to route the card through your own hover-card system. Cats does this so the badge opens with the same delay as its sidebar rows.
- `rows(info)`, `fmtWhen(iso)` and `fmtAge(ms)`: the formatting, on its own.
- `autoMount()`: re-scans the page for `[data-build-badge]`. It is idempotent.

**Theming:** set `--bb-fg`, `--bb-bg`, `--bb-muted`, `--bb-line`, `--bb-accent` and `--bb-font` on `:root`. Dark mode follows `prefers-color-scheme`.

## Sharing

You can share the package in two ways:

- **Import it:** `go get github.com/rohanthewiz/cats/buildbadge`. This adds cats as a dependency in your `go.mod`, but nothing outside this directory gets compiled into your binary.
- **Copy it:** copy this directory into your project and change the path you pass to `stamp.sh`. It has no imports from the rest of cats.

## Tests

```sh
go test ./buildbadge
node buildbadge/buildbadge.test.mjs   # self-contained, no harness needed
```
