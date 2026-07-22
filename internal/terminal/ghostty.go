//go:build ghostty

// This file implements Emulator on top of go-libghostty. It only builds with
// `-tags ghostty` and requires libghostty-vt on PKG_CONFIG_PATH.
//
// go-libghostty is pinned in go.mod (v0.0.0-20260528200934-790a3ff6e9f6,
// commit 790a3ff6e9f6) and makes no API-stability promise yet, so all of its
// surface is confined to this file behind the Emulator interface.
package terminal

import (
	"fmt"

	libghostty "go.mitchellh.com/libghostty"
)

// Default cell pixel size reported to libghostty on resize. The cell grid we
// read back is independent of these; they only matter for pixel-based reports
// (e.g. Kitty graphics, which Phase B doesn't render yet).
const (
	defaultCellWidthPx  = 8
	defaultCellHeightPx = 16

	// defaultMaxScrollback is the history depth (lines) kept per cathost pane.
	// libghostty defaults to 0 (no scrollback), so the Host must opt in for the
	// orchestrator's scrollback to work.
	defaultMaxScrollback = 10000
)

type ghosttyEmulator struct {
	term *libghostty.Terminal

	// Reusable render-state scratch, to avoid per-snapshot allocation.
	rs *libghostty.RenderState
	ri *libghostty.RenderStateRowIterator
	rc *libghostty.RenderStateRowCells
}

// Option configures a new Emulator.
type Option func(*[]libghostty.TerminalOption)

// WithWritePTY registers a callback the terminal invokes when it needs to write
// bytes back to the PTY (e.g. responses to device-attribute / cursor-position
// queries). The Host wires this to the pane's PTY master so query responses are
// handled entirely within Go.
func WithWritePTY(fn func(data []byte)) Option {
	return func(opts *[]libghostty.TerminalOption) {
		*opts = append(*opts, libghostty.WithWritePty(func(_ *libghostty.Terminal, data []byte) {
			// Copy: the slice is only valid for the duration of the callback.
			fn(append([]byte(nil), data...))
		}))
	}
}

// New creates a go-libghostty-backed Emulator of the given cell dimensions.
func New(cols, rows uint16, opts ...Option) (Emulator, error) {
	topts := []libghostty.TerminalOption{
		libghostty.WithSize(cols, rows),
		libghostty.WithMaxScrollback(defaultMaxScrollback),
	}
	for _, o := range opts {
		o(&topts)
	}
	term, err := libghostty.NewTerminal(topts...)
	if err != nil {
		return nil, fmt.Errorf("terminal: new: %w", err)
	}

	rs, err := libghostty.NewRenderState()
	if err != nil {
		term.Close()
		return nil, fmt.Errorf("terminal: render state: %w", err)
	}
	ri, err := libghostty.NewRenderStateRowIterator()
	if err != nil {
		rs.Close()
		term.Close()
		return nil, fmt.Errorf("terminal: row iterator: %w", err)
	}
	rc, err := libghostty.NewRenderStateRowCells()
	if err != nil {
		ri.Close()
		rs.Close()
		term.Close()
		return nil, fmt.Errorf("terminal: row cells: %w", err)
	}

	return &ghosttyEmulator{term: term, rs: rs, ri: ri, rc: rc}, nil
}

// Write feeds raw VT bytes through the parser. It always consumes all bytes.
//
// Scroll-lock is libghostty's native behavior: when the viewport is pinned to the
// active area (the user is at the bottom) new output follows to the live bottom;
// when the user has scrolled up into history the viewport stays pinned to that
// content as output accumulates below (its offset-from-bottom grows). Matches the
// Rust in-process path, which likewise does nothing special on the write path.
func (e *ghosttyEmulator) Write(p []byte) (int, error) {
	return e.term.Write(p)
}

// Scroll moves the viewport by delta lines: negative scrolls up into history,
// positive scrolls back down toward the live bottom. libghostty clamps to the
// scrollable range, so a large positive delta is a reliable "scroll to bottom".
func (e *ghosttyEmulator) Scroll(delta int) error {
	e.term.ScrollViewportDelta(delta)
	return nil
}

// ScrollMetrics reports the current scrollback position from libghostty's live
// scrollbar (no self-tracking), mirroring cats's Rust scroll_metrics. The
// scrollbar gives Total rows, the viewport Offset into them, and the visible Len;
// offset-from-bottom is the rows below the viewport, max is the whole history.
func (e *ghosttyEmulator) ScrollMetrics() (ScrollMetrics, error) {
	sb, err := e.term.Scrollbar()
	if err != nil {
		return ScrollMetrics{}, fmt.Errorf("terminal: scrollbar: %w", err)
	}
	return ScrollMetrics{
		OffsetFromBottom:    int(sb.Total - min(sb.Offset+sb.Len, sb.Total)),
		MaxOffsetFromBottom: int(sb.Total - min(sb.Len, sb.Total)),
		ViewportRows:        int(sb.Len),
	}, nil
}

// FormatSelection resolves the two screen-buffer endpoints to grid references and
// formats the bounded selection as plain text. It mirrors cats's Rust extraction
// (read_text_screen): order endpoints top-left → bottom-right, resolve each via
// PointTagScreen, then format with unwrap+trim. The grid references are borrowed
// views of terminal internals, so they are built and consumed back-to-back with no
// intervening terminal mutation (the Host holds emuMu across this call).
func (e *ghosttyEmulator) FormatSelection(anchor, cursor SelectionEndpoint, rectangle bool) (string, error) {
	start, end := anchor, cursor
	if cursor.Row < anchor.Row || (cursor.Row == anchor.Row && cursor.Col < anchor.Col) {
		start, end = cursor, anchor
	}

	startRef, err := e.term.GridRef(libghostty.Point{Tag: libghostty.PointTagScreen, X: start.Col, Y: start.Row})
	if err != nil {
		return "", fmt.Errorf("terminal: selection start ref: %w", err)
	}
	endRef, err := e.term.GridRef(libghostty.Point{Tag: libghostty.PointTagScreen, X: end.Col, Y: end.Row})
	if err != nil {
		return "", fmt.Errorf("terminal: selection end ref: %w", err)
	}

	return e.formatScreenRange(*startRef, *endRef, rectangle,
		libghostty.FormatterFormatPlain, true, true)
}

// formatScreenRange formats the grid region between two resolved screen-coordinate
// grid references. It is the shared core of FormatSelection and ExtractText: build
// refs, build a Selection, run the libghostty formatter. The refs are borrowed
// views of terminal internals, so callers resolve and pass them without an
// intervening terminal mutation (the Host holds emuMu across the call).
func (e *ghosttyEmulator) formatScreenRange(start, end libghostty.GridRef, rectangle bool, format libghostty.FormatterFormat, unwrap, trim bool) (string, error) {
	sel := libghostty.Selection{Start: start, End: end, Rectangle: rectangle}
	text, err := e.term.SelectionFormatString(
		libghostty.WithSelection(&sel),
		libghostty.WithSelectionFormat(format),
		libghostty.WithSelectionUnwrap(unwrap),
		libghostty.WithSelectionTrim(trim),
	)
	if err != nil {
		return "", fmt.Errorf("terminal: format range: %w", err)
	}
	return text, nil
}

// screenRangeRefs resolves the two screen-coordinate endpoints of a full-width row
// span [startRow, endRow] (cols 0..cols-1) to grid references.
func (e *ghosttyEmulator) screenRangeRefs(startRow, endRow uint32, cols uint16) (libghostty.GridRef, libghostty.GridRef, error) {
	var z libghostty.GridRef
	startRef, err := e.term.GridRef(libghostty.Point{Tag: libghostty.PointTagScreen, X: 0, Y: startRow})
	if err != nil {
		return z, z, fmt.Errorf("terminal: range start ref: %w", err)
	}
	endRef, err := e.term.GridRef(libghostty.Point{Tag: libghostty.PointTagScreen, X: cols - 1, Y: endRow})
	if err != nil {
		return z, z, fmt.Errorf("terminal: range end ref: %w", err)
	}
	return *startRef, *endRef, nil
}

// ExtractText formats buffer text for the given scope, mirroring cats's
// recent_*/visible_* extraction (read_text_screen / read_ansi_screen): a
// full-width screen-coordinate range formatted plain or VT, optionally unwrapped,
// trailing-trimmed.
func (e *ghosttyEmulator) ExtractText(scope TextScope, lines int, ansi, unwrap bool) (string, error) {
	cols, err := e.term.Cols()
	if err != nil {
		return "", fmt.Errorf("terminal: cols: %w", err)
	}
	total, err := e.term.TotalRows()
	if err != nil {
		return "", fmt.Errorf("terminal: total rows: %w", err)
	}
	if cols == 0 || total == 0 {
		return "", nil
	}

	var startRow, endRow uint32
	switch scope {
	case TextVisible:
		// The viewport occupies the bottom ViewportRows, offset up by the current
		// scroll position. Derive it from the live scrollbar (top = history above the
		// viewport; len = visible rows).
		sb, err := e.term.Scrollbar()
		if err != nil {
			return "", fmt.Errorf("terminal: scrollbar: %w", err)
		}
		startRow = uint32(sb.Offset)
		endRow = uint32(min(sb.Offset+sb.Len, sb.Total)) - 1
	default: // TextRecent
		endRow = uint32(total - 1)
		if lines > 0 && uint(lines) < total {
			startRow = uint32(total - uint(lines))
		}
	}

	startRef, endRef, err := e.screenRangeRefs(startRow, endRow, cols)
	if err != nil {
		return "", err
	}
	format := libghostty.FormatterFormatPlain
	if ansi {
		format = libghostty.FormatterFormatVT
	}
	return e.formatScreenRange(startRef, endRef, false, format, unwrap, true)
}

// InputModes queries libghostty for the current input-affecting DEC modes. Mouse
// tracking is a precedence ladder (any > button > normal > x10); encoding and the
// boolean modes are independent flags.
func (e *ghosttyEmulator) InputModes() (InputModes, error) {
	get := func(m libghostty.Mode) bool {
		v, err := e.term.ModeGet(m)
		return err == nil && v
	}

	var mouse MouseMode
	switch {
	case get(libghostty.ModeAnyMouse):
		mouse = MouseAnyMotion
	case get(libghostty.ModeButtonMouse):
		mouse = MouseButtonMotion
	case get(libghostty.ModeNormalMouse):
		mouse = MousePressRelease
	case get(libghostty.ModeX10Mouse):
		mouse = MouseX10
	default:
		mouse = MouseNone
	}

	var enc MouseEncoding
	switch {
	case get(libghostty.ModeSGRMouse):
		enc = MouseEncodingSGR
	case get(libghostty.ModeUTF8Mouse):
		enc = MouseEncodingUTF8
	default:
		enc = MouseEncodingDefault
	}

	alt := false
	if s, err := e.term.ActiveScreen(); err == nil {
		alt = s == libghostty.ScreenAlternate
	}

	var kitty uint16
	if f, err := e.term.KittyKeyboardFlags(); err == nil {
		kitty = uint16(f)
	}

	return InputModes{
		AlternateScreen:      alt,
		ApplicationCursor:    get(libghostty.ModeDECCKM),
		BracketedPaste:       get(libghostty.ModeBracketedPaste),
		FocusReporting:       get(libghostty.ModeFocusEvent),
		MouseMode:            mouse,
		MouseEncoding:        enc,
		MouseAlternateScroll: get(libghostty.ModeAltScroll),
		SynchronizedOutput:   get(libghostty.ModeSyncOutput),
		KittyKeyboardFlags:   kitty,
	}, nil
}

func (e *ghosttyEmulator) Resize(cols, rows uint16) error {
	if err := e.term.Resize(cols, rows, defaultCellWidthPx, defaultCellHeightPx); err != nil {
		return fmt.Errorf("terminal: resize: %w", err)
	}
	return nil
}

func (e *ghosttyEmulator) Title() (string, error) {
	return e.term.Title()
}

func (e *ghosttyEmulator) Snapshot() (*Snapshot, error) {
	if err := e.rs.Update(e.term); err != nil {
		return nil, fmt.Errorf("terminal: render update: %w", err)
	}

	cols, err := e.rs.Cols()
	if err != nil {
		return nil, fmt.Errorf("terminal: cols: %w", err)
	}
	rows, err := e.rs.Rows()
	if err != nil {
		return nil, fmt.Errorf("terminal: rows: %w", err)
	}

	colors, err := e.rs.Colors()
	if err != nil {
		return nil, fmt.Errorf("terminal: colors: %w", err)
	}

	cur, err := e.cursor()
	if err != nil {
		return nil, err
	}

	snap := &Snapshot{
		Cols:      cols,
		Rows:      rows,
		Cursor:    cur,
		DefaultFg: toColor(colors.Foreground),
		DefaultBg: toColor(colors.Background),
		Cells:     make([][]Cell, 0, rows),
	}
	if sm, err := e.ScrollMetrics(); err == nil {
		snap.Scroll = sm
	}

	if err := e.rs.RowIterator(e.ri); err != nil {
		return nil, fmt.Errorf("terminal: row iterator bind: %w", err)
	}

	var style libghostty.RenderCellStyle
	buf := make([]byte, 0, 8)
	var linkRows []uint32 // viewport rows the iterator flags as containing OSC 8 links
	for e.ri.Next() {
		if err := e.ri.Cells(e.rc); err != nil {
			return nil, fmt.Errorf("terminal: cells: %w", err)
		}
		row := make([]Cell, 0, cols)
		for e.rc.Next() {
			g, err := e.rc.AppendGraphemes(buf[:0])
			if err != nil {
				return nil, fmt.Errorf("terminal: graphemes: %w", err)
			}
			if err := e.rc.StyleInto(&style); err != nil {
				return nil, fmt.Errorf("terminal: style: %w", err)
			}
			row = append(row, toCell(string(g), &style))
		}
		// Cheap per-row gate: only rows flagged as having hyperlinks get the
		// (relatively expensive) per-cell URI lookup below. The flag may have
		// false positives, which the per-cell HyperlinkURI ("" = none) absorbs.
		if raw, err := e.ri.Raw(); err == nil {
			if hl, err := raw.Hyperlink(); err == nil && hl {
				linkRows = append(linkRows, uint32(len(snap.Cells)))
			}
		}
		snap.Cells = append(snap.Cells, row)
	}

	// Resolve OSC 8 URIs for flagged rows after the render iteration completes, so
	// GridRef (a borrowed view of terminal internals) never interleaves with the
	// render-state iterators. libghostty does not surface hyperlinks on the
	// render-cell path, only via GridRef.HyperlinkURI.
	for _, y := range linkRows {
		row := snap.Cells[y]
		for x := range row {
			ref, err := e.term.GridRef(libghostty.Point{
				Tag: libghostty.PointTagViewport,
				X:   uint16(x),
				Y:   y,
			})
			if err != nil {
				continue
			}
			uri, err := ref.HyperlinkURI()
			if err != nil || uri == "" {
				continue
			}
			row[x].Link = uri
			snap.HasHyperlinks = true
		}
	}

	return snap, nil
}

func (e *ghosttyEmulator) cursor() (Cursor, error) {
	visible, err := e.rs.CursorVisible()
	if err != nil {
		return Cursor{}, fmt.Errorf("terminal: cursor visible: %w", err)
	}
	// When the viewport is scrolled into history the cursor lies outside it, and
	// libghostty reports its viewport position as invalid; treat that as a hidden
	// cursor rather than failing the whole snapshot.
	x, errX := e.rs.CursorViewportX()
	y, errY := e.rs.CursorViewportY()
	if errX != nil || errY != nil {
		return Cursor{Visible: false}, nil
	}
	vs, err := e.rs.CursorVisualStyle()
	if err != nil {
		return Cursor{}, fmt.Errorf("terminal: cursor style: %w", err)
	}
	return Cursor{X: x, Y: y, Visible: visible, Style: toCursorStyle(vs)}, nil
}

func (e *ghosttyEmulator) Close() error {
	e.rc.Close()
	e.ri.Close()
	e.rs.Close()
	e.term.Close()
	return nil
}

func toColor(c libghostty.ColorRGB) Color {
	return Color{R: c.R, G: c.G, B: c.B}
}

func toCell(rune string, s *libghostty.RenderCellStyle) Cell {
	c := Cell{
		Rune:          rune,
		Bold:          s.Bold,
		Faint:         s.Faint,
		Italic:        s.Italic,
		Underline:     s.Underline,
		Strikethrough: s.Strikethrough,
		Inverse:       s.Inverse,
	}
	if s.HasForeground {
		fg := toColor(s.Foreground)
		c.Fg = &fg
	}
	if s.HasBackground {
		bg := toColor(s.Background)
		c.Bg = &bg
	}
	return c
}

func toCursorStyle(s libghostty.CursorVisualStyle) CursorStyle {
	switch s {
	case libghostty.CursorVisualStyleBar:
		return CursorBar
	case libghostty.CursorVisualStyleUnderline:
		return CursorUnderline
	case libghostty.CursorVisualStyleBlockHollow:
		return CursorBlockHollow
	default:
		return CursorBlock
	}
}
