package orchestration

import (
	"net/url"
	"strings"
)

// oscMaxLen caps a single buffered OSC string so a malformed/unterminated
// sequence cannot grow the scanner buffer without bound.
const oscMaxLen = 4096

// oscScanner extracts OSC 7 working-directory reports from a raw terminal output
// stream, tolerating sequences split across reads. libghostty-vt does not surface
// OSC 7 to Go, so the Host scans the raw PTY bytes itself (as the Rust in-process
// path does). It is not safe for concurrent use: a pane drives one scanner from
// its readPump goroutine.
type oscScanner struct {
	state oscState
	buf   []byte
}

type oscState uint8

const (
	oscNormal     oscState = iota // scanning for ESC
	oscSawEsc                     // last byte was ESC (0x1b)
	oscCollect                    // inside an OSC string, collecting until terminator
	oscCollectEsc                 // inside an OSC string, last byte was ESC (maybe ST = ESC \)
)

// scan consumes a chunk of terminal output and returns the most recent OSC 7
// working directory completed within it (ok=false if none). Returned paths are
// absolute and percent-decoded. State persists across calls so a sequence split
// across reads is still recognized.
func (s *oscScanner) scan(b []byte) (cwd string, ok bool) {
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
			case 0x1b: // ESC ESC => stay, this could still introduce an OSC
			default:
				s.state = oscNormal
			}
		case oscCollect:
			switch c {
			case 0x07: // BEL terminator
				if p, found := parseOSC7Cwd(s.buf); found {
					cwd, ok = p, true
				}
				s.reset()
			case 0x1b: // possible ST (ESC \)
				s.state = oscCollectEsc
			default:
				if len(s.buf) < oscMaxLen {
					s.buf = append(s.buf, c)
				} else {
					s.reset() // overlong / unterminated; abandon
				}
			}
		case oscCollectEsc:
			if c == '\\' { // ST terminator
				if p, found := parseOSC7Cwd(s.buf); found {
					cwd, ok = p, true
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
	return cwd, ok
}

func (s *oscScanner) reset() {
	s.state = oscNormal
	s.buf = s.buf[:0]
}

// parseOSC7Cwd extracts an absolute path from an OSC 7 body, accepting both the
// common "7;file://host/path" form and a bare "7;/abs/path". Returns ok=false for
// other OSC commands or malformed input.
func parseOSC7Cwd(body []byte) (string, bool) {
	rest, ok := strings.CutPrefix(string(body), "7;")
	if !ok {
		return "", false
	}
	if after, ok := strings.CutPrefix(rest, "file://"); ok {
		// after = host/path ; the path begins at the first '/' (host may be empty).
		slash := strings.IndexByte(after, '/')
		if slash < 0 {
			return "", false
		}
		path := after[slash:]
		if decoded, err := url.PathUnescape(path); err == nil {
			path = decoded
		}
		if strings.HasPrefix(path, "/") {
			return path, true
		}
		return "", false
	}
	if strings.HasPrefix(rest, "/") {
		return rest, true
	}
	return "", false
}
