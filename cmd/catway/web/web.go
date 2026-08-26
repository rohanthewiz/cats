// Package web builds the single HTML document catway serves.
//
// It replaces what used to be one 7,400-line index.html. The markup is now Go
// element components (this file and its neighbours), the stylesheet is
// css/*.css and the front-end is js/*.js — see assets.go for how the two asset
// sets are ordered and stitched, and why they ship as one <style> and one
// <script> rather than as separate requests.
//
// The output is deliberately still ONE self-contained document: cmd/catway
// serves it from a single "/" route and renderPage (cmd/catway/page.go) splices
// the operator's theme, keybindings, build identity and home directory in just
// before </head>. Nothing in this package knows about any of that — it produces
// the base page, and the base page has to keep working with no injection at
// all, which is why every themed value in the CSS carries a fallback.
//
// Component layout:
//
//	Page       the document: <head> + <body>
//	  Head     meta, title, the assembled <style>
//	  Sidebar  brand + the six sidebar sections
//	  Main     topbar (tabbar + statusbar) and the pane surface
//	  Chat     the ACP side panel
//
// Everything below the sidebar/main/chat level is built by the front-end at
// runtime, so this package's markup stops at the containers the script looks up
// by id. A container that disappears from here is a null-reference in the
// script, which is why each one is named in a comment beside it.
package web

import (
	"strings"

	"github.com/rohanthewiz/element"
)

// Page renders the base document. catway calls it once, at startup, and keeps
// the result as the base every later config change re-renders from — so this
// builds a fresh builder rather than taking one from the pool, and nothing here
// is on a per-request path.
func Page() []byte {
	b := element.NewBuilder()
	// The doctype is written by hand and the root element built through the
	// generic Ele(): b.Html() emits "<!DOCTYPE html>" itself, with no line
	// break after it, and the two belong on separate lines.
	b.WriteString("<!DOCTYPE html>\n")
	b.Ele("html", "lang", "en").R(
		Head{}.Render(b),
		nl(b, 0),
		b.Body().R(
			nl(b, 0),
			b.Div("id", "app").R(
				Sidebar{}.Render(b),
				nl(b, 1),
				// The drag gutter between the sidebar and the panes. Doubles as
				// the reveal handle once the sidebar is folded away — the CSS
				// paints the handle with body.sb-hidden #splitter::after.
				b.Div("id", "splitter", "title", "drag to resize the sidebar (double-click to reset)").R(),
				Main{}.Render(b),
				Chat{}.Render(b),
				nl(b, 0),
			),
			nl(b, 0),
			// Both live outside #app: they are fixed-position overlays, and
			// #app is the column grid everything else has to fit inside.
			b.Div("id", "banner").R(),
			nl(b, 0),
			b.Div("id", "toasts").R(),
			nl(b, 0),
			Script{}.Render(b),
			nl(b, 0),
		),
		nl(b, 0),
	)
	b.WriteString("\n")
	return b.Bytes()
}

// nl breaks the line and indents, so the served document is still readable in
// "view source" rather than being one enormous line.
//
// This is only safe because every container it is used in is a flex or grid
// box, and those generate no box at all for a whitespace-only text node. It is
// deliberately absent from #brand, which is a plain inline run: a line break
// there would collapse to a rendered space between the cat and the wordmark.
func nl(b *element.Builder, depth int) any {
	return b.T("\n" + strings.Repeat("  ", depth))
}
