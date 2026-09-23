package browserproto

import (
	"strconv"
	"unicode/utf8"
)

// Hand-written JSON for the two messages that are most of the bytes catway
// ever writes: pane_frame and pane_diff.
//
// encoding/json walks each value by reflection, per field of per cell: a full
// frame of a 200×50 pane is 10,000 cells × 5 fields, and it took ~0.9 ms on
// the orchestrator loop — the goroutine every command, keystroke and event
// waits behind. The shapes here are fixed and flat, so writing them directly
// is a loop of appends with no reflection and no intermediate allocations.
//
// The output is BYTE-FOR-BYTE what encoding/json produces for the same value
// (field order, omitempty, HTML-safe string escaping, nil slices as null).
// That is a deliberate constraint, not an accident: it means nothing on the
// receiving side can tell, and it makes the test a plain equality against
// json.Marshal over random values — the encoder cannot drift from the struct
// tags without failing it.

// MarshalFrame encodes a translated frame message (what TranslateView
// returns). Anything else goes through Marshal, so it is safe to call on any
// down-message.
func MarshalFrame(m any) ([]byte, error) {
	switch v := m.(type) {
	case *PaneFrame:
		if v.T == "" || v.T == MsgPaneFrame {
			return appendPaneFrame(make([]byte, 0, 64+len(v.Cells)*10), v), nil
		}
	case *PaneDiff:
		if v.T == "" || v.T == MsgPaneDiff {
			return appendPaneDiff(make([]byte, 0, 64+len(v.Cells)*16), v), nil
		}
	}
	// A mismatched T, or not a frame at all: Marshal reports or handles it.
	return Marshal(m)
}

func appendPaneFrame(b []byte, f *PaneFrame) []byte {
	b = append(b, `{"t":`...)
	b = appendJSONString(b, string(MsgPaneFrame))
	b = append(b, `,"pane":`...)
	b = strconv.AppendUint(b, uint64(f.Pane), 10)
	b = append(b, `,"w":`...)
	b = strconv.AppendUint(b, uint64(f.W), 10)
	b = append(b, `,"h":`...)
	b = strconv.AppendUint(b, uint64(f.H), 10)
	b = append(b, `,"cur":`...)
	b = appendCursor(b, &f.Cur)
	b = append(b, `,"def_fg":`...)
	b = strconv.AppendUint(b, uint64(f.DefFg), 10)
	b = append(b, `,"def_bg":`...)
	b = strconv.AppendUint(b, uint64(f.DefBg), 10)
	if len(f.Links) > 0 {
		b = append(b, `,"links":[`...)
		for i, l := range f.Links {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendJSONString(b, l)
		}
		b = append(b, ']')
	}
	b = append(b, `,"cells":`...)
	if f.Cells == nil {
		b = append(b, "null"...)
	} else {
		b = append(b, '[')
		for i := range f.Cells {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, '{')
			b = appendCellFields(b, &f.Cells[i])
			b = append(b, '}')
		}
		b = append(b, ']')
	}
	if f.Scroll != nil {
		b = append(b, `,"scroll":`...)
		b = appendScroll(b, f.Scroll)
	}
	return append(b, '}')
}

func appendPaneDiff(b []byte, d *PaneDiff) []byte {
	b = append(b, `{"t":`...)
	b = appendJSONString(b, string(MsgPaneDiff))
	b = append(b, `,"pane":`...)
	b = strconv.AppendUint(b, uint64(d.Pane), 10)
	if d.Cur != nil {
		b = append(b, `,"cur":`...)
		b = appendCursor(b, d.Cur)
	}
	if d.Shift != 0 {
		b = append(b, `,"shift":`...)
		b = strconv.AppendInt(b, int64(d.Shift), 10)
	}
	b = append(b, `,"cells":`...)
	if d.Cells == nil {
		b = append(b, "null"...)
	} else {
		b = append(b, '[')
		for i := range d.Cells {
			if i > 0 {
				b = append(b, ',')
			}
			// DiffCell embeds Cell, so its fields flatten after "i".
			b = append(b, `{"i":`...)
			b = strconv.AppendInt(b, int64(d.Cells[i].I), 10)
			b = append(b, ',')
			b = appendCellFields(b, &d.Cells[i].Cell)
			b = append(b, '}')
		}
		b = append(b, ']')
	}
	if d.Scroll != nil {
		b = append(b, `,"scroll":`...)
		b = appendScroll(b, d.Scroll)
	}
	return append(b, '}')
}

// appendCellFields writes a Cell's fields without the braces, so DiffCell can
// put its index in front of them.
func appendCellFields(b []byte, c *Cell) []byte {
	b = append(b, `"s":`...)
	b = appendJSONString(b, c.S)
	if c.F != 0 {
		b = append(b, `,"f":`...)
		b = strconv.AppendUint(b, uint64(c.F), 10)
	}
	if c.B != 0 {
		b = append(b, `,"b":`...)
		b = strconv.AppendUint(b, uint64(c.B), 10)
	}
	if c.M != 0 {
		b = append(b, `,"m":`...)
		b = strconv.AppendUint(b, uint64(c.M), 10)
	}
	if c.H != 0 {
		b = append(b, `,"h":`...)
		b = strconv.AppendUint(b, uint64(c.H), 10)
	}
	return b
}

func appendCursor(b []byte, c *Cursor) []byte {
	b = append(b, `{"x":`...)
	b = strconv.AppendUint(b, uint64(c.X), 10)
	b = append(b, `,"y":`...)
	b = strconv.AppendUint(b, uint64(c.Y), 10)
	b = append(b, `,"vis":`...)
	b = strconv.AppendBool(b, c.Vis)
	b = append(b, `,"shape":`...)
	b = strconv.AppendUint(b, uint64(c.Shape), 10)
	return append(b, '}')
}

func appendScroll(b []byte, s *Scroll) []byte {
	b = append(b, `{"off":`...)
	b = strconv.AppendInt(b, int64(s.Off), 10)
	b = append(b, `,"max":`...)
	b = strconv.AppendInt(b, int64(s.Max), 10)
	b = append(b, `,"rows":`...)
	b = strconv.AppendInt(b, int64(s.Rows), 10)
	return append(b, '}')
}

// appendJSONString is encoding/json's string encoding with its default
// HTML-safe escaping, rule for rule: `"` and `\` backslashed; \b \f \n \r \t
// by name; every other control byte and < > & as \u00XX; invalid UTF-8 as
// \ufffd; U+2028/U+2029 as \u2028/\u2029. The common cell — one printable
// ASCII byte — takes the first branch and nothing else.
func appendJSONString(b []byte, s string) []byte {
	const hex = "0123456789abcdef"
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); {
		if c := s[i]; c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' && c != '<' && c != '>' && c != '&' {
				i++
				continue
			}
			b = append(b, s[start:i]...)
			switch c {
			case '\\', '"':
				b = append(b, '\\', c)
			case '\b':
				b = append(b, '\\', 'b')
			case '\f':
				b = append(b, '\\', 'f')
			case '\n':
				b = append(b, '\\', 'n')
			case '\r':
				b = append(b, '\\', 'r')
			case '\t':
				b = append(b, '\\', 't')
			default:
				b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b = append(b, s[start:i]...)
			b = append(b, `\ufffd`...)
			i += size
			start = i
			continue
		}
		if r == '\u2028' || r == '\u2029' {
			b = append(b, s[start:i]...)
			b = append(b, '\\', 'u', '2', '0', '2', hex[r&0xF])
			i += size
			start = i
			continue
		}
		i += size
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}
