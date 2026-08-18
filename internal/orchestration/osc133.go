package orchestration

import (
	"strconv"
	"strings"
	"time"
)

// OSC 133 shell integration — the marks a shell prints around each command, and
// the pairing that turns them into ledger records.
//
// A terminal cannot see where one command ends and the next begins: it receives
// an undifferentiated byte stream, and "the prompt" is just more output. OSC 133
// is the convention that fixes that — the shell brackets its own prompt and each
// command with escape sequences:
//
//	OSC 133 ; A ST      prompt starts
//	OSC 133 ; B ST      prompt ends, the user's typing starts
//	OSC 133 ; C ST      the command is running; everything after is its output
//	OSC 133 ; D ; 1 ST  the command finished, with this exit status
//
// The one thing OSC 133 does NOT carry is the command line itself, which is the
// field a command ledger exists for. VSCode's shell integration added that as
// `OSC 633 ; E ; <command> ST`, and that spelling is what this scanner reads —
// deliberately, because it means anyone who already has VSCode's integration
// installed feeds the ledger without installing ours.
//
// # Why the daemon does this
//
// The marks are in the pane's byte stream and the pane's byte stream is on this
// machine. catway seeing them at all would mean shipping every byte twice.
// Pairing them here also means the DURATION is measured where the command ran,
// on that machine's clock, rather than including a network hop of a size that
// varies with how good the wifi is.

// osc133MaxLen caps a buffered OSC 133/633 body. A command line is the largest
// thing that arrives here and a very long one is still a few kilobytes; the cap
// only bounds stream garbage.
const osc133MaxLen = 8192

// shellMarkKind is the OSC 133 letter, plus the pseudo-kind for a command line
// arriving out of band on OSC 633.
type shellMarkKind uint8

const (
	markNone shellMarkKind = iota
	markPromptStart
	markInputStart
	markOutputStart
	markCommandEnd
	markCommandLine
)

// shellMark is one decoded mark. Exit is meaningful only for markCommandEnd and
// is nil when the shell reported none; Cmd only for markCommandLine.
type shellMark struct {
	Kind shellMarkKind
	Exit *int
	Cmd  string
}

// osc133Scanner extracts shell-integration marks from a raw terminal output
// stream, tolerating sequences split across reads. Its state machine is the same
// one oscScanner (OSC 7) and osc52Scanner use, for the same reason: libghostty-vt
// surfaces none of this, so the Host scans the raw PTY bytes itself. Not safe for
// concurrent use — a pane drives one scanner from its readPump goroutine.
type osc133Scanner struct {
	state oscState
	buf   []byte
}

// scan consumes a chunk of output and returns every mark completed within it, in
// order. A chunk routinely holds several: a prompt redraw emits A and B back to
// back, and a fast command can produce its whole A-B-E-C-D cycle in one read.
func (s *osc133Scanner) scan(b []byte) []shellMark {
	var out []shellMark
	emit := func() {
		if m, ok := parseShellMark(s.buf); ok {
			out = append(out, m)
		}
	}
	for _, c := range b {
		switch s.state {
		case oscNormal:
			if c == 0x1b {
				s.state = oscSawEsc
			}
		case oscSawEsc:
			switch c {
			case ']':
				s.state = oscCollect
				s.buf = s.buf[:0]
			case 0x1b: // ESC ESC — this could still introduce an OSC
			default:
				s.state = oscNormal
			}
		case oscCollect:
			switch c {
			case 0x07: // BEL terminator
				emit()
				s.reset()
			case 0x1b: // possible ST (ESC \)
				s.state = oscCollectEsc
			default:
				if len(s.buf) < osc133MaxLen {
					s.buf = append(s.buf, c)
				} else {
					s.reset() // overlong / unterminated; abandon
				}
			}
		case oscCollectEsc:
			if c == '\\' { // ST terminator
				emit()
				s.reset()
			} else {
				s.reset()
				if c == 0x1b {
					s.state = oscSawEsc
				}
			}
		}
	}
	return out
}

func (s *osc133Scanner) reset() {
	s.state = oscNormal
	s.buf = s.buf[:0]
}

// parseShellMark decodes one OSC body. Unknown commands and unknown 133/633
// subcommands are not errors — they are the rest of the OSC vocabulary going
// past — so they simply yield ok=false.
func parseShellMark(body []byte) (shellMark, bool) {
	s := string(body)
	if rest, ok := strings.CutPrefix(s, "133;"); ok {
		return parse133(rest)
	}
	if rest, ok := strings.CutPrefix(s, "633;"); ok {
		return parse633(rest)
	}
	return shellMark{}, false
}

// parse133 reads the A/B/C/D marks. Everything after the letter is parameters,
// which differ per emitter (kitty sends "A;k=s", others send nothing) — only D's
// first parameter is defined, and it is the exit status.
func parse133(rest string) (shellMark, bool) {
	if rest == "" {
		return shellMark{}, false
	}
	head, params, _ := strings.Cut(rest, ";")
	switch head {
	case "A":
		return shellMark{Kind: markPromptStart}, true
	case "B":
		return shellMark{Kind: markInputStart}, true
	case "C":
		return shellMark{Kind: markOutputStart}, true
	case "D":
		m := shellMark{Kind: markCommandEnd}
		// "D" alone is legal and means the shell did not report a status. A
		// non-numeric parameter is treated the same way rather than guessed at:
		// a wrong exit code in a ledger is worse than an absent one, because it
		// is the field somebody filters "what failed" on.
		if first, _, _ := strings.Cut(params, ";"); first != "" {
			if code, err := strconv.Atoi(first); err == nil {
				m.Exit = &code
			}
		}
		return m, true
	}
	return shellMark{}, false
}

// parse633 reads VSCode's shell-integration extensions, of which exactly one
// matters here: E, the command line. The rest (its own A/B/C/D aliases, property
// reports) are left alone — a shell emitting both sets would otherwise have every
// command counted twice.
func parse633(rest string) (shellMark, bool) {
	payload, ok := strings.CutPrefix(rest, "E;")
	if !ok {
		return shellMark{}, false
	}
	// A trailing nonce parameter is VSCode's spoofing guard. It is not a
	// security boundary here — anything that can write to the PTY can write to
	// the PTY — so it is simply dropped, and an unescaped ";" inside the command
	// makes the tail its own field, which unescape below cannot recover. Our own
	// script escapes, so this only bites hand-rolled emitters.
	cmd, _, _ := strings.Cut(payload, ";")
	return shellMark{Kind: markCommandLine, Cmd: unescape633(cmd)}, true
}

// unescape633 reverses VSCode's escaping: a backslash escape for the backslash
// itself, and \xNN for anything that would otherwise break the OSC framing
// (";", newlines, the escape character).
func unescape633(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case '\\':
			b.WriteByte('\\')
			i++
		case 'x', 'X':
			if i+3 < len(s) {
				if v, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
					b.WriteByte(byte(v))
					i += 3
					continue
				}
			}
			b.WriteByte(s[i])
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// --- pairing ------------------------------------------------------------------

// commandRecord is one paired command, ready to become a seam event. Start
// records carry Cmd; end records carry Exit and Duration.
type commandRecord struct {
	Start    bool
	Cmd      string
	Exit     *int
	Duration time.Duration
}

// cmdTracker pairs a pane's marks into commands. One per pane, driven from its
// readPump.
//
// **A command with no command line is not recorded**, and that single rule earns
// its keep three times over. It drops the empty Enter that every shell emits a
// full A-B-C-D cycle for; it means a ledger row always has the field the ledger
// exists for; and it makes the feature's precondition one legible sentence —
// "your shell has to tell us what it ran" — rather than a ledger quietly full of
// blank rows that look like a bug.
type cmdTracker struct {
	cmd     string
	running bool
	started time.Time
}

// mark feeds one decoded mark and returns a record when one completes. now is
// injected so the pairing is testable without sleeping, and so the duration is
// measured on this machine's clock.
func (t *cmdTracker) mark(m shellMark, now time.Time) (commandRecord, bool) {
	switch m.Kind {
	case markCommandLine:
		t.cmd = m.Cmd

	case markPromptStart:
		// A new prompt while a command is still open means its D never arrived —
		// the process was killed, the shell was replaced, the integration is
		// half-installed. Close the record with an unknown status rather than
		// leaving it open forever: a row that says "finished, status unknown" is
		// true, and one that stays running is not.
		if t.running {
			t.running = false
			t.cmd = ""
			return commandRecord{Duration: now.Sub(t.started)}, true
		}
		t.cmd = ""

	case markOutputStart:
		if t.cmd == "" {
			return commandRecord{}, false // see the type comment
		}
		t.running = true
		t.started = now
		return commandRecord{Start: true, Cmd: t.cmd}, true

	case markCommandEnd:
		if !t.running {
			return commandRecord{}, false // an end with no start of ours
		}
		t.running = false
		t.cmd = ""
		return commandRecord{Exit: m.Exit, Duration: now.Sub(t.started)}, true
	}
	return commandRecord{}, false
}
