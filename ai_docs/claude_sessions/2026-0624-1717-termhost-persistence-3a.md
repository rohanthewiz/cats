# herdr-web (Go) — Phase B: termhost persistence 3a (seed history + verify detach)

**Date:** 2026-0624-1717
**Repo:** `~/projs/go/herdr-web` (Go daemon) · paired with `~/projs/rust/herdr` (orchestrator).
**Branch (Go):** `roh/phase-b` · **Branch (Rust):** `roh/phase-b-termhost-client`

> Phase 3a of the persistence plan (`ai_docs/termhost-persistence-design.md`). The
> small, low-risk slice: cold-restore history seeding (scenario B at parity with
> in-process `seed_history_ansi`) + verifying client detach/reattach (scenario A).
> Phase 3b — the persistent daemon (live process survival across restart/handoff) —
> is the larger follow-up, deferred.

---

## What shipped

### Go — commit `314c893`
- **`protocol`**: `CreatePane.initial_history` (VT-encoded scrollback, `omitempty`).
- **`host`**: `createPane` writes it to the emulator **before the read pump starts**,
  so it renders as history above the freshly spawned shell. Safe to write directly —
  nothing else touches the emulator yet (single-threaded create path).
- Test: Host integration — `create_pane` with `initial_history` → the seeded lines
  render in a frame.

### Rust — commit herdr `e974e5f`
- **`proto`/`client`/`pane.rs`**: thread `initial_state.history_ansi` (already
  produced by the restore path via `snapshot_history`, gap #2) through
  `finish_termhost` → `PaneSpec.initial_history` → the seam. So a restored termhost
  pane shows its prior scrollback.
- **e2e `termhost_pane_survives_client_reattach`** (scenario A): drop client 1 after
  a marker, reattach client 2, run another command in the *same* shell, confirm both
  the new output renders **and** the pre-detach output is still in the buffer (read
  over the seam). Proves the pane + shell + scrollback survive a TUI-client cycle.

## Why scenario A already works (the key fact)

The daemon's `--exit-on-disconnect` keys on the **herdr↔daemon** connection, not the
TUI client. herdr's daemon connection is a process-wide `static CLIENT`
(termhost/mod.rs), independent of TUI clients. So a client detach (which keeps the
herdr *process* running, in persistence mode) leaves herdr↔daemon intact → the
daemon and all panes survive. The e2e now locks this in.

## Status of the three persistence scenarios

| | Status after 3a |
|---|---|
| **A. Client detach/reattach** | ✅ works (e2e regression added). |
| **B. Cold restore** (herdr restart) | ✅ at "replayed history" parity — a restored pane re-spawns a **fresh** shell with its saved scrollback seeded. (Live process is *not* preserved; that's 3b.) |
| **C. Live handoff** (binary upgrade) | ⛔ still 3b — needs the persistent daemon + reconnect/resync. |

## Verification (all green)

- Go: `-tags ghostty` build; `go test -tags ghostty ./internal/...` (incl. the new
  seed-history Host test); vet/gofmt clean.
- Rust: feature-off + feature-on + clippy clean. `cargo test --bin herdr` = **1892**;
  `--features termhost` = **1917** (+1 proto test). Full e2e **9/9** against the real
  daemon (incl. the new reattach test) in ~7s.
  - *Aside:* a backgrounded test run looked "hung" — it was stale-job/target-lock
    contention, not a real hang; a clean re-run passed in ~47s each.

## Commits

```
Go   (roh/phase-b):                 314c893 feat: seed restored scrollback on create_pane (termhost persistence 3a)
Rust (roh/phase-b-termhost-client): e974e5f feat: seed termhost restored history + verify client reattach (persistence 3a)
```

## Next (persistence 3b — the big one)

Persistent daemon so live shells survive a herdr **restart** (B at full fidelity) and
**handoff** (C). Per the design doc: non-exit lifecycle mode, session-keyed stable
socket, reconnect/resync protocol (replay per-pane frame+modes+cwd+title+agent+scroll),
pane-ID reconciliation, single-writer ownership token, GC policy (clean-quit
`shutdown` vs. crash/handoff disconnect + idle timeout), and handoff integration
(skip termhost panes in the fd-passing path; new herdr reconnects instead). Open
decisions listed in `ai_docs/termhost-persistence-design.md`.

After 3, gap #4 = flip default + delete the in-process path (mostly subtraction).
Kitty graphics remains back-burnered.
</content>
