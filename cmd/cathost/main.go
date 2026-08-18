//go:build ghostty

// Command cathost is the Go terminal backend daemon: it listens for an
// orchestrator and serves the orchestration protocol (internal/orchestration),
// owning PTYs + VT emulation per pane. The catway (cmd/catway) connects to
// it as the orchestrator (workspace/pane tree, layout, detection, session)
// and drives panes through the seam. Run with -persistent so panes survive a
// catway restart or upgrade.
//
// -listen chooses the transport: a unix socket (the default, and what -socket
// still selects), or tcp://loopback / tls://host:port for a daemon a catway on
// another machine attaches to directly. Both network transports require
// -token-file; see listen.go for why.
//
// Build requires libghostty-vt on PKG_CONFIG_PATH and -tags ghostty;
// see `make vt` / scripts/build-libghostty-vt.sh.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/rohanthewiz/cats/internal/detect"
	"github.com/rohanthewiz/cats/internal/orchestration"
	"github.com/rohanthewiz/cats/internal/persist"
)

func main() {
	socket := flag.String("socket", "/tmp/cats-cathost.sock",
		"unix socket path to listen on (shorthand for -listen unix://<path>)")
	listen := flag.String("listen", "",
		"address to listen on: unix://path, tcp://host:port (loopback only) or tls://host:port; overrides -socket")
	tokenFile := flag.String("token-file", "",
		"file holding the bearer token a client's hello must present; required for tcp:// and tls://")
	tlsDir := flag.String("tls-dir", "",
		"directory holding (or receiving) the self-signed tls:// certificate; default is the user config dir")
	tlsSAN := flag.String("tls-san", "",
		"comma-separated extra names the generated certificate must cover (a DNS name or IP this host is reached by)")
	exitOnDisconnect := flag.Bool("exit-on-disconnect", false,
		"exit after the first client disconnects (managed mode: the orchestrator owns our lifecycle)")
	persistent := flag.Bool("persistent", false,
		"keep panes alive across client disconnects; a restarted/handed-off cats reconnects and resyncs (overrides -exit-on-disconnect)")
	idleTimeout := flag.Duration("idle-timeout", 10*time.Minute,
		"in persistent mode, exit if no client is attached for this long (0 disables)")
	hookSocket := flag.String("hook-socket", "",
		"path for the agent hook-relay socket this daemon opens for its panes; empty picks /tmp/cats-hookrelay-<pid>-<n>.sock, \"-\" disables the relay")
	manifestUpdate := flag.Bool("manifest-update", true,
		"fetch agent-detection manifest updates from the herdr.dev catalog at startup (env "+detect.CatalogURLEnv+" overrides the URL)")
	flag.Parse()

	// Agent-detection manifests (WS5): layer any committed remote manifests over
	// the embedded set, and kick off one background update pass. Detection runs
	// in this daemon, so this is where the overlay lives. No resolvable state
	// dir ⇒ embedded manifests only.
	if stateRoot := persist.DefaultDir(); stateRoot != "" {
		dir := filepath.Join(stateRoot, "agent-detection")
		detect.SetRemoteManifestDir(dir)
		if *manifestUpdate {
			go detect.AutoUpdate(dir)
		}
	}

	// -socket is the historical spelling and stays the default, so every script
	// and launchd plist that predates remote hosts keeps working untouched;
	// -listen is the same setting with a scheme in front of it.
	addr := *listen
	if addr == "" {
		addr = "unix://" + *socket
	}

	var token string
	if *tokenFile != "" {
		var err error
		if token, err = readToken(*tokenFile); err != nil {
			fmt.Fprintln(os.Stderr, "cathost:", err)
			os.Exit(1)
		}
	}

	ln, desc, cleanup, err := openListener(addr, *tlsDir, *tlsSAN, token != "")
	if err != nil {
		fmt.Fprintln(os.Stderr, "cathost:", err)
		os.Exit(1)
	}
	defer cleanup()

	if *persistent {
		err = runPersistent(ln, desc, token, *idleTimeout, *hookSocket)
	} else {
		err = run(ln, desc, token, *exitOnDisconnect, *hookSocket)
	}
	if err != nil {
		cleanup() // os.Exit skips the defer
		fmt.Fprintln(os.Stderr, "cathost:", err)
		os.Exit(1)
	}
}

func run(ln net.Listener, desc, token string, exitOnDisconnect bool, hookSocket string) error {
	defer ln.Close()

	// SIGHUP too: in managed mode the orchestrator is our parent, so its exit (or a
	// closed controlling terminal) hangs us up — treat that as a graceful shutdown
	// so the deferred socket cleanup runs instead of the default terminate.
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	go func() {
		<-ctx.Done()
		ln.Close() // unblock Accept
	}()

	log.Printf("cathost listening on %s (protocol v%d-v%d%s)", desc,
		orchestration.MinProtocolVersion, orchestration.ProtocolVersion, authNote(token))

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // shutting down
			}
			return fmt.Errorf("accept: %w", err)
		}
		log.Printf("client connected")
		serve := func() {
			defer conn.Close()
			h := orchestration.NewHost()
			h.RequireToken = token
			h.HookSocketPath = hookSocket
			if err := h.Serve(ctx, conn); err != nil {
				log.Printf("session ended: %v", err)
			} else {
				log.Printf("client disconnected")
			}
		}
		// Managed mode: the orchestrator spawned us and is our only client, so
		// serve it inline and exit when it disconnects (a backstop against a crashed
		// parent leaving us listening forever). Standalone/dev mode keeps the
		// goroutine-per-connection loop so it can serve reconnects.
		if exitOnDisconnect {
			// Close the connection on shutdown so a blocked Serve read unblocks and
			// the graceful exit path (deferred socket removal) runs even when the
			// signal, not a client EOF, ends the session.
			go func() {
				<-ctx.Done()
				conn.Close()
			}()
			serve()
			log.Printf("exiting after client disconnect (managed mode)")
			return nil
		}
		go serve()
	}
}

// runPersistent serves a single long-lived Host whose panes outlive any one
// client. A cats that restarts or hands off reconnects to this same daemon and
// resyncs its surviving panes (the create_pane-less path). The daemon exits on a
// clean-quit shutdown command, on the idle timeout, or on a signal.
func runPersistent(ln net.Listener, desc, token string, idleTimeout time.Duration, hookSocket string) error {
	defer ln.Close()

	// Persistent mode must outlive the orchestrator. When cats dies its controlling
	// terminal closes, which SIGHUPs every process still in that session — including
	// us unless we ignore it. (The orchestrator also spawns us with setsid to detach,
	// but ignoring SIGHUP is the portable backstop and covers a hand-launched daemon.)
	// We still honor explicit SIGINT/SIGTERM as a shutdown.
	signal.Ignore(syscall.SIGHUP)
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	h := orchestration.NewHost()
	h.Persistent = true
	h.IdleTimeout = idleTimeout
	h.HookSocketPath = hookSocket
	h.RequireToken = token
	h.Start(ctx)
	defer h.Stop()

	// Unblock Accept on a signal or a shutdown command / idle timeout. The Host owns
	// panes, so closing the listener here is a clean exit (deferred Stop tears them
	// down and the deferred socket removal runs).
	go func() {
		select {
		case <-ctx.Done():
		case <-h.Exit():
		}
		ln.Close()
	}()

	log.Printf("cathost listening on %s (persistent, protocol v%d-v%d%s)", desc,
		orchestration.MinProtocolVersion, orchestration.ProtocolVersion, authNote(token))

	for {
		conn, err := ln.Accept()
		if err != nil {
			// Listener closed by the shutdown goroutine: a clean exit.
			if ctx.Err() != nil {
				return nil
			}
			select {
			case <-h.Exit():
				return nil
			default:
			}
			return fmt.Errorf("accept: %w", err)
		}
		log.Printf("client connected")
		// Serial Attach is the single-writer guarantee: a second client waits in the
		// accept backlog until the current one detaches. Panes survive the gap.
		if err := h.Attach(ctx, conn); err != nil {
			log.Printf("session ended: %v", err)
		} else {
			log.Printf("client disconnected (panes preserved)")
		}
		conn.Close()
	}
}

// authNote is the startup log's one word about access control. Saying it out
// loud on every start is cheap insurance against the failure that matters: a
// daemon that was meant to require a token, silently not requiring one.
func authNote(token string) string {
	if token == "" {
		return ", no token"
	}
	return ", token required"
}
