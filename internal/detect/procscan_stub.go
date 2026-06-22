//go:build !darwin && !linux

package detect

// ForegroundAgent is unsupported on this platform (no process-group inspection).
func ForegroundAgent(fd uintptr) string { return "" }
