# fold-wsprobe-into-herdrctl

**Date:** 2026-0719-1638
**Session ID:** `ce44757f-d18f-4c91-98ec-b509e3750215`
**Project:** `~/projs/go/herdr-web`

---

## Scope

Close the open question from the prior session (2026-0719-1600): fold the new
main's standalone `cmd/wsprobe` into `herdrctl`, so the repo ships exactly the
three binaries `gateway` / `termhost` / `herdrctl` with diagnostics living in
the CLI. Loaded the prior session doc for context at session start.

---

## Work done

### 1. `cmd/wsprobe` → `herdrctl probe` verb

- `git mv cmd/wsprobe/main.go cmd/herdrctl/probe.go` (history preserved),
  `cmd/wsprobe/` removed.
- Old `main()` became `runProbe(args []string) int` with its own
  `flag.NewFlagSet`, so **all original flags carry over unchanged**
  (`--url`, `--cols`, `--rows`, `--script`, `--timeout`, `--life`, `--token`)
  and the entire op-script language (expect/read/readvp/capture/split/…)
  is untouched.
- Only code rename: internal `run` → `probeRun` (collision with herdrctl's
  `run`). No other identifier clashes existed.
- `main.go` dispatches `probe` **early, before the global flag re-parse** —
  the same pattern as the `integration` verb — because probe speaks the WS9
  browser protocol to the gateway's `/ws` endpoint (not the control socket)
  and owns a disjoint flag set. Package doc + `usage()` updated; doc frames
  probe as "the other transport exception" alongside offline `integration`.
- Exit codes follow the CLI convention: 0 pass, 1 probe/script failure,
  2 bad flags.
- Dependency check: `internal/browserproto` pulls nothing herdrctl didn't
  already depend on via `internal/app`, so herdrctl stays libghostty-free
  when untagged.

### 2. Reference updates

- README: `cmd/wsprobe/` layout entry dropped; `cmd/herdrctl/` entry now
  mentions the probe verb; automation section says `herdrctl probe`.
- `internal/gwauth/gwauth.go`: two comments naming wsprobe as the example
  headless client now say `herdrctl probe`.
- `ai_docs/phase-c-ws9-tasks.md` references left as-is (historical record).

### 3. Verified live (first tagged build in this checkout)

This checkout had synced to post-PR-#1 main only yesterday and had never
built the new tree's VT engine, so:

- `make vt` — downloaded pinned Zig 0.15.2 to `.tools/`, patched an SDK copy
  (395 `.tbd` files), built vendored libghostty-vt. Worked first try.
- `make binaries` — built tagged `bin/gateway`, `bin/termhost`,
  `bin/herdrctl`.
- Stood up a scratch termhost + gateway on `:8431` (`--auth none`, persist
  off, isolated sockets). Gotcha hit on the way: unix socket paths in the
  deep scratchpad dir exceeded macOS's ~104-char `sun_path` limit
  ("bind: invalid argument") — short `/tmp` socket paths fixed it.
- `herdrctl probe --script 'wait:1200; type:echo …; expect:f:…; split:f:h;
  panes:2; close:f; panes:1; dump:0'` → **PASS** (typed text echoed,
  split/close round-tripped through the live layout).
- Control-socket verbs from the same binary (`panes`, `stop`) verified —
  both transports proven in the one CLI.
- `make check` (fmt-check, vet, untagged build+test, vet-ghostty,
  race-ghostty) fully green.

---

## Loose ends

- The retired Phase A gateway from the 2026-0718 sessions was still running
  on `:8420` (`/tmp/herdr-gateway`) — left alone; kill whenever.
- `bin/` and `third_party/libghostty-vt/zig-out/` now exist locally
  (gitignored build artifacts).
