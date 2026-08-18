//go:build ghostty

package orchestration

import (
	"encoding/json"
	"net"
	"strconv"
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

// runMarkedCommand spawns a pane whose shell prints one full OSC 133 cycle for
// cmd, exiting with the given status.
func runMarkedCommand(t *testing.T, c net.Conn, pane uint32, cmd string, exit int) {
	t.Helper()
	script := `printf '\033]133;A\033\\'` +
		`; printf '\033]633;E;%s\033\\' "$1"` +
		`; printf '\033]133;C\033\\'` +
		`; printf 'some output\n'` +
		`; printf '\033]133;D;%s\033\\' "$2"`
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
