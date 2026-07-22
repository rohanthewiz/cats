package integration

// Hand-written YAML editor for hermes' config.yaml, ported line-for-line from
// the Rust source (no YAML library — the user's file layout, comments and
// quoting must survive edits byte-for-byte where nothing changed).
//
// It manages the `cats-agent-state` entry across every plugins layout hermes
// accepts:
//
//	plugins:                       plugins:            plugins: [a, b]
//	  enabled:                       - a               plugins: []
//	    - a                          - b
//
// plus quoted scalars and inline comments on list items. A flow list gains an
// entry by being rewritten as a flat block list (matching Rust).

import "strings"

func ensureHermesPluginEnabled(content string) string {
	return updateHermesEnabledPlugin(content, true)
}

func removeHermesPluginEnabled(content string) string {
	return updateHermesEnabledPlugin(content, false)
}

func updateHermesEnabledPlugin(content string, enabled bool) string {
	trailingNewline := strings.HasSuffix(content, "\n")
	lines := splitLines(content)
	pluginsIndex, ok := topLevelYamlKeyIndex(lines, "plugins")
	if !ok {
		if !enabled {
			return content
		}
		result := strings.TrimRight(content, "\n")
		if result != "" {
			result += "\n"
		}
		result += "plugins:\n  enabled:\n    - cats-agent-state\n"
		return result
	}

	pluginsEnd, ok := nextTopLevelYamlKeyIndex(lines, pluginsIndex+1)
	if !ok {
		pluginsEnd = len(lines)
	}
	var pluginsInlineItems []string
	pluginsInlineOK := false
	if value, ok := yamlKeyValueAtIndent(lines[pluginsIndex], 0, "plugins"); ok {
		pluginsInlineItems, pluginsInlineOK = yamlFlowSequenceItems(value)
	}
	enabledIndex := -1
	for i := pluginsIndex + 1; i < pluginsEnd; i++ {
		if key, ok := yamlKeyAtIndent(lines[i], 2); ok && key == "enabled" {
			enabledIndex = i
			break
		}
	}
	flatListStart := -1
	for i := pluginsIndex + 1; i < pluginsEnd; i++ {
		if _, ok := yamlListItemValueAtIndent(lines[i], 2); ok {
			flatListStart = i
			break
		}
	}

	if enabledIndex >= 0 {
		line := strings.TrimSpace(lines[enabledIndex])
		if line == "enabled: []" || line == "enabled: [] # cats" {
			if enabled {
				lines[enabledIndex] = "  enabled:"
				lines = insertLine(lines, enabledIndex+1, "    - cats-agent-state")
			}
			return joinYamlLines(lines, trailingNewline)
		}

		listStart := enabledIndex + 1
		listEnd := pluginsEnd
		for i := listStart; i < pluginsEnd; i++ {
			indent, ok := yamlIndent(lines[i])
			if !ok || indent > 2 {
				continue
			}
			if _, ok := yamlKeyName(lines[i]); ok {
				listEnd = i
				break
			}
		}
		existingItemIndex := -1
		for i := listStart; i < listEnd; i++ {
			if yamlListItemMatches(lines[i], hermesPluginInstallName) {
				existingItemIndex = i
				break
			}
		}

		switch {
		case (enabled && existingItemIndex >= 0) || (!enabled && existingItemIndex < 0):
			return content
		case enabled:
			lines = insertLine(lines, listStart, "    - cats-agent-state")
		default:
			lines = append(lines[:existingItemIndex], lines[existingItemIndex+1:]...)
		}
		return joinYamlLines(lines, trailingNewline)
	}

	if pluginsInlineOK {
		items := pluginsInlineItems
		existingItemIndex := -1
		for i, item := range items {
			if item == hermesPluginInstallName {
				existingItemIndex = i
				break
			}
		}

		switch {
		case (enabled && existingItemIndex >= 0) || (!enabled && existingItemIndex < 0):
			return content
		case enabled:
			items = append([]string{hermesPluginInstallName}, items...)
		default:
			items = append(items[:existingItemIndex], items[existingItemIndex+1:]...)
		}

		replacement := hermesFlatPluginLines(items)
		lines = append(lines[:pluginsIndex], append(replacement, lines[pluginsEnd:]...)...)
		return joinYamlLines(lines, trailingNewline)
	}

	if flatListStart >= 0 {
		existingItemIndex := -1
		for i := pluginsIndex + 1; i < pluginsEnd; i++ {
			if yamlListItemMatchesAtIndent(lines[i], 2, hermesPluginInstallName) {
				existingItemIndex = i
				break
			}
		}

		switch {
		case (enabled && existingItemIndex >= 0) || (!enabled && existingItemIndex < 0):
			return content
		case enabled:
			lines = insertLine(lines, flatListStart, "  - cats-agent-state")
		default:
			lines = append(lines[:existingItemIndex], lines[existingItemIndex+1:]...)
		}
		return joinYamlLines(lines, trailingNewline)
	}

	if enabled {
		lines = insertLine(lines, pluginsIndex+1, "  enabled:")
		lines = insertLine(lines, pluginsIndex+2, "    - cats-agent-state")
		return joinYamlLines(lines, trailingNewline)
	}

	return content
}

func insertLine(lines []string, index int, line string) []string {
	lines = append(lines, "")
	copy(lines[index+1:], lines[index:])
	lines[index] = line
	return lines
}

func hermesFlatPluginLines(items []string) []string {
	if len(items) == 0 {
		return []string{"plugins: []"}
	}
	lines := []string{"plugins:"}
	for _, item := range items {
		lines = append(lines, "  - "+item)
	}
	return lines
}

func topLevelYamlKeyIndex(lines []string, key string) (int, bool) {
	for i, line := range lines {
		if k, ok := yamlKeyAtIndent(line, 0); ok && k == key {
			return i, true
		}
	}
	return 0, false
}

func nextTopLevelYamlKeyIndex(lines []string, start int) (int, bool) {
	for i := start; i < len(lines); i++ {
		indent, ok := yamlIndent(lines[i])
		if !ok || indent != 0 {
			continue
		}
		if _, ok := yamlKeyName(lines[i]); ok {
			return i, true
		}
	}
	return 0, false
}

func yamlKeyAtIndent(line string, indent int) (string, bool) {
	actual, ok := yamlIndent(line)
	if !ok || actual != indent {
		return "", false
	}
	return yamlKeyName(line)
}

func yamlKeyValueAtIndent(line string, indent int, key string) (string, bool) {
	actual, ok := yamlIndent(line)
	if !ok || actual != indent {
		return "", false
	}
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") {
		return "", false
	}
	lineKey, value, found := strings.Cut(trimmed, ":")
	if !found || strings.TrimSpace(lineKey) != key {
		return "", false
	}
	return strings.TrimSpace(value), true
}

func yamlKeyName(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "-") {
		return "", false
	}
	key, _, found := strings.Cut(trimmed, ":")
	if !found {
		return "", false
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false
	}
	return key, true
}

// yamlIndent returns the leading-whitespace width; blank and comment lines
// have none (they never delimit structure here).
func yamlIndent(line string) (int, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return 0, false
	}
	return len(line) - len(trimmed), true
}

func yamlListItemValue(line string) (string, bool) {
	rest, found := strings.CutPrefix(strings.TrimSpace(line), "- ")
	if !found {
		return "", false
	}
	return strings.TrimSpace(rest), true
}

func yamlListItemMatches(line, value string) bool {
	item, ok := yamlListItemValue(line)
	return ok && yamlScalarValue(item) == value
}

func yamlListItemValueAtIndent(line string, indent int) (string, bool) {
	actual, ok := yamlIndent(line)
	if !ok || actual != indent {
		return "", false
	}
	return yamlListItemValue(line)
}

func yamlListItemMatchesAtIndent(line string, indent int, value string) bool {
	item, ok := yamlListItemValueAtIndent(line, indent)
	return ok && yamlScalarValue(item) == value
}

// yamlFlowSequenceItems parses `[a, "b", 'c']`, honoring quotes (with escapes
// inside double quotes) so commas in strings do not split items.
func yamlFlowSequenceItems(value string) ([]string, bool) {
	value = strings.TrimSpace(stripYamlInlineComment(value))
	inner, found := strings.CutPrefix(value, "[")
	if !found {
		return nil, false
	}
	inner, found = strings.CutSuffix(inner, "]")
	if !found {
		return nil, false
	}
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return []string{}, true
	}

	var items []string
	var current strings.Builder
	var quote rune
	escaped := false

	for _, ch := range inner {
		if quote != 0 {
			current.WriteRune(ch)
			if quote == '"' && ch == '\\' && !escaped {
				escaped = true
				continue
			}
			if ch == quote && !escaped {
				quote = 0
			}
			escaped = false
			continue
		}

		switch ch {
		case '"', '\'':
			quote = ch
			current.WriteRune(ch)
		case ',':
			items = append(items, yamlScalarValue(current.String()))
			current.Reset()
		default:
			current.WriteRune(ch)
		}
	}

	if quote != 0 {
		return nil, false
	}

	items = append(items, yamlScalarValue(current.String()))
	return items, true
}

func yamlScalarValue(value string) string {
	value = strings.TrimSpace(stripYamlInlineComment(value))
	if len(value) >= 2 {
		quoted := (value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'')
		if quoted {
			return value[1 : len(value)-1]
		}
	}
	return value
}

// stripYamlInlineComment removes a ` # ...` trailer, respecting quoting so a
// '#' inside a quoted scalar survives.
func stripYamlInlineComment(value string) string {
	var quote rune
	escaped := false

	for index, ch := range value {
		if quote != 0 {
			if quote == '"' && ch == '\\' && !escaped {
				escaped = true
				continue
			}
			if ch == quote && !escaped {
				quote = 0
			}
			escaped = false
			continue
		}

		switch {
		case ch == '"' || ch == '\'':
			quote = ch
		case ch == '#' && (index == 0 || endsWithWhitespace(value[:index])):
			return strings.TrimRight(value[:index], " \t\n\r")
		}
	}

	return value
}

func endsWithWhitespace(s string) bool {
	if s == "" {
		return false
	}
	return strings.TrimRight(s, " \t\n\r\f\v") != s
}

func joinYamlLines(lines []string, trailingNewline bool) string {
	result := strings.Join(lines, "\n")
	if trailingNewline || result == "" {
		result += "\n"
	}
	return result
}
