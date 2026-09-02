package browserproto

import "github.com/rohanthewiz/cats/internal/app"

// The two wire-to-model converters the old cmd.go re-exported. They live in
// internal/app because they return internal/layout types, which package wire
// must not import (see wire_convert.go there); this keeps catctl and catway
// spelling them the way they always have.
var (
	SplitDirection = app.SplitDirection
	NavDirection   = app.NavDirection
)
