package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// errExit prints a "cats-todo:"-prefixed message to stderr and exits non-zero.
func errExit(args ...any) {
	fmt.Fprintln(os.Stderr, append([]any{"cats-todo:"}, args...)...)
	os.Exit(1)
}

// firstNonEmpty returns the first argument that is not the empty string, or ""
// when all are empty.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// newID returns a short random hex id for a todo. It is not cryptographically
// meaningful — just collision-resistant enough to key a todo across edits.
func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// rand.Read essentially never fails; if it does, a fixed-but-unique-ish
		// fallback keyed off the address is good enough for a local todo id.
		return fmt.Sprintf("t%p", &b)
	}
	return hex.EncodeToString(b[:])
}

// firstLine returns the first non-blank line of s, trimmed, capped to max runes
// (with an ellipsis when truncated). It is used to derive a one-line preview of
// a multi-line prompt for the list.
func firstLine(s string, max int) string {
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		return truncate(ln, max)
	}
	return ""
}

// truncate shortens s to at most max runes, appending "…" when it had to cut.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}
