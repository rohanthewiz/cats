// Package ctlproto is the local control-API wire protocol for driving a running
// herdr server from a CLI or automation client. It is the second front-end onto
// the protocol-neutral §7 command table in internal/app — the browser (WS9,
// internal/browserproto) is the first. A client sends a newline-framed JSON
// Request naming an app command; the server dispatches it through app.Dispatcher
// and replies with a single JSON Response.
//
// The transport is a per-request round trip over a local (unix) socket: one
// Request in, one Response out, then the connection closes. That keeps the first
// slice simple; streaming methods (event subscriptions, wait-for-output) can be
// layered on later without changing this envelope.
package ctlproto

import (
	"bufio"
	"encoding/json"
	"io"
)

// ProtocolVersion is bumped on any breaking change to the envelope shapes. It is
// independent of browserproto.ProtocolVersion and orchestration.ProtocolVersion.
const ProtocolVersion = 1

// MethodPing is a liveness/handshake check answered directly by the control
// server (no session mutation); its Response.Data is a Pong. Every other Method
// is an app §7 command name (app.Cmd*) routed through the dispatcher.
const MethodPing = "ping"

// Request is one control command. Method is an app §7 command name (app.Cmd*) or
// MethodPing. Params is the command's params object, decoded by the dispatcher
// via app.JSONParamDecoder. ID is a client-chosen correlation string echoed back
// in the Response ("" is allowed).
type Request struct {
	ID     string          `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is the reply to a Request. OK reports success; on failure Error carries
// a human-readable message. Data is the command's result payload (e.g. an
// app.ReadResult for "read", a Pong for "ping"), absent when the command yields
// no data.
type Response struct {
	ID    string          `json:"id,omitempty"`
	OK    bool            `json:"ok"`
	Error string          `json:"error,omitempty"`
	Data  json.RawMessage `json:"data,omitempty"`
}

// Pong is the Response.Data for MethodPing: the server's protocol/identity so a
// client can confirm what it is talking to before issuing commands.
type Pong struct {
	Protocol int    `json:"protocol"`
	Service  string `json:"service"`
}

// newResponse builds a Response, marshaling data into Data when non-nil. A
// marshal failure degrades to an error Response so a result is always produced.
func newResponse(id string, ok bool, errMsg string, data any) Response {
	r := Response{ID: id, OK: ok, Error: errMsg}
	if data != nil {
		raw, err := json.Marshal(data)
		if err != nil {
			return Response{ID: id, OK: false, Error: "encode result: " + err.Error()}
		}
		r.Data = raw
	}
	return r
}

// writeMessage encodes v as one newline-terminated JSON frame on w.
func writeMessage(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// readRequest reads one newline-framed Request from br. A request written without
// a trailing newline before the peer closes still decodes (ReadBytes returns the
// buffered bytes alongside io.EOF); a truly empty read surfaces the read error.
func readRequest(br *bufio.Reader) (Request, error) {
	line, err := br.ReadBytes('\n')
	if len(line) == 0 {
		return Request{}, err
	}
	var req Request
	if e := json.Unmarshal(line, &req); e != nil {
		return Request{}, e
	}
	return req, nil
}

// readResponse reads one newline-framed Response from br (client side).
func readResponse(br *bufio.Reader) (Response, error) {
	line, err := br.ReadBytes('\n')
	if len(line) == 0 {
		return Response{}, err
	}
	var resp Response
	if e := json.Unmarshal(line, &resp); e != nil {
		return Response{}, e
	}
	return resp, nil
}
