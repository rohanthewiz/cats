//go:build darwin

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/rohanthewiz/cats/internal/dlog"
)

// The daemon log: every warning, error and crash the supervised daemons write,
// kept in a size-capped file beside boot.log.
//
// Why it exists: launched from Finder, the launcher's stdout and stderr are
// /dev/null, and so were the daemons' once startup ended (the boot tap stops
// recording then). The 2026-09-13 freeze therefore left nothing behind — no
// stalled write, no dropped connection, no exit — and its trigger could only be
// inferred from file mtimes.
//
// Why only warning and above: the daemons narrate normal operation too (hosts
// attaching, configs reloading, every HTTP request under rweb's verbose mode),
// and a file that kept all of it would rotate the one line that matters out of
// existence within hours. Severity is carried in the line itself (internal/dlog),
// so the filter is one prefix check per line.
//
//	daemon stderr ──▶ io.MultiWriter ─┬─▶ our stderr   (a dev terminal still sees everything)
//	                                  ├─▶ bootTap      (startup only)
//	                                  └─▶ daemonTap ──▶ dlog.Classify ──keep──▶ daemons.log
//
// The launcher adds its own lines for what only it can see: a daemon that
// exited when nobody asked it to, and one that would not exit when asked.

// daemonLogFile is the kept log's name, beside app.json and boot.log.
const daemonLogFile = "daemons.log"

// daemonLogMaxBytes caps the file before it rotates to daemons.log.1, which
// replaces the previous one. Two generations of 1 MiB is thousands of warning
// lines — weeks of a healthy session — while staying small enough to attach to
// an issue whole.
const daemonLogMaxBytes = 1 << 20

// daemonLog is the launcher's kept daemon log. A package-level singleton for the
// same reason boot is one: its writers are the output taps exec creates, and
// there is exactly one per process.
var daemonLog = newRotatingLog(daemonLogPath(), daemonLogMaxBytes)

// daemonLogPath is the default location, or "" when the app-data dir cannot be
// located (the log is then simply not kept).
func daemonLogPath() string {
	dir, err := appDataDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, daemonLogFile)
}

// --- the file ----------------------------------------------------------------

// rotatingLog appends whole lines to a file, rotating it to <path>.1 when the
// next line would take it past max. Safe from any goroutine: the four output
// taps (two streams per daemon) and the exit watchers all write to one.
//
// The file is opened on the first line, not at launch, so a session with
// nothing to report creates nothing; and every line is a single write on an
// unbuffered file, so a launcher that dies abruptly loses no kept line.
type rotatingLog struct {
	mu     sync.Mutex
	path   string // "" disables the log
	max    int64
	f      *os.File
	size   int64
	broken bool // opening failed once; don't retry (and re-report) on every line
}

func newRotatingLog(path string, max int64) *rotatingLog {
	return &rotatingLog{path: path, max: max}
}

// writeLine appends line plus a newline.
func (r *rotatingLog) writeLine(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.path == "" || r.broken {
		return
	}
	b := []byte(line + "\n")
	// Rotate before a line that would cross the cap — unless the file is empty,
	// where rotating would only swap one empty file for another and a single
	// over-long line must still be written somewhere.
	if r.f != nil && r.size > 0 && r.size+int64(len(b)) > r.max {
		_ = r.f.Close()
		r.f = nil
		_ = os.Rename(r.path, r.path+".1")
	}
	if r.f == nil && !r.openLocked() {
		return
	}
	n, err := r.f.Write(b)
	r.size += int64(n)
	if err != nil {
		log.Printf("daemon log: write %s: %v", r.path, err)
	}
}

// openLocked opens (or creates) the file for appending and learns its size, so
// a relaunch continues the file rather than resetting the rotation point.
func (r *rotatingLog) openLocked() bool {
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		log.Printf("daemon log: disabled, cannot create %s: %v", filepath.Dir(r.path), err)
		r.broken = true
		return false
	}
	f, err := os.OpenFile(r.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		log.Printf("daemon log: disabled, cannot open %s: %v", r.path, err)
		r.broken = true
		return false
	}
	size := int64(0)
	if st, err := f.Stat(); err == nil {
		size = st.Size()
	}
	r.f, r.size = f, size
	return true
}

// stamp is the time format every kept line starts with. Local time with
// milliseconds: these lines get lined up against boot.log, `log show` and file
// mtimes, all of which are local.
const stamp = "2006-01-02 15:04:05.000"

// note records a line of the launcher's own, about a daemon: "ERROR catway
// (pid 123) exited while the app was running: …". level is one of the dlog
// level words.
func (r *rotatingLog) note(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Print(msg) // a dev terminal sees it too
	r.writeLine(fmt.Sprintf("%s catapp[%d] %s %s", time.Now().Format(stamp), os.Getpid(), level, msg))
}

// --- the tap -------------------------------------------------------------------

// daemonTap is one daemon stream's filter into the kept log: split into lines,
// keep what dlog classifies as severe, and — once a crash report starts — keep
// everything after it, since a goroutine dump is only useful whole.
//
// One tap per stream (not one per daemon) for the reason bootTap gives: the two
// streams are copied by different goroutines, and a shared partial-line buffer
// would splice them together.
type daemonTap struct {
	src string
	cmd *exec.Cmd // for the pid; exec sets Process before it starts copying output
	log *rotatingLog
	now func() time.Time
	mu  sync.Mutex
	buf []byte
	// crashing is set by the first line of a runtime crash report; from then on
	// every line is kept. The process is going down, so it never resets.
	crashing bool
}

func newDaemonTap(src string, cmd *exec.Cmd, l *rotatingLog) *daemonTap {
	return &daemonTap{src: src, cmd: cmd, log: l, now: time.Now}
}

func (t *daemonTap) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	for {
		i := indexByte(t.buf, '\n')
		if i < 0 {
			break
		}
		t.line(string(t.buf[:i]))
		t.buf = t.buf[i+1:]
	}
	// Same bound as the boot tap: output with no newline must not grow the
	// buffer without limit. The fragment is judged like any other line.
	if len(t.buf) > bootTapMaxLine {
		t.line(string(t.buf))
		t.buf = t.buf[:0]
	}
	return len(p), nil
}

// line keeps one line if it is worth keeping. Called with t.mu held.
func (t *daemonTap) line(s string) {
	if !t.crashing {
		switch dlog.Classify(s) {
		case dlog.Routine:
			return
		case dlog.Crash:
			t.crashing = true
		}
	}
	pid := 0
	if t.cmd != nil && t.cmd.Process != nil {
		pid = t.cmd.Process.Pid
	}
	// The daemon's own timestamp is dropped for ours: same clock, and one stamp
	// per line in one format is what makes the file sortable and greppable.
	t.log.writeLine(fmt.Sprintf("%s %s[%d] %s", t.now().Format(stamp), t.src, pid, dlog.StripTimestamp(s)))
}
