package orchestration

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/terminal"
)

// A sparse diff is the dense diff with the unchanged cells left out: each run
// is a maximal stretch of changed cells, in grid order, and nothing else.
func TestSparsifyGathersChangedRuns(t *testing.T) {
	c := func(s string) Cell { return Cell{Symbol: s, Fg: 0x02010101, Bg: 0x02000000} }
	skip := Cell{Symbol: " ", Fg: 0x02010101, Bg: 0x02000000, Skip: true}
	f := &Frame{Cols: 4, Rows: 2, Cells: []Cell{
		skip, c("a"), c("b"), skip,
		skip, skip, skip, c("z"),
	}}
	f.Sparsify()
	if !f.Sparse || f.Cells != nil {
		t.Fatalf("not sparse: sparse=%v cells=%d", f.Sparse, len(f.Cells))
	}
	want := []CellRun{{At: 1, Cells: []Cell{c("a"), c("b")}}, {At: 7, Cells: []Cell{c("z")}}}
	if !reflect.DeepEqual(f.Runs, want) {
		t.Fatalf("runs = %+v\nwant  %+v", f.Runs, want)
	}
}

// A full frame is the receiver's new base — it must keep every cell.
func TestSparsifyLeavesFullFramesAlone(t *testing.T) {
	f := &Frame{Cols: 1, Rows: 1, Full: true, Cells: []Cell{{Symbol: "x"}}}
	f.Sparsify()
	if f.Sparse || len(f.Cells) != 1 || f.Runs != nil {
		t.Fatalf("full frame was changed: %+v", f)
	}
}

// Nothing changed but the cursor: still a sparse frame (the cursor has to get
// through), just one with no runs.
func TestSparsifyEmptyDiff(t *testing.T) {
	f := &Frame{Cols: 2, Rows: 1, Cursor: &Cursor{X: 1}, Cells: []Cell{{Skip: true}, {Skip: true}}}
	f.Sparsify()
	if !f.Sparse || len(f.Runs) != 0 || f.Cursor == nil {
		t.Fatalf("empty diff: %+v", f)
	}
}

// The size win, measured on the case that motivated it: one changed cell on a
// 200×50 screen of plain text.
func TestSparseDiffIsSmall(t *testing.T) {
	const cols, rows = 200, 50
	grid := func(mark string) [][]terminal.Cell {
		g := make([][]terminal.Cell, rows)
		for y := range g {
			g[y] = make([]terminal.Cell, cols)
			for x := range g[y] {
				g[y][x] = terminal.Cell{Rune: "a"}
			}
		}
		g[10][10].Rune = mark
		return g
	}
	cur := terminal.Cursor{Visible: true}
	prev := mkSnap(cols, rows, grid("a"), cur)
	next := mkSnap(cols, rows, grid("b"), cur)

	dense, _ := json.Marshal(NewPaneFrame(1, FrameFromSnapshot(next, prev)))
	f := FrameFromSnapshot(next, prev)
	f.Sparsify()
	sparse, _ := json.Marshal(NewPaneFrame(1, f))
	if len(sparse) > 300 {
		t.Errorf("a one-cell sparse diff is %d bytes: %s", len(sparse), sparse)
	}
	if len(dense) < 100*len(sparse) {
		t.Errorf("dense %d B vs sparse %d B — expected a far larger gap", len(dense), len(sparse))
	}
	t.Logf("one-cell diff on %dx%d: dense %d B, sparse %d B", cols, rows, len(dense), len(sparse))
}

// The omitted zero fields decode to exactly the zeros that used to be spelled
// out, so an older reader sees the same cell.
func TestCellZeroFieldsOmitted(t *testing.T) {
	b, err := json.Marshal(Cell{Symbol: "a", Fg: 0x02010203, Bg: 0x02000000})
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"modifier", "skip", "hyperlink"} {
		if strings.Contains(string(b), gone) {
			t.Errorf("%s still on the wire: %s", gone, b)
		}
	}
	var back Cell
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back != (Cell{Symbol: "a", Fg: 0x02010203, Bg: 0x02000000}) {
		t.Errorf("round trip = %+v", back)
	}
	// A link at index 0 is a non-nil pointer, and must survive omitempty.
	zero := uint32(0)
	b, _ = json.Marshal(Cell{Symbol: "a", Hyperlink: &zero})
	if !strings.Contains(string(b), `"hyperlink":0`) {
		t.Errorf("link index 0 dropped: %s", b)
	}
}

func TestHelloFeatures(t *testing.T) {
	h := NewHello()
	if h.HasFeature(ClientFeatureSparseFrames) {
		t.Fatal("a plain hello claims sparse frames")
	}
	b, _ := json.Marshal(h)
	if strings.Contains(string(b), "features") {
		t.Errorf("empty features on the wire: %s", b)
	}
	h.Features = []string{ClientFeatureSparseFrames}
	var back Hello
	b, _ = json.Marshal(h)
	if err := json.Unmarshal(b, &back); err != nil || !back.HasFeature(ClientFeatureSparseFrames) {
		t.Fatalf("feature lost in transit: %s (%v)", b, err)
	}
}

// peekType is a shortcut, never a different answer: whatever it accepts, the
// full decode would have agreed with, and anything unusual falls through.
func TestPeekType(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want MessageType
		ok   bool
	}{
		{`{"type":"pane_frame","pane_id":1}`, MsgPaneFrame, true},
		{`{"type":"hello"}`, MsgHello, true},
		{`{ "type":"hello"}`, "", false},            // whitespace: not our encoder
		{`{"pane_id":1,"type":"hello"}`, "", false}, // reordered
		{`{"type":"he\"llo"}`, "", false},           // escaped quote ends the scan early…
		{`{"type":"a\\b"}`, "", false},              // …and a backslash is refused outright
		{`{"type":""}`, "", false},
		{`{"type":"unterminated`, "", false},
	} {
		got, ok := peekType([]byte(tc.in))
		if ok != tc.ok || got != tc.want {
			t.Errorf("peekType(%s) = %q,%v want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// A sparse frame survives the framing codec, through ReadMessage's type peek.
func TestSparseFrameCodecRoundTrip(t *testing.T) {
	in := NewPaneFrame(42, &Frame{
		Cols: 3, Rows: 1, Sparse: true,
		Cursor: &Cursor{X: 2, Visible: true, Shape: 2},
		Runs:   []CellRun{{At: 1, Cells: []Cell{{Symbol: "x", Fg: 0x02112233, Bg: 0x02000000}}}},
	})
	var buf bytes.Buffer
	if err := WriteMessage(&buf, in); err != nil {
		t.Fatal(err)
	}
	typ, payload, err := ReadMessage(&buf)
	if err != nil || typ != MsgPaneFrame {
		t.Fatalf("ReadMessage = %q, %v", typ, err)
	}
	var out PaneFrame
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip:\n got  %+v\n want %+v", out.Frame, in.Frame)
	}
}
