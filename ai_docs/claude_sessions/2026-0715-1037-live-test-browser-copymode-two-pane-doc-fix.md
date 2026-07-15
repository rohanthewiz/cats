# Live-test browser copy-mode (WS4 Part-2 leftovers) + two-pane-split doc fix

**Session id:** `dc26f68e-83a9-4b79-a467-b65830002435`
**Date:** 2026-0715-1037 · **Branch:** `roh/phase-b` (herdr-web)
**Continues:** `2026-0713-1832-ws4-control-api-cli-herdrctl-browser-copy.md`, whose top leftover was
"browser copy-mode not click-tested — recommend a live pass." Did exactly that (all three
selection/copy leftovers, end-to-end in a real browser), then chased down the "single pane vs
fixed two-pane split" discrepancy the live test surfaced.

> Two parts. **Part 1:** live-drove the WS4 Part-2 browser leftovers — keyboard copy-mode (⬚),
> copy-scrollback (⧉), and selection-wash dismissal — in the **installed Google Chrome** via
> `playwright-core` against a real gateway2 + persistent termhost. **12/12 checks green**, verified
> against the actual clipboard + toasts + screenshots. **Part 2:** the test rendered a single pane,
> not the "fixed two-pane split" the old doc promised; traced it to the source and confirmed
> **single pane is correct** (WS2 orchestrator design) — the two-pane mention was a stale header
> comment, now fixed. No product-code behavior changed.

---

## Part 1 — live-test browser copy-mode

### Harness (kept in the session scratchpad, not committed)

- Built `gateway2` / `termhost` / `herdrctl` with `-tags ghostty`
  (`PKG_CONFIG_PATH=~/projs/rust/herdr/vendor/libghostty-vt/zig-out/share/pkgconfig`).
- Ran a **persistent termhost** daemon + `gateway2 --auth none` on an isolated port (`:8531`) and
  isolated sockets. **Sockets must live under `/tmp` with short names** — the scratchpad path blows
  past macOS's ~104-char `sun_path` limit (`bind: invalid argument`). Logs/screenshots stayed in
  scratchpad.
- Drove the **installed Google Chrome** (`/Applications/Google Chrome.app/...`) via
  `playwright-core` (`executablePath`, no browser download). Context granted
  `clipboard-read`/`clipboard-write` for the origin so yanks are read back from the **real
  clipboard**. Toasts captured via a MutationObserver into `window.__toasts`.
- For the wash-dismissal internal-state check (`p.sel.done`, `p.sel === null`), injected a
  **read-only** observability hook (`window.__t = {panes, cmp, cell}`) into the served HTML via
  Playwright route-fulfill — logic under test unchanged; only exposes closure state.

### Results — the three leftovers, 12/12 green

- **⬚ keyboard copy-mode:** banner toast on enter; `g 0 v G` motions select the viewport; `y` yanks
  → clipboard got the sentinel (113 chars); copy-mode exits after yank; `Esc` exits without yanking.
  Screenshot shows the wash + the distinct outlined copy-cursor.
- **⧉ copy-scrollback:** click → `capture` whole buffer → clipboard has the sentinel; "copied N
  chars (scrollback)" toast.
- **Selection-wash dismissal:** mouse drag → `finishSelection` copies (clipboard has
  `echo COPYSENTINEL…`) and marks the wash `done`; the next keystroke fires `clearStaleSelections`
  → `p.sel === null`. Screenshots show the two-row wash present then gone.
- No console/page errors throughout. Teardown via **`herdrctl server.stop`** (exercises WS4):
  ping→pong, gateway2 exited, control socket unlinked, **termhost survived** as designed; 0 stray
  procs.

### Two harness gotchas (not product bugs)

- **Playwright `press('Shift+g')` sends `key:'g'`+shift**, which the page's `case "G"` correctly
  ignores. A real browser's Shift+g sends `key:'G'` — and `press('G')` (used in the final driver)
  moved the cursor to the bottom row as expected. The page's `G` motion is correct.
- **Route-fulfilled documents get reclassified** by Chrome's Local Network Access check, blocking
  `ws://localhost` (`ERR_BLOCKED_BY_LOCAL_NETWORK_ACCESS_CHECKS`). Launch with
  `--disable-features=LocalNetworkAccessChecks`. A directly-served page (the normal path) connects
  fine — nothing to change in gateway2.

## Part 2 — "two-pane split" was stale docs

The live test rendered **one** pane, but `cmd/gateway2/main.go`'s header promised "a fixed two-pane
split." Traced the startup path: `newOrch` → `app.NewSession` → `workspace.New`, which builds "one
tab and one root pane." Repo-wide sweep found **no code that creates two panes at startup** and no
default-split call — splits are only created at runtime via the §7 `pane.split` command.

**Verdict: single pane is correct, intended behavior** of the WS2 orchestrator (`gateway.go`, a
single-owner event loop over an `app.Session`). The stale `main.go` header still described the
pre-WS2 "WS9 Stage-4 proof harness … no WS2 orchestrator yet … fixed two-pane split." The accurate
description already lived at `gateway.go:130-131`.

**Fix (doc-only):** rewrote the `main.go` header to describe the real model — one workspace/tab/pane
at startup, runtime splits via the §7 table, state owned by the WS2 orchestrator. gofmt clean.
Left `ai_docs/phase-c-ws9-tasks.md`'s "fixed two-pane horizontal split" untouched (historical task
spec, not a description of current behavior).

## Files

- `cmd/gateway2/main.go` — corrected stale package header comment (only tracked change this session).

## Notes / leftovers

- **Browser copy-mode is now click-tested end-to-end** — closes the last WS4 session's top leftover.
- The Playwright driver (`drive.js`) + screenshots live in the session scratchpad, not committed. If
  we want a repeatable browser-driven check, capture it as a project `/run` skill (dev-server line,
  short `/tmp` sockets, the two Chrome flags above, one representative interaction).
- Still open from WS4: read-only query methods (`*.list`/`*.get`), streaming
  (`events.subscribe`, `pane.wait_for_output`), TOML config + keybindings, ergonomic `herdrctl`
  subcommands, and a `.gitignore` for built binaries (`gateway2`, `herdrctl`, tracked root `termhost`).
