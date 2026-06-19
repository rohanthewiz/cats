// Package herdrconn wraps a connection to a running herdr server's client
// socket: it performs the Hello handshake and exposes typed send/receive for the
// subset of the wire protocol a web gateway needs.
package herdrconn

import (
	"net"
	"sync"
	"time"

	"github.com/rohanthewiz/herdr-web/internal/wire"
)

// Conn is a single attached herdr client connection.
type Conn struct {
	c   net.Conn
	wmu sync.Mutex // serializes frame writes
}

// Dial connects to the herdr client socket and sends Hello with the given size.
func Dial(socket string, cols, rows uint16) (*Conn, error) {
	c, err := net.DialTimeout("unix", socket, 5*time.Second)
	if err != nil {
		return nil, err
	}
	conn := &Conn{c: c}
	if err := conn.send(wire.EncodeHello(cols, rows, 0, 0)); err != nil {
		_ = c.Close()
		return nil, err
	}
	return conn, nil
}

func (h *Conn) send(payload []byte) error {
	h.wmu.Lock()
	defer h.wmu.Unlock()
	return wire.WriteFrame(h.c, payload)
}

// SendInput forwards raw terminal input bytes to the server.
func (h *Conn) SendInput(data []byte) error { return h.send(wire.EncodeInput(data)) }

// SendPaste forwards pasted text as a structured paste event.
func (h *Conn) SendPaste(text string) error { return h.send(wire.EncodePaste(text)) }

// SendClipboardImage forwards a pasted image for the server to stage and paste.
func (h *Conn) SendClipboardImage(ext string, data []byte) error {
	return h.send(wire.EncodeClipboardImage(ext, data))
}

// Resize notifies the server of a new terminal size.
func (h *Conn) Resize(cols, rows uint16) error { return h.send(wire.EncodeResize(cols, rows, 0, 0)) }

// Detach requests a graceful disconnect.
func (h *Conn) Detach() error { return h.send(wire.EncodeDetach()) }

// Read returns the next decoded server message.
func (h *Conn) Read() (*wire.ServerMessage, error) {
	payload, err := wire.ReadFrame(h.c)
	if err != nil {
		return nil, err
	}
	return wire.DecodeServerMessage(payload)
}

// Close closes the underlying socket.
func (h *Conn) Close() error { return h.c.Close() }
