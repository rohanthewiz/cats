# Workstream status & next-step analysis

**Date:** 2026-07-15 19:02 · **Repo:** `~/projs/go/herdr-web` · **Branch:** `roh/phase-b`

Written just after WS4 completed (raw-byte-stream `pane.wait_for_output`, commit
`4b40128`). Grounded in `ai_docs/fbl_go_port_feasibility_analysis.md` (the
workstream map + dependency graph) and a verification pass over the current tree.

**North-star (per user memory):** get all of herdr off Rust; the pivotal lever is
the Go web front-end replacing the ratatui TUI (the Zig VT engine stays as CGO).

---

## Where things stand

| WS | Scope | State |
|----|-------|-------|
| WS0 | Rust Zig-free build (flip termhost default, delete in-process PTY/VT) | done (stages A–D) |
| WS1 | Core data model & layout | done |
| WS2 | Orchestrator / app state & event loop | largely done |
| **WS4** | **Config, keybindings, CLI, control API** | **✅ complete (this session)** |
| WS9 | Browser-facing protocol | done (protocol v1) |
| WS10 | Remote access & auth (TLS, password/none) | done |
| WS8 | Web front-end: HTML chrome + per-pane canvases | core built (canvas rendering exists); polish remains |
| WS5 | Detection wiring | wired (pane_agent surfaced); manifest-update fetcher remains |
| **WS3** | **Session persistence & restore** | **NOT done on the gateway2 side (verified gap)** |
| WS6 | Platform integration (clipboard/notify/procinfo) | clipboard (OSC 52) + procinfo done; native notifications partial |
| WS11 | Cutover, packaging, CI | last |

Dependency graph (from the feasibility doc):

```
WS0 ─▶ WS1 ─┬─▶ WS2 ─┬─▶ WS3
            │        ├─▶ WS4 ─▶ WS7
            │        └─▶ WS5
            ├─▶ WS9 ─▶ WS8 ─▶ WS10
            └─▶ WS6
                              WS11 (last)
```

---

## Recommendation: WS3 — session persistence & restore (gateway2 side)

**The gap is real (verified).** `cmd/gateway2/gateway.go:newOrch` always builds a
**fresh single-pane session** (one workspace, one tab, one pane). On a gateway2
restart it reconnects to the persistent termhost and *adopts the surviving PTYs*
via `daemon.reconcile` — but the **workspace/tab/pane tree, layout split ratios,
custom names, and focus are lost**: the live shells are re-homed into a one-pane
model. The `InitialHistory` scrollback-seed field exists in the β protocol
(`orchestration.CreatePane.InitialHistory`) and termhost consumes it
(`host.go:515`), but **nothing on the gateway2 side ever populates or persists
it** — so scrollback restore is unwired too.

**Why it's the right next pick:**
1. **It's the stated last correctness gap.** `ai_docs/termhost-persistence-design.md`
   calls session survival "the last big correctness gap before termhost can be the
   default and the Rust in-process PTY path retired."
2. **Directly on the north-star.** To cut ratatui over to the web front-end, the
   web front-end must restore sessions the way herdr does, not reset on restart.
3. **Fully unblocked** (WS1 + WS2 done) and **builds straight on freshly-touched
   code** — the termhost reconnect/resync, `reconcile`, and `InitialHistory` paths.

**Concrete shape:**
- Persist the orchestrator's session model to disk (workspace/tab/pane tree + split
  ratios + custom names + focus), updated on model mutations.
- On startup, restore that model instead of the fresh 1-pane default; reconnect to
  the persistent termhost and adopt surviving PTYs through the existing reconcile.
- Cold-seed scrollback via `InitialHistory` for panes the daemon no longer has
  (first run, daemon killed, crash) — the termhost analogue of herdr's
  `seed_history_ansi`. Capturing that scrollback text is a `capture`/`request_text`
  round-trip the WS4 work already exercises.

---

## Alternatives considered

- **WS8 polish** — dialogs / menus / command-palette + remaining chrome states. The
  "hard rendering/input surface"; more open-ended, co-evolves with WS2. Lower
  correctness-urgency than WS3.
- **WS5 tail** — port the manifest-update fetcher (`manifest_update.rs`). Small,
  quick, low-risk win; doesn't move the cutover needle much.
- **WS6 notifications** — surface OSC 9 progress / agent-blocked as native desktop
  notifications (the scanner already extracts OSC 9). Nice-to-have.
- **WS11 cutover prep** — packaging / CI / delete-Rust. Premature until WS3 closes
  the persistence gap.

---

## Open decision for the user

Proceed with **WS3** (I'd open it with a scoped design/plan pass against
`termhost-persistence-design.md` + the current `newOrch`/`reconcile` code), or
redirect to one of the alternatives above.
