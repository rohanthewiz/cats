# Session: Copilot CLI support in cats, on the enterprise account

- **Session ID:** `4f511712-871a-46de-8b02-9f1eb7648a20`
- **Date:** 2026-08-03
- **Branch:** main (from `bb937ef`)
- **Repo:** `cats` (with `ced` added as a working dir for comparison)

## Request

> I want to start incorporating Copilot support into Cats. CEd has support for
> the enterprise Copilot account. I would like to use that account, but perhaps
> Copilot CLI would be the best fit inside of Cats.

## Two premises that did not survive exploration

**ced has no enterprise-specific Copilot code to port.** It spawns
`copilot-language-server` (`ced/internal/app/copilot.go:325`) and delegates all
auth to it — device flow via the `signIn` JSON-RPC method, credentials written by
the server itself. The `workspace/didChangeConfiguration` settings push at
`copilot.go:379` is literally `{}`. ced works with the enterprise account only
because the machine's shared Copilot credential store is signed into it. No host
config, no proxy handling, no token exchange exists to lift.

**cats already supported the Copilot CLI end-to-end.** It was already wired
through all five agent registries:

| Registry | Location |
|---|---|
| process identity | `internal/detect/detect.go:51` |
| detection manifest | `internal/detect/manifests/github-copilot.json` |
| hook integration target | `internal/integration/integration.go:45`, `installers.go:476` |
| hook-source arbitration | `cmd/catway/hooks.go:232-251` |
| resume argv | `cmd/catway/resume.go:55` (`copilot --resume=<id>`) |

The only missing piece was the binary. So the real work was: prove entitlement,
then close the one genuine gap — per-pane model naming, which was claude-only.

## The EMU wrinkle

The enterprise is an **Enterprise Managed Users** setup (SSO at
`github.com/enterprises/cbre-emu/sso`). The machine holds two distinct GitHub
identities:

| Store | Identity | Used by |
|---|---|---|
| `~/.config/github-copilot/apps.json` | the EMU account (3 OAuth app entries) | copilot CLI, copilot-language-server, IDE plugins, ced |
| `~/.config/gh/hosts.yml` | a separate personal account | `gh` CLI only |

The Copilot seat is on the EMU account. Two consequences worth remembering:

1. The Copilot CLI prefers `GH_TOKEN` / `GITHUB_TOKEN` over its own store.
   Neither is set (verified in the shell *and* in the running MacApp's process
   environment). A stray one would authenticate as the personal account, which
   holds no seat, and the failure reads as a broken Copilot rather than a wrong
   account. cats panes inherit the environment, so it would hit every pane.
2. Enterprises can disable *Copilot in the CLI* independently of Copilot in
   IDEs. This was the plan's gating risk, so step 0 was a probe rather than an
   assumption.

## Step 0: the probe (passed)

`npm i -g @github/copilot` → CLI 1.0.77. The npm prefix puts binaries in
`~/node/bin/bin`, which is not on PATH — the same situation
`copilot-language-server` was already in — so `copilot` was symlinked into
`~/bin`, matching the existing convention. `~/bin` *is* on the MacApp's PATH,
which matters because `resumeArgv` execs a bare `copilot`.

The CLI then authenticated **with no login prompt**, reusing the existing EMU
credential: `apps.json` gained no new entry, and the log reports plan
`business`. A headless `copilot -p ... --available-tools=` returned its answer
and a `--resume=<id>` line. CLI is not blocked by policy.

`catctl integration install copilot` then wrote the hook and the `SessionStart`
entry into `~/.copilot/settings.json` — the installer already existed.

## The transcript format (why this was cheap)

`~/.copilot/session-state/<session-id>/` mirrors claude's layout closely enough
to reuse the whole approach:

- `workspace.yaml` — flat scalar keys, including `cwd:` and `client_name:`
- `events.jsonl` — one typed event per line:

```jsonc
{"type":"assistant.message","data":{"model":"claude-sonnet-4.6", ...}}
{"type":"session.model_change","data":{"newModel":"auto","reasoningEffort":null}}
{"type":"session.auto_mode_resolved","data":{"chosenModel":"gpt-5-mini","reasoningBucket":"low"}}
```

The CLI and the language server share this tree — both drive the same embedded
`copilot-agent` — so editor-started sessions are readable too.

**The surprise:** in the default `--model auto` mode, `session.model_change`
reports the literal `"auto"` with a null effort; the real values arrive in
`session.auto_mode_resolved`. So the model must come from `assistant.message`,
and the effort from *whichever of the two events spoke most recently*.

## The change

### `cmd/catway/agentmodel.go` — per-agent resolver table

`claudeAgent` and the `agent != claudeAgent` comparisons are gone, replaced by:

```go
type modelResolver struct {
	root func() string
	read func(root, cwd, session string) string
}

var modelResolvers = map[string]modelResolver{
	"claude":  {root: claudeProjectsDir, read: claudeModel},
	"copilot": {root: copilotStateDir, read: copilotModel},
}
```

`modelRootsFor()` resolves every root once at construction; `orch.modelRoots`
carries the result. `orch.claudeProjects` **stays** — `usage.go:92`'s estimator
depends on it, and usage was explicitly out of scope.

The session-ref pin now compares `s.agent == agent` rather than against a
constant: a pane that ran claude and then copilot carries the older agent's ref
until the new hook fires, and handing claude's id to copilot's reader would at
best miss and at worst name someone else's directory.

New, deliberately parallel to their claude counterparts:

- `copilotStateDir()` — `$COPILOT_HOME` or `~/.copilot`, then `session-state`
- `copilotSession()` — session id names the directory outright (gated by the
  existing `isBareToken` so a bad ref cannot walk out with `..`); otherwise scan
  for a matching `cwd:` and take the newest. Ordered on the **events file's**
  mtime, not the directory's, since copilot also writes `checkpoints/`, `files/`
  and `research/` under the session.
- `copilotWorkspaceCwd()` — line scan for the top-level `cwd:`, no YAML
  dependency; handles both quote styles and YAML's `''` escape
- `lastCopilotModel()` — backward walk over the tail
- `tailLines()` — the tail read + truncated-first-line handling factored out of
  `lastAssistantModel` and now shared by both agents

### `cmd/catway/web/index.html` — `modelLabel`

Not a crash, as first assumed: `modelLabel("gpt-5.4-mini")` already returned
`"gpt"` (the first token is alphabetic and passes). The defect was
under-specificity — `gpt-5.4` and `gpt-5.4-mini` rendered identically in a list
whose whole job is telling rows apart. Now a trailing size qualifier from a
small allowlist is kept: `gpt-5-mini` → `gpt mini`. Every Anthropic label is
byte-identical (`claude-opus-5 · high` → `opus`, `...[1m]` → `sonnet [1M]`).

### `internal/browserproto/down.go`

`PaneAgent`'s comment claimed "only claude is resolvable today". Corrected — and
that comment is mirrored into generated Dart, so `wire.g.dart` was regenerated.

## One real bug, caught by the tests

The first `lastCopilotModel` walked backwards and returned on the first
`assistant.message` — before ever reaching the effort events that *precede* it.
Two of the effort cases failed. The fix scans for model and effort
independently, keeping the first of each kind and stopping once it holds both.
An effort event legitimately sits on either side of the message it applies to:
the change that configured the turn precedes it, while a switch made afterwards
is what the *next* turn runs under — which is what a row read between turns
should say.

## Verification

- **Unit**: 8 new copilot tests (session-id pin, cwd fallback, four effort
  shapes, oversized `events.jsonl`, no-answer-yet, bad refs, workspace.yaml
  quoting, rollup carries the model). Full suite, `vet`, `fmt-check` clean.
- **Real data**: a throwaway test resolved all ten real copilot sessions on
  disk. Models and effort both resolve (`claude-sonnet-4.6 · medium`,
  `claude-sonnet-5 · medium`, `claude-haiku-4.5 · low` from the auto-mode
  bucket); sessions that never got an answer correctly yield nothing. Test
  removed afterwards.
- **End to end**: ran a **separate** catway+cathost instance on `127.0.0.1:8799`
  with its own sockets, leaving the running Cats.app untouched. `copilot` in a
  pane detected as `agent: copilot`, went `blocked` on the folder-trust prompt
  (the manifest's `selection_blocker` rule) and `working` on the turn. A node
  client speaking the browser protocol (upgrade → `init` frame) received:

```json
{"t":"pane_agent","pane":1,"agent":"copilot","state":"idle","model":"gpt-5-mini · medium","seen":true}
{"t":"agents","items":[{"pane":1,"pub":"w1:p1","agent":"copilot","state":"idle","model":"gpt-5-mini · medium", ...}]}
```

  which `modelLabel` renders as `gpt mini`.
- **Environment**: the running MacApp's process env carries no
  `GH_*`/`GITHUB_*`/`COPILOT_*`, and `~/bin` is on its PATH.

## Files

| File | Change |
|---|---|
| `cmd/catway/agentmodel.go` | resolver table; copilot reader; `tailLines` extracted and shared |
| `cmd/catway/catway.go` | `orch.modelRoots` field + init; `claudeProjects` comment scoped to usage |
| `cmd/catway/agentmodel_test.go` | two tests moved to `modelRoots`; 8 copilot tests + fixtures |
| `cmd/catway/web/index.html` | `modelLabel` keeps a size qualifier |
| `internal/browserproto/down.go` | `PaneAgent` model comment corrected |
| `cmd/catgen-dart/testdata/golden/wire.g.dart` | regenerated (comment only) |

Outside the repo: `@github/copilot` installed globally, `~/bin/copilot`
symlinked, `~/.copilot/hooks/cats-agent-state.sh` + `settings.json` written by
`catctl integration install copilot`.

## Open

- **`make macapp` not run** — the installed app is still on the old build.
- **`wire.g.dart` needs mirroring into `cats-mobile/packages/catsproto/lib/src/generated`**;
  that checkout is not on this machine. Doc-comment only, so nothing functional
  drifts meanwhile.
- **Usage/quota deliberately deferred.** `usage.go` stays Anthropic-only. Found
  a better path than the undocumented endpoint originally feared:
  `session.usage_checkpoint.totalPremiumRequests` in `events.jsonl` is a local
  premium-request counter — no network, no second credential read. A
  `browserproto.Usage` carrying two accounts is still the blocker.
- **ACP fallback never needed.** Had the CLI been policy-blocked, the plan was
  to port ced's `copilot-language-server --acp` path into catway. Worth noting
  the CLI itself also accepts `--acp`, so that door is still open for an in-app
  chat surface later.
- `modelLabel("o3-mini")` → `"mini"`. Pre-existing (the `o3` token has a digit,
  so it is skipped), not a regression, and not in copilot's model list here.
