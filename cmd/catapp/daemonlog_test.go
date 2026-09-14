//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// fixedClock stamps every kept line with the same instant, so lines compare.
func fixedClock() time.Time { return time.Date(2026, 9, 13, 22, 8, 31, 0, time.Local) }

// Only warning-and-above survives the tap, lines split across writes are
// reassembled, and the daemon's own timestamp gives way to ours.
func TestDaemonTapKeepsOnlySevereLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemons.log")
	l := newRotatingLog(path, 1<<20)
	tap := &daemonTap{src: "catway", log: l, now: fixedClock}

	_, _ = tap.Write([]byte("2026/09/13 22:08:30 catway: serving at http://localhost:8422\n"))
	_, _ = tap.Write([]byte("2026/09/13 22:08:31 WARN catway: cathost local answ"))
	_, _ = tap.Write([]byte("ered no ping in 1m0s — closing the connection\n2026/09/13 22:08:31 catway: host attached\n"))
	_, _ = tap.Write([]byte("2026/09/13 22:08:32 ERROR catway: save session state: disk full\n"))

	got := strings.Split(strings.TrimSpace(readFile(t, path)), "\n")
	want := []string{
		"2026-09-13 22:08:31.000 catway[0] WARN catway: cathost local answered no ping in 1m0s — closing the connection",
		"2026-09-13 22:08:31.000 catway[0] ERROR catway: save session state: disk full",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("kept:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A crash report is kept whole: the goroutine dump that follows "panic:" has no
// level word on any of its lines, and is still the most useful thing in the file.
func TestDaemonTapKeepsAWholeCrashReport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemons.log")
	l := newRotatingLog(path, 1<<20)
	tap := &daemonTap{src: "cathost", log: l, now: fixedClock}

	_, _ = tap.Write([]byte("2026/09/13 22:08:30 client connected\n" +
		"panic: runtime error: invalid memory address or nil pointer dereference\n" +
		"[signal SIGSEGV: segmentation violation code=0x2 addr=0x0 pc=0x100]\n" +
		"\n" +
		"goroutine 7 [running]:\n" +
		"github.com/rohanthewiz/cats/internal/orchestration.(*Host).emit(...)\n"))

	kept := readFile(t, path)
	if strings.Contains(kept, "client connected") {
		t.Errorf("a routine line before the crash was kept:\n%s", kept)
	}
	for _, want := range []string{"panic: runtime error", "goroutine 7 [running]:", "(*Host).emit"} {
		if !strings.Contains(kept, want) {
			t.Errorf("crash report missing %q:\n%s", want, kept)
		}
	}
}

// Nothing severe, no file: a healthy session leaves nothing behind.
func TestDaemonLogIsNotCreatedForRoutineOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemons.log")
	tap := &daemonTap{src: "catway", log: newRotatingLog(path, 1<<20), now: fixedClock}
	_, _ = tap.Write([]byte("2026/09/13 22:08:30 catway: serving\n"))
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("daemons.log exists after routine output only (stat err %v)", err)
	}
}

// Past the cap the file rotates to .1, replacing the previous generation, and a
// relaunch appends rather than starting over.
func TestRotatingLogRotatesAtTheCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemons.log")
	line := strings.Repeat("x", 39) // 40 bytes with the newline

	l := newRotatingLog(path, 100)
	l.writeLine(line + "1")
	l.writeLine(line + "2") // 82 bytes: under the cap
	l.writeLine(line + "3") // would be 123: rotates first
	if got := readFile(t, path+".1"); !strings.Contains(got, "x1") || !strings.Contains(got, "x2") {
		t.Fatalf(".1 = %q, want lines 1 and 2", got)
	}
	if got := readFile(t, path); strings.TrimSpace(got) != line+"3" {
		t.Fatalf("current = %q, want only line 3", got)
	}

	// A new launch picks up the current size, so it rotates on the same schedule.
	l2 := newRotatingLog(path, 100)
	l2.writeLine(line + "4") // 82 bytes
	l2.writeLine(line + "5") // rotates
	if got := readFile(t, path+".1"); !strings.Contains(got, "x3") || !strings.Contains(got, "x4") {
		t.Fatalf("after relaunch .1 = %q, want lines 3 and 4", got)
	}
}

// An exit nobody asked for is recorded, with how the process ended.
func TestWatcherRecordsAnUnexpectedExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemons.log")
	l := newRotatingLog(path, 1<<20)
	p, err := startDaemon(l, "/bin/sh", "-c", "echo 'WARN about to fail' >&2; exit 3")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("watcher never saw the exit")
	}
	kept := readFile(t, path)
	if !strings.Contains(kept, "sh[") || !strings.Contains(kept, "WARN about to fail") {
		t.Errorf("the daemon's own warning is missing:\n%s", kept)
	}
	if !strings.Contains(kept, "ERROR sh (pid ") || !strings.Contains(kept, "exit status 3") {
		t.Errorf("unexpected exit not recorded as an error with its status:\n%s", kept)
	}
	// The warning must precede the exit line: Wait lets the copiers finish first.
	if strings.Index(kept, "WARN about to fail") > strings.Index(kept, "exited while the app was running") {
		t.Errorf("exit recorded before the daemon's last output:\n%s", kept)
	}
}

// A daemon that ignores SIGTERM is SIGKILLed after its grace, not left behind —
// the orphaned cathost of 2026-09-13 — and a stop we asked for is not reported
// as an unexpected exit.
func TestStopEscalatesToSIGKILL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemons.log")
	l := newRotatingLog(path, 1<<20)
	// exec so the trap applies to the process we signal, not a subshell of it.
	p, err := startDaemon(l, "/bin/sh", "-c", `trap "" TERM; echo ready; exec sleep 60`)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // let the trap install

	start := time.Now()
	p.stop(300 * time.Millisecond)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("stop took %s", took)
	}
	select {
	case <-p.done:
	default:
		t.Fatal("stop returned with the daemon still running")
	}
	if st := p.cmd.ProcessState; st == nil || st.String() != "signal: killed" {
		t.Fatalf("process state %v, want killed", st)
	}
	kept := readFile(t, path)
	if !strings.Contains(kept, "did not exit within 300ms of SIGTERM — sending SIGKILL") {
		t.Errorf("escalation not recorded:\n%s", kept)
	}
	if strings.Contains(kept, "exited while the app was running") {
		t.Errorf("a stop we asked for was reported as unexpected:\n%s", kept)
	}
}

// stop on a daemon that was never started is a no-op, as backend.stop relies on
// for a launch that failed half way.
func TestStopOnNilDaemon(t *testing.T) {
	var p *daemonProc
	p.stop(time.Millisecond)
}
