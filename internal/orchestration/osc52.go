package orchestration

import (
	"bytes"
	"encoding/base64"
)

// osc52MaxPayload caps a single buffered OSC 52 body. 256 KiB of base64 is
// ~192 KiB of text — enough for real source-file copies while still bounding
// memory against stream garbage. Mirrors cats's OSC52_MAX_PAYLOAD_BYTES.
const osc52MaxPayload = 256 * 1024

// osc52Scanner reconstructs OSC 52 clipboard-write sequences from a raw terminal
// output stream, tolerating sequences split across reads. libghostty-vt drops
// clipboard contents, so child clipboard writes never reach the host unless the
// Host forwards them itself (the same reason oscScanner exists for OSC 7). It is
// a distinct scanner from oscScanner because OSC 52 payloads are far larger than
// the 4 KiB OSC 7 cap, mirroring cats's separate Osc52Forwarder / CwdOscTracker.
// Not safe for concurrent use: a pane drives one scanner from its readPump.
type osc52Scanner struct {
	state oscState
	buf   []byte
}

// scan consumes a chunk of terminal output and returns every OSC 52 clipboard
// write completed within it, in order (nil if none). Each entry is the decoded
// clipboard bytes; an empty (non-nil) entry is a clipboard-clear. State persists
// across calls so a sequence split across reads is still recognized.
func (s *osc52Scanner) scan(b []byte) [][]byte {
	var out [][]byte
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
				if data, ok := parseOSC52Clipboard(s.buf); ok {
					out = append(out, data)
				}
				s.reset()
			case 0x1b: // possible ST (ESC \)
				s.state = oscCollectEsc
			default:
				s.buf = append(s.buf, c)
			}
		case oscCollectEsc:
			if c == '\\' { // ST terminator
				if data, ok := parseOSC52Clipboard(s.buf); ok {
					out = append(out, data)
				}
				s.reset()
			} else {
				// ESC followed by a non-terminator: a literal ESC in the payload.
				// Push it back (as cats's forwarder does) and keep collecting; the
				// body will simply fail to base64-decode if it was truly garbage.
				s.buf = append(s.buf, 0x1b, c)
				s.state = oscCollect
			}
		}
		// Bound the buffer every byte (matches cats): an overlong/unterminated
		// body is abandoned and the scanner recovers at the next ESC.
		if len(s.buf) > osc52MaxPayload {
			s.buf = s.buf[:0]
			s.state = oscNormal
		}
	}
	return out
}

func (s *osc52Scanner) reset() {
	s.state = oscNormal
	s.buf = s.buf[:0]
}

// parseOSC52Clipboard extracts the decoded clipboard bytes from an OSC 52 body.
// Accepts `52;c;<base64>` and `52;;<base64>` (the default selection); rejects
// other selections (p/q/s/0-7), queries (`?`, which have no reply path here),
// and payloads that are not valid standard base64. An empty payload (`52;c;`)
// decodes to an empty slice — a clipboard-clear. Mirrors cats's
// parse_osc52_clipboard_write.
func parseOSC52Clipboard(body []byte) ([]byte, bool) {
	rest, ok := bytes.CutPrefix(body, []byte("52;"))
	if !ok {
		return nil, false
	}
	selector, data, ok := bytes.Cut(rest, []byte(";"))
	if !ok {
		return nil, false
	}
	if len(selector) != 0 && string(selector) != "c" {
		return nil, false
	}
	if string(data) == "?" {
		return nil, false // query — no reply path
	}
	decoded, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		return nil, false
	}
	if decoded == nil {
		decoded = []byte{} // empty payload is a clear, not "absent"
	}
	return decoded, true
}
