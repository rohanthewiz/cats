// Package browserproto is the server side of the WS9 browser protocol. The
// message and command types themselves live in package wire, a leaf package
// that out-of-tree clients import; this package re-exports them (wire_aliases.go)
// and adds what only the server can do: frame.go translates β orchestration
// frames into wire messages, layout.go builds the layout message from
// internal/layout + internal/workspace state. Full spec:
// ai_docs/phase-c-ws9-protocol.md.
package browserproto
