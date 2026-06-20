//go:build ghostty

// Command termhost is the Phase B Go terminal backend daemon: it listens on a
// Unix socket and serves the orchestration protocol (internal/orchestration),
// owning PTYs + VT emulation per pane. The Rust herdr server connects to it as
// the orchestrator (workspace/pane tree, layout, detection, session) and drives
// panes through the seam.
//
// Build requires libghostty-vt on PKG_CONFIG_PATH and -tags ghostty;
// see scripts/build-libghostty-vt.sh.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/rohanthewiz/herdr-web/internal/orchestration"
)

func main() {
	socket := flag.String("socket", "/tmp/herdr-termhost.sock", "unix socket path to listen on")
	flag.Parse()

	if err := run(*socket); err != nil {
		fmt.Fprintln(os.Stderr, "termhost:", err)
		os.Exit(1)
	}
}

func run(socket string) error {
	// Remove a stale socket from a previous run.
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	ln, err := net.Listen("unix", socket)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer ln.Close()
	defer os.Remove(socket)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		ln.Close() // unblock Accept
	}()

	log.Printf("termhost listening on %s (protocol v%d)", socket, orchestration.ProtocolVersion)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // shutting down
			}
			return fmt.Errorf("accept: %w", err)
		}
		log.Printf("client connected")
		go func() {
			defer conn.Close()
			h := orchestration.NewHost()
			if err := h.Serve(ctx, conn); err != nil {
				log.Printf("session ended: %v", err)
			} else {
				log.Printf("client disconnected")
			}
		}()
	}
}
