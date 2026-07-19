# WS7 — Porting Specification: `herdr` Integration Module → Go

Source: `/Users/RAllison3/projs/rust/herdr/src/integration/mod.rs` (5913 lines) + `assets/`
Target: `/Users/RAllison3/projs/go/herdr-web` (new `internal/integration` package + CLI wiring)

Produced by an exploration pass over the Rust sources (2026-07-18). Everything in
`mod.rs` is `pub(crate)`; "public surface" below means the crate-internal entry
points other modules call.

---

## 1. Public API Surface (crate-internal entry points + callers)

### 1a. Primary entry points (called from CLI + app/api)

| Function | Signature | Purpose | Callers |
|---|---|---|---|
| `install_target(target)` | `IntegrationTarget -> io::Result<Vec<String>>` | Installs one integration; returns human-readable message lines. Logs `integration_action("install", label, ok/error)`. Wraps `install_target_inner`. | `cli/integration.rs:65`, `app/api/integrations.rs:13`, `app/mod.rs:1101` (auto-install) |
| `uninstall_target(target)` | `IntegrationTarget -> io::Result<Vec<String>>` | Uninstalls one integration; returns message lines. | `cli/integration.rs:82`, `app/api/integrations.rs:33` |
| `installed_integration_statuses()` | `-> Vec<IntegrationStatus>` | Status of every supported target (NotInstalled/Current/Outdated + versions). | `cli/integration.rs:39` (`status` subcommand) |
| `integration_recommendations()` | `-> Vec<IntegrationRecommendation>` | Per-target recommendation incl. availability + status, for the settings UI. | `app/mod.rs:540,1079,1119` |
| `print_outdated_update_notice()` | `-> bool` | Prints to stderr `"installed herdr integrations need updating; run ..."`; returns true if any outdated. | `cli/integration.rs:35` (`status --outdated-only`), `update.rs:2069` |
| `integration_update_instructions(&[targets])` | `-> String` | Builds "run \`herdr integration install X\`, \`...\` and \`...\`" phrase. | internal |
| `integration_target_label(target)` | `-> &'static str` | Maps enum → lowercase label string (pi/omp/claude/...). | CLI, app |
| `apply_pane_env(cmd, pane_id, public_pane_id)` | mutates `CommandBuilder` | Sets `HERDR_SOCKET_PATH` and `HERDR_PANE_ID` env on spawned pane processes. Pane id = `public_pane_id` or `format!("p_{}", pane_id.raw())`. | `pane.rs:781,821,866` |

### 1b. Per-target install/uninstall functions

`install_pi/omp/claude/codex/kimi/copilot/droid/opencode/kilo/hermes/qodercli/cursor` and matching `uninstall_*`. Each returns its own `*InstallPaths` / `*UninstallResult` struct. `install_target_inner` dispatches to these.

### 1c. Constants

- `HERDR_PANE_ID_ENV_VAR = "HERDR_PANE_ID"`
- `INSTALL_WARNING_PREFIX = "warning:"` — used to filter warning lines into UI messages.

### 1d. Types

- `enum IntegrationStatusKind { NotInstalled, Current, Outdated }`
- `struct IntegrationStatus { target, path: PathBuf, state, installed_version: Option<u32>, expected_version: u32 }`
- `struct IntegrationRecommendation { target, label, command, available: bool, path, state }`
  - `needs_install()` = `Outdated || (available && NotInstalled)`
  - `status_label()`: Current→`"installed"`, Outdated→`"update available"`, NotInstalled&available→`"available"`, NotInstalled&!available→`"not found"`
- Install-path structs: `ClaudeInstallPaths{hook_path,settings_path}`, `CodexInstallPaths{hook_path,hooks_path,config_path}`, `KimiInstallPaths{hook_path,config_path}`, `CopilotInstallPaths{hook_path,settings_path}`, `DroidInstallPaths{hook_path,hooks_path,settings_path,updated_legacy_hooks:bool}`, `OpenCodeInstallPaths{plugin_path}`, `KiloInstallPaths{plugin_path}`, `OmpInstallPaths{extension_path,removed_legacy_pi_extension:bool}`, `HermesInstallPaths{plugin_dir,config_path}`, `QodercliInstallPaths{hook_path,settings_path}`, `CursorInstallPaths{hook_path,hooks_path}`.
- Uninstall-result structs (paths + `removed_*`/`updated_*` bools) for each target.

### 1e. The `IntegrationTarget` enum (from `api/schema.rs:452`)

Wire values (snake_case): `pi, omp, claude, codex, copilot, droid, kimi, opencode, kilo, hermes, qodercli, cursor`.

---

## 2. CLI Commands

Hand-rolled arg parsing (no clap), `cli/integration.rs`, dispatched on the `"integration"` command word. Exit codes: `0` ok, `1` error (stderr), `2` usage error.

```
herdr integration install   <target>
herdr integration uninstall <target>
herdr integration status [--outdated-only]
herdr integration help | --help | -h
```

- Exactly one target required. Unknown target → stderr: `"unknown integration target: {target}"` then `"currently supported: pi, omp, claude, codex, copilot, droid, kimi, opencode, kilo, hermes, qodercli, cursor"`, exit 2.
- Usage line: `"usage: herdr integration {action} <pi|omp|claude|codex|copilot|droid|kimi|opencode|kilo|hermes|qodercli|cursor>"`.
- **`status`** — one line per target: `"{target}: {state} ({path})"`, state ∈ `"not installed"`, `"current (v{n})"`, `"outdated (v{n} < v{expected})"`; missing marker renders `"legacy"` for the version.
- **`status --outdated-only`** — `print_outdated_update_notice()` (stderr only), exit 0.
- **`help`** — full command list to **stderr**. Bare `integration` prints help, exit 2.
- Install/uninstall message lines to **stdout**; errors to stderr, exit 1.

---

## 3. What Gets Installed Where

### 3a. Directory resolution (env override → tilde-expanded; else `$HOME/...`)

`config_dir_from_env_or_home(env_var, segments)`: if env set and non-empty, use it with tilde expansion (`~`, `~/`, `~<rest>` → `$HOME`-based). Else `$HOME` + segments. Empty env vars ignored.

| Target | Env override | Base dir | Install file path |
|---|---|---|---|
| pi | `PI_CODING_AGENT_DIR` | `~/.pi/agent` | `<base>/extensions/herdr-agent-state.ts` |
| omp | `PI_CODING_AGENT_DIR` | `~/.omp/agent` | `<base>/extensions/herdr-omp-agent-state.ts` |
| claude | `CLAUDE_CONFIG_DIR` | `~/.claude` | `<base>/hooks/herdr-agent-state.sh` + `<base>/settings.json` |
| codex | `CODEX_HOME` | `~/.codex` | `<base>/herdr-agent-state.sh` + `<base>/hooks.json` + `<base>/config.toml` |
| kimi | `KIMI_CODE_HOME` | `~/.kimi-code` | `<base>/hooks/herdr-agent-state.sh` + `<base>/config.toml` |
| copilot | `COPILOT_HOME` | `~/.copilot` | `<base>/hooks/herdr-agent-state.sh` + `<base>/settings.json` |
| droid | *(none)* | `~/.factory` | `<base>/hooks/herdr-agent-state.sh` + `<base>/settings.json` (+ legacy `<base>/hooks.json` cleanup) |
| opencode | *(none)* | `~/.config/opencode` | `<base>/plugins/herdr-agent-state.js` |
| kilo | *(none)* | `~/.config/kilo` | `<base>/plugin/herdr-agent-state.js` |
| hermes | *(none)* | `~/.hermes` | `<base>/plugins/herdr-agent-state/{plugin.yaml,__init__.py}` + `<base>/config.yaml` |
| qodercli | `QODER_CONFIG_DIR` | `~/.qoder` | `<base>/hooks/herdr-agent-state.sh` + `<base>/settings.json` |
| cursor | `CURSOR_CONFIG_DIR` | `~/.cursor` | `<base>/herdr-agent-state.sh` + `<base>/hooks.json` |

(Windows uses `.ps1` assets + `powershell` commands; the Go port targets unix — see §6.)

### 3c. Permission bits

`make_executable(path)` = mode `0o755` on every hook `.sh` after write. **Not** applied to `.js`/`.ts`/`.py`/`.yaml` plugin files.

### 3d. Markers / sentinels (verbatim)

- Version marker: `HERDR_INTEGRATION_VERSION=` — parsed by stripping a line's leading `/` and `#` chars + whitespace, then prefix-match, parse u32.
- Id marker: `HERDR_INTEGRATION_ID=<target>` (legacy detection; omp removes a legacy pi extension by checking `HERDR_INTEGRATION_ID=pi`).
- Kimi config sentinels in `config.toml`: begin `# >>> herdr kimi integration`, end `# <<< herdr kimi integration`.
- Codex config: `[features]` table with `hooks = true` (removes deprecated `codex_hooks` keys under top-level `[features]` only).
- Hermes `config.yaml`: manages `plugins.enabled` list containing `herdr-agent-state`.

### 3e. Expected versions (each asset's marker must match)

`PI=2, OMP=2, CLAUDE=5, CODEX=5, KIMI=3, COPILOT=2, DROID=2, OPENCODE=5, KILO=1, HERMES=2, QODERCLI=2, CURSOR=1`.

### 3f. Backup behavior

**None.** Hook scripts fully overwritten; JSON/TOML/YAML configs parsed, minimally mutated, rewritten pretty. No `.bak`.

### 3g. JSON settings mutation model

Settings read as generic JSON; top-level `hooks` object ensured; rewritten pretty (2-space). Three hook shapes:

1. **Nested** (claude, codex, droid, qodercli) — event → array of `{ "matcher"?: "...", "hooks": [ { "type":"command", "command":"<cmd>", "timeout": 10 } ] }`. Idempotent: skip if matching `command` present. Empty groups/events pruned on removal.
2. **Flat/direct** (copilot) — event → array of `{ "type":"command", "matcher"?, "bash":"<cmd>", "timeoutSec": 10 }`.
3. **Simple** (cursor) — event → array of `{ "command": "<cmd>" }`, plus top-level `"version": 1`.

**Hook command string** (unix): `bash '<path>' <action>` (single-quoted, `'` → `'"'"'`); action appended only if present. Uninstall removes all historical variants (`hook_command_variants`).

### 3h. Per-target hook events (installed + deprecated-removed)

- **Claude** (nested, matcher `"*"`, timeout 10): installs `SessionStart`→`session`. Removes deprecated: `PostToolUse/PostToolUseFailure/SubagentStop`(working), `PermissionRequest`(blocked), `SessionStart`(idle), `UserPromptSubmit`(working), `PreToolUse`(working), `Stop`(idle), `SessionEnd`(release), `SessionStart`(session dup).
- **Codex** (nested, no matcher, timeout 10): installs `SessionStart`→`session`. Removes: `PermissionRequest`(blocked), `SessionStart`(idle), `UserPromptSubmit/PreToolUse`(working), `Stop`(idle). Plus `config.toml` `[features] hooks = true`.
- **Kimi** (TOML block): 10 `[[hooks]]` tables between sentinels, `KIMI_HOOK_EVENTS`: `SessionStart→session, UserPromptSubmit→working, PreToolUse→working, SubagentStart→working, PreCompact→working, PermissionRequest→blocked, PermissionResult→working, Stop→idle, Interrupt→idle, SessionEnd→release`. Each: `event`, `command`, `timeout = 10`. Min agent version `0.14.0` enforced (`kimi --version`; unrunnable → `warning:` line, proceed; older → hard error `"kimi code X.Y.Z is too old: herdr hooks require kimi code 0.14.0 or newer. upgrade kimi code, then re-run install"`).
- **Copilot** (flat, timeout 10): installs `SessionStart`. Removes legacy: `UserPromptSubmit, PreToolUse, PostToolUse, PostToolUseFailure, Stop, agentStop, SessionEnd, notification, sessionStart`.
- **Droid** (nested, no matcher): installs `SessionStart`→`session`. Removes legacy lifecycle set from **both** `settings.json` and legacy `hooks.json` (latter only cleaned, never written).
- **Qodercli** (nested, matcher `"*"`): installs `SessionStart`→`session`. Removes 12-event legacy set.
- **Cursor** (simple): `bash '<hook_path>' session` on `sessionStart`. Removes `beforeSubmitPrompt/beforeShellExecution/beforeMCPExecution/stop/sessionEnd` (+ `sessionStart` on uninstall). Ensures top-level `"version": 1`.

### 3i. Non-JSON config mutation (port line-for-line, no parser libraries)

- **codex config.toml**: line-based; find top-level `[features]`, set/insert `hooks = true`, remove `codex_hooks` keys, append `[features]\nhooks = true\n` if absent. Only top-level (not `[profiles.*.features]`).
- **kimi config.toml**: remove sentinel block, then append fresh block. TOML strings escaped JSON-like (`toml_basic_string`).
- **hermes config.yaml**: hand-written YAML editor handling: missing `plugins:` (creates `plugins:\n  enabled:\n    - herdr-agent-state\n`), block `enabled:` list, `enabled: []`, inline flow list `plugins: [a, b]`, flat block list under `plugins:`, quoted scalars, inline comments. Most intricate piece.

---

## 4. Asset Inventory (`assets/<agent>/`)

Assets are **embedded byte-for-byte** (no install-time templating). Each carries a "managed by herdr" banner + `HERDR_INTEGRATION_ID=<id>` + `HERDR_INTEGRATION_VERSION=<n>`. Runtime contract: guard on `HERDR_ENV == "1"`, non-empty `HERDR_SOCKET_PATH` + `HERDR_PANE_ID`; connect to unix socket, send one newline-terminated JSON-RPC request, read ≤4096 bytes, close. `source` = `herdr:<agent>`.

Key files: `claude/codex/copilot/cursor/droid/qodercli/*.sh` (session-report hooks → `pane.report_agent_session`), `kimi/*.sh` (full lifecycle → `report_agent`/`release_agent` too), `opencode|kilo/herdr-agent-state.js` (Node plugins), `pi|omp/herdr-agent-state.ts` (rich TS state machines), `hermes/{plugin.yaml,__init__.py}`. `.ps1` variants exist for windows-supported targets (not used by the unix Go port).

---

## 5. Detection / Status Logic

- **Version stamp, not hash.** File absent → `NotInstalled`; else parse `HERDR_INTEGRATION_VERSION=<u32>`; `Current` iff `>= expected`; missing marker → version None → `Outdated` (legacy).
- **Marker file per target** = pi/omp extension `.ts`; claude/kimi/copilot/droid/qodercli `hooks/herdr-agent-state.sh`; codex/cursor base-dir `.sh`; opencode `plugins/*.js`; kilo `plugin/*.js`; hermes `plugins/herdr-agent-state/__init__.py`.
- **Availability**: platform-supported AND (a target command on `PATH` (executable bit) OR target-specific layout exists). Command names = label, except kilo=`[kilo, kilo-code]`, cursor=`[cursor-agent]`. Layout fallbacks: codex standalone binary under `<codex_dir>/packages/standalone/releases/*/bin/codex`. `available` also true when already installed.
- Status labels for UI: `✓`/`↻`/`+`/`–`.

---

## 6. Platform Specifics

- Windows-supported targets (Rust): Claude, Codex, Copilot, Droid, Kimi, Qodercli. Others error `"{label} integration is not supported on Windows"` before any config lookup. **Go port: unix-only for now** — on `runtime.GOOS == "windows"` return not-supported errors; embed only `.sh`/plugin assets.
- Env vars honored: `HERDR_PANE_ID`, `HERDR_SOCKET_PATH`, `HERDR_ENV`, per-target dir overrides (§3a), `HOME`, `PATH`.
- Errors: descriptive, e.g. `"claude directory not found at {path}. install claude code first"`; JSON parse → `"failed to parse {path}: {err}"`; wrong shape → `"{desc} at {path} must be a JSON object"`.
- Idempotency everywhere; writes skipped when content unchanged.

---

## 7. Tests to Mirror (Rust has 93 test fns, ~2600 lines)

Priority set: version-triple parsing/order; status detection incl. legacy marker; per-target install/uninstall/idempotency/env-override/missing-dir (all 12); hermes YAML editor matrix (flat list, flow list, quoted, commented); codex TOML top-level-only migration; kimi sentinel block + hook table assertions; golden asset-content test (`bundled_integration_assets_report_session_refs`: every asset contains `pane.report_agent_session`/`agent_session_id`; claude/codex/droid/qodercli/copilot/cursor NOT contain `pane.release_agent`; kimi contains it; cursor contains `conversation_id`/`sessionStart`); hook-command shell quoting; marker-parse forms (`//`, `#`, `# `).

## Go Port Notes / Gotchas

1. `//go:embed` the assets verbatim (copy the directory from the Rust repo unmodified).
2. JSON mutation must preserve unrelated user keys **and their order** — implement a small ordered-JSON-object type (token-stream decode, order-preserving marshal, 2-space pretty) rather than `map[string]any`.
3. Port the TOML/YAML line editors by hand; do not substitute parser libraries (comment/format preservation is load-bearing for idempotency tests).
4. Exit codes 0/1/2 and stdout/stderr split are CLI contract.
5. Provide `PaneEnv(socketPath, paneID, publicID)` for later spawn-env wiring (`HERDR_SOCKET_PATH`, `HERDR_PANE_ID`, `HERDR_ENV=1`) — not wired yet; the Go side has no `pane.report_agent*` API (WS5/WS2 tail; see session notes).
