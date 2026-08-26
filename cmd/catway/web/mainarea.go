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
// stop server) and is wired in js/40-boot.js.
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
