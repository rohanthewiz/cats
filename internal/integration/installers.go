package integration

// Per-target install/uninstall. Shared contract: hook scripts are fully
// overwritten (no backups) and chmod 0755; JSON/TOML/YAML configs are parsed,
// minimally mutated, and rewritten only through the format-preserving editors;
// config writes are skipped when the content is unchanged where the Rust
// source does so.

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// InstallPaths / UninstallResult structs mirror the Rust ones so callers can
// report exactly what was touched.

type ClaudeInstallPaths struct {
	HookPath     string
	SettingsPath string
}

type CodexInstallPaths struct {
	HookPath   string
	HooksPath  string
	ConfigPath string
}

type KimiInstallPaths struct {
	HookPath   string
	ConfigPath string
}

type CopilotInstallPaths struct {
	HookPath     string
	SettingsPath string
}

type DroidInstallPaths struct {
	HookPath           string
	HooksPath          string
	SettingsPath       string
	UpdatedLegacyHooks bool
}

type OpenCodeInstallPaths struct {
	PluginPath string
}

type KiloInstallPaths struct {
	PluginPath string
}

type OmpInstallPaths struct {
	ExtensionPath            string
	RemovedLegacyPiExtension bool
}

type HermesInstallPaths struct {
	PluginDir  string
	ConfigPath string
}

type QodercliInstallPaths struct {
	HookPath     string
	SettingsPath string
}

type CursorInstallPaths struct {
	HookPath  string
	HooksPath string
}

type PiUninstallResult struct {
	ExtensionPath    string
	RemovedExtension bool
}

type OmpUninstallResult struct {
	ExtensionPath    string
	RemovedExtension bool
}

type ClaudeUninstallResult struct {
	HookPath        string
	SettingsPath    string
	RemovedHookFile bool
	UpdatedSettings bool
}

type CodexUninstallResult struct {
	HookPath        string
	HooksPath       string
	ConfigPath      string
	RemovedHookFile bool
	UpdatedHooks    bool
}

type KimiUninstallResult struct {
	HookPath        string
	ConfigPath      string
	RemovedHookFile bool
	UpdatedConfig   bool
}

type CopilotUninstallResult struct {
	HookPath        string
	SettingsPath    string
	RemovedHookFile bool
	UpdatedSettings bool
}

type DroidUninstallResult struct {
	HookPath        string
	HooksPath       string
	SettingsPath    string
	RemovedHookFile bool
	UpdatedHooks    bool
	UpdatedSettings bool
}

type OpenCodeUninstallResult struct {
	PluginPath    string
	RemovedPlugin bool
}

type KiloUninstallResult struct {
	PluginPath    string
	RemovedPlugin bool
}

type HermesUninstallResult struct {
	PluginDir        string
	ConfigPath       string
	RemovedPluginDir bool
	UpdatedConfig    bool
}

type QodercliUninstallResult struct {
	HookPath        string
	SettingsPath    string
	RemovedHookFile bool
	UpdatedSettings bool
}

type CursorUninstallResult struct {
	HookPath        string
	HooksPath       string
	RemovedHookFile bool
	UpdatedHooks    bool
}

// readSettingsFile parses an existing JSON settings file, or yields a fresh
// empty object when the file does not exist.
func readSettingsFile(path string) (any, error) {
	if !isFile(path) {
		return newJSONObject(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	parsed, err := parseJSONDocument(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse %s: %v", path, err)
	}
	return parsed, nil
}

// readExistingSettingsFile is readSettingsFile for uninstall paths: ok=false
// when the file is absent.
func readExistingSettingsFile(path string) (any, bool, error) {
	if !isFile(path) {
		return nil, false, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	parsed, err := parseJSONDocument(data)
	if err != nil {
		return nil, false, fmt.Errorf("failed to parse %s: %v", path, err)
	}
	return parsed, true, nil
}

func writeSettingsFile(path string, settings any) error {
	return os.WriteFile(path, marshalJSONPretty(settings), 0o644)
}

func makeExecutable(path string) error {
	return os.Chmod(path, 0o755)
}

func removeFileIfExists(path string) (bool, error) {
	err := os.Remove(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func removeDirAllIfExists(path string) (bool, error) {
	// os.RemoveAll succeeds on a missing path, so probe first to report
	// whether anything was actually removed.
	if _, err := os.Lstat(path); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err := os.RemoveAll(path); err != nil {
		return false, err
	}
	return true, nil
}

// InstallPi drops the pi extension into <pi>/extensions; the directory must
// already exist (proof pi itself is installed).
func InstallPi() (string, error) {
	dir, err := piExtensionDir()
	if err != nil {
		return "", err
	}
	if !isDir(dir) {
		return "", fmt.Errorf(
			"pi extension directory not found at %s. install pi and create the extensions directory first", dir)
	}

	path := filepath.Join(dir, piExtensionInstallName)
	if err := os.WriteFile(path, []byte(piExtensionAsset), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// InstallOmp installs the omp extension; the extensions dir is created when
// the agent dir already exists, and a legacy pi extension left behind by the
// pre-fork installer is removed.
func InstallOmp() (OmpInstallPaths, error) {
	dir, err := ompExtensionDir()
	if err != nil {
		return OmpInstallPaths{}, err
	}
	notFound := fmt.Errorf(
		"omp extension directory not found at %s. install omp and create the extensions directory first", dir)
	if !isDir(dir) {
		if isDir(filepath.Dir(dir)) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return OmpInstallPaths{}, err
			}
		} else {
			return OmpInstallPaths{}, notFound
		}
	}
	if !isDir(dir) {
		return OmpInstallPaths{}, notFound
	}

	removedLegacyPi, err := removeLegacyPiExtensionFromOmpDir(dir)
	if err != nil {
		return OmpInstallPaths{}, err
	}
	extensionPath := filepath.Join(dir, ompExtensionInstallName)
	if err := os.WriteFile(extensionPath, []byte(ompExtensionAsset), 0o644); err != nil {
		return OmpInstallPaths{}, err
	}
	return OmpInstallPaths{
		ExtensionPath:            extensionPath,
		RemovedLegacyPiExtension: removedLegacyPi,
	}, nil
}

// removeLegacyPiExtensionFromOmpDir removes an old pi extension only when its
// HERDR_INTEGRATION_ID marker proves it is ours — a user file that happens to
// share the name survives.
func removeLegacyPiExtensionFromOmpDir(dir string) (bool, error) {
	legacyPath := legacyPiExtensionPath(dir)
	if !isFile(legacyPath) {
		return false, nil
	}
	content, err := os.ReadFile(legacyPath)
	if err != nil {
		return false, err
	}
	if !bytes.Contains(content, []byte("HERDR_INTEGRATION_ID=pi")) {
		return false, nil
	}
	if err := os.Remove(legacyPath); err != nil {
		return false, err
	}
	return true, nil
}

// InstallClaude writes the hook script under <claude>/hooks and registers a
// single SessionStart→session entry (matcher "*") in settings.json, removing
// every deprecated per-lifecycle entry earlier versions installed.
func InstallClaude() (ClaudeInstallPaths, error) {
	dir, err := claudeDir()
	if err != nil {
		return ClaudeInstallPaths{}, err
	}
	if !isDir(dir) {
		return ClaudeInstallPaths{}, fmt.Errorf(
			"claude directory not found at %s. install claude code first", dir)
	}

	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return ClaudeInstallPaths{}, err
	}
	hookPath := filepath.Join(hooksDir, claudeHookInstallName)
	if err := os.WriteFile(hookPath, []byte(claudeHookAsset), 0o644); err != nil {
		return ClaudeInstallPaths{}, err
	}
	if err := makeExecutable(hookPath); err != nil {
		return ClaudeInstallPaths{}, err
	}

	settingsPath := filepath.Join(dir, "settings.json")
	settings, err := readSettingsFile(settingsPath)
	if err != nil {
		return ClaudeInstallPaths{}, err
	}

	hooks, err := ensureHooksObject(settings, settingsPath, "claude settings", "claude settings hooks")
	if err != nil {
		return ClaudeInstallPaths{}, err
	}
	removals := []hookEvent{
		{"PostToolUse", "working"},
		{"PostToolUseFailure", "working"},
		{"SubagentStop", "working"},
		{"PermissionRequest", "blocked"},
		{"SessionStart", "idle"},
		{"UserPromptSubmit", "working"},
		{"PreToolUse", "working"},
		{"Stop", "idle"},
		{"SessionEnd", "release"},
		{"SessionStart", "session"},
	}
	for _, he := range removals {
		if _, err := removeHookCommands(hooks, he.event, hookPath, he.action, true); err != nil {
			return ClaudeInstallPaths{}, err
		}
	}
	if err := ensureCommandHook(hooks, "SessionStart",
		hookCommand(hookPath, "session", true), 10, "*", true); err != nil {
		return ClaudeInstallPaths{}, err
	}

	if err := writeSettingsFile(settingsPath, settings); err != nil {
		return ClaudeInstallPaths{}, err
	}
	return ClaudeInstallPaths{HookPath: hookPath, SettingsPath: settingsPath}, nil
}

// InstallCodex writes the hook at the codex home root, registers it in
// hooks.json, and forces `[features] hooks = true` in config.toml.
func InstallCodex() (CodexInstallPaths, error) {
	dir, err := codexDir()
	if err != nil {
		return CodexInstallPaths{}, err
	}
	if !isDir(dir) {
		return CodexInstallPaths{}, fmt.Errorf(
			"codex config directory not found at %s. install codex first", dir)
	}

	hookPath := filepath.Join(dir, codexHookInstallName)
	if err := os.WriteFile(hookPath, []byte(codexHookAsset), 0o644); err != nil {
		return CodexInstallPaths{}, err
	}
	if err := makeExecutable(hookPath); err != nil {
		return CodexInstallPaths{}, err
	}

	hooksPath := filepath.Join(dir, "hooks.json")
	hooksFile, err := readSettingsFile(hooksPath)
	if err != nil {
		return CodexInstallPaths{}, err
	}

	hooks, err := ensureHooksObject(hooksFile, hooksPath, "codex hooks file", "codex hooks file hooks")
	if err != nil {
		return CodexInstallPaths{}, err
	}
	removals := []hookEvent{
		{"PermissionRequest", "blocked"},
		{"SessionStart", "idle"},
		{"UserPromptSubmit", "working"},
		{"PreToolUse", "working"},
		{"Stop", "idle"},
		{"SessionStart", "session"},
	}
	for _, he := range removals {
		if _, err := removeHookCommands(hooks, he.event, hookPath, he.action, true); err != nil {
			return CodexInstallPaths{}, err
		}
	}
	if err := ensureCommandHook(hooks, "SessionStart",
		hookCommand(hookPath, "session", true), 10, "", false); err != nil {
		return CodexInstallPaths{}, err
	}

	if err := writeSettingsFile(hooksPath, hooksFile); err != nil {
		return CodexInstallPaths{}, err
	}

	configPath := filepath.Join(dir, "config.toml")
	existingConfig := ""
	if isFile(configPath) {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return CodexInstallPaths{}, err
		}
		existingConfig = string(data)
	}
	newConfig := buildCodexConfigWithHooks(existingConfig)
	if newConfig != existingConfig {
		if err := os.WriteFile(configPath, []byte(newConfig), 0o644); err != nil {
			return CodexInstallPaths{}, err
		}
	}

	return CodexInstallPaths{HookPath: hookPath, HooksPath: hooksPath, ConfigPath: configPath}, nil
}

// InstallKimi writes the hook under <kimi>/hooks and rewrites the sentinel
// block of [[hooks]] tables in config.toml. The minimum-version gate lives in
// InstallTarget, not here.
func InstallKimi() (KimiInstallPaths, error) {
	dir, err := kimiDir()
	if err != nil {
		return KimiInstallPaths{}, err
	}
	if !isDir(dir) {
		return KimiInstallPaths{}, fmt.Errorf(
			"kimi code config directory not found at %s. install kimi code first", dir)
	}

	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return KimiInstallPaths{}, err
	}
	hookPath := filepath.Join(hooksDir, kimiHookInstallName)
	if err := os.WriteFile(hookPath, []byte(kimiHookAsset), 0o644); err != nil {
		return KimiInstallPaths{}, err
	}
	if err := makeExecutable(hookPath); err != nil {
		return KimiInstallPaths{}, err
	}

	configPath := filepath.Join(dir, "config.toml")
	existingConfig := ""
	if isFile(configPath) {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return KimiInstallPaths{}, err
		}
		existingConfig = string(data)
	}
	newConfig := buildKimiConfigWithHooks(existingConfig, hookPath)
	if newConfig != existingConfig {
		if err := os.WriteFile(configPath, []byte(newConfig), 0o644); err != nil {
			return KimiInstallPaths{}, err
		}
	}

	return KimiInstallPaths{HookPath: hookPath, ConfigPath: configPath}, nil
}

// InstallCopilot writes the hook under <copilot>/hooks and registers the flat
// SessionStart entry in settings.json, clearing the legacy lifecycle set.
func InstallCopilot() (CopilotInstallPaths, error) {
	dir, err := copilotDir()
	if err != nil {
		return CopilotInstallPaths{}, err
	}
	if !isDir(dir) {
		return CopilotInstallPaths{}, fmt.Errorf(
			"copilot config directory not found at %s. install github copilot cli first", dir)
	}

	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return CopilotInstallPaths{}, err
	}
	hookPath := filepath.Join(hooksDir, copilotHookInstallName)
	if err := os.WriteFile(hookPath, []byte(copilotHookAsset), 0o644); err != nil {
		return CopilotInstallPaths{}, err
	}
	if err := makeExecutable(hookPath); err != nil {
		return CopilotInstallPaths{}, err
	}

	settingsPath := filepath.Join(dir, "settings.json")
	settings, err := readSettingsFile(settingsPath)
	if err != nil {
		return CopilotInstallPaths{}, err
	}

	hooks, err := ensureHooksObject(settings, settingsPath, "copilot settings", "copilot settings hooks")
	if err != nil {
		return CopilotInstallPaths{}, err
	}
	command := hookCommand(hookPath, "", false)
	for _, event := range copilotRemovedLifecycleHookEvents {
		if _, err := removeDirectHookCommands(hooks, event, hookPath, "", false); err != nil {
			return CopilotInstallPaths{}, err
		}
	}
	for _, event := range copilotHookEvents {
		if _, err := removeDirectHookCommands(hooks, event, hookPath, "", false); err != nil {
			return CopilotInstallPaths{}, err
		}
	}
	for _, event := range copilotHookEvents {
		if err := ensureDirectCommandHook(hooks, event, command, 10, "", false); err != nil {
			return CopilotInstallPaths{}, err
		}
	}

	if err := writeSettingsFile(settingsPath, settings); err != nil {
		return CopilotInstallPaths{}, err
	}
	return CopilotInstallPaths{HookPath: hookPath, SettingsPath: settingsPath}, nil
}

// InstallDroid registers the hook in settings.json and cleans herdr entries
// out of the legacy hooks.json (which is never written to otherwise).
func InstallDroid() (DroidInstallPaths, error) {
	dir, err := droidDir()
	if err != nil {
		return DroidInstallPaths{}, err
	}
	if !isDir(dir) {
		return DroidInstallPaths{}, fmt.Errorf(
			"droid config directory not found at %s. install droid first", dir)
	}

	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return DroidInstallPaths{}, err
	}
	hookPath := filepath.Join(hooksDir, droidHookInstallName)
	if err := os.WriteFile(hookPath, []byte(droidHookAsset), 0o644); err != nil {
		return DroidInstallPaths{}, err
	}
	if err := makeExecutable(hookPath); err != nil {
		return DroidInstallPaths{}, err
	}

	settingsPath := filepath.Join(dir, "settings.json")
	settings, err := readSettingsFile(settingsPath)
	if err != nil {
		return DroidInstallPaths{}, err
	}

	hooks, err := ensureHooksObject(settings, settingsPath, "droid settings", "droid settings hooks")
	if err != nil {
		return DroidInstallPaths{}, err
	}
	if _, err := removeHookCommands(hooks, "SessionStart", hookPath, "", false); err != nil {
		return DroidInstallPaths{}, err
	}
	for _, he := range droidRemovedLifecycleHookEvents {
		if _, err := removeHookCommands(hooks, he.event, hookPath, he.action, true); err != nil {
			return DroidInstallPaths{}, err
		}
	}
	for _, he := range droidHookEvents {
		if _, err := removeHookCommands(hooks, he.event, hookPath, he.action, true); err != nil {
			return DroidInstallPaths{}, err
		}
	}
	for _, he := range droidHookEvents {
		if err := ensureCommandHook(hooks, he.event,
			hookCommand(hookPath, he.action, true), 10, "", false); err != nil {
			return DroidInstallPaths{}, err
		}
	}

	if err := writeSettingsFile(settingsPath, settings); err != nil {
		return DroidInstallPaths{}, err
	}

	hooksPath := filepath.Join(dir, "hooks.json")
	updatedLegacyHooks := false
	if isFile(hooksPath) {
		hooksFile, _, err := readExistingSettingsFile(hooksPath)
		if err != nil {
			return DroidInstallPaths{}, err
		}
		legacyHooks, err := hooksObjectIfPresent(hooksFile, hooksPath, "droid hooks file", "droid hooks file hooks")
		if err != nil {
			return DroidInstallPaths{}, err
		}
		if legacyHooks != nil {
			removed, err := removeHookCommands(legacyHooks, "SessionStart", hookPath, "", false)
			if err != nil {
				return DroidInstallPaths{}, err
			}
			updatedLegacyHooks = removed
			for _, he := range droidRemovedLifecycleHookEvents {
				removed, err := removeHookCommands(legacyHooks, he.event, hookPath, he.action, true)
				if err != nil {
					return DroidInstallPaths{}, err
				}
				updatedLegacyHooks = updatedLegacyHooks || removed
			}
			for _, he := range droidHookEvents {
				removed, err := removeHookCommands(legacyHooks, he.event, hookPath, he.action, true)
				if err != nil {
					return DroidInstallPaths{}, err
				}
				updatedLegacyHooks = updatedLegacyHooks || removed
			}
		}
		if updatedLegacyHooks {
			if err := writeSettingsFile(hooksPath, hooksFile); err != nil {
				return DroidInstallPaths{}, err
			}
		}
	}

	return DroidInstallPaths{
		HookPath: hookPath, HooksPath: hooksPath, SettingsPath: settingsPath,
		UpdatedLegacyHooks: updatedLegacyHooks,
	}, nil
}

// InstallOpencode drops the Node plugin into <opencode>/plugins.
func InstallOpencode() (OpenCodeInstallPaths, error) {
	dir, err := opencodeDir()
	if err != nil {
		return OpenCodeInstallPaths{}, err
	}
	if !isDir(dir) {
		return OpenCodeInstallPaths{}, fmt.Errorf(
			"opencode config directory not found at %s. install opencode first", dir)
	}

	pluginsDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return OpenCodeInstallPaths{}, err
	}
	pluginPath := filepath.Join(pluginsDir, opencodePluginInstallName)
	if err := os.WriteFile(pluginPath, []byte(opencodePluginAsset), 0o644); err != nil {
		return OpenCodeInstallPaths{}, err
	}
	return OpenCodeInstallPaths{PluginPath: pluginPath}, nil
}

// InstallKilo drops the Node plugin into <kilo>/plugin (singular).
func InstallKilo() (KiloInstallPaths, error) {
	dir, err := kiloDir()
	if err != nil {
		return KiloInstallPaths{}, err
	}
	if !isDir(dir) {
		return KiloInstallPaths{}, fmt.Errorf(
			"kilo config directory not found at %s. install kilo first", dir)
	}

	pluginsDir := filepath.Join(dir, "plugin")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		return KiloInstallPaths{}, err
	}
	pluginPath := filepath.Join(pluginsDir, kiloPluginInstallName)
	if err := os.WriteFile(pluginPath, []byte(kiloPluginAsset), 0o644); err != nil {
		return KiloInstallPaths{}, err
	}
	return KiloInstallPaths{PluginPath: pluginPath}, nil
}

// InstallHermes writes the Python plugin package and enables it in the
// `plugins.enabled` list of config.yaml.
func InstallHermes() (HermesInstallPaths, error) {
	dir, err := hermesDir()
	if err != nil {
		return HermesInstallPaths{}, err
	}
	if !isDir(dir) {
		return HermesInstallPaths{}, fmt.Errorf(
			"hermes config directory not found at %s. install hermes agent first", dir)
	}

	pluginDir, err := hermesPluginDir()
	if err != nil {
		return HermesInstallPaths{}, err
	}
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		return HermesInstallPaths{}, err
	}
	if err := os.WriteFile(filepath.Join(pluginDir, hermesPluginManifestInstallName),
		[]byte(hermesPluginManifestAsset), 0o644); err != nil {
		return HermesInstallPaths{}, err
	}
	if err := os.WriteFile(filepath.Join(pluginDir, hermesPluginInitInstallName),
		[]byte(hermesPluginInitAsset), 0o644); err != nil {
		return HermesInstallPaths{}, err
	}

	configPath := filepath.Join(dir, "config.yaml")
	existingConfig := ""
	if isFile(configPath) {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return HermesInstallPaths{}, err
		}
		existingConfig = string(data)
	}
	newConfig := ensureHermesPluginEnabled(existingConfig)
	if newConfig != existingConfig {
		if err := os.WriteFile(configPath, []byte(newConfig), 0o644); err != nil {
			return HermesInstallPaths{}, err
		}
	}

	return HermesInstallPaths{PluginDir: pluginDir, ConfigPath: configPath}, nil
}

// InstallQodercli registers the hook in ~/.qoder/settings.json. The schema
// mirrors claude settings.json (per https://docs.qoder.com/zh/cli/hooks): a
// top-level `hooks` object keyed by event name, each entry holding a matcher
// plus a list of {type: "command", command, timeout?} invocations.
func InstallQodercli() (QodercliInstallPaths, error) {
	dir, err := qodercliDir()
	if err != nil {
		return QodercliInstallPaths{}, err
	}
	if !isDir(dir) {
		return QodercliInstallPaths{}, fmt.Errorf(
			"qodercli config directory not found at %s. install qodercli first", dir)
	}

	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return QodercliInstallPaths{}, err
	}
	hookPath := filepath.Join(hooksDir, qodercliHookInstallName)
	if err := os.WriteFile(hookPath, []byte(qodercliHookAsset), 0o644); err != nil {
		return QodercliInstallPaths{}, err
	}
	if err := makeExecutable(hookPath); err != nil {
		return QodercliInstallPaths{}, err
	}

	settingsPath := filepath.Join(dir, "settings.json")
	settings, err := readSettingsFile(settingsPath)
	if err != nil {
		return QodercliInstallPaths{}, err
	}

	hooks, err := ensureHooksObject(settings, settingsPath, "qodercli settings", "qodercli settings hooks")
	if err != nil {
		return QodercliInstallPaths{}, err
	}
	for _, he := range qodercliRemovedLifecycleHookEvents {
		if _, err := removeHookCommands(hooks, he.event, hookPath, he.action, true); err != nil {
			return QodercliInstallPaths{}, err
		}
	}
	for _, he := range qodercliHookEvents {
		if _, err := removeHookCommands(hooks, he.event, hookPath, he.action, true); err != nil {
			return QodercliInstallPaths{}, err
		}
	}
	for _, he := range qodercliHookEvents {
		if err := ensureCommandHook(hooks, he.event,
			hookCommand(hookPath, he.action, true), 10, "*", true); err != nil {
			return QodercliInstallPaths{}, err
		}
	}

	if err := writeSettingsFile(settingsPath, settings); err != nil {
		return QodercliInstallPaths{}, err
	}
	return QodercliInstallPaths{HookPath: hookPath, SettingsPath: settingsPath}, nil
}

// InstallCursor writes the hook at the cursor config root and registers the
// simple-shape sessionStart command in hooks.json (ensuring "version": 1).
func InstallCursor() (CursorInstallPaths, error) {
	dir, err := cursorDir()
	if err != nil {
		return CursorInstallPaths{}, err
	}
	if !isDir(dir) {
		return CursorInstallPaths{}, fmt.Errorf(
			"cursor config directory not found at %s. install cursor agent cli first", dir)
	}

	hookPath := filepath.Join(dir, cursorHookInstallName)
	if err := os.WriteFile(hookPath, []byte(cursorHookAsset), 0o644); err != nil {
		return CursorInstallPaths{}, err
	}
	if err := makeExecutable(hookPath); err != nil {
		return CursorInstallPaths{}, err
	}

	hooksPath := filepath.Join(dir, "hooks.json")
	var hooksFile any
	if isFile(hooksPath) {
		parsed, _, err := readExistingSettingsFile(hooksPath)
		if err != nil {
			return CursorInstallPaths{}, err
		}
		hooksFile = parsed
	} else {
		fresh := newJSONObject()
		fresh.Set("version", int64(1))
		hooksFile = fresh
	}

	if root, ok := hooksFile.(*jsonObject); !ok {
		return CursorInstallPaths{}, fmt.Errorf(
			"cursor hooks file at %s must be a JSON object", hooksPath)
	} else if _, present := root.Get("version"); !present {
		root.Set("version", int64(1))
	}

	hooks, err := ensureHooksObject(hooksFile, hooksPath, "cursor hooks file", "cursor hooks file hooks")
	if err != nil {
		return CursorInstallPaths{}, err
	}
	sessionCommand := "bash " + shellSingleQuote(hookPath) + " session"
	for _, event := range []string{"beforeSubmitPrompt", "beforeShellExecution", "beforeMCPExecution", "stop", "sessionEnd"} {
		if _, err := removeSimpleCommandHook(hooks, event, sessionCommand); err != nil {
			return CursorInstallPaths{}, err
		}
	}
	if err := ensureSimpleCommandHook(hooks, "sessionStart", sessionCommand); err != nil {
		return CursorInstallPaths{}, err
	}

	if err := writeSettingsFile(hooksPath, hooksFile); err != nil {
		return CursorInstallPaths{}, err
	}
	return CursorInstallPaths{HookPath: hookPath, HooksPath: hooksPath}, nil
}

func UninstallPi() (PiUninstallResult, error) {
	dir, err := piExtensionDir()
	if err != nil {
		return PiUninstallResult{}, err
	}
	extensionPath := filepath.Join(dir, piExtensionInstallName)
	removed, err := removeFileIfExists(extensionPath)
	if err != nil {
		return PiUninstallResult{}, err
	}
	return PiUninstallResult{ExtensionPath: extensionPath, RemovedExtension: removed}, nil
}

func UninstallOmp() (OmpUninstallResult, error) {
	dir, err := ompExtensionDir()
	if err != nil {
		return OmpUninstallResult{}, err
	}
	extensionPath := filepath.Join(dir, ompExtensionInstallName)
	removed, err := removeFileIfExists(extensionPath)
	if err != nil {
		return OmpUninstallResult{}, err
	}
	return OmpUninstallResult{ExtensionPath: extensionPath, RemovedExtension: removed}, nil
}

func UninstallClaude() (ClaudeUninstallResult, error) {
	dir, err := claudeDir()
	if err != nil {
		return ClaudeUninstallResult{}, err
	}
	hookPath := filepath.Join(dir, "hooks", claudeHookInstallName)
	settingsPath := filepath.Join(dir, "settings.json")
	updatedSettings := false

	settings, present, err := readExistingSettingsFile(settingsPath)
	if err != nil {
		return ClaudeUninstallResult{}, err
	}
	if present {
		hooks, err := hooksObjectIfPresent(settings, settingsPath, "claude settings", "claude settings hooks")
		if err != nil {
			return ClaudeUninstallResult{}, err
		}
		if hooks != nil {
			removals := []hookEvent{
				{"SessionStart", "idle"},
				{"SessionStart", "session"},
				{"UserPromptSubmit", "working"},
				{"PreToolUse", "working"},
				{"PermissionRequest", "blocked"},
				{"PostToolUse", "working"},
				{"PostToolUseFailure", "working"},
				{"SubagentStop", "working"},
				{"Stop", "idle"},
				{"SessionEnd", "release"},
			}
			for _, he := range removals {
				removed, err := removeHookCommands(hooks, he.event, hookPath, he.action, true)
				if err != nil {
					return ClaudeUninstallResult{}, err
				}
				updatedSettings = updatedSettings || removed
			}
		}
		if updatedSettings {
			if err := writeSettingsFile(settingsPath, settings); err != nil {
				return ClaudeUninstallResult{}, err
			}
		}
	}

	removedHookFile, err := removeFileIfExists(hookPath)
	if err != nil {
		return ClaudeUninstallResult{}, err
	}
	return ClaudeUninstallResult{
		HookPath: hookPath, SettingsPath: settingsPath,
		RemovedHookFile: removedHookFile, UpdatedSettings: updatedSettings,
	}, nil
}

func UninstallCodex() (CodexUninstallResult, error) {
	dir, err := codexDir()
	if err != nil {
		return CodexUninstallResult{}, err
	}
	hookPath := filepath.Join(dir, codexHookInstallName)
	hooksPath := filepath.Join(dir, "hooks.json")
	configPath := filepath.Join(dir, "config.toml")
	updatedHooks := false

	hooksFile, present, err := readExistingSettingsFile(hooksPath)
	if err != nil {
		return CodexUninstallResult{}, err
	}
	if present {
		hooks, err := hooksObjectIfPresent(hooksFile, hooksPath, "codex hooks file", "codex hooks file hooks")
		if err != nil {
			return CodexUninstallResult{}, err
		}
		if hooks != nil {
			removals := []hookEvent{
				{"SessionStart", "idle"},
				{"SessionStart", "session"},
				{"UserPromptSubmit", "working"},
				{"PreToolUse", "working"},
				{"PermissionRequest", "blocked"},
				{"Stop", "idle"},
			}
			for _, he := range removals {
				removed, err := removeHookCommands(hooks, he.event, hookPath, he.action, true)
				if err != nil {
					return CodexUninstallResult{}, err
				}
				updatedHooks = updatedHooks || removed
			}
		}
		if updatedHooks {
			if err := writeSettingsFile(hooksPath, hooksFile); err != nil {
				return CodexUninstallResult{}, err
			}
		}
	}

	removedHookFile, err := removeFileIfExists(hookPath)
	if err != nil {
		return CodexUninstallResult{}, err
	}
	return CodexUninstallResult{
		HookPath: hookPath, HooksPath: hooksPath, ConfigPath: configPath,
		RemovedHookFile: removedHookFile, UpdatedHooks: updatedHooks,
	}, nil
}

func UninstallKimi() (KimiUninstallResult, error) {
	dir, err := kimiDir()
	if err != nil {
		return KimiUninstallResult{}, err
	}
	hookPath := filepath.Join(dir, "hooks", kimiHookInstallName)
	configPath := filepath.Join(dir, "config.toml")
	updatedConfig := false

	if isFile(configPath) {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return KimiUninstallResult{}, err
		}
		existingConfig := string(data)
		newConfig := removeKimiConfigBlock(existingConfig)
		if newConfig != existingConfig {
			if err := os.WriteFile(configPath, []byte(newConfig), 0o644); err != nil {
				return KimiUninstallResult{}, err
			}
			updatedConfig = true
		}
	}

	removedHookFile, err := removeFileIfExists(hookPath)
	if err != nil {
		return KimiUninstallResult{}, err
	}
	return KimiUninstallResult{
		HookPath: hookPath, ConfigPath: configPath,
		RemovedHookFile: removedHookFile, UpdatedConfig: updatedConfig,
	}, nil
}

func UninstallCopilot() (CopilotUninstallResult, error) {
	dir, err := copilotDir()
	if err != nil {
		return CopilotUninstallResult{}, err
	}
	hookPath := filepath.Join(dir, "hooks", copilotHookInstallName)
	settingsPath := filepath.Join(dir, "settings.json")
	updatedSettings := false

	settings, present, err := readExistingSettingsFile(settingsPath)
	if err != nil {
		return CopilotUninstallResult{}, err
	}
	if present {
		hooks, err := hooksObjectIfPresent(settings, settingsPath, "copilot settings", "copilot settings hooks")
		if err != nil {
			return CopilotUninstallResult{}, err
		}
		if hooks != nil {
			for _, event := range copilotHookEvents {
				removed, err := removeDirectHookCommands(hooks, event, hookPath, "", false)
				if err != nil {
					return CopilotUninstallResult{}, err
				}
				updatedSettings = updatedSettings || removed
			}
			for _, event := range copilotRemovedLifecycleHookEvents {
				removed, err := removeDirectHookCommands(hooks, event, hookPath, "", false)
				if err != nil {
					return CopilotUninstallResult{}, err
				}
				updatedSettings = updatedSettings || removed
			}
		}
		if updatedSettings {
			if err := writeSettingsFile(settingsPath, settings); err != nil {
				return CopilotUninstallResult{}, err
			}
		}
	}

	removedHookFile, err := removeFileIfExists(hookPath)
	if err != nil {
		return CopilotUninstallResult{}, err
	}
	return CopilotUninstallResult{
		HookPath: hookPath, SettingsPath: settingsPath,
		RemovedHookFile: removedHookFile, UpdatedSettings: updatedSettings,
	}, nil
}

func UninstallDroid() (DroidUninstallResult, error) {
	dir, err := droidDir()
	if err != nil {
		return DroidUninstallResult{}, err
	}
	hookPath := filepath.Join(dir, "hooks", droidHookInstallName)
	hooksPath := filepath.Join(dir, "hooks.json")
	settingsPath := filepath.Join(dir, "settings.json")
	updatedHooks := false
	updatedSettings := false

	removeDroidEntries := func(hooks *jsonObject) (bool, error) {
		updated, err := removeHookCommands(hooks, "SessionStart", hookPath, "", false)
		if err != nil {
			return updated, err
		}
		for _, he := range droidRemovedLifecycleHookEvents {
			removed, err := removeHookCommands(hooks, he.event, hookPath, he.action, true)
			if err != nil {
				return updated, err
			}
			updated = updated || removed
		}
		for _, he := range droidHookEvents {
			removed, err := removeHookCommands(hooks, he.event, hookPath, he.action, true)
			if err != nil {
				return updated, err
			}
			updated = updated || removed
		}
		return updated, nil
	}

	hooksFile, present, err := readExistingSettingsFile(hooksPath)
	if err != nil {
		return DroidUninstallResult{}, err
	}
	if present {
		hooks, err := hooksObjectIfPresent(hooksFile, hooksPath, "droid hooks file", "droid hooks file hooks")
		if err != nil {
			return DroidUninstallResult{}, err
		}
		if hooks != nil {
			updatedHooks, err = removeDroidEntries(hooks)
			if err != nil {
				return DroidUninstallResult{}, err
			}
		}
		if updatedHooks {
			if err := writeSettingsFile(hooksPath, hooksFile); err != nil {
				return DroidUninstallResult{}, err
			}
		}
	}

	settings, present, err := readExistingSettingsFile(settingsPath)
	if err != nil {
		return DroidUninstallResult{}, err
	}
	if present {
		hooks, err := hooksObjectIfPresent(settings, settingsPath, "droid settings", "droid settings hooks")
		if err != nil {
			return DroidUninstallResult{}, err
		}
		if hooks != nil {
			updatedSettings, err = removeDroidEntries(hooks)
			if err != nil {
				return DroidUninstallResult{}, err
			}
		}
		if updatedSettings {
			if err := writeSettingsFile(settingsPath, settings); err != nil {
				return DroidUninstallResult{}, err
			}
		}
	}

	removedHookFile, err := removeFileIfExists(hookPath)
	if err != nil {
		return DroidUninstallResult{}, err
	}
	return DroidUninstallResult{
		HookPath: hookPath, HooksPath: hooksPath, SettingsPath: settingsPath,
		RemovedHookFile: removedHookFile, UpdatedHooks: updatedHooks, UpdatedSettings: updatedSettings,
	}, nil
}

func UninstallOpencode() (OpenCodeUninstallResult, error) {
	dir, err := opencodeDir()
	if err != nil {
		return OpenCodeUninstallResult{}, err
	}
	pluginPath := filepath.Join(dir, "plugins", opencodePluginInstallName)
	removed, err := removeFileIfExists(pluginPath)
	if err != nil {
		return OpenCodeUninstallResult{}, err
	}
	return OpenCodeUninstallResult{PluginPath: pluginPath, RemovedPlugin: removed}, nil
}

func UninstallKilo() (KiloUninstallResult, error) {
	dir, err := kiloDir()
	if err != nil {
		return KiloUninstallResult{}, err
	}
	pluginPath := filepath.Join(dir, "plugin", kiloPluginInstallName)
	removed, err := removeFileIfExists(pluginPath)
	if err != nil {
		return KiloUninstallResult{}, err
	}
	return KiloUninstallResult{PluginPath: pluginPath, RemovedPlugin: removed}, nil
}

func UninstallHermes() (HermesUninstallResult, error) {
	dir, err := hermesDir()
	if err != nil {
		return HermesUninstallResult{}, err
	}
	pluginDir, err := hermesPluginDir()
	if err != nil {
		return HermesUninstallResult{}, err
	}
	configPath := filepath.Join(dir, "config.yaml")

	removedPluginDir, err := removeDirAllIfExists(pluginDir)
	if err != nil {
		return HermesUninstallResult{}, err
	}
	updatedConfig := false
	if isFile(configPath) {
		data, err := os.ReadFile(configPath)
		if err != nil {
			return HermesUninstallResult{}, err
		}
		existingConfig := string(data)
		newConfig := removeHermesPluginEnabled(existingConfig)
		if newConfig != existingConfig {
			if err := os.WriteFile(configPath, []byte(newConfig), 0o644); err != nil {
				return HermesUninstallResult{}, err
			}
			updatedConfig = true
		}
	}

	return HermesUninstallResult{
		PluginDir: pluginDir, ConfigPath: configPath,
		RemovedPluginDir: removedPluginDir, UpdatedConfig: updatedConfig,
	}, nil
}

func UninstallQodercli() (QodercliUninstallResult, error) {
	dir, err := qodercliDir()
	if err != nil {
		return QodercliUninstallResult{}, err
	}
	hookPath := filepath.Join(dir, "hooks", qodercliHookInstallName)
	settingsPath := filepath.Join(dir, "settings.json")
	updatedSettings := false

	settings, present, err := readExistingSettingsFile(settingsPath)
	if err != nil {
		return QodercliUninstallResult{}, err
	}
	if present {
		hooks, err := hooksObjectIfPresent(settings, settingsPath, "qodercli settings", "qodercli settings hooks")
		if err != nil {
			return QodercliUninstallResult{}, err
		}
		if hooks != nil {
			for _, he := range qodercliRemovedLifecycleHookEvents {
				removed, err := removeHookCommands(hooks, he.event, hookPath, he.action, true)
				if err != nil {
					return QodercliUninstallResult{}, err
				}
				updatedSettings = updatedSettings || removed
			}
			for _, he := range qodercliHookEvents {
				removed, err := removeHookCommands(hooks, he.event, hookPath, he.action, true)
				if err != nil {
					return QodercliUninstallResult{}, err
				}
				updatedSettings = updatedSettings || removed
			}
		}
		if updatedSettings {
			if err := writeSettingsFile(settingsPath, settings); err != nil {
				return QodercliUninstallResult{}, err
			}
		}
	}

	removedHookFile, err := removeFileIfExists(hookPath)
	if err != nil {
		return QodercliUninstallResult{}, err
	}
	return QodercliUninstallResult{
		HookPath: hookPath, SettingsPath: settingsPath,
		RemovedHookFile: removedHookFile, UpdatedSettings: updatedSettings,
	}, nil
}

func UninstallCursor() (CursorUninstallResult, error) {
	dir, err := cursorDir()
	if err != nil {
		return CursorUninstallResult{}, err
	}
	hookPath := filepath.Join(dir, cursorHookInstallName)
	hooksPath := filepath.Join(dir, "hooks.json")
	updatedHooks := false

	hooksFile, present, err := readExistingSettingsFile(hooksPath)
	if err != nil {
		return CursorUninstallResult{}, err
	}
	if present {
		hooks, err := hooksObjectIfPresent(hooksFile, hooksPath, "cursor hooks file", "cursor hooks file hooks")
		if err != nil {
			return CursorUninstallResult{}, err
		}
		if hooks != nil {
			sessionCommand := "bash " + shellSingleQuote(hookPath) + " session"
			for _, event := range []string{"sessionStart", "beforeSubmitPrompt", "beforeShellExecution", "beforeMCPExecution", "stop", "sessionEnd"} {
				removed, err := removeSimpleCommandHook(hooks, event, sessionCommand)
				if err != nil {
					return CursorUninstallResult{}, err
				}
				updatedHooks = updatedHooks || removed
			}
		}
		if updatedHooks {
			if err := writeSettingsFile(hooksPath, hooksFile); err != nil {
				return CursorUninstallResult{}, err
			}
		}
	}

	removedHookFile, err := removeFileIfExists(hookPath)
	if err != nil {
		return CursorUninstallResult{}, err
	}
	return CursorUninstallResult{
		HookPath: hookPath, HooksPath: hooksPath,
		RemovedHookFile: removedHookFile, UpdatedHooks: updatedHooks,
	}, nil
}
