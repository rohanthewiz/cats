//go:build darwin

package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/rohanthewiz/cats/internal/dlog"
	"github.com/rohanthewiz/cats/internal/startdir"
)

// backend is the supervised daemon pair for local mode: a persistent cathost
// (owns PTYs + VT emulation) and a catway (serves the browser UI on loopback).
// The launcher spawns both wired to a private socket, waits for the catway to
// accept TCP, points the webview at it, and reaps them when the window closes.
type backend struct {
	cathost *daemonProc
	catway  *daemonProc
	addr    string   // 127.0.0.1:<port> the catway serves
	socket  string   // $TMPDIR unix socket the two daemons share (cathost seam)
	sockets []string // every $TMPDIR socket we point the daemons at, for cleanup
}

// startBackend launches cathost then catway, both wired to a private $TMPDIR
// socket and an ephemeral loopback port, and blocks until the catway accepts
// connections. Local mode runs the catway with --auth none bound to 127.0.0.1
// only: there is no network exposure, so a login prompt would be pure friction.
// cathost runs -persistent so panes survive a catway restart.
func startBackend() (*backend, error) {
	// Each stage is recorded in the boot log as it starts and as it ends, so a
	// launch that stalls says where (bootlog.go). The details are the values
	// worth having when the same launch misbehaves on someone else's machine:
	// which binaries were picked up, which port, which pids.
	find := boot.begin("locating the bundled daemons")
	thPath, err := resolveBinary("cathost")
	if err != nil {
		boot.fail(find, err)
		return nil, err
	}
	gwPath, err := resolveBinary("catway")
	if err != nil {
		boot.fail(find, err)
		return nil, err
	}
	boot.okDetail(find, thPath+"\n"+gwPath)

	portStep := boot.begin("reserving a loopback port")
	port, err := pickPort()
	if err != nil {
		boot.fail(portStep, err)
		return nil, err
	}
	// All three daemon sockets live under $TMPDIR (per-user, 0700 on macOS) keyed
	// by our pid: private, and unique per launch so a second instance — or a
	// hand-launched catway on the default /tmp paths — never collides. Isolating
	// the control + hook sockets (not just the cathost seam) keeps agent
	// hook-reporting (titles/detection) working even alongside another catway.
	thSock := socketPath("th")
	ctlSock := socketPath("ctl")
	hookSock := socketPath("hooks")
	b := &backend{
		addr:    fmt.Sprintf("127.0.0.1:%d", port),
		socket:  thSock,
		sockets: []string{thSock, ctlSock, hookSock},
	}
	boot.okDetail(portStep, b.addr)

	// Register the teardown BEFORE anything is spawned. Startup now runs on a
	// goroutine while the app is alive and quittable — closing the startup
	// window is a quit, and so is ⌘Q — so a launch can be ended between the
	// first exec and the last. b.stop is safe on a partially-started backend,
	// which is exactly what a quit at that moment leaves behind; registering
	// only once everything is up would orphan it.
	registerCleanup(b.stop)

	thStep := boot.begin("starting cathost")
	// Setpgid detaches each daemon into its own process group so a stray signal
	// to the launcher's group (e.g. Ctrl-C in a dev terminal) doesn't pre-empt
	// our orderly teardown; we signal each process explicitly on quit.
	b.cathost, err = startDaemon(daemonLog, thPath, "-persistent", "-socket", thSock)
	if err != nil {
		err = fmt.Errorf("start cathost: %w", err)
		boot.fail(thStep, err)
		return nil, err
	}
	boot.okDetail(thStep, fmt.Sprintf("pid %d on %s", b.cathost.pid(), thSock))

	gwStep := boot.begin("starting catway")
	b.catway, err = startDaemon(daemonLog, gwPath,
		"--addr", b.addr, "--auth", "none",
		"--socket", thSock, "--control-socket", ctlSock, "--hook-socket", hookSock)
	if err != nil {
		b.stop()
		err = fmt.Errorf("start catway: %w", err)
		boot.fail(gwStep, err)
		return nil, err
	}
	boot.okDetail(gwStep, fmt.Sprintf("pid %d", b.catway.pid()))

	// The catway serves HTTP as soon as it binds — it dials cathost lazily with
	// its own retry (cmd/catway/daemon.go) — so a successful TCP dial is a
	// sufficient readiness signal to navigate the webview.
	//
	// This is the step that most often takes real time, and the one whose
	// clock ticking in the startup window tells the user the launch has not
	// died. Whatever the catway writes while we wait is tapped into the log
	// beside it (see command), so a retry loop against a stale cathost socket
	// is readable rather than merely slow.
	readyStep := boot.begin("waiting for the catway to accept connections")
	if err := waitReady(b.addr, 10*time.Second); err != nil {
		b.stop()
		boot.fail(readyStep, err)
		return nil, err
	}
	boot.okDetail(readyStep, "http://"+b.addr)
	return b, nil
}

// command builds an *exec.Cmd for a daemon: pass its output through to our own
// stdio (so daemon logs still surface in a dev terminal) and detach it into its
// own process group.
//
// The output is also tapped into the boot log, which is what turns "waiting for
// the catway — 9.4s" into a reason: the catway says what it is retrying, and
// the line lands under the step that is waiting on it. The tap stops recording
// the moment startup finishes (bootTap.Write), so the rest of the session's
// logging costs a boolean.
//
// And it is tapped into l, the kept daemon log (daemonlog.go), which is the
// only one of the three that lasts past startup and past the session.
func command(l *rotatingLog, path string, args ...string) *exec.Cmd {
	c := exec.Command(path, args...)
	name := filepath.Base(path)
	// One tap per stream rather than one shared: they are written by different
	// goroutines, and a shared partial-line buffer would interleave them into
	// nonsense.
	//
	// Our own stdio is wrapped best-effort. io.MultiWriter stops at the first
	// writer that fails, and exec's copy loop stops with it; a launcher whose
	// terminal has gone (a dev shell closed under it) would then stop draining
	// the daemon's pipe, the pipe would fill, and the daemon would block on its
	// next log line — mid-operation, holding whatever it held. The taps are
	// first for the same reason.
	c.Stdout = io.MultiWriter(newBootTap(name), newDaemonTap(name, c, l), bestEffort{os.Stdout})
	c.Stderr = io.MultiWriter(newBootTap(name), newDaemonTap(name, c, l), bestEffort{os.Stderr})
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Dir = daemonDir()
	// Wait returns once the daemon has exited AND its output pipes have closed.
	// A grandchild that inherited them (a helper the daemon spawned) could hold
	// them open indefinitely, which would hide the daemon's exit from the
	// watcher; WaitDelay caps that.
	c.WaitDelay = 2 * time.Second
	return c
}

// bestEffort is a writer that never fails, for output nobody may be reading.
type bestEffort struct{ w io.Writer }

func (b bestEffort) Write(p []byte) (int, error) {
	_, _ = b.w.Write(p)
	return len(p), nil
}

// Stop grace periods. catway's covers its own graceful shutdown with room to
// spare (it exits by itself within shutdownDeadline, 3s, even when its loop is
// jammed — cmd/catway/signals.go), so reaching SIGKILL means something beyond
// that. cathost has no final save and exits as soon as its panes are torn down.
const (
	catwayStopGrace  = 5 * time.Second
	cathostStopGrace = 3 * time.Second
	// daemonKillWait is how long to wait for the reap after SIGKILL, which the
	// kernel delivers unconditionally; running out of it means an unkillable
	// process (stuck in the kernel), about which nothing more can be done.
	daemonKillWait = 2 * time.Second
)

// daemonProc is one supervised daemon and the watcher that reaps it.
//
// The watcher exists so an exit is noticed when it happens rather than at quit:
// catway dying mid-session leaves a blank window, and before this nothing
// recorded that it had died, let alone how. stopping separates that from the
// exits we cause ourselves, which are not news.
type daemonProc struct {
	name     string
	cmd      *exec.Cmd
	log      *rotatingLog
	done     chan struct{} // closed once cmd.Wait has returned
	stopping atomic.Bool   // set before we signal it; an exit after this was asked for
}

// startDaemon starts a daemon and its watcher.
func startDaemon(l *rotatingLog, path string, args ...string) (*daemonProc, error) {
	c := command(l, path, args...)
	if err := c.Start(); err != nil {
		return nil, err
	}
	p := &daemonProc{name: filepath.Base(path), cmd: c, log: l, done: make(chan struct{})}
	go p.watch()
	return p, nil
}

func (p *daemonProc) pid() int { return p.cmd.Process.Pid }

// watch reaps the daemon and reports an exit nobody asked for. cmd.Wait (not
// Process.Wait) so the output copiers have finished first: a crash report is
// then fully in the log before the line saying the process died.
func (p *daemonProc) watch() {
	_ = p.cmd.Wait()
	defer close(p.done)
	if p.stopping.Load() {
		return
	}
	how := "exited"
	if st := p.cmd.ProcessState; st != nil {
		how = st.String() // "exit status 2", "signal: killed"
	}
	// A clean exit is still unexpected here (catctl server.stop, cathost's idle
	// timeout), but it is a choice someone made rather than a failure.
	level := dlog.LevelError
	if st := p.cmd.ProcessState; st != nil && st.Success() {
		level = dlog.LevelWarn
	}
	p.log.note(level, "%s (pid %d) exited while the app was running: %s", p.name, p.pid(), how)
}

// stop asks the daemon to exit and makes sure it does: SIGTERM, then SIGKILL
// once grace runs out. Safe on nil (a daemon never started).
//
// It used to SIGTERM and give up after the wait, leaving the process to "the
// OS at exit" — which does nothing to a child. On 2026-09-13 that left a
// cathost whose session was wedged running for good: reparented to launchd,
// still holding every pane (two agents among them), its socket file already
// removed and its listener closed, so nothing could ever reach it, and its
// stdio pipes pointing at a launcher that had exited. An orphan like that is
// worse than a killed daemon in every way, so the escalation is unconditional.
func (p *daemonProc) stop(grace time.Duration) {
	if p == nil {
		return
	}
	p.stopping.Store(true)
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
		return
	case <-time.After(grace):
	}
	p.log.note(dlog.LevelWarn, "%s (pid %d) did not exit within %s of SIGTERM — sending SIGKILL", p.name, p.pid(), grace)
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(daemonKillWait):
		p.log.note(dlog.LevelError, "%s (pid %d) still running %s after SIGKILL", p.name, p.pid(), daemonKillWait)
	}
}

// daemonDir is the working directory the daemons run in. Launched from Finder
// (or `open`) our cwd is "/", which the daemons would inherit and hand to every
// pane's shell — a new terminal at the filesystem root. Use the home directory
// instead. Launched from a dev shell we keep that shell's cwd, so `cd project
// && catapp` still opens panes in the project.
func daemonDir() string {
	cwd, _ := os.Getwd()
	return startdir.Usable(cwd) // "" (nothing usable) inherits ours
}

// stop tears the backend down in reverse order: stop the catway (it saves
// session state and exits within its own short grace window), then cathost.
// Each is SIGTERMed and, past its grace, SIGKILLed (daemonProc.stop). cathost is
// persistent, so a future "keep sessions alive in the background" option could
// skip stopping it; for now a window close reaps both to avoid orphaned daemons.
// Safe to call on a partially-started backend.
func (b *backend) stop() {
	b.catway.stop(catwayStopGrace)
	b.cathost.stop(cathostStopGrace)
	// The daemons unlink their own sockets on a clean exit; remove any stragglers
	// as a backstop (a daemon that had to be SIGKILLed never got to).
	for _, s := range b.sockets {
		_ = os.Remove(s)
	}
}

// resolveBinary locates a sibling daemon binary. In a .app bundle every binary
// sits together in Contents/MacOS, so we look next to our own executable first;
// falling back to $PATH keeps `go run ./cmd/catapp` (or a bin/ build) working
// in development.
func resolveBinary(name string) (string, error) {
	if self, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(self), name)
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("cannot find %q next to the launcher or on PATH", name)
}

// The band of loopback ports local mode prefers, in order.
//
// The port is not a free choice: the UI is served from http://127.0.0.1:<port>,
// and a browser scopes localStorage by *origin* — scheme, host AND port. A
// kernel-assigned ephemeral port (:0) is therefore a brand-new origin on every
// launch, which handed the webview an empty store each time and silently reset
// every per-browser preference the page keeps there (sidebar fold state, column
// width, font size, chat panel). Preferring a fixed port keeps the origin — and
// with it the store — stable across restarts.
//
//	launch 1  ->  127.0.0.1:8422  ─┐
//	launch 2  ->  127.0.0.1:8422  ─┴─ same origin, same localStorage
//
// 8422 rather than the catway's own :8421 default because a hand-launched catway
// owns that one, and local mode must not fight it for the port. Concurrent
// launches walk the band in a fixed order, so a second instance lands on 8423
// every time rather than somewhere new — its preferences persist too.
const (
	appPortBase = 8422
	appPortSpan = 10
)

// pickPort reserves a free loopback TCP port for the catway, preferring the
// stable band above so the UI's origin survives a restart (see appPortBase).
// Falls back to a kernel-assigned ephemeral port when the whole band is taken:
// preferences are lost in that case, but serving the UI at all matters more.
//
// Every branch carries the same inherent race — the port is free now but could
// be taken before the catway binds it. On loopback for a desktop app that window
// is negligible, and probing first is what avoids a port already in use.
func pickPort() (int, error) {
	for p := appPortBase; p < appPortBase+appPortSpan; p++ {
		if portFree(p) {
			return p, nil
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve loopback port: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// portFree reports whether the given loopback port can be bound right now. The
// listener is closed immediately — this is a probe, not a reservation; the
// catway is what actually binds it a moment later.
func portFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

// socketPath returns a per-user, private unix socket path under $TMPDIR for the
// given role (e.g. "th", "ctl", "hooks"). On macOS $TMPDIR is a per-user 0700
// dir under /var/folders, so this avoids the world-visible, collision-prone
// default /tmp/cats-*.sock. The pid keeps concurrent launches from clashing.
// Kept short — unix socket paths cap ~104B.
func socketPath(role string) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("cats-%s-%d.sock", role, os.Getpid()))
}

// waitReady blocks until a TCP dial to addr succeeds or the deadline passes,
// mirroring the dial-retry backoff the catway uses for the cathost socket
// (cmd/catway/daemon.go): start at 50ms, double, cap at 500ms.
func waitReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	backoff := 50 * time.Millisecond
	for {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = c.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("catway did not become ready at %s within %s: %w", addr, timeout, err)
		}
		time.Sleep(backoff)
		if backoff < 500*time.Millisecond {
			backoff *= 2
		}
	}
}
