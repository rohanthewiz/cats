//go:build darwin

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
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
	thPath, err := resolveBinary("cathost")
	if err != nil {
		return nil, err
	}
	gwPath, err := resolveBinary("catway")
	if err != nil {
		return nil, err
	}
	port, err := pickPort()
	if err != nil {
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

	// Setpgid detaches each daemon into its own process group so a stray signal
	// to the launcher's group (e.g. Ctrl-C in a dev terminal) doesn't pre-empt
	// our orderly teardown; we signal each process explicitly on quit.
	b.cathost = command(thPath, "-persistent", "-socket", thSock)
	if err := b.cathost.Start(); err != nil {
		return nil, fmt.Errorf("start cathost: %w", err)
	}

	b.catway = command(gwPath,
		"--addr", b.addr, "--auth", "none",
		"--socket", thSock, "--control-socket", ctlSock, "--hook-socket", hookSock)
	if err := b.catway.Start(); err != nil {
		b.stop()
		return nil, fmt.Errorf("start catway: %w", err)
	}

	// The catway serves HTTP as soon as it binds — it dials cathost lazily with
	// its own retry (cmd/catway/daemon.go) — so a successful TCP dial is a
	// sufficient readiness signal to navigate the webview.
	if err := waitReady(b.addr, 10*time.Second); err != nil {
		b.stop()
		return nil, err
	}
	return b, nil
}

// command builds an *exec.Cmd for a daemon: inherit our stdio (so daemon logs
// surface in a dev terminal) and detach into its own process group.
func command(path string, args ...string) *exec.Cmd {
	c := exec.Command(path, args...)
	c.Stdout, c.Stderr = os.Stdout, os.Stderr
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return c
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

// pickPort reserves a free loopback TCP port by binding :0 and reading back the
// assigned port. There is an inherent race — the port is free now but could be
// taken before the catway binds — but on loopback for a desktop app that window
// is negligible, and it avoids colliding with a hardcoded port already in use.
func pickPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("reserve loopback port: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
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
