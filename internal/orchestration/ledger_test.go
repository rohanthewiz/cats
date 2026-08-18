//go:build ghostty

package orchestration

import (
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
)

// The command ledger, end to end through a real daemon and a real shell: the
// capability is advertised, nothing is reported until a client subscribes, and
// a shell printing OSC 133 marks produces one start and one end carrying the
// command, the directory it ran in, its status and its duration.

func TestCommandLedgerCapabilityAdvertised(t *testing.T) {
	c := dialHost(t, nil)
	w := handshake(t, c, NewHello())
	if !contains(w.Features, FeatureCommandLedger) {
		t.Fatalf("features = %v, want %s", w.Features, FeatureCommandLedger)
	}
}

// The subscription is the whole cost control: a pane whose output nobody asked
// to scan is not scanned, so a shell emitting a full command cycle produces
// nothing at all.
func TestCommandMarksAreSilentWithoutASubscription(t *testing.T) {
	c := startTestHost(t)
	runMarkedCommand(t, c, 20, "echo hi", 0)

	// The pane exits after its script, which is the event that proves the whole
	// cycle ran — and no command_start arrived before it.
	for {
		typ, _ := readEvent(t, c)
		if typ == MsgCommandStart || typ == MsgCommandEnd {
			t.Fatalf("reported %q with no subscription", typ)
		}
		if typ == MsgPaneExited {
			return
		}
	}
}

// The headline path. The shell prints the marks a cats shell integration prints;
// the daemon pairs them and reports one command.
func TestCommandMarksReportACommand(t *testing.T) {
	c := startTestHost(t)
	if err := WriteMessage(c, NewRequestCommandMarks(true)); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	runMarkedCommand(t, c, 21, "go test ./...", 3)

	start := readCommandStart(t, c)
	if start.PaneID != 21 || start.Cmd != "go test ./..." {
		t.Fatalf("start = %+v", start)
	}
	if start.Cwd == "" {
		t.Error("no cwd captured; 'where did I run this' is half the point")
	}

	end := readCommandEnd(t, c)
	if end.PaneID != 21 || end.Exit == nil || *end.Exit != 3 {
		t.Fatalf("end = %+v (exit %v)", end, end.Exit)
	}
	if end.DurationMs < 0 {
		t.Errorf("duration = %dms", end.DurationMs)
	}
}

// The subscription can be turned off again, and then the next command is
// silent — the switch is live, not a start-up flag.
func TestCommandMarksCanBeTurnedOff(t *testing.T) {
	c := startTestHost(t)
	if err := WriteMessage(c, NewRequestCommandMarks(true)); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	runMarkedCommand(t, c, 22, "first", 0)
	readCommandStart(t, c)
	readCommandEnd(t, c)

	if err := WriteMessage(c, NewRequestCommandMarks(false)); err != nil {
		t.Fatalf("unsubscribe: %v", err)
	}
	// The unsubscribe has to be processed before the next pane's output, which
	// the daemon guarantees only in the sense that both go through its dispatch
	// goroutine in order — create_pane is sent after, so it is.
	runMarkedCommand(t, c, 23, "second", 0)
	for {
		typ, _ := readEvent(t, c)
		if typ == MsgCommandStart || typ == MsgCommandEnd {
			t.Fatalf("reported %q after unsubscribing", typ)
		}
		if typ == MsgPaneExited {
			return
		}
	}
}

// A shell that prints the marks but never the command line produces nothing:
// the field the ledger exists for is missing, and an empty Enter emits a full
// cycle. Recording those would fill a history with blank rows.
func TestCommandMarksSkipACommandWithNoText(t *testing.T) {
	c := startTestHost(t)
	if err := WriteMessage(c, NewRequestCommandMarks(true)); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	cp := NewCreatePane(24, 40, 5)
	cp.Command = "/bin/sh"
	cp.Args = []string{"-c", `printf '\033]133;A\033\\\033]133;B\033\\\033]133;C\033\\\033]133;D;0\033\\'`}
	if err := WriteMessage(c, cp); err != nil {
		t.Fatalf("create_pane: %v", err)
	}
	for {
		typ, _ := readEvent(t, c)
		if typ == MsgCommandStart || typ == MsgCommandEnd {
			t.Fatalf("reported %q for a command with no text", typ)
		}
		if typ == MsgPaneExited {
			return
		}
	}
}

// keepAlive holds a test shell open after its marks, because a BLOCK is live
// terminal state: the pane's teardown frees its pins, so a lookup racing the
// exit legitimately answers "not found" and would make these tests flap.
const keepAlive = `; sleep 30`

// runMarkedCommand spawns a pane whose shell prints one full OSC 133 cycle for
// cmd, exiting with the given status. tail is appended to the script.
func runMarkedCommand(t *testing.T, c net.Conn, pane uint32, cmd string, exit int, tail ...string) {
	t.Helper()
	script := `printf '\033]133;A\033\\'` +
		`; printf '\033]633;E;%s\033\\' "$1"` +
		`; printf '\033]133;C\033\\'` +
		`; printf 'some output\n'` +
		`; printf '\033]133;D;%s\033\\' "$2"` +
		strings.Join(tail, "")
	cp := NewCreatePane(pane, 40, 5)
	cp.Command = "/bin/sh"
	cp.Args = []string{"-c", script, "--", cmd, strconv.Itoa(exit)}
	if err := WriteMessage(c, cp); err != nil {
		t.Fatalf("create_pane: %v", err)
	}
}

func readCommandStart(t *testing.T, c net.Conn) CommandStart {
	t.Helper()
	payload := waitFor(t, c, MsgCommandStart)
	var ev CommandStart
	if err := json.Unmarshal(payload, &ev); err != nil {
		t.Fatalf("decode command_start: %v", err)
	}
	return ev
}

func readCommandEnd(t *testing.T, c net.Conn) CommandEnd {
	t.Helper()
	payload := waitFor(t, c, MsgCommandEnd)
	var ev CommandEnd
	if err := json.Unmarshal(payload, &ev); err != nil {
		t.Fatalf("decode command_end: %v", err)
	}
	return ev
}

// --- blocks -------------------------------------------------------------------

// A recorded command's block resolves to where its output is NOW, and to the
// text between its two marks. Both ids travel on the start/end pair so a client
// can address the block afterwards.
func TestBlockResolvesToItsOutput(t *testing.T) {
	c := startTestHost(t)
	if err := WriteMessage(c, NewRequestCommandMarks(true)); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	runMarkedCommand(t, c, 30, "print the thing", 0, keepAlive)

	start := readCommandStart(t, c)
	if start.Block == 0 {
		t.Fatal("no block id on command_start")
	}
	end := readCommandEnd(t, c)
	if end.Block != start.Block {
		t.Fatalf("end block %d != start block %d", end.Block, start.Block)
	}

	if err := WriteMessage(c, NewRequestBlock(7, 30, start.Block, true)); err != nil {
		t.Fatalf("request_block: %v", err)
	}
	res := readBlockResult(t, c)
	if res.ID != 7 {
		t.Fatalf("reply id = %d, want the request's 7", res.ID)
	}
	if !res.Found {
		t.Fatal("the block was not found while its output is still on screen")
	}
	if !strings.Contains(res.Text, "some output") {
		t.Errorf("text = %q, want the command's output", res.Text)
	}
	if res.EndRow < res.StartRow {
		t.Errorf("rows = %d..%d", res.StartRow, res.EndRow)
	}
	// TopRow is what turns these rows into a scroll. Zero is legitimate for a
	// pane whose viewport is at the very top of its buffer, which is exactly
	// where a short-lived test pane sits — so the assertion is that the block
	// starts at or after it, not that it is non-zero.
	if res.StartRow < res.TopRow {
		t.Errorf("start row %d is above the viewport top %d", res.StartRow, res.TopRow)
	}
}

// A block nobody recorded, and a pane that never existed, both answer "not
// found" rather than erroring — a caller walking a history wants to know which
// entries are still readable, not to have the walk stop.
func TestBlockNotFound(t *testing.T) {
	c := startTestHost(t)
	if err := WriteMessage(c, NewRequestCommandMarks(true)); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	runMarkedCommand(t, c, 31, "one", 0, keepAlive)
	readCommandStart(t, c)
	readCommandEnd(t, c)

	for _, tc := range []struct {
		name  string
		pane  uint32
		block uint64
	}{
		{"unknown block", 31, 9999},
		{"unknown pane", 4242, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := WriteMessage(c, NewRequestBlock(8, tc.pane, tc.block, true)); err != nil {
				t.Fatalf("request_block: %v", err)
			}
			if res := readBlockResult(t, c); res.Found {
				t.Errorf("found a block that does not exist: %+v", res)
			}
		})
	}
}

// Two commands in one pane get distinct blocks addressing distinct output. This
// is the property that makes a history list clickable rather than decorative.
func TestBlocksAreDistinctPerCommand(t *testing.T) {
	c := startTestHost(t)
	if err := WriteMessage(c, NewRequestCommandMarks(true)); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	// One pane, two cycles, each printing something recognisable.
	script := `printf '\033]133;A\033\\'` +
		`; printf '\033]633;E;first\033\\'; printf '\033]133;C\033\\'; printf 'ALPHA\n'` +
		`; printf '\033]133;D;0\033\\'` +
		`; printf '\033]133;A\033\\'` +
		`; printf '\033]633;E;second\033\\'; printf '\033]133;C\033\\'; printf 'BETA\n'` +
		`; printf '\033]133;D;0\033\\'` + keepAlive
	cp := NewCreatePane(32, 40, 6)
	cp.Command = "/bin/sh"
	cp.Args = []string{"-c", script}
	if err := WriteMessage(c, cp); err != nil {
		t.Fatalf("create_pane: %v", err)
	}

	first := readCommandStart(t, c)
	readCommandEnd(t, c)
	second := readCommandStart(t, c)
	readCommandEnd(t, c)
	if first.Block == 0 || second.Block == 0 || first.Block == second.Block {
		t.Fatalf("blocks = %d and %d", first.Block, second.Block)
	}

	if err := WriteMessage(c, NewRequestBlock(1, 32, first.Block, true)); err != nil {
		t.Fatalf("request_block: %v", err)
	}
	a := readBlockResult(t, c)
	if err := WriteMessage(c, NewRequestBlock(2, 32, second.Block, true)); err != nil {
		t.Fatalf("request_block: %v", err)
	}
	b := readBlockResult(t, c)

	if !a.Found || !b.Found {
		t.Fatalf("found = %v / %v", a.Found, b.Found)
	}
	if !strings.Contains(a.Text, "ALPHA") || strings.Contains(a.Text, "BETA") {
		t.Errorf("first block text = %q", a.Text)
	}
	if !strings.Contains(b.Text, "BETA") || strings.Contains(b.Text, "ALPHA") {
		t.Errorf("second block text = %q", b.Text)
	}
}

// text:false skips the extraction — the jump path wants rows and nothing else,
// and formatting a large block to throw it away is the kind of cost that only
// shows up on somebody's slow machine.
func TestBlockCanSkipTheText(t *testing.T) {
	c := startTestHost(t)
	if err := WriteMessage(c, NewRequestCommandMarks(true)); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	runMarkedCommand(t, c, 33, "quiet", 0, keepAlive)
	start := readCommandStart(t, c)
	readCommandEnd(t, c)

	if err := WriteMessage(c, NewRequestBlock(3, 33, start.Block, false)); err != nil {
		t.Fatalf("request_block: %v", err)
	}
	res := readBlockResult(t, c)
	if !res.Found {
		t.Fatal("block not found")
	}
	if res.Text != "" {
		t.Errorf("text = %q, want none", res.Text)
	}
}

func readBlockResult(t *testing.T, c net.Conn) BlockResult {
	t.Helper()
	payload := waitFor(t, c, MsgBlockResult)
	var ev BlockResult
	if err := json.Unmarshal(payload, &ev); err != nil {
		t.Fatalf("decode block_result: %v", err)
	}
	return ev
}
