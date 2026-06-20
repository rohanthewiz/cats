# claude-code-tab-title-hook

**Date:** 2026-0619-2028
**Session ID:** `858348af-4142-4619-9c88-d4a1c556042a`
**Project:** `~/projs/go/herdr-web` · references `~/projs/rust/herdr` (Rust source, read-only)

---

## Goal

Let Claude Code "take over" the title shown for its pane instead of the static
`terminal` label, preferring the session's human-friendly name. Constraint
(after clarification): keep the change **inside herdr-web**, and "friendly name"
means the **Claude `--resume` session name** (the ai-title).

---

## Key discovery: what herdr-web can and can't control

- The tabs in the herdr UI (`lazygit` / `todo` / `terminal` / `Scratch`) are
  **rendered by herdr's Rust TUI into frame cells**. The gateway receives
  pre-rendered cells and paints them — it has **no structured tab-title field to
  rewrite**. So the canvas tab label can't be changed from herdr-web. (Parked;
  the user said tab ownership isn't a big deal for now.)
- The `terminal` tab is defined in the herdr-plus project config
  `~/.config/herdr/plugins/config/cloudmanic.herdr-plus/projects/pers_projs.toml`
  (`[[tabs]] name = "terminal"`, no command → empty shell). Claude was launched
  in it via `cl`.
- The **only** title herdr-web controls is the **browser tab** (`document.title`,
  set in `cmd/gateway/web/index.html:186` from `{t:"title"}` messages).

### Empirical findings (instrumented the gateway, then reverted)
- herdr **0.7.0 forwards no `SMWindowTitle`** in practice: zero messages across
  reload + multiple tab switches. That channel is dormant.
- Couldn't inject an OSC title from a Bash tool to test forwarding —
  `/dev/tty` is "device not configured" (tool subprocesses are detached from
  herdr's PTY).
- The herdr→client protocol carries **no session name / agent label / usable
  title** at all (`Welcome` has only version/encoding/error). So the gateway has
  no in-protocol source for the name — it must be **pushed in** from the Claude
  side.

### Version gotcha
- Local Rust checkout `~/projs/rust/herdr` is **0.6.10 / protocol 13**; the
  **running** binary is **herdr 0.7.0 / protocol 14** (adds `SMWindowTitle`).
  The local source is a version behind what's running — don't trust it for
  0.7.0 behavior.

### Where the friendly name actually lives
- SessionStart hook payload gives only `session_id` (UUID), `cwd`, `source`,
  `transcript_path`. **No reliable `session_title`; no `sessions-index.json`
  exists** on this machine (the web-guide claims were wrong).
- The friendly name is the **last `ai-title` line** in the transcript jsonl:
  `{"type":"ai-title","aiTitle":"…","sessionId":"…"}`. For this session it was
  `"Customize terminal pane title naming"`.
- **Caveat:** ai-title is generated a turn or two in, so it's usually **absent at
  SessionStart** → need a fallback and a recurring (Stop) refresh.

---

## Solution implemented (option A: push title into the gateway)

End-to-end: **Claude Stop hook → reads latest `aiTitle` → `POST /title` →
gateway broadcasts → `document.title`.**

### 1. Gateway — `cmd/gateway/main.go` (+126/−29, not yet committed)
- New `titleHub`: fans a pushed title to every connected browser pump, remembers
  `latest` so a fresh page load replays it on `subscribe()`. Coalescing
  (buffer-1, latest-wins). `subscribe()` returns an unsubscribe guarded by
  `sync.Once`.
- New `POST /title {"title":"…"}` route → `titles.broadcast(strings.TrimSpace(...))`.
  Empty title restores default.
- Restructured the herdr→browser pump to `select` over **both** herdr messages
  and hub title pushes, **preserving the single-WS-writer invariant**: herdr
  reads now run on a helper goroutine feeding `herdrMsgs chan`; the pump is still
  the only goroutine that calls `ws.WriteMessage`.
- **No browser change** — existing `{t:"title"}` handler already drives
  `document.title` (prepends `"herdr · "`).
- Added imports `strings`, `sync`.

### 2. Hook script — `scripts/claude-title-hook.py` (new, executable)
- Reads hook JSON on stdin; extracts last `aiTitle` from `transcript_path`;
  falls back to `Claude Code · <cwd basename>`; POSTs to
  `${HERDR_GATEWAY_URL:-http://localhost:8420}/title`. All failures swallowed
  (gateway down must never disrupt the session). 1s urllib timeout.

### 3. Wiring — `.claude/settings.json` (new, committed, project-scoped)
- `Stop` (refresh each turn) + `SessionStart` (initial fallback) →
  `python3 "$CLAUDE_PROJECT_DIR/scripts/claude-title-hook.py"`.
- Existing `.claude/settings.local.json` (permissions only) is untouched; both
  apply.

---

## Verified
- `curl -X POST :8420/title` → browser tab showed
  **"herdr · Customize terminal pane title naming"** (user confirmed).
- Hook script extracts `aiTitle` correctly; fallback path works; both exit 0.
- `go build` + `go vet` clean; settings JSON + Python both valid.

## Not yet done / notes
- **Hook not active until Claude Code reloads** — mid-session `settings.json`
  changes aren't picked up live. After reload, every turn refreshes the title.
- **Gateway must be running.** Currently up as `/tmp/herdr-gateway` on `:8420`
  (replaced the old instance). Durable run: `go run ./cmd/gateway`.
- **Scope:** project-committed hook → fires only when Claude's project is
  herdr-web. For *any* herdr pane/project, move the `hooks` block to
  `~/.claude/settings.json` (script can stay, referenced by abs path).
- **Single global title:** gateway broadcasts one title to all browsers (last
  writer wins) — fine for one active Claude pane.
- **Nothing committed yet.** Pending decision: commit, and/or move hook to
  global scope.

## How to run
```bash
cd ~/projs/go/herdr-web
go build ./...
go run ./cmd/gateway --addr :8420   # open http://localhost:8420
# manual title test:
curl -s -X POST localhost:8420/title -H 'Content-Type: application/json' -d '{"title":"hello"}'
```

## Next step
Reload Claude Code to activate the hook, watch the browser tab track the
ai-title live, then decide commit + scope.
