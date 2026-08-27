package web

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

// The page's stylesheet and its front-end script, kept as ordinary .css/.js
// files rather than as Go string literals so an editor still lints and
// highlights them. Both are stitched back together at package init and served
// inside a single <style>/<script> in the one document catway serves.
//
// Why concatenate rather than serve the parts as separate assets:
//
//   - The script is one closure. Every function in it shares a single lexical
//     scope (the `(() => { … })()` this package wraps the parts in) — panes,
//     layoutMsg, sendCmd and a hundred others are closed-over locals, not
//     exports. Split across <script> tags they would each get their own scope
//     and the whole thing would come apart; making them real modules means
//     naming and threading every one of those bindings, which is a rewrite of
//     the front-end, not a re-filing of it. Concatenation in a fixed order is
//     exactly the semantics the single <script> already had.
//   - The cascade is order-dependent in the same way: several later rules win
//     only because they come later at equal specificity. A fixed order keeps
//     that guarantee at the one place it is now written down.
//   - The served page stays a single self-contained document, so the "/" route,
//     the auth middleware and renderPage's <head> injection are untouched.
//
// The split is therefore a source-tree split: the browser sees exactly the
// bytes it saw before, and a reader gets ~30-line-to-500-line files instead of
// one 7,400-line page.

//go:embed css/*.css
var cssFS embed.FS

//go:embed js/*.js
var jsFS embed.FS

// mark.svg is the traced cat's-head logo. It lives in its own file because it
// is machine-generated: `cd scripts/icon/gen-trace && go run .` rewrites the
// path data wholesale, and replacing a file is a cleaner regeneration step than
// splicing a 4KB literal into Go source. See BrandMark for what the two
// hand-tuned attributes on the <svg> are doing.
//
//go:embed mark.svg
var markSVG string

// cssFiles is the cascade, in order. Prefixed numerically so the directory
// listing reads the same way, but the order that actually ships is this list —
// a file the list does not name is not served, and webTestFilesListed (see
// web_test.go) fails the build if the two ever drift apart.
var cssFiles = []string{
	"01-theme.css",      // :root custom properties — the palette renderPage overrides
	"02-layout.css",     // <html>/<body> reset and the #app column grid
	"03-splitter.css",   // the sidebar's drag gutter, plus the folded-away state
	"04-brand.css",      // sidebar shell, wordmark, logo mark, fold button, build badge
	"05-sections.css",   // the shared section / h2 / ul / li skeleton
	"06-usage.css",      // USAGE rows, meters, sparklines, and the heading's refresh control
	"07-workspaces.css", // WORKSPACES rows, todo badge, lock mark
	"08-hosts.css",      // HOSTS rows
	"09-history.css",    // HISTORY rows
	"10-panelist.css",   // workspace-row host/window badges, then PANES rows and agent-state colors
	"11-agentlist.css",  // AGENTS rows
	"12-main.css",       // #main grid, topbar, tabbar, the pane boxes and their headers
	"13-statusbar.css",  // the toolbar buttons at the top right
	"14-chat.css",       // the ACP chat side panel
	"15-toasts.css",     // toast stack
	"16-splits.css",     // draggable pane borders
	"17-modal.css",      // #overlay and the .modal chrome every dialog shares
	"18-ctxmenu.css",    // right-click menus
	"19-panetip.css",    // the pane-row hover card
	"20-palette.css",    // command palette / navigator
	"21-plugins.css",    // plugins dialog (borrows .pal's rows)
	"22-help.css",       // keybindings help
	"23-banner.css",     // the top banner (update ready / link errors)
	"24-toolbar.css",    // gear glyph and its update badge
	"25-dragdrop.css",   // drop bars and the grabbing cursor
	"26-settings.css",   // settings modal + worktree dialogs
	"27-picker.css",     // the inline directory picker
	"28-flags.css",      // user flags: the sidebar mark, the pane-header chip
}

// jsFiles is the front-end, in evaluation order. Order matters twice: `const`
// and `let` at the top of the closure are not hoisted, so anything that reads
// one at load time has to come after it (01-bootstrap declares nearly all of
// them, which is why it is first), and the handful of files that wire event
// listeners or run an IIFE do their work the moment they are reached. Function
// declarations hoist, so the ordinary call graph is free to point backwards.
var jsFiles = []string{
	"01-bootstrap.js",  // closure-wide constants, DOM handles, persisted prefs, font sizing
	"02-color.js",      // packed-u32 colors; newPane and the sidebar's small builders
	"03-layout.js",     // place pane DOM from the server's rects; text inset
	"04-borders.js",    // draggable split borders -> pane.resize_border
	"05-labels.js",     // model/agent label condensing, hue assignment, path shortening
	"06-chrome.js",     // the per-pane header
	"07-workspaces.js", // WORKSPACES: rollups, summaries, todo/lock marks, rows
	"08-panelist.js",   // PANES: the pane.list inventory, grouped by workspace
	"09-hovercard.js",  // the pane-row hover card
	"10-buildbadge.js", // the build hash beside the wordmark
	"11-drag.js",       // press-to-activate, tab/workspace reorder, pane swap
	"12-usage.js",      // USAGE: windows, meters, sparklines, host stats
	"13-agents.js",     // the agents rollup and the auto-reveal it can trigger
	"14-hosts.js",      // the cathost roster and the lookups over it
	"15-history.js",    // HISTORY: the command ledger
	"16-hostdialog.js", // attach-host dialog, host menu, HOSTS rendering
	"17-agentlist.js",  // AGENTS rendering, focus and lock marking
	"18-render.js",     // canvas painting: cells, scrollbar, copy cursor, selection
	"19-messages.js",   // inbound message dispatch and the send helpers
	"20-keys.js",       // keyboard: modifiers, routing, the global keymap
	"21-clipboard.js",  // clipboard read/write and paste
	"22-mouse.js",      // mouse -> cell coordinates
	"23-copymode.js",   // keyboard copy-mode and scrollback selection
	"24-upload.js",     // drag-and-drop file upload
	"25-modal.js",      // overlay/modal/context-menu infrastructure
	"26-picker.js",     // the inline directory picker and the dialog field helpers
	"27-dialogs.js",    // rename dialogs, close confirms, new-workspace
	"28-ctxmenu.js",    // pane / tab / workspace menus
	"29-worktree.js",   // git-worktree dialogs
	"30-plugins.js",    // plugins dialog
	"31-palette.js",    // command palette / navigator
	"32-help.js",       // keybindings help
	"33-settings.js",   // settings modal over config.get/config.set
	"34-banner.js",     // update banner, connection status, toasts
	"35-notify.js",     // agent needs-attention / finished notifications
	"36-windows.js",    // multi-window awareness and the URL that names a workspace
	"37-session.js",    // the WebSocket connection, resize, window focus
	"38-sidebar.js",    // sidebar width, folding, the reveal handle
	"39-chat.js",       // the ACP chat side panel
	"40-boot.js",       // toolbar wiring, then the three calls that start the app
}

// stylesheet and script are assembled once, at package init: the page is
// rendered at startup and on reload, never per request, so there is nothing to
// gain from doing this lazily and a startup failure is better than a lazy one.
var (
	stylesheet = mustJoin(cssFS, "css", cssFiles)
	script     = mustJoin(jsFS, "js", jsFiles)
)

// mustJoin concatenates the named files, in the order given, from an embedded
// directory. It panics rather than returning an error because every input is
// compiled into the binary: the only way to fail is for the list above and the
// directory to disagree, which is a build-time mistake, not a runtime
// condition. web_test.go asserts they agree in both directions.
func mustJoin(fsys fs.FS, dir string, names []string) string {
	var sb strings.Builder
	for _, name := range names {
		b, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			panic(fmt.Sprintf("web: %s is listed but not embedded: %v", name, err))
		}
		sb.Write(b)
	}
	return sb.String()
}
