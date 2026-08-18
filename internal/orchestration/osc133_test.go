package orchestration

import (
	"reflect"
	"testing"
	"time"
)

func esc(body string) string { return "\x1b]" + body + "\x1b\\" }

// The four marks, the command line, and the exit status — decoded out of a
// stream that also carries ordinary output and unrelated OSC traffic.
func TestOSC133ScanMarks(t *testing.T) {
	var s osc133Scanner
	stream := "$ " + esc("133;A") + esc("133;B") +
		esc("633;E;go test ./...") + esc("133;C") +
		"ok  \tpkg\t0.4s\n" + esc("0;a title") + esc("7;file:///tmp") + esc("133;D;1")
	got := s.scan([]byte(stream))

	want := []shellMark{
		{Kind: markPromptStart},
		{Kind: markInputStart},
		{Kind: markCommandLine, Cmd: "go test ./..."},
		{Kind: markOutputStart},
		{Kind: markCommandEnd, Exit: intp(1)},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d marks, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Kind != want[i].Kind || got[i].Cmd != want[i].Cmd {
			t.Errorf("mark %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if got[4].Exit == nil || *got[4].Exit != 1 {
		t.Errorf("exit = %v, want 1", got[4].Exit)
	}
}

// A sequence split across reads is still recognised: the scanner is fed one byte
// at a time, which is the worst case a slow pipe can produce.
func TestOSC133ScanAcrossReads(t *testing.T) {
	var s osc133Scanner
	stream := []byte(esc("633;E;ls -la") + esc("133;C"))
	var got []shellMark
	for _, c := range stream {
		got = append(got, s.scan([]byte{c})...)
	}
	if len(got) != 2 || got[0].Cmd != "ls -la" || got[1].Kind != markOutputStart {
		t.Fatalf("marks = %+v", got)
	}
}

// BEL is a legal terminator too, and every emitter in the wild uses one or the
// other.
func TestOSC133BelTerminator(t *testing.T) {
	var s osc133Scanner
	got := s.scan([]byte("\x1b]133;A\x07\x1b]133;D;0\x07"))
	if len(got) != 2 || got[0].Kind != markPromptStart || got[1].Exit == nil || *got[1].Exit != 0 {
		t.Fatalf("marks = %+v", got)
	}
}

// Everything the scanner must NOT turn into a mark.
func TestOSC133IgnoresTheRest(t *testing.T) {
	cases := []struct{ name, stream string }{
		{"other osc commands", esc("7;file:///tmp") + esc("52;c;aGk=") + esc("9;4;3;")},
		{"unknown 133 subcommand", esc("133;P;key=value")},
		{"vscode marks other than E", esc("633;A") + esc("633;C") + esc("633;P;Cwd=/tmp")},
		{"bare 133", esc("133;")},
		{"plain text that mentions 133", "OSC 133;C is a mark"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var s osc133Scanner
			if got := s.scan([]byte(c.stream)); len(got) != 0 {
				t.Errorf("marks = %+v, want none", got)
			}
		})
	}
}

// D's parameter is trusted only when it is a number. A wrong exit code in a
// ledger is worse than an absent one — it is the field "what failed" filters on.
func TestOSC133ExitParsing(t *testing.T) {
	cases := []struct {
		body string
		want *int
	}{
		{"133;D", nil},
		{"133;D;", nil},
		{"133;D;0", intp(0)},
		{"133;D;130", intp(130)},
		{"133;D;abc", nil},
		{"133;D;2;extra", intp(2)},
	}
	for _, c := range cases {
		var s osc133Scanner
		got := s.scan([]byte(esc(c.body)))
		if len(got) != 1 {
			t.Fatalf("%s: got %d marks", c.body, len(got))
		}
		if !reflect.DeepEqual(got[0].Exit, c.want) {
			t.Errorf("%s: exit = %v, want %v", c.body, deref(got[0].Exit), deref(c.want))
		}
	}
}

// The command line survives VSCode's escaping, which exists because ";" and
// newlines would otherwise break the OSC framing.
func TestUnescape633(t *testing.T) {
	cases := []struct{ in, want string }{
		{"git status", "git status"},
		{`echo a\x3bb`, "echo a;b"},
		{`printf '1\x0a2'`, "printf '1\n2'"},
		{`echo \\`, `echo \`},
		{`trailing\`, `trailing\`},
		{`bad \xZZ escape`, `bad \xZZ escape`},
	}
	for _, c := range cases {
		if got := unescape633(c.in); got != c.want {
			t.Errorf("unescape633(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// An overlong body is abandoned rather than buffered without bound, and the
// scanner keeps working afterwards.
func TestOSC133BoundsItsBuffer(t *testing.T) {
	var s osc133Scanner
	junk := make([]byte, osc133MaxLen+64)
	for i := range junk {
		junk[i] = 'x'
	}
	s.scan([]byte("\x1b]"))
	s.scan(junk)
	if got := s.scan([]byte(esc("133;A"))); len(got) != 1 || got[0].Kind != markPromptStart {
		t.Fatalf("scanner did not recover: %+v", got)
	}
}

// --- pairing ------------------------------------------------------------------

// The ordinary cycle: a command line, a start, an end with its status and the
// duration measured between them.
func TestCmdTrackerPairsACommand(t *testing.T) {
	var tr cmdTracker
	t0 := time.Unix(1000, 0)

	if _, ok := tr.mark(shellMark{Kind: markPromptStart}, t0); ok {
		t.Fatal("prompt start produced a record")
	}
	if _, ok := tr.mark(shellMark{Kind: markCommandLine, Cmd: "go build ./..."}, t0); ok {
		t.Fatal("a command line alone produced a record")
	}
	start, ok := tr.mark(shellMark{Kind: markOutputStart}, t0)
	if !ok || !start.Start || start.Cmd != "go build ./..." {
		t.Fatalf("start = %+v (ok=%v)", start, ok)
	}
	end, ok := tr.mark(shellMark{Kind: markCommandEnd, Exit: intp(2)}, t0.Add(1500*time.Millisecond))
	if !ok || end.Start || end.Exit == nil || *end.Exit != 2 || end.Duration != 1500*time.Millisecond {
		t.Fatalf("end = %+v (ok=%v)", end, ok)
	}
}

// A bare Enter emits a whole A-B-C-D cycle with no command line. Recording it
// would fill the ledger with blank rows that look like a bug.
func TestCmdTrackerDropsAnEmptyCommand(t *testing.T) {
	var tr cmdTracker
	t0 := time.Unix(1000, 0)
	tr.mark(shellMark{Kind: markPromptStart}, t0)
	tr.mark(shellMark{Kind: markInputStart}, t0)
	if _, ok := tr.mark(shellMark{Kind: markOutputStart}, t0); ok {
		t.Fatal("recorded a command with no command line")
	}
	if _, ok := tr.mark(shellMark{Kind: markCommandEnd, Exit: intp(0)}, t0); ok {
		t.Fatal("recorded an end for a command that never started")
	}
}

// A command line does not leak into the NEXT command: a new prompt clears it, so
// an integration that reports the line only sometimes cannot mislabel a run.
func TestCmdTrackerClearsTheCommandAtThePrompt(t *testing.T) {
	var tr cmdTracker
	t0 := time.Unix(1000, 0)
	tr.mark(shellMark{Kind: markCommandLine, Cmd: "rm -rf /tmp/x"}, t0)
	tr.mark(shellMark{Kind: markOutputStart}, t0)
	tr.mark(shellMark{Kind: markCommandEnd, Exit: intp(0)}, t0)

	tr.mark(shellMark{Kind: markPromptStart}, t0)
	if _, ok := tr.mark(shellMark{Kind: markOutputStart}, t0); ok {
		t.Fatal("the previous command's text was reused for the next run")
	}
}

// A command whose D never arrives — killed shell, half-installed integration —
// is closed at the next prompt with an unknown status. A row that says
// "finished, status unknown" is true; one that stays running is not.
func TestCmdTrackerClosesAnAbandonedCommand(t *testing.T) {
	var tr cmdTracker
	t0 := time.Unix(1000, 0)
	tr.mark(shellMark{Kind: markCommandLine, Cmd: "sleep 100"}, t0)
	tr.mark(shellMark{Kind: markOutputStart}, t0)

	end, ok := tr.mark(shellMark{Kind: markPromptStart}, t0.Add(3*time.Second))
	if !ok || end.Start || end.Exit != nil || end.Duration != 3*time.Second {
		t.Fatalf("end = %+v (ok=%v)", end, ok)
	}
	// And the tracker is clean afterwards.
	if _, ok := tr.mark(shellMark{Kind: markCommandEnd, Exit: intp(0)}, t0); ok {
		t.Fatal("a late D reopened the closed command")
	}
}

func intp(v int) *int { return &v }

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
