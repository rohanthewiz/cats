package app

// The wire vocabulary (package wire) is dependency-free by design: it is what
// an out-of-tree client such as the phone imports, and it must build for
// GOOS=js and under gomobile with nothing but the standard library. These four
// helpers are the only places the vocabulary touches internal/layout or
// internal/flags, so they live here on the server side of the seam rather
// than in wire. Each maps a wire value onto the model type the orchestrator
// actually operates on.

import (
	"github.com/rohanthewiz/cats/internal/flags"
	"github.com/rohanthewiz/cats/internal/layout"
)

// SplitDirection maps a wire direction value onto layout.Direction.
func SplitDirection(s string) (layout.Direction, bool) {
	switch s {
	case SplitH:
		return layout.Horizontal, true
	case SplitV:
		return layout.Vertical, true
	}
	return 0, false
}

// NavDirection maps a wire cardinal value onto layout.NavDirection.
func NavDirection(s string) (layout.NavDirection, bool) {
	switch s {
	case DirLeft:
		return layout.Left, true
	case DirRight:
		return layout.Right, true
	case DirUp:
		return layout.Up, true
	case DirDown:
		return layout.Down, true
	}
	return 0, false
}

// NewFlagInfo projects a model flag onto the wire; a nil flag yields the zero
// FlagInfo, which is the unflagged encoding.
func NewFlagInfo(f *flags.Flag) FlagInfo {
	if f == nil {
		return FlagInfo{}
	}
	return FlagInfo{Flag: string(f.Kind), FlagNote: f.Note, FlagAtMs: f.AtMs}
}

// optPaneID converts an optional wire pane id into an optional layout.PaneID
// (nil = the focused pane).
func optPaneID(p *uint32) *layout.PaneID {
	if p == nil {
		return nil
	}
	id := layout.PaneID(*p)
	return &id
}
