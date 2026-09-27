//go:build !darwin && !linux

package detect

// ForegroundAgent is unsupported on this platform (no process-group inspection).
func ForegroundAgent(fd uintptr) string { return "" }

// ForegroundAgentPIDs is unsupported on this platform; with no pids there is no
// agent session to resolve either (see AgentSessionID).
func ForegroundAgentPIDs(fd uintptr) (string, []int) { return "", nil }

// ForegroundPGID is unsupported on this platform; -1 means "no foreground group".
func ForegroundPGID(fd uintptr) int { return -1 }

// ProcessCwd is unsupported on this platform; panes fall back to whatever cwd
// their shell reports over OSC 7.
func ProcessCwd(pid int) string { return "" }

// ProcessExe is unsupported on this platform; a pane's foreground job is then
// never matched to a plugin, and only launched plugin panes get a row.
func ProcessExe(pid int) string { return "" }

// ProcessArgs is unsupported on this platform; a script plugin typed at a
// prompt is then never matched either.
func ProcessArgs(pid int) []string { return nil }
