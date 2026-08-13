// Package ctlproto is the local control-API wire protocol for driving a running
// cats server from a CLI or automation client. It is the second front-end onto
// the protocol-neutral §7 command table in internal/app — the browser (WS9,
// internal/browserproto) is the first. A client sends a newline-framed JSON
// Request naming an app command; the server dispatches it through app.Dispatcher
// and replies with a single JSON Response.
//
// The default transport is a per-request round trip over a local (unix) socket:
// one Request in, one Response out, then the connection closes. Two methods layer
// onto that base without disturbing it:
//
//   - pane.wait_for_output is still one Request → one Response, only the response
//     is delayed until the pane's output matches (or the wait times out). It rides
//     the unchanged envelope; the server just grants it a longer backstop.
//   - events.subscribe (MethodEventsSubscribe) is the one streaming method: the
//     server writes an ack Response, then zero or more Event frames on the same
//     newline-framed connection until the client disconnects (see stream.go).
package ctlproto

import (
	"bufio"
	"encoding/json"
	"io"
	"slices"
)

// ProtocolVersion is bumped on any breaking change to the envelope shapes. It is
// independent of browserproto.ProtocolVersion and orchestration.ProtocolVersion.
const ProtocolVersion = 1

// MethodPing is a liveness/handshake check answered directly by the control
// server (no session mutation); its Response.Data is a Pong. Every other Method
// is an app §7 command name (app.Cmd*) routed through the dispatcher.
const MethodPing = "ping"

// MethodEventsSubscribe is the one streaming method: instead of a single Response
// the server sends an ack Response then a stream of Event frames (see stream.go).
// The server routes it to its StreamDispatch rather than the unary dispatcher, so
// it is not an app §7 command name and is absent from app.CommandNames().
const MethodEventsSubscribe = "events.subscribe"

// MethodPair mints a single-use device-pairing grant; its Response.Data is a
// PairInfo. Like MethodPing it is deliberately *not* an app §7 command: the §7
// table is shared with the browser front end, and a browser that could mint
// pairing grants would be an escalation path from a stolen session cookie to a
// permanent second credential. Keeping it off that table is what confines it to
// the owner-only control socket. The server answers it before app.Dispatcher
// ever sees the method name.
const MethodPair = "pair"

// MethodClipboardRead returns the host system clipboard's text; its
// Response.Data is a ClipboardData. Like MethodPair it is deliberately NOT an app
// §7 command, and for the same structural reason: the §7 table is shared with the
// browser front end, so anything on it is reachable from a network-facing client
// holding a session cookie. The clipboard is the user's, not the session's — it
// holds whatever they last copied anywhere on the machine, in any application —
// and a remote client that could ask for it would be reading over their shoulder.
// Keeping the method off that table is what confines it to the owner-only control
// socket; the server answers it before app.Dispatcher ever sees the name.
//
// There is no config flag gating it beyond that. A caller already holding this
// socket can run `pane.send_input` to type `pbpaste` into any shell pane and read
// the answer back with `capture`, so a switch here would gate nothing it does not
// already have — it would only make the honest path look more privileged than the
// dishonest one. The socket's 0600 owner-only mode is the boundary.
//
// The browser needs none of this: it has navigator.clipboard (and, in the mac
// app, catapp's native bridge) for its own clipboard already.
const MethodClipboardRead = "clipboard.read"

// ClipboardData is the Response.Data for MethodClipboardRead.
//
// An empty Text with no error is an empty clipboard — an ordinary answer, since
// "nothing has been copied yet" is a normal state and not a fault the caller
// should report as one. Truncated marks a clipboard larger than
// clipboard.MaxBytes: the text is the leading portion, cut on a whole-rune
// boundary, so a consumer can still use it while telling the user it is partial.
type ClipboardData struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

// PairInfo is the Response.Data for MethodPair: everything a new device needs to
// reach this catway, and nothing it does not.
//
// Token is short-lived and single-use (internal/gwauth), redeemed by POSTing it
// to /login in place of the password; what the device ends up holding is an
// ordinary session credential, which a restart or the session TTL revokes. The
// shared secret is never part of this payload — that is the entire point of the
// pairing flow.
type PairInfo struct {
	// URL is the catway's base URL as a client on another device should reach
	// it — a routable address, never localhost.
	URL string `json:"url"`
	// Token is the pairing grant. Treat it as a secret for its short life.
	Token string `json:"token"`
	// ExpiresAt is when the token stops being redeemable, in Unix seconds.
	ExpiresAt int64 `json:"expires_at"`
	// Fingerprint is the hex SHA-256 of the server certificate's DER, for a
	// client pinning a self-signed cert on first use. Empty when the catway is
	// serving plain HTTP, or when the certificate could not be read.
	//
	// It pins the certificate rather than the public key because gwtls mints a
	// fresh key on every regeneration — SPKI pinning would survive nothing that
	// DER pinning does not.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// TransportMethods returns every method answered by the control layer itself
// rather than routed through app.Dispatcher, in a stable order.
//
// It exists because these names are the exception to "a method is an app §7
// command" and three separate places had grown their own `m != MethodPing && m
// != …` chain to say so — a client validating a typed method name, the help
// topic lookup, and the tests that assert a §7 command never shadows one of
// these. A fourth member (clipboard.read) is what made the drift a real risk:
// a list nobody updates silently rejects the new method as unknown.
func TransportMethods() []string {
	return []string{MethodPing, MethodEventsSubscribe, MethodPair, MethodClipboardRead}
}

// IsTransportMethod reports whether m is answered by the transport rather than by
// the §7 command table (app.CommandNames).
func IsTransportMethod(m string) bool {
	return slices.Contains(TransportMethods(), m)
}

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

// Event is one server-pushed frame on a streaming connection (events.subscribe).
// After the subscribe ack Response, the server writes zero or more Events on the
// same newline-framed transport until the client disconnects. Name is the event
// type (app.EventPane*); Data is its payload (an app.PaneExitedEvent, etc.). A
// streaming client reads the ack as a Response, then reads Events — the two frame
// shapes never interleave, so each is unambiguous.
type Event struct {
	Name string          `json:"event"`
	Data json.RawMessage `json:"data,omitempty"`
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

// readEvent reads one newline-framed Event from br (streaming client side). A
// clean stream end surfaces the read error (io.EOF) with an empty line.
func readEvent(br *bufio.Reader) (Event, error) {
	line, err := br.ReadBytes('\n')
	if len(line) == 0 {
		return Event{}, err
	}
	var ev Event
	if e := json.Unmarshal(line, &ev); e != nil {
		return Event{}, e
	}
	return ev, nil
}
