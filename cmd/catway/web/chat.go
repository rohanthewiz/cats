package web

import "github.com/rohanthewiz/element"

// Chat is the ACP agent side panel — the fourth column of #app.
//
// It is closed by default and stays out of the layout entirely while closed:
// body.chat-open is what gives --chat-w a width, and without that class the
// column is 0px and #chat is display:none. That matters because the pane grid
// is measured from #main, so an open panel genuinely reshapes the terminals
// rather than overlaying them — see setChatOpen in js/39-chat.js.
//
// Everything inside #chat-log is built at runtime from chat messages; the
// markup here is the frame: a header with a live status, the scrolling log, and
// the composer.
type Chat struct{}

func (Chat) Render(b *element.Builder) (x any) {
	nl(b, 1)
	b.Aside("id", "chat").R(
		nl(b, 2),
		b.Div("id", "chat-head").R(
			b.Span("id", "chat-title").T("Chat"),
			// Fills with the agent's connection/turn state; empty until one is
			// attached, which is why the header still reads as just a title.
			b.Span("id", "chat-status").R(),
		),
		nl(b, 2),
		b.Div("id", "chat-log").R(),
		nl(b, 2),
		b.Div("id", "chat-compose").R(
			nl(b, 3),
			// rows="2" is the floor; #chat-input's min-height carries the real
			// size. Enter sends and Shift+Enter breaks the line (js/39-chat.js),
			// which the placeholder says out loud because the opposite
			// convention is just as common.
			b.TextArea("id", "chat-input", "rows", "2",
				"placeholder", "Ask… (Enter sends, Shift+Enter for a new line)").R(),
			nl(b, 3),
			b.Div("id", "chat-ctl").R(
				// Stop starts disabled: there is no turn to cancel until one is
				// in flight, and the front-end flips it per turn.
				b.ButtonClass("cbtn", "id", "chat-stop", "title", "stop the current turn", "disabled", "").T("⏹"),
				b.ButtonClass("cbtn", "id", "chat-clear", "title", "clear the conversation").T("⌫"),
			),
			nl(b, 2),
		),
		nl(b, 1),
	)
	return
}
