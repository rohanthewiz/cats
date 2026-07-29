# Session: Pane Popup — Current Model In Use

- **Session ID:** `302052d0-25a0-4af2-9778-a1454628cac0`
- **Date:** 2026-07-28
- **Branch:** main

## Request

> In the pane popup please include the current model in use

(with a screenshot of the sidebar pane hover card: PANE / TITLE / DIR / AGENT /
FOCUS / SIZE / WINDOW / LINK)

## The problem: nothing on the wire knows the model

Neither existing agent-identity channel carries it:

- **Detection** (`internal/detect`) evaluates screen regex rules and only ever
  yields `(agent, state)` — the manifest schema has no extraction concept.
- **Hooks** (`cmd/catway/hooks.go`): claude is a *reserved native source* whose
  hook reports session identity only, and its installed asset registers exactly
  one `SessionStart` entry. Claude Code's docs confirm `model` is a
  `SessionStart`-only, not-guaranteed field — and it would go stale the moment
  the user switches with `/model`. Rejected as the source.

What **is** live is claude's own transcript: `~/.claude/projects/<slug>/<session
id>.jsonl`, one JSONL record per message, every assistant record naming the
model that produced it. That became the source.

Checked against this machine: no cats hooks installed in `~/.claude/settings.json`
(`hooks: {}`), so the **session-id path never fires here** — the cwd fallback is
what actually runs for this user. That shaped the design.

## Decisions

- **Read the transcript, don't extend the hook protocol.** Live across `/model`,
  no integration version bump, no reinstall needed by users.
- **Two resolution paths, exact first.** Hook-reported claude session id names
  the file outright (glob spans all project dirs — claude slugs the dir it
  started in, not the pane's cwd). Otherwise the pane's cwd picks the project
  dir and the newest transcript in it wins.
- **Both slug spellings are searched.** Verified empirically against
  `~/.claude.json` project keys vs on-disk dir names: current Claude Code maps
  every non-alphanumeric char to `-`; older versions kept `_` and those dirs are
  still on disk (`-Users-…-cbre_projs-lezen` alongside `-Users-…-cbre-projs-lezen`).
- **claude only.** Every other agent writes its history elsewhere in its own
  shape; their panes show no MODEL row (field is `omitempty`).
- **Raw model id** (`claude-opus-5`) — this is a developer tool; no prettifying.

## What was done

### New: `cmd/catway/agentmodel.go`

- `claudeProjectsDir()` — `CLAUDE_CONFIG_DIR` or `~/.claude`, + `/projects`;
  resolved once into new orch field `claudeProjects` (`""` disables; tests point
  it at a fixture tree).
- `claudeTranscript(projects, cwd, session)` — session-id glob, else newest
  `*.jsonl` across `claudeProjectSlugs(cwd)` (current + legacy spelling).
- `isTranscriptID()` — gates the hook-reported id before it enters a glob
  pattern (hooks.go only validates length/control chars; `*` would match another
  pane's transcript).
- `lastAssistantModel(path)` — reads the last 256 KB, drops the partial first
  record, scans lines backward with a cheap `"model"` `bytes.Contains` gate;
  skips `isSidechain` (sub-agent) records and `<synthetic>` (fabricated, not
  sampled) messages.
- `refreshAgentModel(rt, agent)` — non-claude clears synchronously; otherwise
  spawns the read **off the loop goroutine** (throttled: `modelBusy` +
  `modelRefreshInterval` 20s) and posts `setAgentModel` back.
- `setAgentModel(pid, model)` — re-checks the arbitrated agent so a read that
  raced claude *out* of the pane can't put a model back; broadcasts only on an
  actual change, and only for visible panes.
- `runAgentModels()` — 30s sweep goroutine (started in `main.go` beside
  `runHistoryCapture`), which is what catches a `/model` switch on a pane that
  then sits idle with no state transition.

### Wiring

- `internal/browserproto/down.go`: `PaneAgent` gains `Model string
  json:"model,omitempty"`; `NewPaneAgent(pane, agent, state, model, seen)` —
  3 call sites (`notify.go` publish, `catway.go` broadcastPaneChrome + resync).
- `cmd/catway/catway.go`: `paneRuntime` gains `agentModel` / `modelAt` /
  `modelBusy`; orch gains `claudeProjects`.
- `cmd/catway/notify.go`: `publishAgent` calls `refreshAgentModel` **before**
  the broadcast, so a departed agent's model clears in the same message.
- `cmd/catway/web/index.html`: pane state gains `agentModel`; `pane_agent`
  handler sets `p.agentModel = msg.model || ""` (absent clears); `showPaneTip`
  pushes `["Model", p.agentModel]` right after the Agent row → renders as
  **MODEL** (the `.k` CSS uppercases).
- `ai_docs/phase-c-ws9-protocol.md`: `pane_agent` shape updated with `model?`.

## Verification

- `make check` green: fmt-check, vet, build, test, **vet-ghostty, race-ghostty**.
- New `cmd/catway/agentmodel_test.go`: session-id resolution across project
  dirs; cwd + newest-wins + legacy slug + unknown-cwd; sidechain/synthetic/
  missing-file skipping; tail truncation (mid-record first line dropped, a
  scrolled-out model never reported); the id gate; and an orch-level test that
  the model follows the arbitrated agent (resolved for claude, dropped when the
  agent leaves) — pumping the mailbox by hand since these tests own the loop.
- Ad-hoc run against the real `~/.claude/projects` (temporary test file, since
  deleted): cwd `…/projs/go/cats` → `claude-opus-5`, cwd `…/projs/go/r-ed` →
  `claude-opus-5`, session-id path → `claude-opus-5`.

## Gotchas / notes for next time

- **Two claude panes in the same directory are a coin flip** on the cwd path —
  both show the newest session's model. Documented in the code. The fix is
  installing the claude integration (`catctl integration install claude`), which
  makes the session-id path exact; consider prompting for that if it bites.
- **No MODEL row until claude has answered once** — a brand-new session's
  transcript has no assistant record yet. Deliberate: `""` is honest, and
  falling back to an older transcript would report another session's model.
- **The transcript must be on catway's machine.** Same assumption the hook seam
  already makes (panes dial a unix socket), noted in the file header.
- **The MacApp needs a rebuilt/reinstalled bundle** to pick this up (`make
  macapp`, or `make local` if running catway from `~/bin`) — stale-bundle
  confusion is the recurring trap here.
- Slug rules have already drifted once (`_` preserved → mapped). If model
  resolution ever silently stops, re-derive the rule by diffing `~/.claude.json`
  project keys against `~/.claude/projects/` dir names — that's how it was
  pinned down this time.
- `go build -tags ghostty ./cmd/catway` drops a `catway` binary in the repo root;
  removed it before committing.
- gopls still flags `cmd/catway/*.go` as excluded (needs `-tags ghostty`) —
  harmless, same as the last two sessions.
