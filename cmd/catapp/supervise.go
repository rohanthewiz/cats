//go:build darwin

package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rohanthewiz/cats/internal/startdir"
)

// backend is the supervised daemon pair for local mode: a persistent cathost
// (owns PTYs + VT emulation) and a catway (serves the browser UI on loopback).
// The launcher spawns both wired to a private socket, waits for the catway to
// accept TCP, points the webview at it, and reaps them when the window closes.
type backend struct {
	cathost *exec.Cmd
	catway  *exec.Cmd
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
	b.cathost = command(thPath, "-persistent", "-socket", thSock)
	if err := b.cathost.Start(); err != nil {
		err = fmt.Errorf("start cathost: %w", err)
		boot.fail(thStep, err)
		return nil, err
	}
	boot.okDetail(thStep, fmt.Sprintf("pid %d on %s", b.cathost.Process.Pid, thSock))

	gwStep := boot.begin("starting catway")
	b.catway = command(gwPath,
		"--addr", b.addr, "--auth", "none",
		"--socket", thSock, "--control-socket", ctlSock, "--hook-socket", hookSock)
	if err := b.catway.Start(); err != nil {
		b.stop()
		err = fmt.Errorf("start catway: %w", err)
		boot.fail(gwStep, err)
		return nil, err
	}
	boot.okDetail(gwStep, fmt.Sprintf("pid %d", b.catway.Process.Pid))

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
func command(path string, args ...string) *exec.Cmd {
	c := exec.Command(path, args...)
	name := filepath.Base(path)
	// One tap per stream rather than one shared: they are written by different
	// goroutines, and a shared partial-line buffer would interleave them into
	// nonsense.
	c.Stdout = io.MultiWriter(os.Stdout, newBootTap(name))
	c.Stderr = io.MultiWriter(os.Stderr, newBootTap(name))
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Dir = daemonDir()
	return c
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

// stop tears the backend down in reverse order: SIGTERM the catway (it saves
// session state and exits within its own short grace window), wait briefly, then
// SIGTERM cathost. cathost is persistent, so a future "keep sessions alive in
// the background" option could skip signalling it; for now a window close reaps
// both to avoid orphaned daemons. Safe to call on a partially-started backend.
func (b *backend) stop() {
	if b.catway != nil && b.catway.Process != nil {
		_ = b.catway.Process.Signal(syscall.SIGTERM)
		waitOrTimeout(b.catway, 3*time.Second)
	}
	if b.cathost != nil && b.cathost.Process != nil {
		_ = b.cathost.Process.Signal(syscall.SIGTERM)
		waitOrTimeout(b.cathost, 3*time.Second)
	}
	// The daemons unlink their own sockets on a clean exit; remove any stragglers
	// as a backstop (e.g. a daemon that was SIGKILLed by the OS at app exit).
	for _, s := range b.sockets {
		_ = os.Remove(s)
	}
}

// waitOrTimeout reaps a signalled child, giving up (leaving it to the OS at
// process exit) if it doesn't die within d. Reaping avoids leaving zombies while
// the launcher is still running.
func waitOrTimeout(c *exec.Cmd, d time.Duration) {
	done := make(chan struct{})
	go func() { _, _ = c.Process.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
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
