//go:build ghostty

// Command gateway2 is the WS9 Stage-4 proof harness: it speaks the browser
// protocol (internal/browserproto, spec ai_docs/phase-c-ws9-protocol.md) over
// WebSocket and sources pane content directly from the termhost daemon over
// the β orchestration seam — no WS2 orchestrator yet. The model is hard-coded:
// one workspace, one tab, a fixed two-pane split (internal/layout). Structured
// key/mouse/paste input is encoded server-side via internal/inputenc (D4).
//
// Build (same prerequisite as cmd/termhost — prebuilt libghostty-vt, no Zig):
//
//	PKG_CONFIG_PATH=$HERDR/vendor/libghostty-vt/zig-out/share/pkgconfig \
//	  go build -tags ghostty ./cmd/gateway2
//
// Run a persistent daemon first:
//
//	termhost -socket /tmp/herdr-termhost.sock -persistent
//
// Usage:
//
//	gateway2 [--addr :8421] [--socket /tmp/herdr-termhost.sock]
package main

import (
	"embed"
	"flag"
	"log"
	"os"

	"github.com/rohanthewiz/rweb"
)

//go:embed web/index.html
var webFS embed.FS

func main() {
	addr := flag.String("addr", ":8421", "listen address")
	socket := flag.String("socket", "/tmp/herdr-termhost.sock", "termhost daemon socket path")
	flag.Parse()

	indexHTML, err := webFS.ReadFile("web/index.html")
	if err != nil {
		log.Fatalf("gateway2: read embedded page: %v", err)
	}

	cwd, _ := os.Getwd()
	g, err := newGateway(*socket, cwd)
	if err != nil {
		log.Fatalf("gateway2: %v", err)
	}
	go g.daemon.run()

	s := rweb.NewServer(rweb.ServerOptions{Address: *addr, Verbose: true})
	s.Get("/", func(ctx rweb.Context) error {
		return ctx.WriteHTML(string(indexHTML))
	})
	s.WebSocket("/ws", func(ws *rweb.WSConn) error {
		return g.serve(ws)
	})

	log.Printf("gateway2: serving at http://localhost%s (termhost socket %s)", *addr, *socket)
	log.Fatal(s.Run())
}
