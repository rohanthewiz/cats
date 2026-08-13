//go:build ghostty

package main

import (
	"testing"

	"github.com/rohanthewiz/cats/internal/clipboard"
	"github.com/rohanthewiz/cats/internal/ctlproto"
)

// The security boundary, asserted the same way pairing's is: clipboard.read is
// answered before app.Dispatcher, so it never becomes a §7 command the browser
// front end could also reach. If the intercept in controlDispatch were removed
// the request would land on the mailbox instead — which is what this measures —
// and from there it would fall through to the dispatcher's unknown-command
// branch, which is a silent downgrade rather than a visible break.
func TestControlDispatchAnswersClipboardWithoutTheDispatcher(t *testing.T) {
	o := &orch{mailbox: make(chan func(), 4)}

	r := &pairResponder{}
	o.controlDispatch(ctlproto.MethodClipboardRead, nil, r)
	if !r.done {
		t.Fatal("clipboard.read was not answered inline")
	}
	if n := len(o.mailbox); n != 0 {
		t.Fatalf("clipboard.read posted %d closure(s) onto the orchestrator loop", n)
	}

	// A §7 command still takes the loop, so the assertion above measures the
	// intercept and not an inert mailbox.
	o.controlDispatch("pane.list", nil, &pairResponder{})
	if n := len(o.mailbox); n != 1 {
		t.Fatalf("a §7 command posted %d closures, want 1", n)
	}
}

// On a host with a clipboard tool the handler answers with a ClipboardData —
// whatever the clipboard happens to hold, including nothing. An empty clipboard
// is a successful answer: reporting it as a failure would have an editor show an
// error for the ordinary state of having copied nothing yet.
func TestHandleClipboardRead(t *testing.T) {
	o := &orch{}
	r := &pairResponder{}
	o.handleClipboardRead(r)

	if !r.done {
		t.Fatal("handleClipboardRead did not answer")
	}
	if !clipboard.Available() {
		if r.err == "" {
			t.Fatal("a host with no clipboard reader should fail, not answer")
		}
		return
	}
	if r.err != "" {
		t.Fatalf("clipboard read failed: %s", r.err)
	}
	data, ok := r.data.(ctlproto.ClipboardData)
	if !ok {
		t.Fatalf("payload is %T, want ctlproto.ClipboardData", r.data)
	}
	if len(data.Text) > clipboard.MaxBytes {
		t.Fatalf("payload is %d bytes, over the %d cap", len(data.Text), clipboard.MaxBytes)
	}
}
