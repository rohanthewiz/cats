//go:build ghostty

package main

import (
	"crypto/tls"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A cathost that listens on a port is reachable by anyone who can route to it,
// and what it hands out is a shell. These tests pin the two refusals that keep
// that from happening by accident — a network listener with no token, and a
// cleartext listener that isn't confined to this machine — plus the fact that
// the historical unix path still opens exactly as it did.

func TestOpenListenerUnix(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "cathost.sock")
	ln, desc, cleanup, err := openListener("unix://"+sock, "", "", false)
	if err != nil {
		t.Fatalf("openListener(unix): %v", err)
	}
	defer ln.Close()
	if desc != sock {
		t.Errorf("desc = %q, want the socket path", desc)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket not created: %v", err)
	}
	ln.Close()
	cleanup()
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("cleanup left the socket behind: %v", err)
	}
}

// A stale socket from a killed run is removed rather than fataling the daemon:
// the file outliving its process is the normal aftermath of a SIGKILL.
func TestOpenListenerUnixReplacesStaleSocket(t *testing.T) {
	// Not t.TempDir(): its path carries the test's name, and macOS caps a unix
	// socket path at ~104 bytes — a long test name alone is enough to fail the
	// bind, which has nothing to do with what is under test here.
	dir, err := os.MkdirTemp("", "cathost")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "cathost.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ln, _, cleanup, err := openListener("unix://"+sock, "", "", false)
	if err != nil {
		t.Fatalf("openListener over a stale socket: %v", err)
	}
	ln.Close()
	cleanup()
}

func TestOpenListenerRefusesUnsafeNetworkBinds(t *testing.T) {
	for name, tc := range map[string]struct {
		addr     string
		hasToken bool
		want     string
	}{
		"tcp without a token":    {addr: "tcp://127.0.0.1:0", hasToken: false, want: "token"},
		"tls without a token":    {addr: "tls://127.0.0.1:0", hasToken: false, want: "token"},
		"tcp on every interface": {addr: "tcp://:0", hasToken: true, want: "interface"},
		"tcp off the loopback":   {addr: "tcp://192.0.2.7:0", hasToken: true, want: "loopback"},
		"no scheme at all":       {addr: "/tmp/cathost.sock", hasToken: false, want: "scheme"},
	} {
		t.Run(name, func(t *testing.T) {
			ln, _, _, err := openListener(tc.addr, "", "", tc.hasToken)
			if err == nil {
				ln.Close()
				t.Fatalf("%s should have been refused", tc.addr)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestOpenListenerTCPLoopback(t *testing.T) {
	ln, _, _, err := openListener("tcp://127.0.0.1:0", "", "", true)
	if err != nil {
		t.Fatalf("openListener(tcp loopback): %v", err)
	}
	ln.Close()
}

// The tls:// listener mints its certificate on first use and serves it. The
// fingerprint of that file is what the operator copies into the catway's
// hosts[].fingerprint, so the certificate has to be a real one on disk rather
// than an in-memory ephemeral.
func TestOpenListenerTLSMintsCertificate(t *testing.T) {
	dir := t.TempDir()
	ln, desc, _, err := openListener("tls://127.0.0.1:0", dir, "127.0.0.1", true)
	if err != nil {
		t.Fatalf("openListener(tls): %v", err)
	}
	defer ln.Close()
	if !strings.Contains(desc, "tls") {
		t.Errorf("desc = %q, want it to name the transport", desc)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no certificate written to %s (%v)", dir, err)
	}

	// It really terminates TLS: an unpinned client completes a handshake.
	go func() {
		c, err := ln.Accept()
		if err == nil {
			c.(*tls.Conn).Handshake()
			c.Close()
		}
	}()
	conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	conn.Close()
}

// The token comes from a file so the secret never lands in a process listing,
// and the trailing newline every editor adds must not silently break every
// handshake.
func TestReadToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("  s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, err := readToken(path)
	if err != nil {
		t.Fatalf("readToken: %v", err)
	}
	if tok != "s3cret" {
		t.Fatalf("token = %q, want it trimmed", tok)
	}

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readToken(empty); err == nil {
		t.Fatal("an empty token file must be an error, not an empty token")
	}
	if _, err := readToken(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing token file must be an error")
	}
}
