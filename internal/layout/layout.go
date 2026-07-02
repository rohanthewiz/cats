// Package layout is a port of herdr's BSP pane-tiling tree (src/layout.rs).
// It tracks pane *identity* only — never content — and computes pane rects on
// demand from a caller-supplied screen Rect.
//
// Geometry parity with the Rust original is load-bearing: splitRect matches
// f32 round-half-away-from-zero + u16 saturating subtraction, and directional
// focus navigation tiebreaks by (edgeDistance, -overlap, centerDistance,
// index) exactly as layout.rs does.
package layout

import (
	"math"
	"slices"
	"sync/atomic"
)

// PaneID identifies a pane within a layout tree.
type PaneID uint32

// nextPaneID is the process-global counter for unique PaneID allocation
// across all workspaces (cf. NEXT_PANE_ID in layout.rs; ids start at 1).
var nextPaneID atomic.Uint32

// AllocPaneID allocates a globally unique PaneID.
func AllocPaneID() PaneID {
	return PaneID(nextPaneID.Add(1))
}

// Rect is an axis-aligned cell rectangle (replaces ratatui's Rect).
type Rect struct {
	X, Y          uint16
	Width, Height uint16
}

// Direction is the orientation of a split (replaces ratatui's Direction).
type Direction uint8

const (
	// Horizontal splits side-by-side (divider is a vertical line at an x).
	Horizontal Direction = iota
	// Vertical splits top/bottom (divider is a horizontal line at a y).
	Vertical
)

// NavDirection is a cardinal direction for pane navigation.
type NavDirection uint8

const (
	Left NavDirection = iota
	Right
	Up
	Down
)

// PaneInfo is a snapshot of a pane's position and focus state after layout.
type PaneInfo struct {
	ID PaneID
	// Rect is the outer rect (including borders if present).
	Rect Rect
	// InnerRect is the content area, excluding borders. Used for selection.
	InnerRect Rect
	// ScrollbarRect is the visible scrollbar lane, when scrollback is
	// present. InnerRect may still exclude a stable hidden gutter when this
	// is nil.
	ScrollbarRect *Rect
	IsFocused     bool
}

// SplitBorder describes a split boundary, used for mouse drag resize.
type SplitBorder struct {
	// Pos is the divider line position (x for horizontal split, y for vertical).
	Pos uint16
	// Direction of the split that created this border.
	Direction Direction
	// Ratio assigned to the first child of this split.
	Ratio float32
	// Area is the total area of the split node.
	Area Rect
	// Path from root to this split node (false=first, true=second).
	Path []bool
}

// Node is a node in the BSP tree: either a *PaneNode leaf or a *SplitNode.
// Public for serialization.
type Node interface{ isNode() }

// PaneNode is a leaf holding a pane's identity.
type PaneNode struct {
	ID PaneID
}

// SplitNode is an interior node dividing its area between two children.
type SplitNode struct {
	Direction Direction
	Ratio     float32
	First     Node
	Second    Node
}

func (*PaneNode) isNode()  {}
func (*SplitNode) isNode() {}

// TileLayout is a BSP tiling layout. Tracks a tree of splits and a focused pane.
type TileLayout struct {
	root  Node
	focus PaneID
	// alloc overrides PaneID allocation when non-nil (deterministic tests);
	// nil means AllocPaneID.
	alloc func() PaneID
}

// New creates a layout with a single pane (globally unique ID).
// Returns (layout, rootPaneID) so the caller can create the pane.
func New() (*TileLayout, PaneID) {
	return NewWithAllocator(nil)
}

// NewWithAllocator is New with an injectable PaneID allocator; a nil alloc
// falls back to AllocPaneID.
func NewWithAllocator(alloc func() PaneID) (*TileLayout, PaneID) {
	l := &TileLayout{alloc: alloc}
	rootID := l.allocID()
	l.root = &PaneNode{ID: rootID}
	l.focus = rootID
	return l, rootID
}

// FromSaved reconstructs a layout from a saved tree.
func FromSaved(root Node, focus PaneID) *TileLayout {
	return &TileLayout{root: root, focus: focus}
}

func (l *TileLayout) allocID() PaneID {
	if l.alloc != nil {
		return l.alloc()
	}
	return AllocPaneID()
}

// Focused returns the focused pane's id.
func (l *TileLayout) Focused() PaneID {
	return l.focus
}

// PaneCount returns the number of panes in the tree.
func (l *TileLayout) PaneCount() int {
	return countPanes(l.root)
}

// Panes computes rects for all panes given the available area.
func (l *TileLayout) Panes(area Rect) []PaneInfo {
	var result []PaneInfo
	collectPanes(l.root, area, l.focus, &result)
	return result
}

// Splits collects all split boundaries for mouse drag resize.
func (l *TileLayout) Splits(area Rect) []SplitBorder {
	var result []SplitBorder
	collectSplits(l.root, area, nil, &result)
	return result
}

// SplitFocused splits the focused pane 50/50. Returns the new pane's id.
func (l *TileLayout) SplitFocused(direction Direction) PaneID {
	return l.SplitFocusedWithRatio(direction, 0.5)
}

// SplitFocusedWithRatio splits the focused pane with a custom first-child ratio.
func (l *TileLayout) SplitFocusedWithRatio(direction Direction, ratio float32) PaneID {
	newID := l.allocID()
	l.root = splitAt(l.root, l.focus, direction, newID, validSplitRatio(ratio))
	l.focus = newID
	return newID
}

// CloseFocused closes the focused pane, promoting its sibling. Returns false
// if it's the last pane.
func (l *TileLayout) CloseFocused() bool {
	if l.PaneCount() <= 1 {
		return false
	}
	target := l.focus
	ids := l.PaneIDs()
	pos := 0
	for i, id := range ids {
		if id == target {
			pos = i
			break
		}
	}
	var newFocus PaneID
	if pos+1 < len(ids) {
		newFocus = ids[pos+1]
	} else {
		newFocus = ids[pos-1]
	}
	newRoot := removePane(l.root, target)
	if newRoot == nil {
		return false
	}
	l.root = newRoot
	l.focus = newFocus
	return true
}

// FocusPane focuses the given pane if it exists in the tree.
func (l *TileLayout) FocusPane(id PaneID) {
	if slices.Contains(l.PaneIDs(), id) {
		l.focus = id
	}
}

// SwapPanes swaps two pane ids in the layout tree while preserving split
// shape and ratios. Returns true only when both panes exist and are different.
func (l *TileLayout) SwapPanes(first, second PaneID) bool {
	if first == second {
		return false
	}
	ids := l.PaneIDs()
	if !slices.Contains(ids, first) || !slices.Contains(ids, second) {
		return false
	}
	swapPaneIDs(l.root, first, second)
	return true
}

// SetRatioAt sets the ratio of the split node at the given path.
func (l *TileLayout) SetRatioAt(path []bool, ratio float32) {
	setRatioAt(l.root, path, clampRatio(ratio))
}

// ResizeFocused adjusts the nearest split in the given direction for the
// focused pane. delta is positive to grow, negative to shrink.
func (l *TileLayout) ResizeFocused(nav NavDirection, delta float32, area Rect) {
	var focused *PaneInfo
	panes := l.Panes(area)
	for i := range panes {
		if panes[i].IsFocused {
			focused = &panes[i]
			break
		}
	}
	if focused == nil {
		return
	}
	focusedRect := focused.Rect
	splits := l.Splits(area)

	var targetDir Direction
	switch nav {
	case Left, Right:
		targetDir = Horizontal
	default:
		targetDir = Vertical
	}
	grows := nav == Right || nav == Down

	best := nearestResizeSplit(splits, targetDir, focusedRect, nav)
	if best == nil {
		best = nearestResizeSplit(splits, targetDir, focusedRect, oppositeDirection(nav))
	}

	if best != nil {
		currentRatio, ok := getRatioAt(l.root, best.Path)
		if !ok {
			currentRatio = 0.5
		}
		adj := delta
		if !grows {
			adj = -delta
		}
		l.SetRatioAt(best.Path, currentRatio+adj)
	}
}

// ResizePane resizes a specific pane as if it were focused, restoring focus
// afterwards. Reports whether any split ratio changed.
func (l *TileLayout) ResizePane(paneID PaneID, nav NavDirection, delta float32, area Rect) bool {
	if !slices.Contains(l.PaneIDs(), paneID) {
		return false
	}
	before := splitRatios(l.root)
	previousFocus := l.focus
	l.focus = paneID
	l.ResizeFocused(nav, delta, area)
	l.focus = previousFocus
	return !ratiosEqual(splitRatios(l.root), before)
}

// PaneIDs returns all pane ids in layout (in-order) traversal.
func (l *TileLayout) PaneIDs() []PaneID {
	var ids []PaneID
	collectIDs(l.root, &ids)
	return ids
}

// Root returns the tree root for serialization.
func (l *TileLayout) Root() Node {
	return l.root
}

// --- Directional pane navigation ---

// FindInDirection finds the nearest pane in the given direction from focused.
// Candidates tiebreak by (edgeDistance, larger overlap, centerDistance,
// layout order); returns false when no pane lies in that direction.
func FindInDirection(focused *PaneInfo, direction NavDirection, panes []PaneInfo) (PaneID, bool) {
	fr := focused.Rect

	type navKey struct {
		edgeDistance   uint16
		overlap        uint16 // larger preferred (Reverse in Rust)
		centerDistance uint16
		index          int
	}
	keyLess := func(a, b navKey) bool {
		if a.edgeDistance != b.edgeDistance {
			return a.edgeDistance < b.edgeDistance
		}
		if a.overlap != b.overlap {
			return a.overlap > b.overlap
		}
		if a.centerDistance != b.centerDistance {
			return a.centerDistance < b.centerDistance
		}
		return a.index < b.index
	}

	var bestID PaneID
	var bestKey navKey
	found := false
	for index := range panes {
		p := &panes[index]
		if p.ID == focused.ID {
			continue
		}
		r := p.Rect
		var inDirection bool
		switch direction {
		case Left:
			inDirection = r.X+r.Width <= fr.X && rangesOverlap(r.Y, r.Height, fr.Y, fr.Height)
		case Right:
			inDirection = r.X >= fr.X+fr.Width && rangesOverlap(r.Y, r.Height, fr.Y, fr.Height)
		case Up:
			inDirection = r.Y+r.Height <= fr.Y && rangesOverlap(r.X, r.Width, fr.X, fr.Width)
		case Down:
			inDirection = r.Y >= fr.Y+fr.Height && rangesOverlap(r.X, r.Width, fr.X, fr.Width)
		}
		if !inDirection {
			continue
		}

		var key navKey
		key.index = index
		switch direction {
		case Left:
			key.edgeDistance = satSubU16(fr.X, r.X+r.Width)
		case Right:
			key.edgeDistance = satSubU16(r.X, fr.X+fr.Width)
		case Up:
			key.edgeDistance = satSubU16(fr.Y, r.Y+r.Height)
		case Down:
			key.edgeDistance = satSubU16(r.Y, fr.Y+fr.Height)
		}
		switch direction {
		case Left, Right:
			key.overlap = rangeOverlapAmount(r.Y, r.Height, fr.Y, fr.Height)
			key.centerDistance = rangeCenterDistance(r.Y, r.Height, fr.Y, fr.Height)
		default:
			key.overlap = rangeOverlapAmount(r.X, r.Width, fr.X, fr.Width)
			key.centerDistance = rangeCenterDistance(r.X, r.Width, fr.X, fr.Width)
		}

		if !found || keyLess(key, bestKey) {
			found = true
			bestKey = key
			bestID = p.ID
		}
	}
	return bestID, found
}

func rangesOverlap(aStart, aLen, bStart, bLen uint16) bool {
	return aStart < bStart+bLen && aStart+aLen > bStart
}

func splitOnRequestedEdge(split *SplitBorder, focused Rect, nav NavDirection) bool {
	return splitEdgeDistance(split, focused, nav) <= 1
}

func splitAreaOverlapsFocusedPane(split *SplitBorder, focused Rect, nav NavDirection) bool {
	switch nav {
	case Left, Right:
		return rangesOverlap(split.Area.Y, split.Area.Height, focused.Y, focused.Height)
	default:
		return rangesOverlap(split.Area.X, split.Area.Width, focused.X, focused.Width)
	}
}

func nearestResizeSplit(splits []SplitBorder, targetDir Direction, focused Rect, nav NavDirection) *SplitBorder {
	var best *SplitBorder
	var bestDistance uint32
	for i := range splits {
		s := &splits[i]
		if s.Direction != targetDir {
			continue
		}
		if !splitAreaOverlapsFocusedPane(s, focused, nav) {
			continue
		}
		if !splitOnRequestedEdge(s, focused, nav) {
			continue
		}
		distance := splitEdgeDistance(s, focused, nav)
		if best == nil || distance < bestDistance {
			best = s
			bestDistance = distance
		}
	}
	return best
}

func oppositeDirection(nav NavDirection) NavDirection {
	switch nav {
	case Left:
		return Right
	case Right:
		return Left
	case Up:
		return Down
	default:
		return Up
	}
}

func splitEdgeDistance(split *SplitBorder, focused Rect, nav NavDirection) uint32 {
	var d int32
	switch nav {
	case Left:
		d = int32(split.Pos) - int32(focused.X)
	case Right:
		d = int32(split.Pos) - int32(focused.X+focused.Width)
	case Up:
		d = int32(split.Pos) - int32(focused.Y)
	default:
		d = int32(split.Pos) - int32(focused.Y+focused.Height)
	}
	if d < 0 {
		d = -d
	}
	return uint32(d)
}

func rangeOverlapAmount(aStart, aLen, bStart, bLen uint16) uint16 {
	aEnd := satAddU16(aStart, aLen)
	bEnd := satAddU16(bStart, bLen)
	return satSubU16(min(aEnd, bEnd), max(aStart, bStart))
}

func rangeCenterDistance(aStart, aLen, bStart, bLen uint16) uint16 {
	// Centers are doubled to stay in integers (cf. layout.rs).
	aCenter := satAddU16(satMulU16(aStart, 2), aLen)
	bCenter := satAddU16(satMulU16(bStart, 2), bLen)
	if aCenter > bCenter {
		return aCenter - bCenter
	}
	return bCenter - aCenter
}

// --- Tree operations ---

func countPanes(node Node) int {
	switch n := node.(type) {
	case *PaneNode:
		return 1
	case *SplitNode:
		return countPanes(n.First) + countPanes(n.Second)
	}
	return 0
}

func collectPanes(node Node, area Rect, focus PaneID, result *[]PaneInfo) {
	switch n := node.(type) {
	case *PaneNode:
		*result = append(*result, PaneInfo{
			ID:   n.ID,
			Rect: area,
			// InnerRect is set during render when we know if borders are shown.
			InnerRect: area,
			IsFocused: n.ID == focus,
		})
	case *SplitNode:
		a, b := splitRect(area, n.Direction, n.Ratio)
		collectPanes(n.First, a, focus, result)
		collectPanes(n.Second, b, focus, result)
	}
}

func collectSplits(node Node, area Rect, path []bool, result *[]SplitBorder) {
	n, ok := node.(*SplitNode)
	if !ok {
		return
	}
	a, b := splitRect(area, n.Direction, n.Ratio)
	var pos uint16
	if n.Direction == Horizontal {
		pos = a.X + a.Width
	} else {
		pos = a.Y + a.Height
	}
	*result = append(*result, SplitBorder{
		Pos:       pos,
		Direction: n.Direction,
		Ratio:     n.Ratio,
		Area:      area,
		Path:      append([]bool(nil), path...),
	})
	collectSplits(n.First, a, append(append([]bool(nil), path...), false), result)
	collectSplits(n.Second, b, append(append([]bool(nil), path...), true), result)
}

func collectIDs(node Node, ids *[]PaneID) {
	switch n := node.(type) {
	case *PaneNode:
		*ids = append(*ids, n.ID)
	case *SplitNode:
		collectIDs(n.First, ids)
		collectIDs(n.Second, ids)
	}
}

type pathRatio struct {
	path  []bool
	ratio float32
}

func splitRatios(node Node) []pathRatio {
	var out []pathRatio
	var collect func(node Node, path []bool)
	collect = func(node Node, path []bool) {
		n, ok := node.(*SplitNode)
		if !ok {
			return
		}
		out = append(out, pathRatio{path: append([]bool(nil), path...), ratio: n.Ratio})
		collect(n.First, append(path, false))
		collect(n.Second, append(path, true))
	}
	collect(node, nil)
	return out
}

func ratiosEqual(a, b []pathRatio) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ratio != b[i].ratio || len(a[i].path) != len(b[i].path) {
			return false
		}
		for j := range a[i].path {
			if a[i].path[j] != b[i].path[j] {
				return false
			}
		}
	}
	return true
}

func swapPaneIDs(node Node, first, second PaneID) {
	switch n := node.(type) {
	case *PaneNode:
		switch n.ID {
		case first:
			n.ID = second
		case second:
			n.ID = first
		}
	case *SplitNode:
		swapPaneIDs(n.First, first, second)
		swapPaneIDs(n.Second, first, second)
	}
}

func splitAt(node Node, target PaneID, direction Direction, newID PaneID, splitRatio float32) Node {
	switch n := node.(type) {
	case *PaneNode:
		if n.ID == target {
			return &SplitNode{
				Direction: direction,
				Ratio:     splitRatio,
				First:     n,
				Second:    &PaneNode{ID: newID},
			}
		}
		return n
	case *SplitNode:
		n.First = splitAt(n.First, target, direction, newID, splitRatio)
		n.Second = splitAt(n.Second, target, direction, newID, splitRatio)
		return n
	}
	return node
}

func clampRatio(ratio float32) float32 {
	if ratio < 0.1 {
		return 0.1
	}
	if ratio > 0.9 {
		return 0.9
	}
	return ratio
}

func validSplitRatio(ratio float32) float32 {
	if math.IsNaN(float64(ratio)) || math.IsInf(float64(ratio), 0) {
		return 0.5
	}
	return clampRatio(ratio)
}

// removePane removes the target leaf, promoting its sibling. Returns nil when
// the whole subtree is removed.
func removePane(node Node, target PaneID) Node {
	switch n := node.(type) {
	case *PaneNode:
		if n.ID == target {
			return nil
		}
		return n
	case *SplitNode:
		f := removePane(n.First, target)
		s := removePane(n.Second, target)
		switch {
		case f == nil && s == nil:
			return nil
		case f == nil:
			return s
		case s == nil:
			return f
		default:
			n.First = f
			n.Second = s
			return n
		}
	}
	return node
}

func setRatioAt(node Node, path []bool, newRatio float32) {
	n, ok := node.(*SplitNode)
	if !ok {
		return
	}
	if len(path) == 0 {
		n.Ratio = newRatio
	} else if path[0] {
		setRatioAt(n.Second, path[1:], newRatio)
	} else {
		setRatioAt(n.First, path[1:], newRatio)
	}
}

func getRatioAt(node Node, path []bool) (float32, bool) {
	n, ok := node.(*SplitNode)
	if !ok {
		return 0, false
	}
	if len(path) == 0 {
		return n.Ratio, true
	}
	if path[0] {
		return getRatioAt(n.Second, path[1:])
	}
	return getRatioAt(n.First, path[1:])
}

// splitRect divides area per the ratio. Parity with layout.rs: the first
// chunk is round(len*ratio) computed in f32, the second is a saturating
// remainder.
func splitRect(area Rect, direction Direction, ratio float32) (Rect, Rect) {
	if direction == Horizontal {
		firstW := roundF32ToU16(float32(area.Width) * ratio)
		secondW := satSubU16(area.Width, firstW)
		return Rect{X: area.X, Y: area.Y, Width: firstW, Height: area.Height},
			Rect{X: area.X + firstW, Y: area.Y, Width: secondW, Height: area.Height}
	}
	firstH := roundF32ToU16(float32(area.Height) * ratio)
	secondH := satSubU16(area.Height, firstH)
	return Rect{X: area.X, Y: area.Y, Width: area.Width, Height: firstH},
		Rect{X: area.X, Y: area.Y + firstH, Width: area.Width, Height: secondH}
}

// roundF32ToU16 mirrors Rust's `f.round() as u16`: round half away from
// zero, then saturate into u16 (NaN -> 0).
func roundF32ToU16(v float32) uint16 {
	r := math.Round(float64(v))
	if math.IsNaN(r) || r <= 0 {
		return 0
	}
	if r >= math.MaxUint16 {
		return math.MaxUint16
	}
	return uint16(r)
}

func satAddU16(a, b uint16) uint16 {
	if s := uint32(a) + uint32(b); s <= math.MaxUint16 {
		return uint16(s)
	}
	return math.MaxUint16
}

func satSubU16(a, b uint16) uint16 {
	if b >= a {
		return 0
	}
	return a - b
}

func satMulU16(a, b uint16) uint16 {
	if p := uint32(a) * uint32(b); p <= math.MaxUint16 {
		return uint16(p)
	}
	return math.MaxUint16
}
