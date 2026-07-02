package layout

import "testing"

// f32Epsilon is Rust's f32::EPSILON (2^-23), used by the ported ratio asserts.
const f32Epsilon = float32(0x1p-23)

func almostEq(a, b float32) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < f32Epsilon
}

func pane(id uint32) PaneID { return PaneID(id) }

// sampleLayout mirrors layout.rs sample_layout: H(0.3){1, V(0.6){2, H(0.4){3,4}}},
// focus on pane 2.
func sampleLayout() *TileLayout {
	return FromSaved(
		&SplitNode{
			Direction: Horizontal,
			Ratio:     0.3,
			First:     &PaneNode{ID: pane(1)},
			Second: &SplitNode{
				Direction: Vertical,
				Ratio:     0.6,
				First:     &PaneNode{ID: pane(2)},
				Second: &SplitNode{
					Direction: Horizontal,
					Ratio:     0.4,
					First:     &PaneNode{ID: pane(3)},
					Second:    &PaneNode{ID: pane(4)},
				},
			},
		},
		pane(2),
	)
}

type idRect struct {
	id   PaneID
	rect Rect
}

func paneRects(l *TileLayout) []idRect {
	var out []idRect
	for _, info := range l.Panes(Rect{0, 0, 100, 40}) {
		out = append(out, idRect{id: info.ID, rect: info.Rect})
	}
	return out
}

func paneRect(t *testing.T, l *TileLayout, paneID PaneID) Rect {
	t.Helper()
	for _, ir := range paneRects(l) {
		if ir.id == paneID {
			return ir.rect
		}
	}
	t.Fatalf("pane %d should exist", paneID)
	return Rect{}
}

type dirRatio struct {
	direction Direction
	ratio     float32
}

func splitSnapshot(l *TileLayout) []dirRatio {
	var out []dirRatio
	var collect func(node Node)
	collect = func(node Node) {
		if n, ok := node.(*SplitNode); ok {
			out = append(out, dirRatio{direction: n.Direction, ratio: n.Ratio})
			collect(n.First)
			collect(n.Second)
		}
	}
	collect(l.Root())
	return out
}

func snapshotsEqual(a, b []dirRatio) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func rectsEqual(a, b []idRect) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSwapPanesExchangesLeafIDsWithoutChangingCells(t *testing.T) {
	layout := sampleLayout()
	beforeRects := paneRects(layout)
	beforeSplits := splitSnapshot(layout)

	if !layout.SwapPanes(pane(2), pane(4)) {
		t.Fatal("swap should succeed")
	}

	if got := layout.PaneCount(); got != 4 {
		t.Fatalf("pane count = %d, want 4", got)
	}
	if !snapshotsEqual(splitSnapshot(layout), beforeSplits) {
		t.Fatal("split snapshot changed")
	}
	if layout.Focused() != pane(2) {
		t.Fatalf("focused = %d, want 2", layout.Focused())
	}

	afterRects := paneRects(layout)
	if afterRects[0] != beforeRects[0] {
		t.Fatalf("rect[0] changed: %+v != %+v", afterRects[0], beforeRects[0])
	}
	if want := (idRect{id: pane(4), rect: beforeRects[1].rect}); afterRects[1] != want {
		t.Fatalf("rect[1] = %+v, want %+v", afterRects[1], want)
	}
	if afterRects[2] != beforeRects[2] {
		t.Fatalf("rect[2] changed: %+v != %+v", afterRects[2], beforeRects[2])
	}
	if want := (idRect{id: pane(2), rect: beforeRects[3].rect}); afterRects[3] != want {
		t.Fatalf("rect[3] = %+v, want %+v", afterRects[3], want)
	}
}

func TestSwapPanesIsNoopForSameOrMissingPane(t *testing.T) {
	layout := sampleLayout()
	beforeRects := paneRects(layout)
	beforeSplits := splitSnapshot(layout)
	beforeFocus := layout.Focused()

	if layout.SwapPanes(pane(2), pane(2)) {
		t.Fatal("same-pane swap should fail")
	}
	if layout.SwapPanes(pane(2), pane(99)) {
		t.Fatal("missing-second swap should fail")
	}
	if layout.SwapPanes(pane(99), pane(2)) {
		t.Fatal("missing-first swap should fail")
	}

	if !rectsEqual(paneRects(layout), beforeRects) {
		t.Fatal("pane rects changed")
	}
	if !snapshotsEqual(splitSnapshot(layout), beforeSplits) {
		t.Fatal("split snapshot changed")
	}
	if layout.Focused() != beforeFocus {
		t.Fatalf("focus changed: %d != %d", layout.Focused(), beforeFocus)
	}
}

func TestSplitFocusedWithRatioSetsNewSplitRatio(t *testing.T) {
	layout, root := New()
	layout.FocusPane(root)

	layout.SplitFocusedWithRatio(Horizontal, 0.333)

	splits := splitSnapshot(layout)
	if len(splits) != 1 {
		t.Fatalf("split count = %d, want 1", len(splits))
	}
	if splits[0].direction != Horizontal {
		t.Fatal("split direction should be horizontal")
	}
	if !almostEq(splits[0].ratio, 0.333) {
		t.Fatalf("ratio = %v, want 0.333", splits[0].ratio)
	}
}

func TestResizePanePreservesFocusAndReportsChange(t *testing.T) {
	layout := sampleLayout()
	originalFocus := layout.Focused()

	if !layout.ResizePane(pane(1), Right, 0.05, Rect{0, 0, 100, 40}) {
		t.Fatal("resize should report a change")
	}

	if layout.Focused() != originalFocus {
		t.Fatalf("focus = %d, want %d", layout.Focused(), originalFocus)
	}
	split := splitSnapshot(layout)[0]
	if split.direction != Horizontal {
		t.Fatal("split direction should be horizontal")
	}
	if !almostEq(split.ratio, 0.35) {
		t.Fatalf("ratio = %v, want 0.35", split.ratio)
	}
}

func TestResizeSecondChildTowardSplitDecreasesRatio(t *testing.T) {
	layout, root := New()
	right := layout.SplitFocused(Horizontal)
	layout.FocusPane(root)

	if !layout.ResizePane(right, Left, 0.05, Rect{0, 0, 100, 40}) {
		t.Fatal("resize should report a change")
	}

	split := splitSnapshot(layout)[0]
	if split.direction != Horizontal {
		t.Fatal("split direction should be horizontal")
	}
	if !almostEq(split.ratio, 0.45) {
		t.Fatalf("ratio = %v, want 0.45", split.ratio)
	}
	if layout.Focused() != root {
		t.Fatalf("focus = %d, want %d", layout.Focused(), root)
	}
}

func TestResizeOuterEdgesShrinkFocusedPane(t *testing.T) {
	cases := []struct {
		name        string
		direction   Direction
		resizeFirst bool // resize the first child (root pane) vs the new pane
		nav         NavDirection
		wantRatio   float32
	}{
		{"horizontal first child left", Horizontal, true, Left, 0.45},
		{"horizontal second child right", Horizontal, false, Right, 0.55},
		{"vertical first child up", Vertical, true, Up, 0.45},
		{"vertical second child down", Vertical, false, Down, 0.55},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			layout, first := New()
			second := layout.SplitFocused(tc.direction)
			target := second
			if tc.resizeFirst {
				target = first
			}

			if !layout.ResizePane(target, tc.nav, 0.05, Rect{0, 0, 100, 40}) {
				t.Fatal("resize should report a change")
			}
			split := splitSnapshot(layout)[0]
			if split.direction != tc.direction {
				t.Fatalf("split direction = %v, want %v", split.direction, tc.direction)
			}
			if !almostEq(split.ratio, tc.wantRatio) {
				t.Fatalf("ratio = %v, want %v", split.ratio, tc.wantRatio)
			}
		})
	}
}

func TestResizeOuterEdgeFallsBackToHorizontalAncestorSplit(t *testing.T) {
	layout := FromSaved(
		&SplitNode{
			Direction: Horizontal,
			Ratio:     0.6,
			First: &SplitNode{
				Direction: Vertical,
				Ratio:     0.5,
				First:     &PaneNode{ID: pane(1)},
				Second:    &PaneNode{ID: pane(2)},
			},
			Second: &PaneNode{ID: pane(3)},
		},
		pane(1),
	)
	before := paneRect(t, layout, pane(1))

	if !layout.ResizePane(pane(1), Left, 0.05, Rect{0, 0, 100, 40}) {
		t.Fatal("resize should report a change")
	}

	after := paneRect(t, layout, pane(1))
	if after.Height != before.Height {
		t.Fatalf("height changed: %d != %d", after.Height, before.Height)
	}
	if after.Width >= before.Width {
		t.Fatalf("width should shrink: %d >= %d", after.Width, before.Width)
	}
	splits := splitSnapshot(layout)
	if splits[0].direction != Horizontal {
		t.Fatal("first split should be horizontal")
	}
	if !almostEq(splits[0].ratio, 0.55) {
		t.Fatalf("ratio = %v, want 0.55", splits[0].ratio)
	}
	if want := (dirRatio{Vertical, 0.5}); splits[1] != want {
		t.Fatalf("splits[1] = %+v, want %+v", splits[1], want)
	}
}

func TestResizeOuterEdgeFallsBackToVerticalAncestorSplit(t *testing.T) {
	layout := FromSaved(
		&SplitNode{
			Direction: Vertical,
			Ratio:     0.6,
			First: &SplitNode{
				Direction: Horizontal,
				Ratio:     0.5,
				First:     &PaneNode{ID: pane(1)},
				Second:    &PaneNode{ID: pane(2)},
			},
			Second: &PaneNode{ID: pane(3)},
		},
		pane(1),
	)
	before := paneRect(t, layout, pane(1))

	if !layout.ResizePane(pane(1), Up, 0.05, Rect{0, 0, 100, 40}) {
		t.Fatal("resize should report a change")
	}

	after := paneRect(t, layout, pane(1))
	if after.Width != before.Width {
		t.Fatalf("width changed: %d != %d", after.Width, before.Width)
	}
	if after.Height >= before.Height {
		t.Fatalf("height should shrink: %d >= %d", after.Height, before.Height)
	}
	splits := splitSnapshot(layout)
	if splits[0].direction != Vertical {
		t.Fatal("first split should be vertical")
	}
	if !almostEq(splits[0].ratio, 0.55) {
		t.Fatalf("ratio = %v, want 0.55", splits[0].ratio)
	}
	if want := (dirRatio{Horizontal, 0.5}); splits[1] != want {
		t.Fatalf("splits[1] = %+v, want %+v", splits[1], want)
	}
}

func TestResizeUsesSplitInSameBranchWhenBordersShareCoordinate(t *testing.T) {
	layout := FromSaved(
		&SplitNode{
			Direction: Vertical,
			Ratio:     0.5,
			First: &SplitNode{
				Direction: Horizontal,
				Ratio:     0.5,
				First:     &PaneNode{ID: pane(1)},
				Second:    &PaneNode{ID: pane(2)},
			},
			Second: &SplitNode{
				Direction: Horizontal,
				Ratio:     0.5,
				First:     &PaneNode{ID: pane(3)},
				Second:    &PaneNode{ID: pane(4)},
			},
		},
		pane(3),
	)

	if !layout.ResizePane(pane(3), Right, 0.05, Rect{0, 0, 100, 40}) {
		t.Fatal("resize should report a change")
	}

	splits := splitSnapshot(layout)
	if want := (dirRatio{Vertical, 0.5}); splits[0] != want {
		t.Fatalf("splits[0] = %+v, want %+v", splits[0], want)
	}
	if want := (dirRatio{Horizontal, 0.5}); splits[1] != want {
		t.Fatalf("splits[1] = %+v, want %+v", splits[1], want)
	}
	if splits[2].direction != Horizontal {
		t.Fatal("splits[2] should be horizontal")
	}
	if !almostEq(splits[2].ratio, 0.55) {
		t.Fatalf("splits[2] ratio = %v, want 0.55", splits[2].ratio)
	}
}

func TestFindInDirectionTiebreaksByLargerOverlapBeforeLayoutOrder(t *testing.T) {
	focused := PaneInfo{
		ID:        pane(1),
		Rect:      Rect{10, 10, 10, 10},
		InnerRect: Rect{10, 10, 10, 10},
		IsFocused: true,
	}
	smallOverlapFirst := PaneInfo{
		ID:        pane(2),
		Rect:      Rect{0, 10, 10, 2},
		InnerRect: Rect{0, 10, 10, 2},
	}
	largerOverlapSecond := PaneInfo{
		ID:        pane(3),
		Rect:      Rect{0, 10, 10, 8},
		InnerRect: Rect{0, 10, 10, 8},
	}
	panes := []PaneInfo{focused, smallOverlapFirst, largerOverlapSecond}

	got, ok := FindInDirection(&focused, Left, panes)
	if !ok {
		t.Fatal("expected a pane to the left")
	}
	if got != pane(3) {
		t.Fatalf("got pane %d, want 3", got)
	}
}
