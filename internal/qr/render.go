package qr

import "strings"

// Terminal rendering. A QR module is square but a terminal cell is roughly twice
// as tall as it is wide, so one cell carries two vertically-stacked modules via
// the half-block glyph ▀ — the foreground paints the upper module, the
// background the lower. A version-7 symbol with its quiet zone then measures
// 53 columns by 27 rows, which fits any terminal worth pairing a phone from.
//
// Polarity is the thing to get right, not aesthetics: dark modules must look
// dark and light modules light. Phone cameras — iOS's built-in scanner in
// particular — are unreliable on inverted codes, so an "it looked fine to me"
// render is not good enough.

const (
	// quietZone is the light margin the spec requires around a symbol. Scanners
	// use it to find the symbol's edge; without it a code butted against
	// surrounding text often will not acquire at all.
	quietZone = 4

	// upperHalf paints the top half of a cell in the foreground colour and the
	// bottom half in the background colour.
	upperHalf = "▀"
	fullBlock = "█"
	lowerHalf = "▄"

	// ansiLight/ansiDark set foreground and background from the 256-colour cube.
	// Explicit colours are the whole point of the coloured mode: they make the
	// render's polarity independent of whatever theme the terminal is using.
	ansiFgLight = "\x1b[38;5;231m"
	ansiFgDark  = "\x1b[38;5;16m"
	ansiBgLight = "\x1b[48;5;231m"
	ansiBgDark  = "\x1b[48;5;16m"
	ansiReset   = "\x1b[0m"
)

// TerminalOptions selects how Terminal renders a symbol.
type TerminalOptions struct {
	// Color emits explicit ANSI foreground/background colours so the code scans
	// correctly on a light or a dark terminal alike. Without it the render is
	// only correct on a dark background (see Terminal).
	Color bool
	// Indent is prepended to every line, for laying the code out inside a
	// message block.
	Indent string
}

// Terminal renders the symbol as half-block text, one line per two module rows,
// including the quiet zone.
//
// With Color set, every cell carries an explicit foreground/background pair, so
// dark modules are black and light modules white whatever the terminal theme is.
//
// Without it, the render draws *light* modules as ink and leaves dark modules as
// the terminal's background — correct on a dark background, inverted on a light
// one. That is the right compromise for the uncoloured path: it is used when
// output is piped or NO_COLOR is set, where the pairing URI printed alongside is
// the real payload and the code is a courtesy.
func (c *Code) Terminal(opt TerminalOptions) string {
	span := c.Size + 2*quietZone
	var b strings.Builder

	// Two module rows per line; an odd-height span leaves the final line's lower
	// half in the quiet zone, which Dark already reports as light.
	for y := -quietZone; y < c.Size+quietZone; y += 2 {
		b.WriteString(opt.Indent)
		if opt.Color {
			c.writeColorRow(&b, y, span)
		} else {
			c.writePlainRow(&b, y, span)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// writeColorRow emits one line as ▀ glyphs whose colours are set explicitly,
// re-emitting the SGR escape only when the colour pair changes so a full symbol
// costs a few hundred bytes rather than a few thousand.
func (c *Code) writeColorRow(b *strings.Builder, y, span int) {
	prevUpper, prevLower := -1, -1
	for i := range span {
		x := i - quietZone
		upper, lower := 0, 0
		if c.Dark(x, y) {
			upper = 1
		}
		if c.Dark(x, y+1) {
			lower = 1
		}
		if upper != prevUpper || lower != prevLower {
			if upper == 1 {
				b.WriteString(ansiFgDark)
			} else {
				b.WriteString(ansiFgLight)
			}
			if lower == 1 {
				b.WriteString(ansiBgDark)
			} else {
				b.WriteString(ansiBgLight)
			}
			prevUpper, prevLower = upper, lower
		}
		b.WriteString(upperHalf)
	}
	b.WriteString(ansiReset)
}

// writePlainRow emits one line using the four half-block glyphs, drawing light
// modules as ink. See Terminal for why this direction.
func (c *Code) writePlainRow(b *strings.Builder, y, span int) {
	for i := range span {
		x := i - quietZone
		upperLight, lowerLight := !c.Dark(x, y), !c.Dark(x, y+1)
		switch {
		case upperLight && lowerLight:
			b.WriteString(fullBlock)
		case upperLight:
			b.WriteString(upperHalf)
		case lowerLight:
			b.WriteString(lowerHalf)
		default:
			b.WriteByte(' ')
		}
	}
}
