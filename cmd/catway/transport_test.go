//go:build ghostty

package main

import (
	"crypto/tls"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/config"
	"github.com/rohanthewiz/cats/internal/gwtls"
	"github.com/rohanthewiz/cats/internal/orchestration"
)

// Phase 4's transports and handshake, from catway's side: what it will dial,
// what it presents when it gets there, and which peer versions it will speak.
// The through-line is that every refusal happens where the operator can see it
// — at roster build for an address that can never be safe, at the handshake for
// a peer that cannot be trusted — rather than as a host that retries forever.

// tlsCathost starts a TLS listener with a freshly minted self-signed
// certificate and returns its address and fingerprint: the two values a real
// cathost prints at startup for the catway's config.
func tlsCathost(t *testing.T) (addr, fingerprint string) {
	t.Helper()
	dir := t.TempDir()
	certPath, keyPath, err := gwtls.EnsureSelfSigned(dir, []string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("EnsureSelfSigned: %v", err)
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("LoadX509KeyPair: %v", err)
	}
	fingerprint, err = gwtls.Fingerprint(certPath)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ln := tls.NewListener(raw, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// Reading is what drives the server side of the handshake to
			// completion; the bytes themselves are of no interest.
			go func() {
				defer c.Close()
				_, _ = c.Read(make([]byte, 1))
			}()
		}
	}()
	return ln.Addr().String(), fingerprint
}

// The pin is the whole security model of a self-signed cathost: with no CA in
// sight, "this is the certificate I was told to expect" is the only statement
// worth making about the peer. A mismatch must fail the dial, not warn.
func TestTLSDialerPinsFingerprint(t *testing.T) {
	addr, fp := tlsCathost(t)

	_, dial, err := dialerFor(config.Host{ID: "devbox", Addr: "tls://" + addr, Fingerprint: fp})
	if err != nil {
		t.Fatalf("dialerFor(pinned): %v", err)
	}
	conn, err := dial()
	if err != nil {
		t.Fatalf("dial with the matching fingerprint: %v", err)
	}
	conn.Close()

	// The same daemon, pinned to somebody else's certificate.
	other := strings.Repeat("ab", 32)
	_, dial, err = dialerFor(config.Host{ID: "devbox", Addr: "tls://" + addr, Fingerprint: other})
	if err != nil {
		t.Fatalf("dialerFor(mismatched): %v", err)
	}
	if conn, err := dial(); err == nil {
		conn.Close()
		t.Fatal("a mismatched fingerprint must fail the dial")
	} else if !strings.Contains(err.Error(), "fingerprint") {
		t.Fatalf("dial error = %v, want it to name the fingerprint", err)
	}
}

// The value gets copied out of a log line, a chat message, or a certificate
// viewer, so the shapes it arrives in are all accepted — but only the shapes
// that are genuinely the same hash.
func TestFingerprintNormalization(t *testing.T) {
	want := strings.Repeat("ab", 32)
	for _, in := range []string{
		want,
		strings.ToUpper(want),
		"  " + want + "\n",
		strings.Join(strings.Split(want, "")[:2], "") + ":" + want[2:], // colon-separated, as openssl prints
	} {
		if got := normalizeFingerprint(in); got != want {
			t.Errorf("normalizeFingerprint(%q) = %q", in, got)
		}
	}
}

// fakeCathost answers d's handshake with a welcome at the given version and
// then holds the connection open, so the daemon reaches its steady state. It
// returns the hello it was sent.
func fakeCathost(t *testing.T, d *daemon, version int) orchestration.Hello {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })

	done := make(chan error, 1)
	go func() { done <- d.session(client) }()

	mt, payload, err := orchestration.ReadMessage(server)
	if err != nil {
		t.Fatalf("read hello: %v", err)
	}
	if mt != orchestration.MsgHello {
		t.Fatalf("first message = %q, want hello", mt)
	}
	var h orchestration.Hello
	if err := json.Unmarshal(payload, &h); err != nil {
		t.Fatalf("decode hello: %v", err)
	}
	if err := orchestration.WriteMessage(server, orchestration.NewWelcomeAt(version, "", nil)); err != nil {
		t.Fatalf("write welcome: %v", err)
	}
	// Drain whatever the orchestrator sends next (reconcile replays the model,
	// which for a fresh session is a create_pane). net.Pipe is unbuffered, so
	// leaving it unread would block the orchestrator loop itself.
	go func() {
		for {
			if _, _, err := orchestration.ReadMessage(server); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		server.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})
	return h
}

// The token is read from its file at each handshake, not once at startup: a
// rotation's whole point is that the next connection uses the new value, and
// the reconnect it causes is that connection.
func TestSessionPresentsTokenFromFile(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	go o.run()

	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("  s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d := o.hosts[o.defaultHost]
	d.tokenFile = path

	if got := fakeCathost(t, d, orchestration.ProtocolVersion).Token; got != "s3cret" {
		t.Fatalf("hello token = %q, want the file's contents trimmed", got)
	}
}

// A daemon one version behind is served, and says so: catway keeps resolving
// branches itself for it, because a v2 daemon is by construction the local one.
// A v3 daemon owns its panes' branches instead.
func TestSessionNegotiatesPeerVersion(t *testing.T) {
	for _, tc := range []struct {
		version      int
		wantResolves bool
	}{
		{orchestration.MinProtocolVersion, false},
		{orchestration.ProtocolVersion, true},
	} {
		o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
		if err != nil {
			t.Fatalf("newOrch: %v", err)
		}
		go o.run()
		d := o.hosts[o.defaultHost]
		fakeCathost(t, d, tc.version)

		awaitTrue(t, func() bool { return d.connected() })
		if got := d.resolvesBranch(); got != tc.wantResolves {
			t.Errorf("peer v%d: resolvesBranch = %v, want %v", tc.version, got, tc.wantResolves)
		}
	}
}

// A version outside the supported range ends the session with the reason, which
// is what lands on the host's roster row.
func TestSessionRejectsUnsupportedPeerVersion(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	go o.run()
	d := o.hosts[o.defaultHost]

	client, server := net.Pipe()
	t.Cleanup(func() { client.Close(); server.Close() })
	done := make(chan error, 1)
	go func() { done <- d.session(client) }()

	if _, _, err := orchestration.ReadMessage(server); err != nil {
		t.Fatalf("read hello: %v", err)
	}
	if err := orchestration.WriteMessage(server, orchestration.NewWelcomeAt(1, "", nil)); err != nil {
		t.Fatalf("write welcome: %v", err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "protocol 1") {
			t.Fatalf("session error = %v, want it to name the peer's version", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session accepted a v1 daemon")
	}
	if d.connected() {
		t.Fatal("a rejected daemon must not be left marked connected")
	}
}

// awaitTrue polls until cond holds or the test gives up. The daemon publishes
// its state from its own goroutine (and the orchestrator loop is running here,
// unlike the mailbox-pumping waitFor), so a bare read races the handshake.
func awaitTrue(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}
