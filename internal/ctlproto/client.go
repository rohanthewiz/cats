package ctlproto

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"time"
)

// DefaultSocket is the control socket path used when neither a flag nor the
// HERDR_CONTROL_SOCKET env var overrides it. It mirrors the termhost socket
// convention (/tmp/herdr-*.sock) so server and client agree by default.
const DefaultSocket = "/tmp/herdr-control.sock"

// SocketEnvVar overrides the control socket path for both server and client.
const SocketEnvVar = "HERDR_CONTROL_SOCKET"

// ResolveSocket picks the control socket path with the standard precedence: an
// explicit non-empty override (a CLI flag) wins; else HERDR_CONTROL_SOCKET; else
// DefaultSocket. Server and client both call it so they agree by default.
func ResolveSocket(override string) string {
	if override != "" {
		return override
	}
	if v := os.Getenv(SocketEnvVar); v != "" {
		return v
	}
	return DefaultSocket
}

// Call dials the control socket, sends one Request, and returns the server's
// Response. It is the whole client transport: connect, write one framed request,
// read one framed response, close. timeout bounds the dial and the round trip;
// use 0 for no deadline.
func Call(socket string, req Request, timeout time.Duration) (Response, error) {
	dialTimeout := timeout
	if dialTimeout == 0 {
		dialTimeout = 5 * time.Second // still bound the dial even with no round-trip deadline
	}
	conn, err := net.DialTimeout("unix", socket, dialTimeout)
	if err != nil {
		return Response{}, fmt.Errorf("dial control socket %s: %w", socket, err)
	}
	defer conn.Close()
	if timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(timeout))
	}
	if err := writeMessage(conn, req); err != nil {
		return Response{}, fmt.Errorf("send request: %w", err)
	}
	resp, err := readResponse(bufio.NewReader(conn))
	if err != nil {
		return Response{}, fmt.Errorf("read response: %w", err)
	}
	return resp, nil
}
