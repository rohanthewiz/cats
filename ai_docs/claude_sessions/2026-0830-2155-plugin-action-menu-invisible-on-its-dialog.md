# Session: The Menu That Matched Its Own Dialog — project vs global, invisible

> Session: https://claude.ai/code/session_01W5CDEtYkMjekgmM4Zc3ojh
> Date: 2026-08-30
> Repo: cats, on main (uncommitted at doc time; committed with this doc)
> Started in: cats-todo (`~/projs/go/cats-todo`), which again turned out to be innocent

## The prompt

> When launching cats-todo the popup for project vs global isn't clearly visible

## Finding the popup

cats-todo has no launch-time scope chooser. It has a `--project` / `--global`
flag pair (`context.go`'s `launchScope`), and `launch.go`'s comment says why
the only-modes exist at all:

> the only-modes exist for the plugins dialog, whose two actions promise a
> *project* view and a *global* view

So the popup is cats', not cats-todo's. `cats-plugin.toml` declares two actions
— "Cats Todo — this project" and "Cats Todo — global only" — and a plugin with
more than one action does not launch straight away. `pluginPickAction`
(`cmd/catway/web/js/30-plugins.js:117`) asks which:

```js
function pluginPickAction(p, e, launch) {
  if (p.actions.length === 1) { launch(p.actions[0]); return; }
  const r = e.target.getBoundingClientRect();
  openCtx(r.left, r.bottom + 4, p.actions.map((a) => (
    { label: a.title || a.id, fn: () => launch(a) }
  )));
```

That is the popup: an ordinary `#ctxmenu`, anchored under the plugins dialog's
**run** button — which means it is the one context menu in the UI that opens
*on top of a `.modal`* rather than on the terminal canvas.

The `--project` route was also checked for a CLI-side prompt (`catctl plugin
run`, `cmd/catctl/plugin.go`) — there is none; it resolves an action id or the
first action and sends one `tab.create`. Web UI only.

## The bug

Two surfaces, one colour.

```
17-modal.css:4   .modal   { background: var(--panel2); … }
18-ctxmenu.css:2 #ctxmenu { background: var(--panel2); … }
01-theme.css:5   --panel2: #2b322c;  --line: #38403a;
```

The menu floats above the dialog (`z-index` 9 over the overlay's 8, so stacking
was never the problem) — but it is painted in exactly the dialog's colour, with
the same 6/8px radius, separated only by a 1px `--line` border at about **1.3:1**
against its own background and a `rgba(0,0,0,.5)` drop shadow that does nothing
dark-on-dark. What the user sees is two labels sitting loose on the dialog, not
a menu asking a question.

Every other `openCtx` call site opens over the terminal canvas at `--bg`
(`#1f2420`), where `--panel2` *is* a visible lift. That is why only this one
looked broken, and why the rule had survived unnoticed since WS8.

## The fix

Four files, no Go changes.

**1. Separation by edge, not by surface** (`18-ctxmenu.css`). The obvious move —
tint the menu's background — collides with `--chrome-focus`, the row-hover
colour one tier up: lift the base far enough to clear `--panel2` and hover stops
reading as hover. So the menu keeps its surface and gains an edge:

```css
#ctxmenu { … border:1px solid var(--accent-dim);
  box-shadow:0 0 0 1px rgba(0,0,0,.45), 0 10px 28px rgba(0,0,0,.6); }
```

An accent-tinted border with a hard dark ring immediately outside it. The
light/dark pair is what the eye reads as a raised edge, and it survives the
light themes (solarized-light, and the `#f7f8fa` one) where a shadow alone
almost disappears. `--accent-dim` is a derived token
(`internal/theme/theme.go`: `accent` at α .45), so every theme gets one.

**2. Dim the dialog underneath** (`17-modal.css`):

```css
#overlay .modal { transition:filter .12s ease; }
#overlay.ctx-dim .modal { filter:brightness(.68); }
```

The same move the backdrop already makes for the app behind the dialog, one step
further down the stack: while a menu is up, it is the only lit layer.

**3/4. The class toggle** (`25-modal.js`, `28-ctxmenu.js`). `setCtxDim()` lives
beside `closeCtx` because `modalEl` is the state that decides whether there is
anything to dim — a right-click on the canvas has no overlay and it is a no-op.
`openCtx` sets it; `closeCtx` clears it. Deliberately *not* in `buildCtx`, which
recurses for submenus: only the root menu dims what is under it.

## Checks

- `go build ./...` — ok
- `go test ./cmd/catway/...` — ok (`web_test.go`'s `webTestFilesListed` still
  matches; no files added or renamed)
- Not verified on screen: the Chrome extension disconnected on both attempts,
  and the running `Cats.app` serves `go:embed`ed assets, so there is no
  serve-from-disk path to preview against a live server. Rebuild + restart to
  see it.

## Notes for next time

- `cmd/catway/web/` has no dev-serve flag (`assets.go` embeds `css/*.css` and
  `js/*.js` and stitches them into one `<style>`/`<script>`). Any front-end
  change costs a rebuild and a server restart to look at.
- If the menu still does not pop after a rebuild, the next levers are the
  `brightness(.68)` value and giving `#ctxmenu` an accent left-border the way
  `.toast` has one.
- Third session now that opened in cats-todo and landed in cats. When a report
  names a surface cats-todo *does not draw* — chrome, popups, glyph painting —
  check the manifest and the catway front-end before reading cats-todo at all.
