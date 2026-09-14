// Package dlog gives the daemons' log lines a severity, and lets whoever
// captures their output tell a warning from routine chatter.
//
// The daemons log through the standard library's log package, one line per
// event, and keep doing so: most of that output is a narrative of normal
// operation (a host attached, a config reloaded) that is useful in a dev
// terminal and worth nothing after the fact. What is worth keeping is the rest
// — a stalled write, a failed save, a session that ended with an error — and
// catapp keeps exactly that, in a file beside boot.log (cmd/catapp/daemonlog.go).
// The 2026-09-13 freeze left no record at all because everything went to
// /dev/null; the fix is to keep the lines that would have explained it without
// keeping the thousands that would have buried them.
//
// The contract is a level word at the start of the message, straight after the
// log package's own date and time:
//
//	2026/09/13 22:08:31 WARN catway: cathost local answered no ping in 1m0s — closing the connection
//	└──── log flags ────┘ └┬─┘ └──────────────────── message ─────────────────────────────────┘
//	                       level
//
// A text prefix rather than slog's structured output, deliberately: the
// existing calls are Printf-style and read well in a terminal, the reader only
// ever needs one bit per line (keep it or not), and the producer and the reader
// sharing this one package is what keeps the two from drifting. Lines with no
// level word are informational.
//
// Output the runtime writes itself is classified here too, because it is the
// most severe output a Go process ever produces and it bypasses log entirely: a
// panic, a fatal error ("concurrent map writes") or a signal dump starts a
// crash report, and every line after it on that stream belongs to the report.
package dlog

import (
	"fmt"
	"log"
	"os"
	"strings"
)

// Level words, as they appear on the wire. Exported so tests and the reader
// name them rather than retyping them.
const (
	LevelWarn  = "WARN"
	LevelError = "ERROR"
	LevelFatal = "FATAL"
)

// Warnf logs something that went wrong without stopping the daemon: a
// connection dropped, a feature disabled, a request refused.
func Warnf(format string, args ...any) { output(LevelWarn, format, args...) }

// Errorf logs a failure that loses work or leaves the daemon degraded: state
// that could not be saved, an invariant that did not hold.
func Errorf(format string, args ...any) { output(LevelError, format, args...) }

// Fatalf logs and exits with status 1, like log.Fatalf.
func Fatalf(format string, args ...any) {
	output(LevelFatal, format, args...)
	os.Exit(1)
}

// output writes through the standard logger so the line keeps whatever flags
// and prefix the process configured. calldepth 3 attributes the line to
// Warnf's caller should Lshortfile ever be turned on.
func output(level, format string, args ...any) {
	_ = log.Output(3, level+" "+fmt.Sprintf(format, args...))
}

// Class is what a captured line is worth keeping as.
type Class int

const (
	// Routine is informational output: not kept.
	Routine Class = iota
	// Severe is a line logged at warning level or above: kept.
	Severe
	// Crash is the first line of a runtime crash report: kept, and so is every
	// line after it on the same stream.
	Crash
)

// Classify reports what one line of daemon output (without its newline) is.
func Classify(line string) Class {
	if crashStart(line) {
		return Crash
	}
	msg := StripTimestamp(line)
	for _, lv := range [...]string{LevelWarn, LevelError, LevelFatal} {
		if strings.HasPrefix(msg, lv+" ") {
			return Severe
		}
	}
	return Routine
}

// crashStart matches the first line the Go runtime writes when the process is
// going down on its own: "panic: …", "fatal error: …", or the signal banner of
// a dump such as "SIGQUIT: quit" / "SIGABRT: abort". Those are written at the
// start of a line with no log timestamp, which is what keeps an ordinary
// message that merely mentions a panic from matching.
func crashStart(line string) bool {
	if strings.HasPrefix(line, "panic: ") || strings.HasPrefix(line, "fatal error: ") {
		return true
	}
	// "SIG" + upper-case name + ": ", e.g. SIGSEGV: segmentation violation.
	rest, ok := strings.CutPrefix(line, "SIG")
	if !ok {
		return false
	}
	name, _, found := strings.Cut(rest, ": ")
	if !found || name == "" {
		return false
	}
	for _, r := range name {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// StripTimestamp removes the log package's default date and time
// ("2006/01/02 15:04:05 ", optionally with ".000000" microseconds) from the
// start of a line, returning the line unchanged when it has none. A reader that
// stamps lines with its own clock uses it to avoid printing two timestamps.
func StripTimestamp(line string) string {
	const date = "2006/01/02 "
	const clock = "15:04:05"
	if len(line) < len(date)+len(clock)+1 || !digitsAt(line, date) {
		return line
	}
	rest := line[len(date):]
	if !digitsAt(rest, clock) {
		return line
	}
	rest = rest[len(clock):]
	// Lmicroseconds adds ".ffffff".
	if len(rest) >= 7 && rest[0] == '.' && digitsAt(rest[1:], "000000") {
		rest = rest[7:]
	}
	if !strings.HasPrefix(rest, " ") {
		return line
	}
	return rest[1:]
}

// digitsAt reports whether s starts with layout, where every digit in layout
// matches any digit in s and every other byte must match exactly.
func digitsAt(s, layout string) bool {
	if len(s) < len(layout) {
		return false
	}
	for i := 0; i < len(layout); i++ {
		c, l := s[i], layout[i]
		if l >= '0' && l <= '9' {
			if c < '0' || c > '9' {
				return false
			}
		} else if c != l {
			return false
		}
	}
	return true
}
