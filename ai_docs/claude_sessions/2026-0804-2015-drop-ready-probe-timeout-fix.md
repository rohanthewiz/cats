# Session: Drop ready-probe timeout fix (~10s new-session drops)

Session id: `d34a3185-1dbf-4828-b500-b19e5447287d`

## Problem

Dropping a todo from cats-todo into a **new** claude code session still took
~10s. Measured baseline first: claude itself starts headless in ~2.3s in the
cats-todo repo, shell init is 0.75s, no MCP servers, small config — the CLI was
not the slow part.

## Root cause — two compounding bugs

The drop path (`cats-todo/drop.go` → `client.waitForAgentReady`) waits up to
**12s** for one of `claudeReadyProbes` to appear in the pane before pasting,
and on timeout pastes anyway. Every claude drop was riding the full timeout:

1. **Stale probes** (`cats-todo/client.go`): none of the five probe strings
   ("for shortcuts", "Welcome to Claude", "/help for help", "esc to
   interrupt", "Bypassing Permissions") are drawn by Claude Code 2.1.x at
   startup. Verified by capturing a real 2.1.222 launch in a Python pty
   (`pty.fork` + `select` read loop, SIGALRM watchdog — macOS `script` fails
   in the sandbox with tcgetattr on a socket). The banner now reads
   "Claude Code v2.1.222 / Welcome back <user>!".

2. **Space-eating stripper** (`cats/cmd/catway/outscan.go`): Claude Code's
   renderer draws word gaps as cursor-column escapes (`Welcome\x1b[28Gback`),
   and the wait_for_output stripper deleted CSI sequences outright — the match
   buffer saw `WelcomebackRo!`, so even a corrected multi-word probe could
   never match the raw stream. (The seed capture of the rendered screen has
   real spaces, but it fires at waiter registration, before claude draws —
   the raw-stream path is the live one for drops.)

## Fixes (both committed to main and pushed)

- **cats `cf6333b`** — `fix(catway): cursor-movement escapes read as word gaps
  in wait_for_output`. `outputScanner` now appends one space for a same-row
  move (CHA/CUF/HPA/HPR finals `G C \` a`) and one newline for a row move
  (CUP/VPA/CUU/CUD/CNL/CPL/VPR finals `H f d A B E F e`), collapsed against an
  existing separator so redraw repositioning can't smear the buffer. SGR/erase
  sequences still strip to nothing (colour-wrapped text stays contiguous).
  New tests in `outscan_test.go` use the exact captured byte shapes.

- **cats-todo `f52d2c4`** — `fix(drop): refresh Claude Code ready probes for
  the 2.x banner`. Added "Claude Code v" (version-agnostic banner title) and
  "Welcome back"; kept the old probes for older layouts. Comment notes the
  spaced probes depend on catway's separator fix, and that slow drops mean
  re-capturing a startup and re-checking the list.

Verified end-to-end at the data level: the captured 2.1.222 raw stream fed
through the new scanner matches both new probes (throwaway test, deleted).
Full test suites green in both repos.

## Follow-ups / caveats

- The running MacApp keeps the old stripper until rebuilt/relaunched
  (`make macapp`); the cats-todo binary in panes needs a rebuild too. Not done
  in-session — restarting the server would drop live sessions.
- Expected drop latency after rebuild: ~2–3s (probe fires when the banner
  draws).
- If drops go slow again, capture a fresh startup (pty script pattern above)
  and re-check `claudeReadyProbes` first.
