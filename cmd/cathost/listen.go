//go:build ghostty

package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/rohanthewiz/cats/internal/config"
	"github.com/rohanthewiz/cats/internal/gwtls"
)

// Where cathost listens.
//
// Until v3 there was exactly one answer — a unix socket — and it was enough,
// because the only client was a catway on the same machine (an `ssh -L` forward
// stretches that to a remote box without the daemon knowing). -listen adds the
// native transports so a host can be attached with no ssh session babysitting
// it:
//
//	unix:///tmp/cats-cathost.sock   the historical path; file permissions are
//	                                the whole access control story
//	tcp://127.0.0.1:8422            cleartext — refused off the loopback, since
//	                                a pane's keystrokes and output would
//	                                otherwise cross the network in the clear
//	tls://0.0.0.0:8422              the real remote transport: self-signed by
//	                                default, with the fingerprint printed for
//	                                the client to pin
//
// Both network transports demand a token (-token-file). A unix socket is
// reachable only by someone who can already open the file; a port is reachable
// by anyone who can route to it, and an unauthenticated one hands them a shell.

// openListener resolves addr into a listening socket. desc is the string for
// log lines, cleanup removes anything the listener left on disk (the unix
// socket), and hasToken reports whether -token-file was supplied — a network
// listener without one is refused rather than opened.
func openListener(addr, tlsDir, sans string, hasToken bool) (ln net.Listener, desc string, cleanup func(), err error) {
	// One address parser for the whole product: this is exactly the string a
	// catway puts in hosts[].addr, and two parsers would eventually disagree
	// about some edge (a path with a colon, an IPv6 literal) in a way that
	// reads as "the daemon isn't listening".
	scheme, target, err := config.Host{Addr: addr}.Transport()
	if err != nil {
		return nil, "", nil, err
	}
	if target == "" {
		return nil, "", nil, fmt.Errorf("listen %q: empty target", addr)
	}

	switch scheme {
	case config.HostUnix:
		// Remove a stale socket from a previous run.
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return nil, "", nil, fmt.Errorf("remove stale socket: %w", err)
		}
		ln, err = net.Listen("unix", target)
		if err != nil {
			return nil, "", nil, fmt.Errorf("listen: %w", err)
		}
		return ln, target, func() { os.Remove(target) }, nil

	case config.HostTCP:
		if !hasToken {
			return nil, "", nil, fmt.Errorf("listen %q: tcp needs -token-file (an open port is an unauthenticated shell)", addr)
		}
		if err := requireLoopback(target); err != nil {
			return nil, "", nil, fmt.Errorf("listen %q: %w", addr, err)
		}
		ln, err = net.Listen("tcp", target)
		if err != nil {
			return nil, "", nil, fmt.Errorf("listen: %w", err)
		}
		return ln, target, func() {}, nil

	case config.HostTLS:
		if !hasToken {
			return nil, "", nil, fmt.Errorf("listen %q: tls needs -token-file (a certificate proves who we are, not who they are)", addr)
		}
		cfg, err := serverTLS(tlsDir, sans)
		if err != nil {
			return nil, "", nil, err
		}
		raw, err := net.Listen("tcp", target)
		if err != nil {
			return nil, "", nil, fmt.Errorf("listen: %w", err)
		}
		return tls.NewListener(raw, cfg), target + " (tls)", func() {}, nil
	}
	return nil, "", nil, fmt.Errorf("listen %q: unknown scheme %q", addr, scheme)
}

// requireLoopback refuses a cleartext bind that anything but this machine can
// reach. An empty host means "every interface", which is the one spelling most
// likely to be typed by accident.
func requireLoopback(target string) error {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return err
	}
	if host == "" {
		return fmt.Errorf("tcp binds every interface in the clear — use tls://, or bind 127.0.0.1 explicitly")
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("tcp is cleartext, so %s may only be a loopback address — use tls:// to leave this machine", host)
	}
	return nil
}

// serverTLS builds the TLS config for a tls:// listener from a self-signed
// certificate cached in dir (minted on first use), and logs the fingerprint.
//
// Self-signed is the right default: there is no CA for a personal fleet of dev
// boxes, and the client pins this exact certificate by fingerprint — which is
// stronger than chain validation, not weaker, as long as the operator copies
// the printed value rather than clicking through it. An operator with real
// certificates points -tls-dir at a directory holding them under the names
// gwtls uses, or fronts the daemon with their own terminator.
func serverTLS(dir, sans string) (*tls.Config, error) {
	if dir == "" {
		cfgDir, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("locate config dir: %w", err)
		}
		// Its own directory, not the catway's: the two are different identities
		// even when they share a machine, and a client pins one of them.
		dir = filepath.Join(cfgDir, "cats", "cathost-tls")
	}
	certPath, keyPath, err := gwtls.EnsureSelfSigned(dir, splitSANs(sans))
	if err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load certificate: %w", err)
	}
	if fp, err := gwtls.Fingerprint(certPath); err == nil {
		// The one line an operator has to copy: it becomes hosts[].fingerprint
		// in the catway's config, and without it the client cannot tell this
		// daemon from anything else answering on that port.
		log.Printf("cathost TLS certificate fingerprint (put this in the catway's hosts[].fingerprint):\n  %s", fp)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}

// splitSANs turns the comma-separated -tls-san flag into the list gwtls wants.
// gwtls itself rejects a malformed entry, so nothing is validated here beyond
// dropping blanks.
func splitSANs(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// readToken loads the bearer token a client must present, from a file so the
// secret never appears in a process listing or a shell history. Surrounding
// whitespace is trimmed: the file is nearly always written with an editor or a
// `>` redirect, and a trailing newline that silently broke every handshake
// would be a miserable thing to debug.
func readToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read token file: %w", err)
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", fmt.Errorf("token file %s is empty", path)
	}
	return tok, nil
}
