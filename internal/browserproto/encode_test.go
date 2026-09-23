package browserproto

import (
	"bytes"
	"encoding/json"
	"math/rand"
	"testing"

	"github.com/rohanthewiz/cats/internal/orchestration"
)

// Symbols chosen to hit every branch of the string encoder: plain ASCII, the
// HTML-escaped trio, quote and backslash, named and unnamed control bytes,
// multi-byte runes, U+2028/2029, invalid UTF-8, and the empty string.
var encodeSymbols = []string{"a", " ", "Z", "<", ">", "&", `"`, `\`, "\n", "\t", "\b", "\f", "\r",
	"\x00", "\x1f", "\x7f", "é", "界", "🍏", "e\u0301", "\u2028", "\u2029", "\xff", "a\xc3", "", "─"}

func randWireCell(rng *rand.Rand) Cell {
	c := Cell{S: encodeSymbols[rng.Intn(len(encodeSymbols))]}
	if rng.Intn(2) == 0 {
		c.F = rng.Uint32()
	}
	if rng.Intn(3) == 0 {
		c.B = rng.Uint32()
	}
	if rng.Intn(4) == 0 {
		c.M = uint16(rng.Intn(1 << 16))
	}
	if rng.Intn(6) == 0 {
		c.H = uint32(rng.Intn(5))
	}
	return c
}

// The appender's contract is exact equality with encoding/json, over random
// values that exercise every optional field in both states.
func TestMarshalFrameMatchesEncodingJSON(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := range 2000 {
		var m any
		if i%2 == 0 {
			f := &PaneFrame{T: MsgPaneFrame, Pane: rng.Uint32(), W: uint16(rng.Intn(300)), H: uint16(rng.Intn(80)),
				Cur:   Cursor{X: uint16(rng.Intn(300)), Y: uint16(rng.Intn(80)), Vis: rng.Intn(2) == 0, Shape: uint8(rng.Intn(7))},
				DefFg: rng.Uint32(), DefBg: rng.Uint32()}
			if rng.Intn(3) == 0 {
				for range rng.Intn(3) + 1 {
					f.Links = append(f.Links, "https://example.com/?q="+encodeSymbols[rng.Intn(len(encodeSymbols))])
				}
			}
			switch rng.Intn(5) {
			case 0: // nil cells: null, like encoding/json
			case 1:
				f.Cells = []Cell{}
			default:
				for range rng.Intn(40) + 1 {
					f.Cells = append(f.Cells, randWireCell(rng))
				}
			}
			if rng.Intn(2) == 0 {
				f.Scroll = &Scroll{Off: rng.Intn(1000) - 10, Max: rng.Intn(1000), Rows: rng.Intn(100)}
			}
			if rng.Intn(4) == 0 {
				f.T = "" // Marshal stamps it; so must the appender
			}
			m = f
		} else {
			d := &PaneDiff{T: MsgPaneDiff, Pane: rng.Uint32()}
			if rng.Intn(2) == 0 {
				d.Cur = &Cursor{X: uint16(rng.Intn(300)), Y: uint16(rng.Intn(80)), Vis: rng.Intn(2) == 0}
			}
			if rng.Intn(3) == 0 {
				d.Shift = rng.Intn(50) + 1
			}
			switch rng.Intn(5) {
			case 0:
			case 1:
				d.Cells = []DiffCell{}
			default:
				for range rng.Intn(40) + 1 {
					d.Cells = append(d.Cells, DiffCell{I: rng.Intn(20000), Cell: randWireCell(rng)})
				}
			}
			if rng.Intn(2) == 0 {
				d.Scroll = &Scroll{Off: rng.Intn(10), Max: rng.Intn(1000), Rows: rng.Intn(100)}
			}
			if rng.Intn(4) == 0 {
				d.T = ""
			}
			m = d
		}
		want, err := Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		got, err := MarshalFrame(m)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("case %d (%T):\n got %s\nwant %s", i, m, got, want)
		}
	}
}

// Anything that is not a frame, or a frame with the wrong discriminator,
// takes Marshal's path and gets Marshal's answer.
func TestMarshalFrameFallsBack(t *testing.T) {
	got, err := MarshalFrame(NewTitle("x"))
	want, _ := Marshal(NewTitle("x"))
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("title: %s, %v", got, err)
	}
	if _, err := MarshalFrame(&PaneFrame{T: MsgPaneDiff}); err == nil {
		t.Fatal("a pane_frame typed as pane_diff was encoded")
	}
}

// One encoding per translator state, and every translator ends where its own
// TranslateView would have left it, with the bytes it would have produced.
func TestViewEncoderSharesByState(t *testing.T) {
	cells := func(syms string) []orchestration.Cell {
		var out []orchestration.Cell
		for _, s := range syms {
			out = append(out, orchestration.Cell{Symbol: string(s), Fg: 0x02c8c8c8, Bg: 0x02000000})
		}
		return out
	}
	full := &orchestration.Frame{Cols: 4, Rows: 1, Full: true, Cells: cells("abcd"),
		Cursor: &orchestration.Cursor{Visible: true}}
	diff := &orchestration.Frame{Cols: 4, Rows: 1, Cells: cells("abXd"),
		Cursor: &orchestration.Cursor{X: 3, Visible: true}}
	for i := range diff.Cells {
		diff.Cells[i].Skip = i != 2
	}

	// Three connections streaming together, and one that just gained the pane.
	mk := func() *FrameTranslator { t := NewFrameTranslator(5); t.Translate(full); return t }
	a, b, c := mk(), mk(), mk()
	fresh := NewFrameTranslator(5)
	// And the same four, translated directly, as the reference.
	ra, rb, rc := mk(), mk(), mk()
	rfresh := NewFrameTranslator(5)

	v := DenseView(diff)
	enc := NewViewEncoder(&v)
	for i, pair := range [][2]*FrameTranslator{{a, ra}, {b, rb}, {fresh, rfresh}, {c, rc}} {
		got, err := enc.Encode(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		want, _ := json.Marshal(pair[1].TranslateView(&v))
		if !bytes.Equal(got, want) {
			t.Fatalf("connection %d:\n got %s\nwant %s", i, got, want)
		}
		if pair[0].translatorState != pair[1].translatorState {
			t.Fatalf("connection %d state = %+v, want %+v", i, pair[0].translatorState, pair[1].translatorState)
		}
	}
	if len(enc.entries) != 2 {
		t.Fatalf("%d encodings for two distinct states", len(enc.entries))
	}
}

func benchFrame() *PaneFrame {
	rng := rand.New(rand.NewSource(3))
	f := &PaneFrame{T: MsgPaneFrame, Pane: 1, W: 200, H: 50, DefFg: 0x02c8c8c8, DefBg: 0x02101010}
	for range 200 * 50 {
		c := Cell{S: string(rune('a' + rng.Intn(26)))}
		if rng.Intn(5) == 0 {
			c.F = 0x02ff8800
		}
		f.Cells = append(f.Cells, c)
	}
	return f
}

func BenchmarkMarshalFullFrameReflect(b *testing.B) {
	f := benchFrame()
	for b.Loop() {
		_, _ = Marshal(f)
	}
}

func BenchmarkMarshalFullFrameAppend(b *testing.B) {
	f := benchFrame()
	for b.Loop() {
		_, _ = MarshalFrame(f)
	}
}
