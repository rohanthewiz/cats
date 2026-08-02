// Package qr encodes a short byte string as a QR code so `catctl pair` can put
// a pairing grant on screen for a phone camera. It implements just enough of
// ISO/IEC 18004 to do that job: byte mode, error-correction level M, versions
// 1 through 10 (up to 213 bytes).
//
// It exists rather than a dependency because cats holds itself to a small
// direct-dependency budget, and the alternative — printing a 120-character URI
// for the operator to retype on a phone keyboard — defeats the convenience the
// pairing flow is chasing.
//
// What is deliberately absent: numeric, alphanumeric and kanji modes (a pairing
// URI is neither digits nor uppercase-only, so they would never be selected),
// EC levels L/Q/H (M is the general-purpose choice, and a terminal render has no
// print noise to defend against), versions above 10, structured append, and any
// form of decoding. The encoder is a one-way function feeding one call site.
//
//	Encode("…") ──▶ segment bits ──▶ data codewords ──▶ + Reed-Solomon
//	                                                       │
//	         module matrix ◀── interleave blocks ◀──────────┘
//	              │
//	              ├─ function patterns (finder, timing, alignment, format)
//	              └─ 8 candidate masks, lowest penalty wins ──▶ Code
package qr

import (
	"errors"
	"fmt"
)

// MaxVersion is the largest symbol this package produces. Version 10 is 57×57
// modules and holds 213 bytes at level M — comfortably more than a pairing URI
// needs, while keeping the on-screen code small enough to scan from a terminal.
const MaxVersion = 10

// ErrTooLong reports a payload that will not fit in MaxVersion. Callers are
// expected to fall back to printing the text itself rather than failing.
var ErrTooLong = errors.New("qr: payload too long for version 10 at EC level M")

// Code is a rendered QR symbol: a square grid of modules, true meaning dark.
// Version and Size are derived from the payload length and are useful to a
// caller deciding how much terminal space the render will take.
type Code struct {
	Version int
	Size    int // modules per side: 17 + 4*Version
	modules []bool
}

// Dark reports whether the module at (x, y) is dark — x across, y down, both
// zero-based from the top-left corner. Out-of-range coordinates read as light,
// which makes the quiet zone fall out of the same accessor.
func (c *Code) Dark(x, y int) bool {
	if x < 0 || y < 0 || x >= c.Size || y >= c.Size {
		return false
	}
	return c.modules[y*c.Size+x]
}

func (c *Code) set(x, y int, dark bool) { c.modules[y*c.Size+x] = dark }

// versionSpec is one row of the ISO block table for EC level M. Data codewords
// are split into two groups whose blocks differ in length by exactly one, each
// getting its own ecPerBlock error-correction codewords.
type versionSpec struct {
	total    int // data + EC codewords in the whole symbol
	ecPerBlk int
	g1Blocks int
	g1Data   int
	g2Blocks int
	g2Data   int
}

// specs indexes by version (1-based; specs[0] is unused padding). Every row is
// self-checking: g1Blocks*g1Data + g2Blocks*g2Data + (g1Blocks+g2Blocks)*ecPerBlk
// must equal total, which TestVersionSpecsConsistent asserts — transcription is
// the only way these numbers go wrong.
var specs = [MaxVersion + 1]versionSpec{
	{},
	{total: 26, ecPerBlk: 10, g1Blocks: 1, g1Data: 16},
	{total: 44, ecPerBlk: 16, g1Blocks: 1, g1Data: 28},
	{total: 70, ecPerBlk: 26, g1Blocks: 1, g1Data: 44},
	{total: 100, ecPerBlk: 18, g1Blocks: 2, g1Data: 32},
	{total: 134, ecPerBlk: 24, g1Blocks: 2, g1Data: 43},
	{total: 172, ecPerBlk: 16, g1Blocks: 4, g1Data: 27},
	{total: 196, ecPerBlk: 18, g1Blocks: 4, g1Data: 31},
	{total: 242, ecPerBlk: 22, g1Blocks: 2, g1Data: 38, g2Blocks: 2, g2Data: 39},
	{total: 292, ecPerBlk: 22, g1Blocks: 3, g1Data: 36, g2Blocks: 2, g2Data: 37},
	{total: 346, ecPerBlk: 26, g1Blocks: 4, g1Data: 43, g2Blocks: 1, g2Data: 44},
}

// alignCenters lists the row/column coordinates of alignment-pattern centres per
// version. Patterns sit at every combination of two coordinates except the three
// that would land on a finder pattern.
var alignCenters = [MaxVersion + 1][]int{
	nil,
	nil,
	{6, 18}, {6, 22}, {6, 26}, {6, 30}, {6, 34},
	{6, 22, 38}, {6, 24, 42}, {6, 26, 46}, {6, 28, 50},
}

// dataCodewords is the payload capacity of a version, in codewords.
func (s versionSpec) dataCodewords() int {
	return s.g1Blocks*s.g1Data + s.g2Blocks*s.g2Data
}

// byteCapacity is how many payload bytes a version holds in byte mode: the data
// codewords less the 4-bit mode indicator and the character-count field, which
// widens from 8 to 16 bits at version 10.
func byteCapacity(version int) int {
	return (specs[version].dataCodewords()*8 - 4 - countBits(version)) / 8
}

// countBits is the width of the byte-mode character-count field. The step at
// version 10 is a spec boundary, not a rounding: a version-9 symbol read with a
// 16-bit count decodes to garbage.
func countBits(version int) int {
	if version >= 10 {
		return 16
	}
	return 8
}

// Encode renders text as the smallest QR symbol that holds it, at EC level M.
func Encode(text string) (*Code, error) {
	data := []byte(text)
	version := 0
	for v := 1; v <= MaxVersion; v++ {
		if len(data) <= byteCapacity(v) {
			version = v
			break
		}
	}
	if version == 0 {
		return nil, fmt.Errorf("%w: %d bytes, limit %d", ErrTooLong, len(data), byteCapacity(MaxVersion))
	}

	codewords := interleave(version, payloadCodewords(version, data))

	c := &Code{Version: version, Size: 17 + 4*version}
	c.modules = make([]bool, c.Size*c.Size)
	isFunc := make([]bool, c.Size*c.Size)
	c.drawFunctionPatterns(isFunc)
	c.drawCodewords(codewords, isFunc)
	c.applyBestMask(isFunc)
	return c, nil
}

// payloadCodewords builds the data codeword sequence for one symbol: the byte-mode
// segment header, the payload, a terminator, and the alternating pad bytes that
// fill the version exactly.
func payloadCodewords(version int, data []byte) []byte {
	capBits := specs[version].dataCodewords() * 8
	var b bitBuffer
	b.append(0b0100, 4) // byte mode
	b.append(uint32(len(data)), countBits(version))
	for _, v := range data {
		b.append(uint32(v), 8)
	}
	// The terminator is up to four zero bits, truncated if the symbol is nearly
	// full — a short terminator at the very end of the capacity is legal, and
	// over-running it would corrupt the last codeword.
	b.append(0, min(4, capBits-b.len()))
	b.append(0, (8-b.len()%8)%8) // pad to a codeword boundary

	// 0xEC/0x11 alternating is the spec's filler. The values are arbitrary but
	// fixed; a decoder stops at the terminator and never looks at them.
	for pad := byte(0xEC); b.len() < capBits; pad ^= 0xEC ^ 0x11 {
		b.append(uint32(pad), 8)
	}
	return b.bytes
}

// interleave splits the data codewords into the version's blocks, computes each
// block's error-correction codewords, and emits them in the interleaved order
// the spec places into the matrix.
//
// Interleaving is what makes the error correction useful: a scratch or a glare
// spot damages a contiguous run of modules, and spreading each block's codewords
// across the whole symbol turns that burst into a few isolated errors per block
// rather than the total loss of one.
func interleave(version int, data []byte) []byte {
	s := specs[version]
	blocks := make([][]byte, 0, s.g1Blocks+s.g2Blocks)
	ecs := make([][]byte, 0, s.g1Blocks+s.g2Blocks)

	off := 0
	appendBlocks := func(count, size int) {
		for range count {
			blk := data[off : off+size]
			off += size
			blocks = append(blocks, blk)
			ecs = append(ecs, rsRemainder(blk, s.ecPerBlk))
		}
	}
	appendBlocks(s.g1Blocks, s.g1Data)
	appendBlocks(s.g2Blocks, s.g2Data)

	out := make([]byte, 0, s.total)
	longest := max(s.g1Data, s.g2Data)
	for i := range longest {
		for _, blk := range blocks {
			if i < len(blk) {
				out = append(out, blk[i])
			}
		}
	}
	for i := range s.ecPerBlk {
		for _, ec := range ecs {
			out = append(out, ec[i])
		}
	}
	return out
}

// bitBuffer accumulates a big-endian bit stream into whole bytes. Only Encode
// uses it, so it stays minimal: append-only, no reads.
type bitBuffer struct {
	bytes []byte
	n     int // bits appended so far
}

func (b *bitBuffer) len() int { return b.n }

// append writes the low width bits of val, most significant first.
func (b *bitBuffer) append(val uint32, width int) {
	for i := width - 1; i >= 0; i-- {
		if b.n%8 == 0 {
			b.bytes = append(b.bytes, 0)
		}
		if val>>uint(i)&1 == 1 {
			b.bytes[b.n/8] |= 1 << uint(7-b.n%8)
		}
		b.n++
	}
}
