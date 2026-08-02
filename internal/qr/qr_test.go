package qr

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"
)

// The block table is the one place transcription error is invisible: a wrong
// number produces a symbol that encodes fine and decodes to nothing. Every row
// has to account for exactly the version's codeword budget.
func TestVersionSpecsConsistent(t *testing.T) {
	for v := 1; v <= MaxVersion; v++ {
		s := specs[v]
		blocks := s.g1Blocks + s.g2Blocks
		got := s.dataCodewords() + blocks*s.ecPerBlk
		if got != s.total {
			t.Errorf("v%d: %d data + %d×%d EC = %d codewords, table says %d",
				v, s.dataCodewords(), blocks, s.ecPerBlk, got, s.total)
		}
		// Group 2 blocks, when present, are exactly one codeword longer.
		if s.g2Blocks > 0 && s.g2Data != s.g1Data+1 {
			t.Errorf("v%d: group2 data %d, want group1+1 = %d", v, s.g2Data, s.g1Data+1)
		}
		if s.g2Blocks == 0 && s.g2Data != 0 {
			t.Errorf("v%d: group2 has %d data codewords but no blocks", v, s.g2Data)
		}
	}
}

// α generates the multiplicative group, so exp and log must be exact inverses
// over every non-zero element. If they are not, every Reed-Solomon codeword is
// silently wrong.
func TestGaloisFieldTables(t *testing.T) {
	for v := 1; v < 256; v++ {
		if got := gfExp[gfLog[v]]; got != byte(v) {
			t.Fatalf("gfExp[gfLog[%d]] = %d", v, got)
		}
	}
	for v := 1; v < 256; v++ {
		if got := gfMul(byte(v), 1); got != byte(v) {
			t.Fatalf("gfMul(%d, 1) = %d, want %d", v, got, v)
		}
	}
	if gfMul(0, 42) != 0 || gfMul(42, 0) != 0 {
		t.Fatal("multiplication by zero is not zero")
	}
	// Distributivity over a handful of triples — the property the table-based
	// multiply would break if the reduction polynomial were wrong.
	for _, tc := range [][3]byte{{2, 3, 5}, {0x53, 0xCA, 0x11}, {0xFF, 0xFF, 0xFF}} {
		a, b, cc := tc[0], tc[1], tc[2]
		if gfMul(a, b^cc) != gfMul(a, b)^gfMul(a, cc) {
			t.Fatalf("distributivity fails at %v", tc)
		}
	}
}

// A Reed-Solomon codeword is by definition a multiple of the generator
// polynomial. Dividing the full data||EC sequence by that generator has to leave
// nothing behind — an independent check of the encoder that does not re-run it.
func TestReedSolomonDivisibility(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for _, ecLen := range []int{10, 16, 18, 22, 24, 26} {
		data := make([]byte, 40)
		for i := range data {
			data[i] = byte(rng.Intn(256))
		}
		full := append(append([]byte{}, data...), rsRemainder(data, ecLen)...)
		rem := rsRemainder(full, ecLen)
		for i, v := range rem {
			if v != 0 {
				t.Fatalf("ec=%d: remainder[%d] = %d, want a clean division", ecLen, i, v)
			}
		}
	}
}

// The generator polynomial's roots are α^0..α^(n-1) by construction; evaluating
// it at each of them must give zero. This checks rsGenerator without assuming
// any of the spec's tabulated coefficients.
func TestGeneratorRoots(t *testing.T) {
	for _, n := range []int{10, 16, 26} {
		gen := rsGenerator(n) // descending powers, leading 1 implied
		coeffs := append([]byte{1}, gen...)
		for i := range n {
			root := gfExp[i]
			// Horner's method over GF(256).
			acc := byte(0)
			for _, c := range coeffs {
				acc = gfMul(acc, root) ^ c
			}
			if acc != 0 {
				t.Fatalf("n=%d: α^%d is not a root (evaluates to %d)", n, i, acc)
			}
		}
	}
}

// Format information is BCH(15,5)-protected: the 15-bit word, once unmasked,
// must divide cleanly by the code's generator, and its top five bits must be the
// EC level and mask we asked for. Getting this wrong makes a symbol that looks
// perfect and decodes to nothing.
func TestFormatBitsRecoverable(t *testing.T) {
	for mask := range 8 {
		c := &Code{Version: 1, Size: 21, modules: make([]bool, 21*21)}
		c.drawFormatBits(mask, c.set)

		// Read the first copy back out of the matrix, exactly reversing the
		// placement in drawFormatBits.
		bits := 0
		read := func(i int, x, y int) {
			if c.Dark(x, y) {
				bits |= 1 << uint(i)
			}
		}
		for i := range 6 {
			read(i, 8, i)
		}
		read(6, 8, 7)
		read(7, 8, 8)
		read(8, 7, 8)
		for i := 9; i < 15; i++ {
			read(i, 14-i, 8)
		}

		unmasked := bits ^ 0x5412
		rem := unmasked
		for i := 14; i >= 10; i-- {
			if rem>>uint(i)&1 == 1 {
				rem ^= 0x537 << uint(i-10)
			}
		}
		if rem != 0 {
			t.Errorf("mask %d: BCH remainder %d, want 0", mask, rem)
		}
		if got := unmasked >> 10; got != mask {
			t.Errorf("mask %d: format data bits = %05b, want %05b", mask, got, mask)
		}
	}
}

// Version information (v7+) carries the same risk, and a wrong value sends a
// decoder to the wrong block table.
func TestVersionBitsRecoverable(t *testing.T) {
	for v := 7; v <= MaxVersion; v++ {
		size := 17 + 4*v
		c := &Code{Version: v, Size: size, modules: make([]bool, size*size)}
		c.drawVersionBits(c.set)

		bits := 0
		for i := range 18 {
			if c.Dark(size-11+i%3, i/3) {
				bits |= 1 << uint(i)
			}
		}
		rem := bits
		for i := 17; i >= 12; i-- {
			if rem>>uint(i)&1 == 1 {
				rem ^= 0x1F25 << uint(i-12)
			}
		}
		if rem != 0 {
			t.Errorf("v%d: BCH remainder %d, want 0", v, rem)
		}
		if got := bits >> 12; got != v {
			t.Errorf("v%d: version bits say %d", v, got)
		}
		// The second copy is the transpose of the first.
		for i := range 18 {
			a, b := size-11+i%3, i/3
			if c.Dark(a, b) != c.Dark(b, a) {
				t.Fatalf("v%d: version block copies disagree at bit %d", v, i)
			}
		}
	}
}

// The patterns a scanner locates the symbol by, checked directly: three finders
// with their light ring and separator, alternating timing lines, and the dark
// module that is set unconditionally.
func TestStructuralPatterns(t *testing.T) {
	for _, tc := range []struct{ text string }{{"x"}, {strings.Repeat("A", 120)}} {
		c, err := Encode(tc.text)
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		if c.Size != 17+4*c.Version {
			t.Fatalf("v%d: size %d", c.Version, c.Size)
		}
		for _, p := range [][2]int{{3, 3}, {c.Size - 4, 3}, {3, c.Size - 4}} {
			for dy := -3; dy <= 3; dy++ {
				for dx := -3; dx <= 3; dx++ {
					want := max(abs(dx), abs(dy)) != 2
					if got := c.Dark(p[0]+dx, p[1]+dy); got != want {
						t.Fatalf("v%d finder at %v: module (%d,%d) = %v, want %v",
							c.Version, p, dx, dy, got, want)
					}
				}
			}
		}
		// Timing patterns run between the finders and must alternate, starting
		// dark at the even coordinate.
		for i := 8; i < c.Size-8; i++ {
			if c.Dark(6, i) != (i%2 == 0) || c.Dark(i, 6) != (i%2 == 0) {
				t.Fatalf("v%d: timing pattern breaks at %d", c.Version, i)
			}
		}
		if !c.Dark(8, c.Size-8) {
			t.Fatalf("v%d: the always-dark module is light", c.Version)
		}
	}
}

// The end-to-end claim: everything Encode produces reads back as the text that
// went in. decodeSymbol below re-derives the mask from the symbol's own format
// bits, walks the zigzag independently, and reverses the block interleave — so a
// fault in placement, masking or interleaving surfaces here as a wrong string
// rather than as an unscannable code nobody notices until they hold a phone.
func TestEncodeRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	payloads := []string{
		"x",
		"cats",
		"cats://pair?u=https%3A%2F%2F192.168.1.24%3A8421&t=Xk3dQm9pR2sV7wYbN4tL8cJz",
		"cats://pair?u=https%3A%2F%2Fstudio.local%3A8421&t=Xk3dQm9pR2sV7wYbN4tL8cJz" +
			"&f=9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
	}
	// One payload at each version boundary, so every block layout in the table
	// (including the two-group versions 8–10) is exercised.
	for v := 1; v <= MaxVersion; v++ {
		b := make([]byte, byteCapacity(v))
		for i := range b {
			b[i] = byte('!' + rng.Intn(90)) // printable ASCII keeps failures readable
		}
		payloads = append(payloads, string(b))
	}

	for _, want := range payloads {
		c, err := Encode(want)
		if err != nil {
			t.Fatalf("Encode(%d bytes): %v", len(want), err)
		}
		got := decodeSymbol(t, c)
		if got != want {
			t.Fatalf("v%d: round trip lost the payload\n got %q\nwant %q",
				c.Version, truncate(got), truncate(want))
		}
	}
}

// Version selection has to be tight: the smallest version that fits, and one
// byte more than a version holds must move up to the next.
func TestVersionSelection(t *testing.T) {
	for v := 1; v <= MaxVersion; v++ {
		c, err := Encode(strings.Repeat("a", byteCapacity(v)))
		if err != nil {
			t.Fatalf("v%d at capacity: %v", v, err)
		}
		if c.Version != v {
			t.Fatalf("%d bytes chose v%d, want v%d", byteCapacity(v), c.Version, v)
		}
	}
	if _, err := Encode(strings.Repeat("a", byteCapacity(MaxVersion)+1)); !errors.Is(err, ErrTooLong) {
		t.Fatalf("one byte over capacity: err = %v, want ErrTooLong", err)
	}
}

// A caller that overruns falls back to printing the URI, so the error has to be
// an error and not a panic or a silently truncated symbol.
func TestEncodeTooLongIsRecoverable(t *testing.T) {
	c, err := Encode(strings.Repeat("z", 4096))
	if c != nil {
		t.Fatal("a too-long payload returned a symbol")
	}
	if !errors.Is(err, ErrTooLong) {
		t.Fatalf("err = %v, want ErrTooLong", err)
	}
}

// The rendered block has to be sized for a terminal: one line per two module
// rows, one cell per module column, quiet zone included on all four sides.
func TestTerminalDimensions(t *testing.T) {
	c, err := Encode("cats://pair?t=abc")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	span := c.Size + 2*quietZone

	for _, opt := range []TerminalOptions{{}, {Color: true}, {Indent: "  "}} {
		out := c.Terminal(opt)
		lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
		if want := (span + 1) / 2; len(lines) != want {
			t.Fatalf("%+v: %d lines, want %d", opt, len(lines), want)
		}
		for i, ln := range lines {
			if !strings.HasPrefix(ln, opt.Indent) {
				t.Fatalf("%+v: line %d is not indented", opt, i)
			}
			body := strings.TrimPrefix(ln, opt.Indent)
			if opt.Color {
				// Colour escapes are zero-width; count only the glyphs.
				if n := strings.Count(body, upperHalf); n != span {
					t.Fatalf("%+v: line %d has %d cells, want %d", opt, i, n, span)
				}
				if !strings.HasSuffix(body, ansiReset) {
					t.Fatalf("%+v: line %d does not reset colours", opt, i)
				}
				continue
			}
			if n := utf8.RuneCountInString(body); n != span {
				t.Fatalf("%+v: line %d has %d cells, want %d", opt, i, n, span)
			}
		}
	}
}

// The quiet zone must render as light modules on every side, or scanners will
// not acquire the symbol.
func TestTerminalQuietZone(t *testing.T) {
	c, err := Encode("q")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(c.Terminal(TerminalOptions{}), "\n"), "\n")
	// The first quietZone/2 lines are entirely inside the top margin.
	for i := range quietZone / 2 {
		if strings.Trim(lines[i], fullBlock) != "" {
			t.Fatalf("top quiet-zone line %d is not all light: %q", i, lines[i])
		}
	}
	for i, ln := range lines {
		runes := []rune(ln)
		for j := range quietZone {
			if runes[j] != '█' || runes[len(runes)-1-j] != '█' {
				t.Fatalf("line %d: side quiet zone is not light", i)
			}
		}
	}
}

// --- test-only decoder -------------------------------------------------------

// decodeSymbol reads a Code back to its payload. It is deliberately a separate
// implementation of the read path rather than a call into the encoder: it takes
// the mask from the symbol's own format bits, walks the zigzag itself, undoes
// the block interleave itself, and parses the segment header itself. Only the
// function-pattern map is shared, and a fault there shows up in
// TestStructuralPatterns instead.
//
// It performs no error *correction* — a symbol read off its own matrix should
// need none — but it does verify the error-correction codewords, which is what
// makes the left half of the matrix count. See the comment at that check.
func decodeSymbol(t *testing.T, c *Code) string {
	t.Helper()

	// Recover the mask from format copy 1 (bits 12..14 of the unmasked word hold
	// the EC level, bits 10..12 the mask; see drawFormatBits).
	bits := 0
	place := [][3]int{{0, 8, 0}, {1, 8, 1}, {2, 8, 2}, {3, 8, 3}, {4, 8, 4}, {5, 8, 5},
		{6, 8, 7}, {7, 8, 8}, {8, 7, 8}}
	for _, p := range place {
		if c.Dark(p[1], p[2]) {
			bits |= 1 << uint(p[0])
		}
	}
	for i := 9; i < 15; i++ {
		if c.Dark(14-i, 8) {
			bits |= 1 << uint(i)
		}
	}
	unmasked := bits ^ 0x5412
	mask := unmasked >> 10 & 0b111
	if lvl := unmasked >> 13; lvl != 0b00 {
		t.Fatalf("format bits report EC level %02b, want M (00)", lvl)
	}

	// Rebuild the function-module map for this version and unmask a copy.
	scratch := &Code{Version: c.Version, Size: c.Size, modules: make([]bool, c.Size*c.Size)}
	isFunc := make([]bool, c.Size*c.Size)
	scratch.drawFunctionPatterns(isFunc)

	plain := make([]bool, len(c.modules))
	copy(plain, c.modules)
	for y := range c.Size {
		for x := range c.Size {
			if !isFunc[y*c.Size+x] && maskBit(mask, x, y) {
				plain[y*c.Size+x] = !plain[y*c.Size+x]
			}
		}
	}

	// Walk the zigzag, most-significant bit first, collecting codewords.
	s := specs[c.Version]
	stream := make([]byte, 0, s.total)
	cur, nbits := byte(0), 0
	for right := c.Size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := range c.Size {
			for j := range 2 {
				x := right - j
				y := vert
				if (right+1)&2 == 0 {
					y = c.Size - 1 - vert
				}
				if isFunc[y*c.Size+x] || len(stream) == s.total {
					continue
				}
				cur <<= 1
				if plain[y*c.Size+x] {
					cur |= 1
				}
				if nbits++; nbits == 8 {
					stream = append(stream, cur)
					cur, nbits = 0, 0
				}
			}
		}
	}
	if len(stream) != s.total {
		t.Fatalf("v%d: read %d codewords, want %d", c.Version, len(stream), s.total)
	}

	// Reverse the interleave: hand each codeword back to the block it came from.
	blockLens := make([]int, 0, s.g1Blocks+s.g2Blocks)
	for range s.g1Blocks {
		blockLens = append(blockLens, s.g1Data)
	}
	for range s.g2Blocks {
		blockLens = append(blockLens, s.g2Data)
	}
	blocks := make([][]byte, len(blockLens))
	pos := 0
	for i := range max(s.g1Data, s.g2Data) {
		for b, n := range blockLens {
			if i < n {
				blocks[b] = append(blocks[b], stream[pos])
				pos++
			}
		}
	}
	var data []byte
	for _, b := range blocks {
		data = append(data, b...)
	}

	// Check the error-correction codewords too, which is not optional rigour.
	// The data codewords occupy the *start* of the stream and so the right-hand
	// side of the matrix; the EC codewords fill everything to the left of them.
	// Reading only the payload back therefore leaves roughly half the modules —
	// including every module in the left-most columns — unverified, and a fault
	// confined to that half round-trips perfectly while producing a symbol no
	// real scanner can read.
	//
	// A Reed-Solomon codeword is a multiple of the generator polynomial, so
	// dividing each reconstructed block by it catches any misplacement without
	// needing to know what the right answer was.
	ecBlocks := make([][]byte, len(blockLens))
	for range s.ecPerBlk {
		for b := range blockLens {
			ecBlocks[b] = append(ecBlocks[b], stream[pos])
			pos++
		}
	}
	if pos != len(stream) {
		t.Fatalf("v%d: de-interleave consumed %d of %d codewords", c.Version, pos, len(stream))
	}
	for b := range blocks {
		full := append(append([]byte{}, blocks[b]...), ecBlocks[b]...)
		for i, v := range rsRemainder(full, s.ecPerBlk) {
			if v != 0 {
				t.Fatalf("v%d block %d: error correction does not check out at %d (%d)",
					c.Version, b, i, v)
			}
		}
	}

	// Parse the byte-mode segment.
	rd := &bitReader{data: data}
	if mode := rd.read(4); mode != 0b0100 {
		t.Fatalf("v%d: mode indicator %04b, want byte mode", c.Version, mode)
	}
	n := rd.read(countBits(c.Version))
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(rd.read(8))
	}
	return string(out)
}

type bitReader struct {
	data []byte
	pos  int
}

func (r *bitReader) read(n int) int {
	v := 0
	for range n {
		v <<= 1
		if r.pos < len(r.data)*8 && r.data[r.pos/8]>>uint(7-r.pos%8)&1 == 1 {
			v |= 1
		}
		r.pos++
	}
	return v
}

func truncate(s string) string {
	if len(s) <= 60 {
		return s
	}
	return fmt.Sprintf("%s… (%d bytes)", s[:60], len(s))
}
