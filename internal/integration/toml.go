package integration

// Line-based TOML editors, ported from the Rust source without a TOML
// library: comment and formatting preservation is load-bearing (idempotency
// is asserted against exact file content).
//
//   - codex config.toml: force `hooks = true` under the TOP-LEVEL [features]
//     table only (never [profiles.*.features]), removing deprecated
//     `codex_hooks` keys there, appending a fresh [features] table if absent.
//   - kimi config.toml: replace the sentinel-delimited herdr block with a
//     freshly generated set of [[hooks]] tables, leaving user hooks alone.

import (
	"fmt"
	"strings"
)

// splitLines mirrors Rust's str::lines(): no trailing empty element for a
// trailing newline, and a lone "\n" yields one empty line. CR before LF is
// stripped.
func splitLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}
	return lines
}

func joinTomlLines(lines []string, trailingNewline bool) string {
	result := strings.Join(lines, "\n")
	if trailingNewline || result == "" {
		result += "\n"
	}
	return result
}

// tomlTableHeader returns the bracketed header ("[features]", "[[hooks]]") if
// the line is one, tolerating a trailing comment.
func tomlTableHeader(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if strings.HasPrefix(trimmed, "#") || !strings.HasPrefix(trimmed, "[") {
		return "", false
	}

	var headerEnd int
	if strings.HasPrefix(trimmed, "[[") {
		index := strings.Index(trimmed, "]]")
		if index < 0 {
			return "", false
		}
		headerEnd = index + 2
	} else {
		index := strings.Index(trimmed, "]")
		if index < 0 {
			return "", false
		}
		headerEnd = index + 1
	}
	header := trimmed[:headerEnd]
	rest := strings.TrimLeft(trimmed[headerEnd:], " \t")
	if rest != "" && !strings.HasPrefix(rest, "#") {
		return "", false
	}
	return header, true
}

func isTomlKey(line, key string) bool {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "#") || !strings.HasPrefix(trimmed, key) {
		return false
	}
	return strings.HasPrefix(strings.TrimLeft(trimmed[len(key):], " \t"), "=")
}

// buildCodexConfigWithHooks rewrites config.toml so the top-level [features]
// table carries `hooks = true` (and no deprecated codex_hooks keys).
func buildCodexConfigWithHooks(content string) string {
	lines := splitLines(content)
	trailingNewline := strings.HasSuffix(content, "\n")
	inTopLevelFeatures := false
	featuresHeaderIndex := -1
	hooksIndex := -1
	var deprecatedHooksIndexes []int

	for index, line := range lines {
		if header, ok := tomlTableHeader(line); ok {
			inTopLevelFeatures = header == "[features]"
			if inTopLevelFeatures && featuresHeaderIndex < 0 {
				featuresHeaderIndex = index
			}
			continue
		}
		if !inTopLevelFeatures {
			continue
		}
		if isTomlKey(line, "codex_hooks") {
			deprecatedHooksIndexes = append(deprecatedHooksIndexes, index)
		} else if isTomlKey(line, "hooks") {
			hooksIndex = index
		}
	}

	if hooksIndex >= 0 {
		lines[hooksIndex] = "hooks = true"
	}

	for i := len(deprecatedHooksIndexes) - 1; i >= 0; i-- {
		index := deprecatedHooksIndexes[i]
		lines = append(lines[:index], lines[index+1:]...)
	}

	if hooksIndex < 0 {
		if featuresHeaderIndex >= 0 {
			lines = append(lines[:featuresHeaderIndex+1],
				append([]string{"hooks = true"}, lines[featuresHeaderIndex+1:]...)...)
			return joinTomlLines(lines, trailingNewline)
		}

		result := strings.TrimRight(content, "\n")
		if result != "" {
			result += "\n\n"
		}
		result += "[features]\nhooks = true\n"
		return result
	}

	return joinTomlLines(lines, trailingNewline)
}

// buildKimiConfigWithHooks strips any existing herdr block and appends a
// fresh one holding all lifecycle [[hooks]] tables between the sentinels.
func buildKimiConfigWithHooks(content, hookPath string) string {
	result := strings.TrimRight(removeKimiConfigBlock(content), "\n")
	if result != "" {
		result += "\n\n"
	}

	result += kimiConfigBlockBegin + "\n"
	for _, he := range kimiHookEvents {
		result += kimiHookTable(he.event, hookPath, he.action)
	}
	result += kimiConfigBlockEnd + "\n"
	return result
}

func kimiHookTable(event, hookPath, action string) string {
	command := hookCommand(hookPath, action, true)
	return fmt.Sprintf("[[hooks]]\nevent = %s\ncommand = %s\ntimeout = 10\n\n",
		tomlBasicString(event), tomlBasicString(command))
}

func removeKimiConfigBlock(content string) string {
	trailingNewline := strings.HasSuffix(content, "\n")
	var lines []string
	inBlock := false
	removedBlock := false

	for _, line := range splitLines(content) {
		trimmed := strings.TrimSpace(line)
		if trimmed == kimiConfigBlockBegin {
			inBlock = true
			removedBlock = true
			continue
		}
		if inBlock {
			if trimmed == kimiConfigBlockEnd {
				inBlock = false
			}
			continue
		}
		lines = append(lines, line)
	}

	if !removedBlock {
		return content
	}

	result := joinTomlLines(lines, trailingNewline)
	for strings.HasSuffix(result, "\n\n") {
		result = result[:len(result)-1]
	}
	if result == "\n" {
		return ""
	}
	return result
}

// tomlBasicString renders a TOML basic (double-quoted) string.
func tomlBasicString(value string) string {
	var b strings.Builder
	b.Grow(len(value) + 2)
	b.WriteByte('"')
	for _, ch := range value {
		switch ch {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if ch <= 0x1f || ch == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, ch)
			} else {
				b.WriteRune(ch)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
