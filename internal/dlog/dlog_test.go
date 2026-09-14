package dlog

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// The producer and the reader live in this one package precisely so that what
// Warnf writes is what Classify keeps. Round-trip through a real logger with the
// default flags, which is how every daemon runs.
func TestLevelsRoundTripThroughTheStandardLogger(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(log.LstdFlags)
	defer func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}()

	log.Printf("catway: host local attached")
	Warnf("catway: cathost %s answered no ping", "local")
	Errorf("catway: save session state: %v", "disk full")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3: %q", len(lines), lines)
	}
	want := []Class{Routine, Severe, Severe}
	for i, line := range lines {
		if got := Classify(line); got != want[i] {
			t.Errorf("Classify(%q) = %v, want %v", line, got, want[i])
		}
	}
	if got := StripTimestamp(lines[1]); got != "WARN catway: cathost local answered no ping" {
		t.Errorf("StripTimestamp = %q", got)
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		line string
		want Class
	}{
		{"2026/09/13 22:08:31 WARN catway: x", Severe},
		{"2026/09/13 22:08:31.123456 ERROR catway: x", Severe},
		{"2026/09/13 22:08:31 FATAL catway: x", Severe},
		{"WARN catway: no timestamp (log.SetFlags(0))", Severe},
		{"2026/09/13 22:08:31 catway: serving at http://…", Routine},
		// A level word anywhere but the start of the message is prose.
		{"2026/09/13 22:08:31 catway: no WARN here", Routine},
		{"2026/09/13 22:08:31 WARNING catway: x", Routine},
		// Runtime crash reports start at column 0 with no timestamp.
		{"panic: runtime error: index out of range [3] with length 3", Crash},
		{"fatal error: concurrent map writes", Crash},
		{"SIGQUIT: quit", Crash},
		{"SIGSEGV: segmentation violation", Crash},
		// …and a logged message that mentions one is not one.
		{"2026/09/13 22:08:31 catway: recovered panic: boom", Routine},
		{"SIGnal: lowercase is not a signal banner", Routine},
		{"goroutine 1 [running]:", Routine}, // only kept as part of a crash already under way
		{"", Routine},
	}
	for _, c := range cases {
		if got := Classify(c.line); got != c.want {
			t.Errorf("Classify(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

func TestStripTimestampLeavesOtherLinesAlone(t *testing.T) {
	for _, line := range []string{
		"catway: hello",
		"2026/09/13 catway: date only",
		"2026-09-13 22:08:31 catway: wrong separators",
		"2026/09/13 22:08:31", // nothing after it
		"",
	} {
		if got := StripTimestamp(line); got != line {
			t.Errorf("StripTimestamp(%q) = %q, want it unchanged", line, got)
		}
	}
}
