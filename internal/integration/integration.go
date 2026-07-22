// Package integration ports cats's Rust integration module: shell-hook and
// plugin installers that wire coding agents (claude, codex, kimi, ...) to a
// running cats server. Each install drops an embedded asset into the agent's
// config tree and registers it in the agent's own settings format — JSON
// rewritten order-preservingly, TOML and YAML edited line-wise — so unrelated
// user configuration survives byte-for-byte. Status detection reads the
// CATS_INTEGRATION_VERSION marker stamped in every asset.
//
// The port is unix-only: on Windows every target reports not-supported (the
// Rust .ps1 assets are not embedded).
package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// CatsPaneIDEnvVar names the env var carrying the public pane id into spawned
// pane processes; the hook scripts echo it back to the server.
const CatsPaneIDEnvVar = "CATS_PANE_ID"

// InstallWarningPrefix marks message lines that are warnings rather than
// results; UIs filter on it.
const InstallWarningPrefix = "warning:"

const (
	catsSocketPathEnvVar = "CATS_SOCKET_PATH"
	catsEnvEnvVar        = "CATS_ENV"
)

// Target is one supported coding-agent integration.
type Target int

// Declaration order is the canonical order: integration_specs, status listings
// and the CLI's "currently supported" line all follow it.
const (
	TargetPi Target = iota
	TargetOmp
	TargetClaude
	TargetCodex
	TargetCopilot
	TargetDroid
	TargetKimi
	TargetOpencode
	TargetKilo
	TargetHermes
	TargetQodercli
	TargetCursor
)

// AllTargets returns every target in canonical order.
func AllTargets() []Target {
	return []Target{
		TargetPi, TargetOmp, TargetClaude, TargetCodex, TargetCopilot, TargetDroid,
		TargetKimi, TargetOpencode, TargetKilo, TargetHermes, TargetQodercli, TargetCursor,
	}
}

// Label returns the lowercase wire/CLI name of the target.
func (t Target) Label() string {
	switch t {
	case TargetPi:
		return "pi"
	case TargetOmp:
		return "omp"
	case TargetClaude:
		return "claude"
	case TargetCodex:
		return "codex"
	case TargetCopilot:
		return "copilot"
	case TargetDroid:
		return "droid"
	case TargetKimi:
		return "kimi"
	case TargetOpencode:
		return "opencode"
	case TargetKilo:
		return "kilo"
	case TargetHermes:
		return "hermes"
	case TargetQodercli:
		return "qodercli"
	case TargetCursor:
		return "cursor"
	}
	return fmt.Sprintf("target(%d)", int(t))
}

// ParseTarget maps a label back to its Target.
func ParseTarget(name string) (Target, bool) {
	for _, t := range AllTargets() {
		if t.Label() == name {
			return t, true
		}
	}
	return 0, false
}

// Install names, embedded-asset versions and per-target env overrides. The
// versions must equal the CATS_INTEGRATION_VERSION marker inside the matching
// embedded asset (asserted by tests); bump both together.
const (
	piExtensionInstallName = "cats-agent-state.ts"
	piIntegrationVersion   = 2

	ompExtensionInstallName = "cats-omp-agent-state.ts"
	ompIntegrationVersion   = 2

	piCodingAgentDirEnvVar = "PI_CODING_AGENT_DIR"

	claudeHookInstallName    = "cats-agent-state.sh"
	claudeIntegrationVersion = 5
	claudeConfigDirEnvVar    = "CLAUDE_CONFIG_DIR"

	codexHookInstallName    = "cats-agent-state.sh"
	codexIntegrationVersion = 5
	codexHomeEnvVar         = "CODEX_HOME"

	kimiHookInstallName    = "cats-agent-state.sh"
	kimiIntegrationVersion = 3
	kimiCodeHomeEnvVar     = "KIMI_CODE_HOME"
	kimiConfigBlockBegin   = "# >>> cats kimi integration"
	kimiConfigBlockEnd     = "# <<< cats kimi integration"
	kimiMinVersion         = "0.14.0"

	copilotHookInstallName    = "cats-agent-state.sh"
	copilotIntegrationVersion = 2
	copilotHomeEnvVar         = "COPILOT_HOME"

	droidHookInstallName    = "cats-agent-state.sh"
	droidIntegrationVersion = 2

	opencodePluginInstallName  = "cats-agent-state.js"
	opencodeIntegrationVersion = 5

	kiloPluginInstallName  = "cats-agent-state.js"
	kiloIntegrationVersion = 1

	hermesPluginInstallName         = "cats-agent-state"
	hermesPluginManifestInstallName = "plugin.yaml"
	hermesPluginInitInstallName     = "__init__.py"
	hermesIntegrationVersion        = 2

	qodercliHookInstallName    = "cats-agent-state.sh"
	qodercliIntegrationVersion = 2
	qodercliConfigDirEnvVar    = "QODER_CONFIG_DIR"

	cursorHookInstallName    = "cats-agent-state.sh"
	cursorIntegrationVersion = 1
	cursorConfigDirEnvVar    = "CURSOR_CONFIG_DIR"

	integrationVersionMarker = "CATS_INTEGRATION_VERSION="
)

type hookEvent struct {
	event  string
	action string
}

// kimiHookEvents is the full lifecycle table written between the kimi
// config.toml sentinels.
var kimiHookEvents = []hookEvent{
	{"SessionStart", "session"},
	{"UserPromptSubmit", "working"},
	{"PreToolUse", "working"},
	{"SubagentStart", "working"},
	{"PreCompact", "working"},
	{"PermissionRequest", "blocked"},
	{"PermissionResult", "working"},
	{"Stop", "idle"},
	{"Interrupt", "idle"},
	{"SessionEnd", "release"},
}

var copilotHookEvents = []string{"SessionStart"}

var copilotRemovedLifecycleHookEvents = []string{
	"UserPromptSubmit",
	"PreToolUse",
	"PostToolUse",
	"PostToolUseFailure",
	"Stop",
	"agentStop",
	"SessionEnd",
	"notification",
	"sessionStart",
}

var droidHookEvents = []hookEvent{{"SessionStart", "session"}}

var droidRemovedLifecycleHookEvents = []hookEvent{
	{"SessionStart", "idle"},
	{"UserPromptSubmit", "working"},
	{"PreToolUse", "working"},
	{"PostToolUse", "working"},
	{"Notification", "blocked"},
	{"Stop", "idle"},
	{"SubagentStop", "working"},
	{"PreCompact", "working"},
	{"SessionEnd", "release"},
}

var qodercliHookEvents = []hookEvent{{"SessionStart", "session"}}

var qodercliRemovedLifecycleHookEvents = []hookEvent{
	{"SessionStart", "idle"},
	{"UserPromptSubmit", "working"},
	{"PreToolUse", "working"},
	{"PostToolUse", "working"},
	{"PostToolUseFailure", "working"},
	{"SubagentStart", "working"},
	{"SubagentStop", "working"},
	{"PreCompact", "working"},
	{"Notification", "blocked"},
	{"PermissionRequest", "blocked"},
	{"Stop", "idle"},
	{"SessionEnd", "release"},
}

// PaneEnv builds the env entries ("KEY=VALUE") a spawned pane process needs so
// the installed hooks can reach the server: socket path, pane id (the public
// id, or "p_<raw>" when none is assigned yet), and the CATS_ENV=1 guard the
// hook scripts check before doing anything.
func PaneEnv(socketPath string, paneID uint64, publicPaneID string) []string {
	id := publicPaneID
	if id == "" {
		id = fmt.Sprintf("p_%d", paneID)
	}
	return []string{
		catsSocketPathEnvVar + "=" + socketPath,
		CatsPaneIDEnvVar + "=" + id,
		catsEnvEnvVar + "=1",
	}
}

// StatusKind is the install state of one integration.
type StatusKind int

const (
	StatusNotInstalled StatusKind = iota
	StatusCurrent
	StatusOutdated
)

// Status reports one target's marker file, its parsed version and whether it
// is current against the embedded asset.
type Status struct {
	Target Target
	Path   string
	State  StatusKind
	// InstalledVersion is the parsed CATS_INTEGRATION_VERSION, or -1 when
	// the file exists without a readable marker (a legacy install).
	InstalledVersion int
	ExpectedVersion  int
}

// Recommendation is one target's row for a settings UI: whether the agent
// looks present on this machine and whether its integration needs work.
type Recommendation struct {
	Target    Target
	Label     string
	Command   string
	Available bool
	Path      string
	State     StatusKind
}

// NeedsInstall reports whether install would change anything useful: the
// integration is outdated, or the agent is present but not integrated.
func (r Recommendation) NeedsInstall() bool {
	return r.State == StatusOutdated || (r.Available && r.State == StatusNotInstalled)
}

// StatusLabel renders the state for display.
func (r Recommendation) StatusLabel() string {
	switch {
	case r.State == StatusCurrent:
		return "installed"
	case r.State == StatusOutdated:
		return "update available"
	case r.Available:
		return "available"
	default:
		return "not found"
	}
}

// InstallTarget installs one integration and returns human-readable message
// lines (warnings carry InstallWarningPrefix).
func InstallTarget(target Target) ([]string, error) {
	if !targetSupported(target) {
		return nil, fmt.Errorf("%s integration is not supported on Windows", target.Label())
	}

	var versionWarning string
	if req := versionRequirementFor(target); req != nil {
		warning, err := enforceAgentVersion(req)
		if err != nil {
			return nil, err
		}
		versionWarning = warning
	}

	messages, err := installTargetMessages(target)
	if err != nil {
		return nil, err
	}
	if versionWarning != "" {
		messages = append(messages, versionWarning)
	}
	return messages, nil
}

func installTargetMessages(target Target) ([]string, error) {
	switch target {
	case TargetPi:
		path, err := InstallPi()
		if err != nil {
			return nil, err
		}
		return []string{fmt.Sprintf("installed pi integration to %s", path)}, nil
	case TargetOmp:
		installed, err := InstallOmp()
		if err != nil {
			return nil, err
		}
		var messages []string
		if installed.RemovedLegacyPiExtension {
			messages = append(messages, fmt.Sprintf(
				"removed legacy pi integration from omp extension directory at %s",
				filepath.Join(filepath.Dir(installed.ExtensionPath), piExtensionInstallName)))
		}
		messages = append(messages,
			fmt.Sprintf("installed omp integration to %s", installed.ExtensionPath))
		return messages, nil
	case TargetClaude:
		installed, err := InstallClaude()
		if err != nil {
			return nil, err
		}
		return []string{
			fmt.Sprintf("installed claude integration hook to %s", installed.HookPath),
			fmt.Sprintf("ensured claude settings at %s", installed.SettingsPath),
		}, nil
	case TargetCodex:
		installed, err := InstallCodex()
		if err != nil {
			return nil, err
		}
		return []string{
			fmt.Sprintf("installed codex integration hook to %s", installed.HookPath),
			fmt.Sprintf("ensured codex hooks at %s", installed.HooksPath),
			fmt.Sprintf("ensured codex config at %s", installed.ConfigPath),
		}, nil
	case TargetCopilot:
		installed, err := InstallCopilot()
		if err != nil {
			return nil, err
		}
		return []string{
			fmt.Sprintf("installed copilot integration hook to %s", installed.HookPath),
			fmt.Sprintf("ensured copilot settings at %s", installed.SettingsPath),
		}, nil
	case TargetKimi:
		installed, err := InstallKimi()
		if err != nil {
			return nil, err
		}
		return []string{
			fmt.Sprintf("installed kimi integration hook to %s", installed.HookPath),
			fmt.Sprintf("ensured kimi config at %s", installed.ConfigPath),
			fmt.Sprintf("requires kimi code %s or newer", kimiMinVersion),
		}, nil
	case TargetDroid:
		installed, err := InstallDroid()
		if err != nil {
			return nil, err
		}
		messages := []string{
			fmt.Sprintf("installed droid integration hook to %s", installed.HookPath),
			fmt.Sprintf("ensured droid hooks at %s", installed.SettingsPath),
		}
		if installed.UpdatedLegacyHooks {
			messages = append(messages, fmt.Sprintf(
				"removed legacy cats droid hook entries from %s", installed.HooksPath))
		}
		return messages, nil
	case TargetOpencode:
		installed, err := InstallOpencode()
		if err != nil {
			return nil, err
		}
		return []string{
			fmt.Sprintf("installed opencode integration plugin to %s", installed.PluginPath),
		}, nil
	case TargetKilo:
		installed, err := InstallKilo()
		if err != nil {
			return nil, err
		}
		return []string{
			fmt.Sprintf("installed kilo integration plugin to %s", installed.PluginPath),
		}, nil
	case TargetHermes:
		installed, err := InstallHermes()
		if err != nil {
			return nil, err
		}
		return []string{
			fmt.Sprintf("installed hermes integration plugin to %s", installed.PluginDir),
			fmt.Sprintf("enabled hermes plugin in %s", installed.ConfigPath),
		}, nil
	case TargetQodercli:
		installed, err := InstallQodercli()
		if err != nil {
			return nil, err
		}
		return []string{
			fmt.Sprintf("installed qodercli integration hook to %s", installed.HookPath),
			fmt.Sprintf("ensured qodercli settings at %s", installed.SettingsPath),
		}, nil
	case TargetCursor:
		installed, err := InstallCursor()
		if err != nil {
			return nil, err
		}
		return []string{
			fmt.Sprintf("installed cursor integration hook to %s", installed.HookPath),
			fmt.Sprintf("updated cursor hooks at %s", installed.HooksPath),
		}, nil
	}
	return nil, fmt.Errorf("unknown integration target %d", int(target))
}

// UninstallTarget removes one integration and returns message lines.
func UninstallTarget(target Target) ([]string, error) {
	switch target {
	case TargetPi:
		result, err := UninstallPi()
		if err != nil {
			return nil, err
		}
		if result.RemovedExtension {
			return []string{fmt.Sprintf("removed pi integration extension at %s", result.ExtensionPath)}, nil
		}
		return []string{fmt.Sprintf("no pi integration extension found at %s", result.ExtensionPath)}, nil
	case TargetOmp:
		result, err := UninstallOmp()
		if err != nil {
			return nil, err
		}
		if result.RemovedExtension {
			return []string{fmt.Sprintf("removed omp integration extension at %s", result.ExtensionPath)}, nil
		}
		return []string{fmt.Sprintf("no omp integration extension found at %s", result.ExtensionPath)}, nil
	case TargetClaude:
		result, err := UninstallClaude()
		if err != nil {
			return nil, err
		}
		var messages []string
		if result.RemovedHookFile {
			messages = append(messages, fmt.Sprintf("removed claude hook at %s", result.HookPath))
		} else {
			messages = append(messages, fmt.Sprintf("no claude hook found at %s", result.HookPath))
		}
		if result.UpdatedSettings {
			messages = append(messages, fmt.Sprintf("removed cats claude hook entries from %s", result.SettingsPath))
		} else {
			messages = append(messages, fmt.Sprintf("no cats claude hook entries found in %s", result.SettingsPath))
		}
		return messages, nil
	case TargetCodex:
		result, err := UninstallCodex()
		if err != nil {
			return nil, err
		}
		var messages []string
		if result.RemovedHookFile {
			messages = append(messages, fmt.Sprintf("removed codex hook at %s", result.HookPath))
		} else {
			messages = append(messages, fmt.Sprintf("no codex hook found at %s", result.HookPath))
		}
		if result.UpdatedHooks {
			messages = append(messages, fmt.Sprintf("removed cats codex hook entries from %s", result.HooksPath))
		} else {
			messages = append(messages, fmt.Sprintf("no cats codex hook entries found in %s", result.HooksPath))
		}
		messages = append(messages, fmt.Sprintf("left codex config unchanged at %s", result.ConfigPath))
		return messages, nil
	case TargetCopilot:
		result, err := UninstallCopilot()
		if err != nil {
			return nil, err
		}
		var messages []string
		if result.RemovedHookFile {
			messages = append(messages, fmt.Sprintf("removed copilot hook at %s", result.HookPath))
		} else {
			messages = append(messages, fmt.Sprintf("no copilot hook found at %s", result.HookPath))
		}
		if result.UpdatedSettings {
			messages = append(messages, fmt.Sprintf("removed cats copilot hook entries from %s", result.SettingsPath))
		} else {
			messages = append(messages, fmt.Sprintf("no cats copilot hook entries found in %s", result.SettingsPath))
		}
		return messages, nil
	case TargetKimi:
		result, err := UninstallKimi()
		if err != nil {
			return nil, err
		}
		var messages []string
		if result.RemovedHookFile {
			messages = append(messages, fmt.Sprintf("removed kimi hook at %s", result.HookPath))
		} else {
			messages = append(messages, fmt.Sprintf("no kimi hook found at %s", result.HookPath))
		}
		if result.UpdatedConfig {
			messages = append(messages, fmt.Sprintf("removed cats kimi hook entries from %s", result.ConfigPath))
		} else {
			messages = append(messages, fmt.Sprintf("no cats kimi hook entries found in %s", result.ConfigPath))
		}
		return messages, nil
	case TargetDroid:
		result, err := UninstallDroid()
		if err != nil {
			return nil, err
		}
		var messages []string
		if result.RemovedHookFile {
			messages = append(messages, fmt.Sprintf("removed droid hook at %s", result.HookPath))
		} else {
			messages = append(messages, fmt.Sprintf("no droid hook found at %s", result.HookPath))
		}
		if result.UpdatedHooks {
			messages = append(messages, fmt.Sprintf("removed legacy cats droid hook entries from %s", result.HooksPath))
		} else {
			messages = append(messages, fmt.Sprintf("no legacy cats droid hook entries found in %s", result.HooksPath))
		}
		if result.UpdatedSettings {
			messages = append(messages, fmt.Sprintf("removed cats droid hook entries from %s", result.SettingsPath))
		} else {
			messages = append(messages, fmt.Sprintf("no cats droid hook entries found in %s", result.SettingsPath))
		}
		return messages, nil
	case TargetOpencode:
		result, err := UninstallOpencode()
		if err != nil {
			return nil, err
		}
		if result.RemovedPlugin {
			return []string{fmt.Sprintf("removed opencode integration plugin at %s", result.PluginPath)}, nil
		}
		return []string{fmt.Sprintf("no opencode integration plugin found at %s", result.PluginPath)}, nil
	case TargetKilo:
		result, err := UninstallKilo()
		if err != nil {
			return nil, err
		}
		if result.RemovedPlugin {
			return []string{fmt.Sprintf("removed kilo integration plugin at %s", result.PluginPath)}, nil
		}
		return []string{fmt.Sprintf("no kilo integration plugin found at %s", result.PluginPath)}, nil
	case TargetHermes:
		result, err := UninstallHermes()
		if err != nil {
			return nil, err
		}
		var messages []string
		if result.RemovedPluginDir {
			messages = append(messages, fmt.Sprintf("removed hermes integration plugin at %s", result.PluginDir))
		} else {
			messages = append(messages, fmt.Sprintf("no hermes integration plugin found at %s", result.PluginDir))
		}
		if result.UpdatedConfig {
			messages = append(messages, fmt.Sprintf("disabled hermes plugin in %s", result.ConfigPath))
		} else {
			messages = append(messages, fmt.Sprintf("no hermes plugin entry found in %s", result.ConfigPath))
		}
		return messages, nil
	case TargetQodercli:
		result, err := UninstallQodercli()
		if err != nil {
			return nil, err
		}
		var messages []string
		if result.RemovedHookFile {
			messages = append(messages, fmt.Sprintf("removed qodercli hook at %s", result.HookPath))
		} else {
			messages = append(messages, fmt.Sprintf("no qodercli hook found at %s", result.HookPath))
		}
		if result.UpdatedSettings {
			messages = append(messages, fmt.Sprintf("removed cats qodercli hook entries from %s", result.SettingsPath))
		} else {
			messages = append(messages, fmt.Sprintf("no cats qodercli hook entries found in %s", result.SettingsPath))
		}
		return messages, nil
	case TargetCursor:
		result, err := UninstallCursor()
		if err != nil {
			return nil, err
		}
		var messages []string
		if result.RemovedHookFile {
			messages = append(messages, fmt.Sprintf("removed cursor hook at %s", result.HookPath))
		} else {
			messages = append(messages, fmt.Sprintf("no cursor hook found at %s", result.HookPath))
		}
		if result.UpdatedHooks {
			messages = append(messages, fmt.Sprintf("removed cats cursor hook entries from %s", result.HooksPath))
		} else {
			messages = append(messages, fmt.Sprintf("no cats cursor hook entries found in %s", result.HooksPath))
		}
		return messages, nil
	}
	return nil, fmt.Errorf("unknown integration target %d", int(target))
}

// targetSupported: the Go port supports every target on unix and none on
// Windows (the Rust build supports the CLI-hook subset there via .ps1 assets).
func targetSupported(Target) bool {
	return runtime.GOOS != "windows"
}

func targetCommand(target Target) string {
	return targetCommandNames(target)[0]
}

// targetCommandNames lists the executables whose presence on PATH marks the
// agent as installed. Most agents ship a binary named after their label.
func targetCommandNames(target Target) []string {
	switch target {
	case TargetKilo:
		return []string{"kilo", "kilo-code"}
	case TargetCursor:
		return []string{"cursor-agent"}
	default:
		return []string{target.Label()}
	}
}

func targetAvailable(target Target) bool {
	if !targetSupported(target) {
		return false
	}
	for _, command := range targetCommandNames(target) {
		if commandAvailable(command) {
			return true
		}
	}
	return targetInstallLayoutAvailable(target)
}

// targetInstallLayoutAvailable covers agents detectable by their install tree
// rather than a PATH entry. Hermes' layout fallback is Windows-only in Rust,
// so it contributes nothing on this unix-only port.
func targetInstallLayoutAvailable(target Target) bool {
	if target == TargetCodex {
		return codexStandaloneBinaryAvailable()
	}
	return false
}

func commandAvailable(command string) bool {
	paths := os.Getenv("PATH")
	if paths == "" {
		return false
	}
	for _, dir := range filepath.SplitList(paths) {
		if executableFileExists(filepath.Join(dir, command)) {
			return true
		}
	}
	return false
}

func executableFileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}

// codexStandaloneBinaryAvailable detects codex's self-contained install layout
// (<codex_dir>/packages/standalone/releases/<v>/bin/codex) when no `codex` is
// on PATH.
func codexStandaloneBinaryAvailable() bool {
	dir, err := codexDir()
	if err != nil {
		return false
	}
	entries, err := os.ReadDir(filepath.Join(dir, "packages", "standalone", "releases"))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		candidate := filepath.Join(dir, "packages", "standalone", "releases", entry.Name(), "bin", "codex")
		if executableFileExists(candidate) {
			return true
		}
	}
	return false
}

type integrationSpec struct {
	target          Target
	path            string
	pathErr         error
	expectedVersion int
}

func integrationSpecs() []integrationSpec {
	spec := func(target Target, dir string, dirErr error, expected int, segments ...string) integrationSpec {
		if dirErr != nil {
			return integrationSpec{target: target, pathErr: dirErr, expectedVersion: expected}
		}
		return integrationSpec{
			target:          target,
			path:            filepath.Join(append([]string{dir}, segments...)...),
			expectedVersion: expected,
		}
	}

	piDir, piErr := piExtensionDir()
	ompDir, ompErr := ompExtensionDir()
	claudeDir, claudeErr := claudeDir()
	codexDir, codexErr := codexDir()
	copilotDir, copilotErr := copilotDir()
	droidDir, droidErr := droidDir()
	kimiDir, kimiErr := kimiDir()
	opencodeDir, opencodeErr := opencodeDir()
	kiloDir, kiloErr := kiloDir()
	hermesPluginDir, hermesErr := hermesPluginDir()
	qodercliDir, qodercliErr := qodercliDir()
	cursorDir, cursorErr := cursorDir()

	return []integrationSpec{
		spec(TargetPi, piDir, piErr, piIntegrationVersion, piExtensionInstallName),
		spec(TargetOmp, ompDir, ompErr, ompIntegrationVersion, ompExtensionInstallName),
		spec(TargetClaude, claudeDir, claudeErr, claudeIntegrationVersion, "hooks", claudeHookInstallName),
		spec(TargetCodex, codexDir, codexErr, codexIntegrationVersion, codexHookInstallName),
		spec(TargetCopilot, copilotDir, copilotErr, copilotIntegrationVersion, "hooks", copilotHookInstallName),
		spec(TargetDroid, droidDir, droidErr, droidIntegrationVersion, "hooks", droidHookInstallName),
		spec(TargetKimi, kimiDir, kimiErr, kimiIntegrationVersion, "hooks", kimiHookInstallName),
		spec(TargetOpencode, opencodeDir, opencodeErr, opencodeIntegrationVersion, "plugins", opencodePluginInstallName),
		spec(TargetKilo, kiloDir, kiloErr, kiloIntegrationVersion, "plugin", kiloPluginInstallName),
		spec(TargetHermes, hermesPluginDir, hermesErr, hermesIntegrationVersion, hermesPluginInitInstallName),
		spec(TargetQodercli, qodercliDir, qodercliErr, qodercliIntegrationVersion, "hooks", qodercliHookInstallName),
		spec(TargetCursor, cursorDir, cursorErr, cursorIntegrationVersion, cursorHookInstallName),
	}
}

// InstalledIntegrationStatuses reports the install state of every supported
// target whose install path can be resolved.
func InstalledIntegrationStatuses() []Status {
	var statuses []Status
	for _, spec := range integrationSpecs() {
		if !targetSupported(spec.target) || spec.pathErr != nil {
			continue
		}
		statuses = append(statuses, integrationStatusAt(spec.target, spec.path, spec.expectedVersion))
	}
	return statuses
}

// Recommendations builds the per-target settings-UI rows: availability
// (command on PATH, install layout, or already installed) plus install state.
func Recommendations() []Recommendation {
	var recs []Recommendation
	for _, spec := range integrationSpecs() {
		if !targetSupported(spec.target) || spec.pathErr != nil {
			continue
		}
		status := integrationStatusAt(spec.target, spec.path, spec.expectedVersion)
		recs = append(recs, Recommendation{
			Target:    spec.target,
			Label:     spec.target.Label(),
			Command:   targetCommand(spec.target),
			Available: targetAvailable(spec.target) || status.State != StatusNotInstalled,
			Path:      spec.path,
			State:     status.State,
		})
	}
	return recs
}

func outdatedInstalledIntegrations() []Status {
	var outdated []Status
	for _, status := range InstalledIntegrationStatuses() {
		if status.State == StatusOutdated {
			outdated = append(outdated, status)
		}
	}
	return outdated
}

// UpdateInstructions builds the "run `catctl integration install X`, ... and
// `...`" phrase for a set of targets.
func UpdateInstructions(targets []Target) string {
	commands := make([]string, 0, len(targets))
	for _, target := range targets {
		commands = append(commands, fmt.Sprintf("`catctl integration install %s`", target.Label()))
	}
	switch len(commands) {
	case 0:
		return ""
	case 1:
		return "run " + commands[0]
	default:
		return fmt.Sprintf("run %s and %s",
			strings.Join(commands[:len(commands)-1], ", "), commands[len(commands)-1])
	}
}

// OutdatedUpdateNotice returns the "integrations need updating" notice, or
// ok=false when every installed integration is current.
func OutdatedUpdateNotice() (notice string, ok bool) {
	outdated := outdatedInstalledIntegrations()
	if len(outdated) == 0 {
		return "", false
	}
	targets := make([]Target, 0, len(outdated))
	for _, status := range outdated {
		targets = append(targets, status.Target)
	}
	return fmt.Sprintf("installed cats integrations need updating; %s.",
		strings.ReplaceAll(UpdateInstructions(targets), "`", "")), true
}

func integrationStatusAt(target Target, path string, expectedVersion int) Status {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return Status{
			Target: target, Path: path, State: StatusNotInstalled,
			InstalledVersion: -1, ExpectedVersion: expectedVersion,
		}
	}

	installedVersion := -1
	if content, err := os.ReadFile(path); err == nil {
		if version, ok := parseIntegrationVersion(string(content)); ok {
			installedVersion = version
		}
	}
	state := StatusOutdated
	if installedVersion >= 0 && installedVersion >= expectedVersion {
		state = StatusCurrent
	}
	return Status{
		Target: target, Path: path, State: state,
		InstalledVersion: installedVersion, ExpectedVersion: expectedVersion,
	}
}

// parseIntegrationVersion scans for the CATS_INTEGRATION_VERSION marker,
// tolerating `//` and `#` comment prefixes; a file without a parseable marker
// is a legacy install.
func parseIntegrationVersion(content string) (int, bool) {
	for _, line := range strings.Split(content, "\n") {
		markerLine := strings.TrimSpace(line)
		markerLine = strings.TrimLeft(markerLine, "/")
		markerLine = strings.TrimLeft(markerLine, "#")
		markerLine = strings.TrimSpace(markerLine)
		rest, found := strings.CutPrefix(markerLine, integrationVersionMarker)
		if !found {
			continue
		}
		version, err := strconv.ParseUint(strings.TrimSpace(rest), 10, 32)
		if err != nil {
			continue
		}
		return int(version), true
	}
	return 0, false
}

// Directory resolution: a per-target env override (tilde-expanded) wins;
// otherwise the path is rooted at $HOME.

func piExtensionDir() (string, error) {
	dir, err := configDirFromEnvOrHome(piCodingAgentDirEnvVar, ".pi", "agent")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "extensions"), nil
}

func ompExtensionDir() (string, error) {
	dir, err := configDirFromEnvOrHome(piCodingAgentDirEnvVar, ".omp", "agent")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "extensions"), nil
}

func claudeDir() (string, error) {
	return configDirFromEnvOrHome(claudeConfigDirEnvVar, ".claude")
}

func codexDir() (string, error) {
	return configDirFromEnvOrHome(codexHomeEnvVar, ".codex")
}

func kimiDir() (string, error) {
	return configDirFromEnvOrHome(kimiCodeHomeEnvVar, ".kimi-code")
}

func copilotDir() (string, error) {
	return configDirFromEnvOrHome(copilotHomeEnvVar, ".copilot")
}

func droidDir() (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".factory"), nil
}

func opencodeDir() (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "opencode"), nil
}

func kiloDir() (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "kilo"), nil
}

func hermesDir() (string, error) {
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".hermes"), nil
}

func hermesPluginDir() (string, error) {
	dir, err := hermesDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "plugins", hermesPluginInstallName), nil
}

func qodercliDir() (string, error) {
	return configDirFromEnvOrHome(qodercliConfigDirEnvVar, ".qoder")
}

func cursorDir() (string, error) {
	return configDirFromEnvOrHome(cursorConfigDirEnvVar, ".cursor")
}

func configDirFromEnvOrHome(envVar string, homeRelativeSegments ...string) (string, error) {
	if value := os.Getenv(envVar); value != "" {
		return expandTildePath(value)
	}
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{home}, homeRelativeSegments...)...), nil
}

func expandTildePath(path string) (string, error) {
	if path == "~" {
		return homeDir()
	}
	for _, prefix := range []string{"~/", "~\\", "~"} {
		if rest, found := strings.CutPrefix(path, prefix); found {
			home, err := homeDir()
			if err != nil {
				return "", err
			}
			return filepath.Join(home, rest), nil
		}
	}
	return path, nil
}

func homeDir() (string, error) {
	if home := os.Getenv("HOME"); home != "" {
		return home, nil
	}
	return "", fmt.Errorf("home directory is not set; cannot locate home directory")
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
