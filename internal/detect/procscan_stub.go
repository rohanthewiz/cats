//go:build !darwin && !linux

package detect

// ForegroundAgent is unsupported on this platform (no process-group inspection).
func ForegroundAgent(fd uintptr) string { return "" }

// ForegroundPGID is unsupported on this platform; -1 means "no foreground group".
func ForegroundPGID(fd uintptr) int { return -1 }
