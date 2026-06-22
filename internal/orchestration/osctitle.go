package orchestration

import "bytes"

// oscTitleMaxLen caps a single buffered OSC 0/2 body. Titles are short; the cap
// only bounds garbage. oscProgressMaxChars (defined in osc9.go) further caps the
// retained string, mirroring herdr's AGENT_OSC_MAX_CHARS.
const oscTitleMaxLen = 4096

// oscTitleScanner extracts OSC 0/2 window-title reports from a raw terminal output
// stream, tolerating sequences split across reads. libghostty-vt *does* surface the
// title to the emulator (Emulator.Title, used by the detector), but the termhost
// seam carries no title to the orchestrator, so a termhost pane's border can't show
// the program's title the way an in-process pane's can. This scanner reconstructs
// the title from the raw bytes — the same approach as oscScanner (OSC 7) /
// osc52Scanner (OSC 52) / osc9Scanner (OSC 9), and the same raw-scan herdr's
// AgentOscStateTracker uses for OSC 0/2. Not safe for concurrent use: a pane drives
// one scanner from its readPump goroutine.
type oscTitleScanner struct {
	state oscState
	buf   []byte
}

// scan consumes a chunk of terminal output and returns the most recent OSC 0/2
// title completed within it (ok=false if none) — the body after the `0;`/`2;`
// command, sanitized. An empty payload (e.g. `ESC ]2; BEL`) returns ("", true): a
// title-clear. State persists across calls so a split sequence is still recognized.
func (s *oscTitleScanner) scan(b []byte) (title string, ok bool) {
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
				if t, found := parseOSCTitle(s.buf); found {
					title, ok = t, true
				}
				s.reset()
			case 0x1b: // possible ST (ESC \)
				s.state = oscCollectEsc
			default:
				if len(s.buf) < oscTitleMaxLen {
					s.buf = append(s.buf, c)
				} else {
					s.reset() // overlong / unterminated; abandon
				}
			}
		case oscCollectEsc:
			if c == '\\' { // ST terminator
				if t, found := parseOSCTitle(s.buf); found {
					title, ok = t, true
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
	return title, ok
}

func (s *oscTitleScanner) reset() {
	s.state = oscNormal
	s.buf = s.buf[:0]
}

// parseOSCTitle extracts the title from an OSC 0 (icon+title) or OSC 2 (title) body
// — everything after the command, sanitized. OSC 1 (icon name only) and other OSC
// commands return ok=false. An empty payload is a title-clear (returns "", true).
func parseOSCTitle(body []byte) (string, bool) {
	cmd, payload, found := bytes.Cut(body, []byte(";"))
	if !found {
		return "", false
	}
	if string(cmd) != "0" && string(cmd) != "2" {
		return "", false
	}
	return sanitizeOSCString(payload, oscProgressMaxChars), true
}
