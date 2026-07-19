# Agent-session persistence + resume-on-restore

**Session id:** `58a89454-a80f-434c-9d2f-187db971ebbb`
**Date:** 2026-0718-2002 · **Branch:** `roh/phase-b` (herdr-web)
**Continues:** `2026-0718-0748-hook-ingestion-worktree-settings-reorder.md`
(its "next candidate before WS11" leftover — hook-reported session refs were
memory-only — is exactly this session).

> The port of Rust `agent_resume::plan` / `pending_agent_resume_plan`:
> hook-reported agent session refs now persist in `session.json` and a cold
> start relaunches each agent's native conversation (`claude --resume <id>`,
> per-agent argv table). Spec-first (two exploration agents: Rust semantics,
> Go landing zones), then implemented inline. Live-verified across two real
> cold restarts plus the missing-binary fallback.

---

## Design (and the two deliberate adaptations)

- **Persist:** `sessionFile` gains `pane_agent_sessions` —
  `persist.AgentSession{source, agent, kind, value}`, herdr's exact four
  fields (no TTL, no timestamps). Additive, Version stays 1.
  `SaveSession`/`LoadSession` signatures grew a map (all callers updated).
- **Save triggers:** any genuine ref change arms `saveSoon` via
  `noteSessionRefChanged` (which also deletes the pane's restart-restored
  entry — the live lifecycle owns the identity from then on). `saveNow`
  merges live `rt.agentSession` over not-yet-respawned `o.restoredAgents`,
  the `restoredCwds` pattern.
- **Lifecycle parity ported into existing seams:**
  - `setSessionRef` (hooks.go): herdr's `conflicting_current_session_ref` —
    a *different* id for the held conversation (same source+agent, both id
    kind) is **ignored**; a nested/sub-agent session must not clobber the
    resumable parent. This flipped one assertion in
    `TestHookReservedNativeSessionOnly` (old behavior was overwrite).
  - `onPaneAgent` (notify.go): detection contradicting the ref clears it —
    different agent on screen, or the ref's own agent changed/disappeared
    (conversation over; computed against prev detection *before* overwrite).
  - Release (matching source) and pane exit clear the ref;
    `clearHookOnExit` now also kills a restored-but-unconsumed entry (for a
    resumed pane the root process IS the agent).
- **Restore (new `cmd/gateway2/resume.go`):** `resumeArgv` re-validates
  (id ≤512, path pi-only + absolute ≤4096, control chars rejected, official
  `herdr:*` sources only) and returns herdr's exact 11-agent table — note
  copilot's joined `--resume=<v>` and cursor's binary `cursor-agent`.
  `planResume` dedupes shared conversations (NUL-joined dedupe key,
  first-pane-wins by **ascending pane id** — deterministic, not herdr's
  traversal order), drops duplicates' refs, and marks history suppression
  for winner *and* duplicate (the resumed agent owns the scrollback; herdr
  suppresses both too). Resume off ⇒ refs kept, no plans (herdr parity).
- **Consumption:** `createPane` sets `cp.Command/cp.Args` from the plan on a
  connected send (consumed once, like seeds/cwds) and moves the restored ref
  live onto `rt.agentSession`. `buildOrch` deletes suppressed panes' seeds.
  `reconcile` adoption: survivor keeps its live PTY — plan deleted, saved
  ref seeded onto the runtime (normal lifecycle rules own it from there).
- **Adaptation 1 — direct exec:** herdr spawns a shell, waits ≤750 ms for
  host theme, types the shell-quoted command + `\r`. The gateway execs the
  argv via the existing `CreatePane.Command/Args` seam (termhost already
  honored it; first production caller). Id stays argv data — no quoting, no
  timing gate. Visible difference: agent exit ⇒ pane exit (not drop to
  shell).
- **Adaptation 2 — termhost shell fallback** (internal/orchestration
  host.go): a pane with an explicit command that fails to start (missing
  agent binary) emits the error and retries with the default shell — herdr's
  typed-into-a-shell approach leaves a usable shell, so match that outcome
  instead of a dead pane.
- **Config:** `persistence.resume_agents` (default true) — herdr's
  `session.resume_agents_on_restore`. Config-file only; settings modal
  doesn't cover persistence, so no UI change.

## Verification

- Full repo green untagged and `-tags ghostty -race`; `gofmt` clean.
- New `resume_test.go`: argv table (all 11), validation rejects, dedupe/
  suppression/resume-off, createPane consumption + disconnected retention,
  saveNow persistence, exit clear, detection-clear matrix, reconcile
  adoption. Extended persist round-trip (+ old-file-without-field load).
- **Live** (scratch termhost+gateway2, short /tmp dir, scratch
  HERDR_CONFIG + state_dir, fake `hermes` on PATH):
  1. hook client reported `herdr:hermes` id `resume-live-xyz` → appeared in
     `session.json` within the debounce;
  2. cold restart (both processes killed) → log `1 agent session(s)
     eligible for resume`, fake binary received exactly
     `--resume resume-live-xyz`;
  3. ref survived the resume → second cold restart resumed again;
  4. binary removed → gateway logged `exec: "hermes": executable file not
     found in $PATH — falling back to shell`, pane came up as live zsh
     (child of termhost).

## Files

- **New:** `cmd/gateway2/resume.go` (+`resume_test.go`).
- internal/persist persist.go (+AgentSession, envelope field, signatures;
  tests), internal/config config.go (Persistence.ResumeAgents),
  internal/orchestration host.go (createPane shell fallback),
  cmd/gateway2: gateway.go (orch fields `restoredAgents`/`resumePlans`,
  createPane consumption), persist.go (saveNow merge), main.go (buildOrch
  load/plan/suppress), hooks.go (conflict-drop, noteSessionRefChanged,
  release/exit clears), notify.go (detection clear), daemon.go (reconcile
  adoption), hooks_test.go/persist_test.go signature+parity updates.

## Notes / leftovers

- Resumed pane exits with the agent (direct-exec adaptation); a
  "respawn shell on agent exit" refinement is possible if it grates.
- Dedupe winner is lowest pane id, not herdr's restore-traversal order.
- Not ported (unchanged from herdr): no file-existence check on pi's path
  ref, no staleness limit — deliberate parity.
- Per the workstream map, remaining before **WS11 cutover**: packaging/CI
  and the deferred niche chrome (global launcher, pane drag-reorder,
  bell/activity markers, onboarding).
