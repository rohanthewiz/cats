package integration

// JSON hook-entry surgery over the order-preserving tree. Three shapes exist:
//
//   - nested (claude, codex, droid, qodercli):
//     event → [ { "matcher"?, "hooks": [ {"type":"command","command",...} ] } ]
//   - flat/direct (copilot):
//     event → [ { "type":"command", "matcher"?, "bash", "timeoutSec" } ]
//   - simple (cursor): event → [ { "command" } ]
//
// The helpers are kept separate per shape so install/uninstall preserves
// unrelated hooks in each agent's native format instead of normalizing user
// configuration. All removals prune emptied groups and events.

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ensureHooksObject returns the top-level "hooks" object, inserting an empty
// one if absent. The root and the hooks value must both be JSON objects.
func ensureHooksObject(settings any, settingsPath, rootDescription, hooksDescription string) (*jsonObject, error) {
	root, ok := settings.(*jsonObject)
	if !ok {
		return nil, fmt.Errorf("%s at %s must be a JSON object", rootDescription, settingsPath)
	}
	hooksValue, ok := root.Get("hooks")
	if !ok {
		hooksValue = newJSONObject()
		root.Set("hooks", hooksValue)
	}
	hooks, ok := hooksValue.(*jsonObject)
	if !ok {
		return nil, fmt.Errorf("%s at %s must be a JSON object", hooksDescription, settingsPath)
	}
	return hooks, nil
}

// hooksObjectIfPresent is ensureHooksObject without the insert: nil when the
// key is absent (uninstall paths must not create it).
func hooksObjectIfPresent(settings any, settingsPath, rootDescription, hooksDescription string) (*jsonObject, error) {
	root, ok := settings.(*jsonObject)
	if !ok {
		return nil, fmt.Errorf("%s at %s must be a JSON object", rootDescription, settingsPath)
	}
	hooksValue, ok := root.Get("hooks")
	if !ok {
		return nil, nil
	}
	hooks, ok := hooksValue.(*jsonObject)
	if !ok {
		return nil, fmt.Errorf("%s at %s must be a JSON object", hooksDescription, settingsPath)
	}
	return hooks, nil
}

// eventEntries fetches the event's entry array, inserting an empty one when
// insert is true. ok=false means the event is absent (and insert was false).
func eventEntries(hooks *jsonObject, event string, insert bool) ([]any, bool, error) {
	value, present := hooks.Get(event)
	if !present {
		if !insert {
			return nil, false, nil
		}
		value = []any{}
		hooks.Set(event, value)
	}
	entries, ok := value.([]any)
	if !ok {
		return nil, false, fmt.Errorf("hook entries for %s must be an array", event)
	}
	return entries, true, nil
}

// ensureCommandHook installs the nested shape idempotently: if any group
// already carries the exact command, nothing changes.
func ensureCommandHook(hooks *jsonObject, event, command string, timeout int64, matcher string, hasMatcher bool) error {
	entries, _, err := eventEntries(hooks, event, true)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		hookEntriesValue, ok := objField(entry, "hooks")
		if !ok {
			continue
		}
		hookEntries, ok := hookEntriesValue.([]any)
		if !ok {
			continue
		}
		for _, hook := range hookEntries {
			if isMatchingCommandHook(hook, command) {
				return nil
			}
		}
	}

	entry := newJSONObject()
	if hasMatcher {
		entry.Set("matcher", matcher)
	}
	hook := newJSONObject()
	hook.Set("type", "command")
	hook.Set("command", command)
	hook.Set("timeout", timeout)
	entry.Set("hooks", []any{hook})

	hooks.Set(event, append(entries, entry))
	return nil
}

// ensureDirectCommandHook installs the copilot flat shape: an existing entry
// matching the command is normalized in place, otherwise a new one is added.
func ensureDirectCommandHook(hooks *jsonObject, event, command string, timeoutSec int64, matcher string, hasMatcher bool) error {
	entries, _, err := eventEntries(hooks, event, true)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		typ, _ := objFieldString(entry, "type")
		if typ != "command" || !isMatchingDirectCommandEntry(entry, command) {
			continue
		}
		entryObject, ok := entry.(*jsonObject)
		if !ok {
			return nil
		}
		entryObject.Delete("command")
		entryObject.Delete("bash")
		entryObject.Delete("powershell")
		entryObject.Set(directCommandField, command)
		entryObject.Set("timeoutSec", timeoutSec)
		if hasMatcher {
			entryObject.Set("matcher", matcher)
		} else {
			entryObject.Delete("matcher")
		}
		return nil
	}

	entry := newJSONObject()
	entry.Set("type", "command")
	if hasMatcher {
		entry.Set("matcher", matcher)
	}
	entry.Set(directCommandField, command)
	entry.Set("timeoutSec", timeoutSec)
	hooks.Set(event, append(entries, entry))
	return nil
}

// directCommandField is "powershell" on Windows in Rust; the Go port is
// unix-only.
const directCommandField = "bash"

func isMatchingDirectCommandEntry(entry any, command string) bool {
	for _, field := range []string{"command", "bash", "powershell"} {
		if value, ok := objFieldString(entry, field); ok && value == command {
			return true
		}
	}
	return false
}

// removeCommandHook removes the command from nested groups, dropping emptied
// groups and (when no groups remain) the event key itself.
func removeCommandHook(hooks *jsonObject, event, command string) (bool, error) {
	entries, present, err := eventEntries(hooks, event, false)
	if err != nil || !present {
		return false, err
	}

	removed := false
	kept := entries[:0]
	for _, entry := range entries {
		entryObject, ok := entry.(*jsonObject)
		if !ok {
			kept = append(kept, entry)
			continue
		}
		hookEntriesValue, ok := entryObject.Get("hooks")
		if !ok {
			kept = append(kept, entry)
			continue
		}
		hookEntries, ok := hookEntriesValue.([]any)
		if !ok {
			kept = append(kept, entry)
			continue
		}

		filtered := hookEntries[:0]
		for _, hook := range hookEntries {
			if !isMatchingCommandHook(hook, command) {
				filtered = append(filtered, hook)
			}
		}
		if len(filtered) != len(hookEntries) {
			removed = true
		}
		if len(filtered) == 0 {
			continue // drop the emptied group
		}
		entryObject.Set("hooks", filtered)
		kept = append(kept, entry)
	}

	if len(kept) == 0 {
		hooks.Delete(event)
	} else {
		hooks.Set(event, kept)
	}
	return removed, nil
}

func removeDirectCommandHook(hooks *jsonObject, event, command string) (bool, error) {
	entries, present, err := eventEntries(hooks, event, false)
	if err != nil || !present {
		return false, err
	}

	kept := entries[:0]
	for _, entry := range entries {
		typ, _ := objFieldString(entry, "type")
		if typ == "command" && isMatchingDirectCommandEntry(entry, command) {
			continue
		}
		kept = append(kept, entry)
	}
	removed := len(kept) != len(entries)
	if len(kept) == 0 {
		hooks.Delete(event)
	} else {
		hooks.Set(event, kept)
	}
	return removed, nil
}

// ensureSimpleCommandHook installs the cursor `{ "command": ... }` shape.
func ensureSimpleCommandHook(hooks *jsonObject, event, command string) error {
	entries, _, err := eventEntries(hooks, event, true)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if value, ok := objFieldString(entry, "command"); ok && value == command {
			return nil
		}
	}
	entry := newJSONObject()
	entry.Set("command", command)
	hooks.Set(event, append(entries, entry))
	return nil
}

func removeSimpleCommandHook(hooks *jsonObject, event, command string) (bool, error) {
	entries, present, err := eventEntries(hooks, event, false)
	if err != nil || !present {
		return false, err
	}
	kept := entries[:0]
	for _, entry := range entries {
		if value, ok := objFieldString(entry, "command"); ok && value == command {
			continue
		}
		kept = append(kept, entry)
	}
	removed := len(kept) != len(entries)
	if len(kept) == 0 {
		hooks.Delete(event)
	} else {
		hooks.Set(event, kept)
	}
	return removed, nil
}

// removeHookCommands removes every historical command variant for the hook
// path + action from the nested shape.
func removeHookCommands(hooks *jsonObject, event, hookPath, action string, hasAction bool) (bool, error) {
	removed := false
	for _, command := range hookCommandVariants(hookPath, action, hasAction) {
		r, err := removeCommandHook(hooks, event, command)
		if err != nil {
			return removed, err
		}
		removed = removed || r
	}
	return removed, nil
}

func removeDirectHookCommands(hooks *jsonObject, event, hookPath, action string, hasAction bool) (bool, error) {
	removed := false
	for _, command := range hookCommandVariants(hookPath, action, hasAction) {
		r, err := removeDirectCommandHook(hooks, event, command)
		if err != nil {
			return removed, err
		}
		removed = removed || r
	}
	return removed, nil
}

// hookCommandVariants lists current and historical command strings to purge.
// On unix the legacy bash form equals the current form, so this is a single
// entry (Windows adds powershell/bash variants in Rust).
func hookCommandVariants(hookPath, action string, hasAction bool) []string {
	return []string{hookCommand(hookPath, action, hasAction)}
}

func isMatchingCommandHook(hook any, command string) bool {
	typ, _ := objFieldString(hook, "type")
	cmd, _ := objFieldString(hook, "command")
	return typ == "command" && cmd == command
}

// shellSingleQuote wraps for sh: embedded single quotes become '"'"'.
func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

// hookCommand is the registered command string: `bash '<path>' [action]`.
func hookCommand(hookPath, action string, hasAction bool) string {
	command := "bash " + shellSingleQuote(hookPath)
	if hasAction {
		command += " " + action
	}
	return command
}

// legacyPiExtensionPath is where an old pi install would sit inside the omp
// extension dir.
func legacyPiExtensionPath(dir string) string {
	return filepath.Join(dir, piExtensionInstallName)
}
