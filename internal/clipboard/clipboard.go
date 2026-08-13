// Package clipboard reads the host system clipboard through whichever
// command-line tool the platform provides.
//
// It exists because cats has no clipboard of its own. Every clipboard path in
// the product so far runs at a FRONT END — the browser writes with
// navigator.clipboard, the mac app bridges pbcopy/pbpaste into the page
// (cmd/catapp/clipboard.go), and a pane app writes with OSC 52 — and none of
// those is reachable from a program running inside a pane. An editor in a pane
// that wants to diff the buffer against what the user just copied has, until
// now, had to ask the user to paste it in by hand.
//
// Reading rather than writing is deliberate and asymmetric: OSC 52 already gives
// a pane app a working WRITE path (that is how "copy" works from inside a pane),
// and it is write-only by design, because a terminal that answered clipboard
// reads would let any program that can print bytes exfiltrate whatever the user
// last copied. The read path therefore stays off the terminal stream entirely and
// goes through the owner-only control socket — see ctlproto.MethodClipboardRead
// for why it is not a §7 command.
package clipboard

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// readTimeout bounds one clipboard read. The tools below are tiny and local, so
// this is a backstop against a wedged one, not a budget: a hung wl-paste
// (a compositor that stopped answering) would otherwise hold the control
// connection open until its own deadline, and the caller is an interactive
// editor waiting to open a panel.
const readTimeout = 2 * time.Second

// MaxBytes caps how much clipboard text one read returns. The clipboard can hold
// an arbitrarily large paste and the control protocol frames a response as one
// JSON line, so an uncapped read turns "the user copied a log file" into a
// multi-megabyte line every client must buffer. Callers see Truncated rather than
// an error: a truncated diff is still useful, a failed one is not.
const MaxBytes = 4 << 20 // 4 MiB

// ErrUnsupported is returned when no clipboard tool is available: an unknown
// platform, or a Linux box with none of wl-paste/xclip/xsel installed (a headless
// server, typically — where there is genuinely no clipboard to read rather than a
// missing package). Callers surface it as a capability answer, not a fault.
var ErrUnsupported = errors.New("clipboard: no reader available on this platform")

// reader is one candidate way to read the clipboard: the program to look for on
// PATH and the arguments that make it print the clipboard to stdout.
type reader struct {
	name string
	args []string
	// emptyIsError marks a tool that exits non-zero for an EMPTY clipboard
	// instead of printing nothing. wl-paste does this ("Nothing is copied"), and
	// treating it as a failure would report a broken clipboard every time the
	// user has not copied anything yet.
	emptyIsError bool
}

// readers returns the candidates for this platform, best first. The first one
// present on PATH wins; a tool that is installed but fails is NOT skipped in
// favour of the next, because a Wayland session with xclip also installed would
// otherwise silently answer from an X11 clipboard that is not the one the user
// is copying into.
func readers() []reader {
	switch runtime.GOOS {
	case "darwin":
		return []reader{{name: "pbpaste"}}
	case "linux", "freebsd", "openbsd", "netbsd":
		return []reader{
			// Wayland first: on a session running Xwayland both are present, and
			// wl-paste is the one that sees the compositor's real selection.
			{name: "wl-paste", args: []string{"--no-newline"}, emptyIsError: true},
			{name: "xclip", args: []string{"-selection", "clipboard", "-o"}},
			{name: "xsel", args: []string{"--clipboard", "--output"}},
		}
	case "windows":
		return []reader{{name: "powershell", args: []string{"-NoProfile", "-Command", "Get-Clipboard"}}}
	}
	return nil
}

// Available reports whether this host has a clipboard reader at all, without
// running one. A capability probe uses it to answer "can I offer this?" for the
// price of a PATH lookup.
func Available() bool {
	_, ok := pick()
	return ok
}

// pick resolves the first candidate present on PATH.
func pick() (reader, bool) {
	for _, r := range readers() {
		if _, err := exec.LookPath(r.name); err == nil {
			return r, true
		}
	}
	return reader{}, false
}

// Read returns the host system clipboard's text, and whether it was truncated at
// MaxBytes. An empty clipboard is ("", false, nil) — an ordinary answer, not a
// failure. ErrUnsupported means there is no reader on this host.
func Read() (text string, truncated bool, err error) {
	r, ok := pick()
	if !ok {
		return "", false, ErrUnsupported
	}
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.name, r.args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		// Distinguish "the deadline fired" from "the tool said no": the first is
		// worth naming because the tool is still running somewhere.
		if ctx.Err() != nil {
			return "", false, fmt.Errorf("clipboard: %s timed out after %s", r.name, readTimeout)
		}
		if r.emptyIsError && len(out) == 0 {
			return "", false, nil // see reader.emptyIsError
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", false, fmt.Errorf("clipboard: %s: %s", r.name, firstLine(msg))
		}
		return "", false, fmt.Errorf("clipboard: %s: %w", r.name, err)
	}
	if len(out) > MaxBytes {
		// Cut on the byte cap, then back off to the last whole rune so the result
		// is never a string ending in a broken multi-byte character — JSON
		// encoding would silently replace it and the caller would diff against a
		// U+FFFD it never copied.
		return string(trimPartialRune(out[:MaxBytes])), true, nil
	}
	return string(out), false, nil
}

// firstLine keeps an error message to one line. A clipboard tool's stderr can
// run to a usage block, and this message ends up in an editor's status bar.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// trimPartialRune drops a trailing incomplete UTF-8 sequence from b. UTF-8
// continuation bytes are 10xxxxxx, so walk back over them (at most 3) and drop
// the lead byte too when the sequence it starts is longer than what remains.
func trimPartialRune(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i > len(b)-4; i-- {
		c := b[i]
		if c&0xC0 == 0x80 {
			continue // continuation byte; keep walking back to the lead
		}
		if c&0x80 == 0 {
			return b // ASCII lead: the tail is whole
		}
		// A lead byte: 110x = 2 bytes, 1110 = 3, 11110 = 4.
		want := 2
		switch {
		case c&0xF8 == 0xF0:
			want = 4
		case c&0xF0 == 0xE0:
			want = 3
		}
		if len(b)-i < want {
			return b[:i] // the sequence is cut short — drop it whole
		}
		return b
	}
	return b
}
