// Package detect identifies which AI coding agent is running in a pane and (in
// later stages) its state. Stage A covers process-based identity: given a pane's
// PTY, find the foreground process group and map its command(s) to a canonical
// agent label. The Go daemon owns the PTY child, so this lives here rather than
// in the Rust orchestrator.
//
// The label vocabulary mirrors cats's detect::identify_agent table so the Rust
// side can map labels back via parse_agent_label.
package detect

import "strings"

// normalize lowercases, strips a path, and drops common executable suffixes —
// mirroring cats's normalized_agent_lookup_name + path_basename.
func normalize(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndexAny(n, "/\\"); i >= 0 {
		n = n[i+1:]
	}
	for _, suf := range []string{".exe", ".cmd", ".bat", ".ps1", ".js"} {
		if strings.HasSuffix(n, suf) {
			n = n[:len(n)-len(suf)]
			break
		}
	}
	return n
}

// IdentifyAgent maps a single process/command name to a canonical agent label,
// or "" for a plain shell or unrecognized program.
func IdentifyAgent(name string) string {
	switch normalize(name) {
	case "pi":
		return "pi"
	case "claude", "claude-code":
		return "claude"
	case "codex":
		return "codex"
	case "gemini":
		return "gemini"
	case "cursor", "cursor-agent":
		return "cursor"
	case "agy", "antigravity", "antigravity-cli":
		return "agy"
	case "cline":
		return "cline"
	case "opencode", "open-code":
		return "opencode"
	case "copilot", "github-copilot", "ghcs":
		return "copilot"
	case "kimi", "kimi-code":
		return "kimi"
	case "kiro", "kiro-cli":
		return "kiro"
	case "droid":
		return "droid"
	case "amp", "amp-local":
		return "amp"
	case "grok", "grok-build":
		return "grok"
	case "hermes", "hermes-agent":
		return "hermes"
	case "kilo", "kilo-code":
		return "kilo"
	case "qodercli", "qoderclicn", "qoder", "qodercn":
		return "qodercli"
	}
	return ""
}

// IdentifyFirst returns the first non-empty agent label among the candidates
// (e.g. a process's comm, exec-path basename, and argv[0]/argv[1]).
func IdentifyFirst(candidates ...string) string {
	for _, c := range candidates {
		if label := IdentifyAgent(c); label != "" {
			return label
		}
	}
	return ""
}
