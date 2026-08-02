package qr

// Matrix construction: the function patterns a decoder locates the symbol by,
// the zigzag that lays codewords into everything left over, and the mask that
// keeps the result from looking like a function pattern.
//
// Module layout of a version-2 (25×25) symbol:
//
//	┌───────┬─┬─────────────┬───────┐
//	│finder │ │   timing    │finder │   finders anchor the three corners;
//	│ 7×7   │ │  row 6 ═════│ 7×7   │   the fourth is left free so a scanner
//	├───────┘ └─────────────┴───────┤   can tell the symbol's orientation
//	│ t ║                           │
//	│ i ║        data region        │   timing runs the full width/height at
//	│ m ║   (zigzag, 2 cols wide)   │   row 6 and column 6, giving the
//	│ i ║                    ┌───┐  │   decoder a module-size ruler
//	├───────┐                │ali│  │
//	│finder │                └───┘  │   alignment patterns (v2+) re-anchor the
//	│ 7×7   │                       │   grid where perspective has drifted
//	└───────┴───────────────────────┘
//
// Format information (EC level + mask) is written twice, next to the finders, so
// losing one corner does not lose the ability to read the rest.

// drawFunctionPatterns lays down everything that is not payload and marks those
// modules in isFunc, which both the codeword placement and the mask must skip.
func (c *Code) drawFunctionPatterns(isFunc []bool) {
	set := func(x, y int, dark bool) {
		c.set(x, y, dark)
		isFunc[y*c.Size+x] = true
	}

	// Timing patterns: alternating modules along row 6 and column 6. Drawn first
	// so the finder and alignment patterns overwrite their own overlaps.
	for i := range c.Size {
		set(6, i, i%2 == 0)
		set(i, 6, i%2 == 0)
	}

	// Finder patterns plus their separators. The 9×9 sweep covers the 7×7 ring
	// and the one-module light border around it in a single expression: at
	// Chebyshev distance 2 (the light ring) and 4 (the separator) a module is
	// light, everywhere else dark.
	for _, p := range [][2]int{{3, 3}, {c.Size - 4, 3}, {3, c.Size - 4}} {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				x, y := p[0]+dx, p[1]+dy
				if x < 0 || y < 0 || x >= c.Size || y >= c.Size {
					continue
				}
				d := max(abs(dx), abs(dy))
				set(x, y, d != 2 && d != 4)
			}
		}
	}

	// Alignment patterns at every pair of centres, minus the three that would sit
	// on a finder. Skipping by index (rather than by coordinate arithmetic) keeps
	// the rule readable: first×first, first×last and last×first are the corners
	// the finders already own.
	centers := alignCenters[c.Version]
	for i, cy := range centers {
		for j, cx := range centers {
			if (i == 0 && j == 0) ||
				(i == 0 && j == len(centers)-1) ||
				(i == len(centers)-1 && j == 0) {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					set(cx+dx, cy+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}

	// Reserve the format area with a placeholder; applyBestMask rewrites it once
	// the mask is chosen. Reserving now is what stops the zigzag from laying
	// payload there.
	c.drawFormatBits(0, set)
	c.drawVersionBits(set)
}

// drawFormatBits writes the 15-bit format information for the chosen mask, in
// both copies. The 5 data bits (EC level, then mask) are protected by a BCH(15,5)
// code and masked with 0x5412 so an all-zero format — a plausible failure of a
// damaged symbol — is not itself a valid codeword.
//
// setFn is passed in because this runs twice: once during reservation, where the
// modules must also be marked as function modules, and once after masking, where
// they must not be re-marked (they already are).
func (c *Code) drawFormatBits(mask int, setFn func(x, y int, dark bool)) {
	// EC level M encodes as 00 in the format field. The two bits are not the
	// level's ordinal — the spec's order is L=01, M=00, Q=11, H=10.
	const ecLevelM = 0b00
	data := ecLevelM<<3 | mask
	rem := data
	for range 10 {
		rem = (rem << 1) ^ ((rem >> 9) * 0x537)
	}
	bits := (data<<10 | rem) ^ 0x5412

	bit := func(i int) bool { return bits>>uint(i)&1 == 1 }

	// First copy: down the left edge of the top-left finder, then across its
	// bottom. The two 90° turns at bits 6-8 are the spec's layout, not a pattern.
	for i := range 6 {
		setFn(8, i, bit(i))
	}
	setFn(8, 7, bit(6))
	setFn(8, 8, bit(7))
	setFn(7, 8, bit(8))
	for i := 9; i < 15; i++ {
		setFn(14-i, 8, bit(i))
	}

	// Second copy: along the bottom of the top-right finder and up the right of
	// the bottom-left one.
	for i := range 8 {
		setFn(c.Size-1-i, 8, bit(i))
	}
	for i := 8; i < 15; i++ {
		setFn(8, c.Size-15+i, bit(i))
	}
	setFn(8, c.Size-8, true) // the dark module, always set, no informational content
}

// drawVersionBits writes the 18-bit version information block, present only from
// version 7 up (below that the version is inferred from the module count). Six
// version bits are protected by a BCH(18,6) code; the block appears twice,
// transposed, beside the top-right and bottom-left finders.
func (c *Code) drawVersionBits(setFn func(x, y int, dark bool)) {
	if c.Version < 7 {
		return
	}
	rem := c.Version
	for range 12 {
		rem = (rem << 1) ^ ((rem >> 11) * 0x1F25)
	}
	bits := c.Version<<12 | rem

	for i := range 18 {
		dark := bits>>uint(i)&1 == 1
		a, b := c.Size-11+i%3, i/3
		setFn(a, b, dark)
		setFn(b, a, dark)
	}
}

// drawCodewords lays the interleaved codewords into every non-function module.
//
// The path is a two-module-wide serpentine starting at the bottom-right corner
// and working left, alternating upward and downward, with the right module of
// each pair filled before the left. Column 6 is skipped whole — it is the
// vertical timing pattern — which is why the column index steps over it rather
// than the isFunc test simply absorbing it: skipping keeps the *pairing* of
// columns aligned with the spec.
//
// The data region is a few modules larger than the codeword stream for most
// versions (seven "remainder bits" at versions 2–6). Those trailing modules are
// simply left at their zero value and then masked along with everything else,
// which is exactly what the spec asks for — hence no explicit handling.
func (c *Code) drawCodewords(data []byte, isFunc []bool) {
	i := 0 // bit index into data
	for right := c.Size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := range c.Size {
			for j := range 2 {
				x := right - j
				upward := (right+1)&2 == 0
				y := vert
				if upward {
					y = c.Size - 1 - vert
				}
				if isFunc[y*c.Size+x] || i >= len(data)*8 {
					continue
				}
				c.set(x, y, data[i>>3]>>uint(7-i&7)&1 == 1)
				i++
			}
		}
	}
}

// applyBestMask XORs each of the eight mask patterns over the data modules,
// scores the result, and keeps the lowest-scoring one.
//
// Masking is not obfuscation: an unmasked symbol can easily contain a run that
// looks like a finder pattern, or large single-colour fields a scanner cannot
// threshold. The penalty function is the spec's proxy for "how hard is this to
// read", and trying all eight is cheap enough at these sizes that there is no
// reason to guess.
func (c *Code) applyBestMask(isFunc []bool) {
	bestMask, bestScore := 0, -1
	for mask := range 8 {
		c.applyMask(mask, isFunc)
		c.drawFormatBits(mask, c.set)
		if score := c.penalty(); bestScore < 0 || score < bestScore {
			bestMask, bestScore = mask, score
		}
		c.applyMask(mask, isFunc) // XOR is its own inverse — undo before the next try
	}
	c.applyMask(bestMask, isFunc)
	c.drawFormatBits(bestMask, c.set)
}

// applyMask inverts the data modules selected by pattern mask. Function modules
// are never touched: they are how the decoder finds the format bits that name
// this very mask.
func (c *Code) applyMask(mask int, isFunc []bool) {
	for y := range c.Size {
		for x := range c.Size {
			if isFunc[y*c.Size+x] || !maskBit(mask, x, y) {
				continue
			}
			c.set(x, y, !c.Dark(x, y))
		}
	}
}

// maskBit reports whether module (x, y) is inverted by the given mask pattern.
// The eight formulas are fixed by the spec; between them they cover stripes,
// checks and blocks at several periods, so at least one is usually a poor match
// for any given data layout.
func maskBit(mask, x, y int) bool {
	switch mask {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (x/3+y/2)%2 == 0
	case 5:
		return x*y%2+x*y%3 == 0
	case 6:
		return (x*y%2+x*y%3)%2 == 0
	case 7:
		return ((x+y)%2+x*y%3)%2 == 0
	}
	return false
}

// penalty scores a masked symbol by the spec's four rules — lower is better.
// The weights (3/3/40/10) are the spec's; they encode how badly each feature
// hurts a real scanner, not anything derivable.
func (c *Code) penalty() int {
	score := 0

	// Rule 1: runs of five or more same-coloured modules in a row or column.
	// A long uniform run gives the scanner nothing to synchronise its module
	// grid against.
	for _, byRow := range []bool{true, false} {
		for a := range c.Size {
			runLen, runDark := 0, false
			for b := range c.Size {
				x, y := b, a
				if !byRow {
					x, y = a, b
				}
				dark := c.Dark(x, y)
				if b > 0 && dark == runDark {
					runLen++
					if runLen == 5 {
						score += 3
					} else if runLen > 5 {
						score++
					}
					continue
				}
				runLen, runDark = 1, dark
			}
		}
	}

	// Rule 2: every 2×2 block of one colour. Solid areas are the same
	// synchronisation problem in two dimensions.
	for y := range c.Size - 1 {
		for x := range c.Size - 1 {
			v := c.Dark(x, y)
			if c.Dark(x+1, y) == v && c.Dark(x, y+1) == v && c.Dark(x+1, y+1) == v {
				score += 3
			}
		}
	}

	// Rule 3: the 1:1:3:1:1 finder ratio appearing in the data, with the four
	// light modules that would complete the illusion. This is the expensive
	// mistake — a decoder that mistakes data for a finder mislocates the whole
	// symbol — hence the weight of 40.
	const (
		finderLead  = 0b10111010000
		finderTrail = 0b00001011101
		window      = 11
	)
	for _, byRow := range []bool{true, false} {
		for a := range c.Size {
			acc := 0
			for b := range c.Size {
				x, y := b, a
				if !byRow {
					x, y = a, b
				}
				acc = (acc << 1) & (1<<window - 1)
				if c.Dark(x, y) {
					acc |= 1
				}
				if b >= window-1 && (acc == finderLead || acc == finderTrail) {
					score += 40
				}
			}
		}
	}

	// Rule 4: deviation from an even light/dark split, in 5% steps. A symbol
	// that is mostly one colour is harder to threshold under uneven lighting.
	dark := 0
	for _, m := range c.modules {
		if m {
			dark++
		}
	}
	total := c.Size * c.Size
	deviation := abs(dark*100/total - 50)
	score += deviation / 5 * 10
	return score
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
