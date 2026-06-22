package orchestration

import (
	"bytes"
	"strings"
	"unicode"
)

// osc9MaxLen caps a single buffered OSC 9 body. Progress payloads are tiny
// (`4;3;`); the cap only bounds garbage. oscProgressMaxChars further caps the
// retained string, mirroring herdr's AGENT_OSC_MAX_CHARS.
const (
	osc9MaxLen          = 4096
	oscProgressMaxChars = 256
)

// osc9Scanner extracts OSC 9 progress reports from a raw terminal output stream,
// tolerating sequences split across reads. libghostty-vt does not surface OSC 9
// (it does surface OSC 0/2 title via Emulator.Title), so the Host scans the raw
// PTY bytes itself — the same approach as oscScanner (OSC 7) and osc52Scanner
// (OSC 52). The detector feeds the retained progress into detect.Input.OscProgress
// so manifest `osc_progress` rules (e.g. Claude's `^4;0` idle) can match.
//
// Mirrors the OSC 9 half of herdr's AgentOscStateTracker. Not safe for concurrent
// use: a pane drives one scanner from its readPump goroutine.
type osc9Scanner struct {
	state oscState
	buf   []byte
}

// scan consumes a chunk of terminal output and returns the most recent OSC 9
// progress payload completed within it (ok=false if none) — the body after the
// `9;` command, sanitized. State persists across calls so a sequence split across
// reads is still recognized.
func (s *osc9Scanner) scan(b []byte) (progress string, ok bool) {
	for _, c := range b {
		switch s.state {
		case oscNormal:
			if c == 0x1b {
				s.state = oscSawEsc
			}
		case oscSawEsc:
			switch c {
			case ']': // ESC ] => OSC introducer
				s.state = oscCollect
				s.buf = s.buf[:0]
			case 0x1b: // ESC ESC => stay; this could still introduce an OSC
			default:
				s.state = oscNormal
			}
		case oscCollect:
			switch c {
			case 0x07: // BEL terminator
				if p, found := parseOSC9Progress(s.buf); found {
					progress, ok = p, true
				}
				s.reset()
			case 0x1b: // possible ST (ESC \)
				s.state = oscCollectEsc
			default:
				if len(s.buf) < osc9MaxLen {
					s.buf = append(s.buf, c)
				} else {
					s.reset() // overlong / unterminated; abandon
				}
			}
		case oscCollectEsc:
			if c == '\\' { // ST terminator
				if p, found := parseOSC9Progress(s.buf); found {
					progress, ok = p, true
				}
				s.reset()
			} else {
				s.reset()
				if c == 0x1b {
					s.state = oscSawEsc
				}
			}
		}
	}
	return progress, ok
}

func (s *osc9Scanner) reset() {
	s.state = oscNormal
	s.buf = s.buf[:0]
}

// parseOSC9Progress extracts the progress payload from an OSC 9 body — everything
// after the `9;` command, sanitized. Returns ok=false for other OSC commands.
// Mirrors how herdr's AgentOscStateTracker stores OSC 9 payloads verbatim (after
// the command) as latest_progress.
func parseOSC9Progress(body []byte) (string, bool) {
	rest, ok := bytes.CutPrefix(body, []byte("9;"))
	if !ok {
		return "", false
	}
	return sanitizeOSCString(rest, oscProgressMaxChars), true
}

// sanitizeOSCString drops control characters and caps the result at maxChars
// runes — untrusted child output bounded for safety. Mirrors herdr's
// sanitize_agent_osc_string.
func sanitizeOSCString(payload []byte, maxChars int) string {
	var b strings.Builder
	n := 0
	for _, r := range string(payload) {
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
		if n++; n >= maxChars {
			break
		}
	}
	return b.String()
}
