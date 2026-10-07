//go:build ghostty

package main

import (
	"path/filepath"
	"testing"

	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/clipboard"
	"github.com/rohanthewiz/cats/internal/ctlproto"
	"github.com/rohanthewiz/cats/internal/orchestration"
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

// A pane may set the user's clipboard but may not empty it. An OSC 52 write with
// no payload reaches catway as a pane_clipboard with empty data, and relaying it
// would have the mac app run pbcopy with no input, which empties the pasteboard
// with nothing on screen to say why. The clear has to stop at dispatch in both
// shapes an empty []byte takes on the seam (`""` from the Host's parser, `null`
// from a nil slice), and a real write right behind it still has to reach the
// browser. Without that last part, a handler that dropped every write would
// also pass.
func TestPaneClipboardClearIsNotRelayed(t *testing.T) {
	o, err := newOrch(filepath.Join(t.TempDir(), "s.sock"), t.TempDir())
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	c := &client{o: o, out: make(chan []byte, 8), trans: map[uint32]*browserproto.FrameTranslator{}}
	o.conns[c] = struct{}{}
	pid := uint32(o.session.AllPaneIDs()[0])
	d := o.hosts[o.defaultHost]

	d.dispatch(orchestration.MsgPaneClipboard, mustJSON(t, orchestration.NewPaneClipboard(pid, []byte{})))
	d.dispatch(orchestration.MsgPaneClipboard, mustJSON(t, orchestration.NewPaneClipboard(pid, nil)))
	d.dispatch(orchestration.MsgPaneClipboard, mustJSON(t, orchestration.NewPaneClipboard(pid, []byte("hello"))))

	// The loop is not running, so the closures dispatch posted run here in
	// order. That is the same work o.run would do, without its dialer and
	// timers.
	for len(o.mailbox) > 0 {
		(<-o.mailbox)()
	}

	var writes []string
	for _, m := range drainDown(t, c) {
		if cb, ok := m.(*browserproto.Clipboard); ok {
			writes = append(writes, string(cb.Data))
		}
	}
	if len(writes) != 1 || writes[0] != "hello" {
		t.Fatalf("clipboard writes relayed = %q, want only \"hello\"", writes)
	}
}
