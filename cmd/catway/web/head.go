package web

import "github.com/rohanthewiz/element"

// Head is the document head: the two metas the app needs, the title, and the
// assembled stylesheet.
//
// renderPage (cmd/catway/page.go) injects a second <style> plus a few small
// <script>s immediately before </head>, i.e. after everything here. That order
// is what lets the operator's theme override the :root custom properties this
// stylesheet declares, so nothing may be appended to the head after the
// stylesheet without thinking about the cascade.
type Head struct{}

func (Head) Render(b *element.Builder) (x any) {
	b.Head().R(
		nl(b, 0),
		b.Meta("charset", "utf-8"),
		nl(b, 0),
		// user-scalable=no: pinch-zoom on a phone would scale the terminal
		// canvases, which are already sized in device pixels for the pane grid.
		b.Meta("name", "viewport", "content", "width=device-width, initial-scale=1, user-scalable=no"),
		nl(b, 0),
		b.Title().T("Cats Mux"),
		nl(b, 0),
		b.Style().T("\n", stylesheet),
		nl(b, 0),
	)
	return
}

// Script carries the front-end, wrapped in the arrow-IIFE that gives every
// js/*.js part the one shared closure they are written against — see the note
// at the top of assets.go. It sits at the end of <body> so the DOM the script
// looks up by id already exists when it runs; there is no defer/DOMContentLoaded
// anywhere in the front-end, and this placement is why none is needed.
type Script struct{}

func (Script) Render(b *element.Builder) (x any) {
	b.Script().T("\n(() => {\n", script, "})();\n")
	return
}
