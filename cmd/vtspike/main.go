//go:build ghostty

// Command vtspike is the Phase B proof-of-concept: it drives a go-libghostty
// terminal entirely in Go (no Rust server), feeds it VT escape sequences, and
// reads the result back two ways:
//
//  1. the plain-text formatter (proves VT emulation works), and
//  2. a per-cell render-state walk (proves we can pull glyph + fg/bg per cell,
//     which is what the browser renderer needs in Phase B).
//
// Build requires libghostty-vt on PKG_CONFIG_PATH; see scripts/build-libghostty-vt.sh.
package main

import (
	"fmt"
	"os"

	libghostty "go.mitchellh.com/libghostty"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "vtspike:", err)
		os.Exit(1)
	}
}

func run() error {
	const cols, rows = 40, 4

	term, err := libghostty.NewTerminal(libghostty.WithSize(cols, rows))
	if err != nil {
		return fmt.Errorf("new terminal: %w", err)
	}
	defer term.Close()

	// Feed VT sequences: bold green "world", a default line, and a line that
	// sets an explicit red foreground so we can confirm per-cell color readout.
	term.VTWrite([]byte("Hello, \x1b[1;32mworld\x1b[0m!\r\n"))
	term.VTWrite([]byte("plain line two\r\n"))
	term.VTWrite([]byte("\x1b[31mRED\x1b[0m text"))

	// --- Readback 1: plain-text formatter ---
	f, err := libghostty.NewFormatter(term,
		libghostty.WithFormatterFormat(libghostty.FormatterFormatPlain),
		libghostty.WithFormatterTrim(true),
	)
	if err != nil {
		return fmt.Errorf("new formatter: %w", err)
	}
	defer f.Close()

	plain, err := f.FormatString()
	if err != nil {
		return fmt.Errorf("format: %w", err)
	}
	fmt.Println("=== plain-text readback ===")
	fmt.Println(plain)

	// --- Readback 2: per-cell render-state walk ---
	rs, err := libghostty.NewRenderState()
	if err != nil {
		return fmt.Errorf("new render state: %w", err)
	}
	defer rs.Close()
	if err := rs.Update(term); err != nil {
		return fmt.Errorf("render state update: %w", err)
	}

	ri, err := libghostty.NewRenderStateRowIterator()
	if err != nil {
		return fmt.Errorf("new row iterator: %w", err)
	}
	defer ri.Close()
	if err := rs.RowIterator(ri); err != nil {
		return fmt.Errorf("row iterator: %w", err)
	}

	rc, err := libghostty.NewRenderStateRowCells()
	if err != nil {
		return fmt.Errorf("new row cells: %w", err)
	}
	defer rc.Close()

	fmt.Println("=== per-cell render-state walk (trailing blanks trimmed) ===")
	buf := make([]byte, 0, 8)
	rowNum := 0
	for ri.Next() {
		if err := ri.Cells(rc); err != nil {
			return fmt.Errorf("cells: %w", err)
		}
		var line string
		var spans []string
		col := 0
		for rc.Next() {
			g, err := rc.AppendGraphemes(buf[:0])
			if err != nil {
				return fmt.Errorf("graphemes: %w", err)
			}
			ch := string(g)
			if ch == "" {
				ch = " "
			}
			line += ch
			// Record color spans for any cell with an explicit fg/bg color.
			fg, _ := rc.FgColor()
			bg, _ := rc.BgColor()
			if fg != nil || bg != nil {
				spans = append(spans, fmt.Sprintf("[%d]%q fg=%s bg=%s", col, ch, colorStr(fg), colorStr(bg)))
			}
			col++
		}
		fmt.Printf("row %d | %q\n", rowNum, trimRight(line))
		for _, s := range spans {
			fmt.Printf("        %s\n", s)
		}
		rowNum++
	}

	fmt.Printf("\nOK: drove a %dx%d go-libghostty terminal in pure Go and read back %d rows.\n", cols, rows, rowNum)
	return nil
}

func colorStr(c *libghostty.ColorRGB) string {
	if c == nil {
		return "default"
	}
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

func trimRight(s string) string {
	i := len(s)
	for i > 0 && s[i-1] == ' ' {
		i--
	}
	return s[:i]
}
