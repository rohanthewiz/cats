package web

import "github.com/rohanthewiz/element"

// Main is the middle column: a fixed-height top bar over the pane surface.
//
// #main is a two-row grid (30px + 1fr) rather than a flex column so the pane
// surface gets an exact pixel height to lay terminals out in — the front-end
// measures #panes and hands the server a rows/cols grid, and a row that could
// be fractional would make that measurement drift.
type Main struct{}

func (Main) Render(b *element.Builder) (x any) {
	nl(b, 1)
	b.Div("id", "main").R(
		nl(b, 2),
		b.Div("id", "topbar").R(
			nl(b, 3),
			// Tabs are rendered from the layout message (renderTabbar), which
			// includes the trailing "+" row — hence an empty container here.
			b.Div("id", "tabbar").R(),
			nl(b, 3),
			StatusBar{}.Render(b),
			nl(b, 2),
		),
		nl(b, 2),
		// The pane surface. Every .pane is absolutely positioned inside it from
		// the server's computed rects; nothing about the BSP layout is
		// expressed in CSS.
		b.Div("id", "panes").R(),
		nl(b, 1),
	)
	return
}

// StatusBar is the cluster at the top right. Each entry is a .tbtn carrying a
// .tmk glyph and a word: the glyph is the keyboard route where there is one
// (⌘K), or a mark that stands for the thing where there is not.
//
// Plugins earns a top-level slot rather than a place in the gear menu because
// installing and running plugins is routine work, not a settings excursion. The
// gear is the launcher menu (settings / keybinds / reload config / update /
// stop server) and is wired in js/41-boot.js.
//
// The macro recorder earns one for a different reason: it is a MODE. Arming it
// from a menu would work, but nothing in a menu can show that the session is
// currently being recorded, and a recorder somebody forgot about is the failure
// this whole surface exists to prevent (see runbook.record's own note: an
// armed recording that captured nothing is indistinguishable from one that is
// working). So the slot is not really a button, it is an indicator that also
// takes clicks — hence the live step count in its label.
//
// The spans are deliberately whitespace-free inside: .tbtn is a flex row whose
// gap does the spacing, so a stray space would only ever add to it.
type StatusBar struct{}

func (StatusBar) Render(b *element.Builder) (x any) {
	b.Div("id", "statusbar").R(
		nl(b, 4),
		tbtn(b, "palhint", "command palette (⌘K / Ctrl+Alt+K)", "⌘K", "palette"),
		nl(b, 4),
		tbtn(b, "pluginsbtn", "plugins — install, run, update", "⧉", "plugins"),
		nl(b, 4),
		tbtn(b, "chatbtn", "chat — AI agent side panel", "✦", "chat"),
		nl(b, 4),
		// The recorder starts idle and stays out of the way: the glyph is the
		// hollow mark, the word is "rec", and js/40-record.js swaps in the
		// filled dot and the captured-step count while a recording is armed.
		// Like the gear, its glyph is wrapped in a .g so the armed pulse has a
		// box the size of the GLYPH to sit in rather than the padded button.
		b.SpanClass("tbtn", "id", "recbtn", "title", "record a macro (runbook.record)").R(
			b.SpanClass("tmk").R(
				b.SpanClass("g").T("◦"),
			),
			b.T("rec"),
			// The captured-step count, filled in by the front-end and empty
			// whenever nothing is being recorded. It is a span the server
			// renders rather than one the client creates, so the idle button
			// and the armed one are the same DOM shape and the count cannot
			// land in the wrong place; .tbtn's flex gap would otherwise show
			// as a trailing space, which is what #recbtn .n:empty is for.
			b.SpanClass("n").R(),
		),
		nl(b, 4),
		// The gear is the odd one out: no word, and its glyph is wrapped in a
		// .g so the update badge (#gear.badge .g::after) has something
		// position:relative to hang off that is the glyph rather than the
		// whole button.
		b.SpanClass("tbtn", "id", "gear", "title", "menu").R(
			b.SpanClass("g").T("⚙"),
		),
		nl(b, 3),
	)
	return
}

func tbtn(b *element.Builder, id, title, mark, label string) any {
	b.SpanClass("tbtn", "id", id, "title", title).R(
		b.SpanClass("tmk").T(mark),
		b.T(label),
	)
	return nil
}
