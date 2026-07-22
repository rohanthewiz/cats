package browserproto

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func mustCmd(t *testing.T, id, name string, params any) Cmd {
	t.Helper()
	c, err := NewCmd(id, name, params)
	if err != nil {
		t.Fatalf("NewCmd(%s): %v", name, err)
	}
	return c
}

func mustCmdResult(t *testing.T, id string, ok bool, errMsg string, data any) CmdResult {
	t.Helper()
	r, err := NewCmdResult(id, ok, errMsg, data)
	if err != nil {
		t.Fatalf("NewCmdResult: %v", err)
	}
	return r
}

// TestRoundTrip pins that every message survives Marshal → Decode unchanged
// (the discipline of β's protocol_test.go, one entry per message type).
func TestRoundTrip(t *testing.T) {
	pane := uint32(7)
	sb := Rect{78, 0, 1, 24}
	tests := []struct {
		name   string
		msg    any
		decode func([]byte) (any, error)
	}{
		// Down.
		{"welcome", NewWelcome("too old"), DecodeDown},
		{"layout", Layout{T: MsgLayout,
			Workspaces: []WorkspaceInfo{{ID: "w1", Name: "proj", Active: true, AgentSummary: "1 working"}},
			Tabs:       []TabInfo{{Num: 1, Name: "1", Active: true}, {Num: 3, Name: "logs", Zoomed: true}},
			Panes: []PaneRectInfo{{Pane: 5, Pub: "w1:p1", Rect: Rect{0, 0, 40, 24},
				Inner: Rect{1, 1, 38, 22}, Scrollbar: &sb, Focused: true}},
			Borders: []BorderInfo{{ID: "r0", Pos: 40, Dir: 0, Ratio: 0.5, Area: Rect{0, 0, 80, 24}}},
		}, DecodeDown},
		{"agents", NewAgents([]AgentItem{{Pane: 5, Pub: "w1:p1", Workspace: "w1",
			Agent: "claude", State: AgentWorking, Seen: true}}), DecodeDown},
		{"pane_title", NewPaneTitle(pane, "vim"), DecodeDown},
		{"pane_cwd", NewPaneCwd(pane, "/tmp/x"), DecodeDown},
		{"pane_agent", NewPaneAgent(pane, "claude", AgentBlocked, false), DecodeDown},
		{"pane_modes", PaneModes{T: MsgPaneModes, Pane: pane, Mouse: true, AltScreen: true}, DecodeDown},
		{"pane_exited", NewPaneExited(pane, 130), DecodeDown},
		{"pane_frame", PaneFrame{T: MsgPaneFrame, Pane: pane, W: 2, H: 1,
			Cur: Cursor{X: 1, Vis: true, Shape: 6}, DefFg: 0x02c8c8c8, DefBg: 0x02000000,
			Links:  []string{"https://example.com"},
			Cells:  []Cell{{S: "h", F: 0x02cc6666, M: 1, H: 1}, {S: " "}},
			Scroll: &Scroll{Off: 2, Max: 10, Rows: 24}}, DecodeDown},
		{"pane_diff", PaneDiff{T: MsgPaneDiff, Pane: pane,
			Cur:   &Cursor{X: 3, Y: 2, Vis: true, Shape: 2},
			Cells: []DiffCell{{I: 3, Cell: Cell{S: "x", B: 0x02112233}}}}, DecodeDown},
		{"clipboard", NewClipboard([]byte("copied")), DecodeDown},
		{"notify", NewNotify("agent", "claude is blocked", "w1:p3"), DecodeDown},
		{"title", NewTitle("cats — proj"), DecodeDown},
		{"error", NewError(pane, "pane gone"), DecodeDown},
		{"shutdown", NewShutdown(), DecodeDown},
		{"update_ready", NewUpdateReady("1.2.3", "brew upgrade cats"), DecodeDown},
		{"cmd_result", mustCmdResult(t, "42", true, "", ReadResult{Text: "hello\n"}), DecodeDown},

		// Up.
		{"init", Init{T: MsgInit, V: 1, Cols: 120, Rows: 40, DPR: 2, CellWPx: 9, CellHPx: 18}, DecodeUp},
		{"key", Key{T: MsgKey, Code: "KeyA", Key: "a", Mods: ModCtrl | ModAlt, Kind: KeyDown}, DecodeUp},
		{"mouse", Mouse{T: MsgMouse, Pane: pane, X: 10, Y: 5, Btn: BtnNone,
			Kind: MouseWheel, Mods: ModShift, DY: -3}, DecodeUp},
		{"paste", Paste{T: MsgPaste, Data: "ls -la\n"}, DecodeUp},
		{"image", Image{T: MsgImage, Data: []byte{0x89, 0x50}, Ext: "png"}, DecodeUp},
		{"resize", Resize{T: MsgResize, Cols: 200, Rows: 60}, DecodeUp},
		{"raw", Raw{T: MsgRaw, Data: []byte{0x1b, '[', 'A'}}, DecodeUp},
		{"cmd", mustCmd(t, "9", CmdPaneSplit, SplitParams{Direction: SplitV}), DecodeUp},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := Marshal(tc.msg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got, err := tc.decode(data)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			// Decode returns a pointer to the concrete struct.
			want := reflect.New(reflect.TypeOf(tc.msg))
			want.Elem().Set(reflect.ValueOf(tc.msg))
			if !reflect.DeepEqual(got, want.Interface()) {
				t.Fatalf("round-trip mismatch:\n got  %#v\n want %#v", got, tc.msg)
			}
		})
	}
}

// TestWireShapes pins exact JSON for representative messages so field names
// and omission rules can't drift from the spec (phase-c-ws9-protocol.md).
func TestWireShapes(t *testing.T) {
	tests := []struct {
		name string
		msg  any
		want string
	}{
		{"key", Key{T: MsgKey, Code: "KeyA", Key: "a", Mods: 6, Kind: "d"},
			`{"t":"key","code":"KeyA","key":"a","mods":6,"kind":"d"}`},
		{"pane_diff omits defaults and scroll", PaneDiff{T: MsgPaneDiff, Pane: 7,
			Cur:   &Cursor{X: 1, Y: 2, Vis: true, Shape: 2},
			Cells: []DiffCell{{I: 3, Cell: Cell{S: "x"}}}},
			`{"t":"pane_diff","pane":7,"cur":{"x":1,"y":2,"vis":true,"shape":2},"cells":[{"i":3,"s":"x"}]}`},
		{"pane_frame", PaneFrame{T: MsgPaneFrame, Pane: 3, W: 2, H: 1,
			Cur: Cursor{X: 1, Vis: true, Shape: 2}, DefFg: 1, DefBg: 2,
			Cells: []Cell{{S: "h", F: 9, M: 1}, {S: " "}}},
			`{"t":"pane_frame","pane":3,"w":2,"h":1,"cur":{"x":1,"y":0,"vis":true,"shape":2},"def_fg":1,"def_bg":2,"cells":[{"s":"h","f":9,"m":1},{"s":" "}]}`},
		{"layout", Layout{T: MsgLayout,
			Workspaces: []WorkspaceInfo{{ID: "w1", Name: "a", Active: true}},
			Tabs:       []TabInfo{{Num: 1, Name: "1", Active: true}},
			Panes: []PaneRectInfo{{Pane: 5, Pub: "w1:p1", Rect: Rect{0, 0, 80, 24},
				Inner: Rect{0, 0, 80, 24}, Focused: true}},
			Borders: []BorderInfo{}},
			`{"t":"layout","workspaces":[{"id":"w1","name":"a","active":true}],"tabs":[{"num":1,"name":"1","active":true,"zoomed":false}],"panes":[{"pane":5,"pub":"w1:p1","rect":[0,0,80,24],"inner":[0,0,80,24],"focused":true}],"borders":[]}`},
		{"cmd omits nil pane and empty id", func() any {
			c, _ := NewCmd("", CmdPaneSplit, SplitParams{Direction: SplitV})
			return c
		}(),
			`{"t":"cmd","name":"pane.split","params":{"direction":"v"}}`},
		{"parameterless cmd", func() any {
			c, _ := NewCmd("5", CmdServerReloadConfig, nil)
			return c
		}(),
			`{"t":"cmd","id":"5","name":"server.reload_config"}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := Marshal(tc.msg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(data) != tc.want {
				t.Fatalf("wire shape drift:\n got  %s\n want %s", data, tc.want)
			}
		})
	}
}

func TestUnknownTypeIgnorable(t *testing.T) {
	for _, decode := range []func([]byte) (any, error){DecodeUp, DecodeDown} {
		if _, err := decode([]byte(`{"t":"bogus_v9"}`)); !errors.Is(err, ErrUnknownType) {
			t.Errorf("unknown t should report ErrUnknownType, got %v", err)
		}
	}
	// Direction separation: an up-only type is unknown to the down decoder.
	data, _ := Marshal(Init{T: MsgInit, V: 1})
	if _, err := DecodeDown(data); !errors.Is(err, ErrUnknownType) {
		t.Errorf("init is not a down message, got %v", err)
	}
	if _, err := DecodeUp([]byte(`{not json`)); err == nil || errors.Is(err, ErrUnknownType) {
		t.Errorf("malformed JSON should be a plain error, got %v", err)
	}
}

func TestCmdParamsRoundTrip(t *testing.T) {
	paneID := uint32(12)
	c := mustCmd(t, "3", CmdPaneSplit, SplitParams{Pane: &paneID, Direction: SplitH})
	data, err := Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := DecodeUp(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	cmd, ok := got.(*Cmd)
	if !ok || cmd.Name != CmdPaneSplit || cmd.ID != "3" {
		t.Fatalf("decoded cmd = %#v", got)
	}
	var p SplitParams
	if err := json.Unmarshal(cmd.Params, &p); err != nil {
		t.Fatalf("params: %v", err)
	}
	if p.Pane == nil || *p.Pane != paneID || p.Direction != SplitH {
		t.Fatalf("params = %+v", p)
	}
	if d, ok := SplitDirection(p.Direction); !ok || d != 0 {
		t.Fatalf("SplitDirection(%q) = %v, %v (want layout.Horizontal)", p.Direction, d, ok)
	}
}

func TestDirectionMappings(t *testing.T) {
	if _, ok := SplitDirection("x"); ok {
		t.Error("bad split direction accepted")
	}
	for _, dir := range []string{DirLeft, DirRight, DirUp, DirDown} {
		if _, ok := NavDirection(dir); !ok {
			t.Errorf("NavDirection(%q) rejected", dir)
		}
	}
	if _, ok := NavDirection("northwest"); ok {
		t.Error("bad nav direction accepted")
	}
}

func TestCellOmission(t *testing.T) {
	data, err := Marshal(Cell{S: " "})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"s":" "}` {
		t.Fatalf("default-colored blank cell should marshal minimal, got %s", data)
	}
	for _, field := range []string{`"f"`, `"b"`, `"m"`, `"h"`} {
		if strings.Contains(string(data), field) {
			t.Errorf("field %s should be omitted", field)
		}
	}
}
