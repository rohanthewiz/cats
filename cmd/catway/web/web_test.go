package web

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/flags"
)

// The one invariant the split introduces: the ordered lists in assets.go and
// the directories on disk have to name exactly the same files. A file added to
// css/ or js/ but not to the list is silently not served — the page would
// render fine and just be missing a feature — so it is asserted rather than
// left to review.
func TestAssetListsMatchDirectories(t *testing.T) {
	for _, c := range []struct {
		dir   string
		fsys  fs.FS
		names []string
		glob  string
	}{
		{"css", cssFS, cssFiles, "css/*.css"},
		{"js", jsFS, jsFiles, "js/*.js"},
	} {
		found, err := fs.Glob(c.fsys, c.glob)
		if err != nil {
			t.Fatalf("%s: glob: %v", c.dir, err)
		}
		for i := range found {
			found[i] = strings.TrimPrefix(found[i], c.dir+"/")
		}
		listed := append([]string(nil), c.names...)
		sort.Strings(listed)
		sort.Strings(found)
		if strings.Join(listed, "\n") != strings.Join(found, "\n") {
			t.Errorf("%s: the ordered list in assets.go and the directory disagree\n listed: %v\n  found: %v",
				c.dir, listed, found)
		}
		// The numeric prefixes exist so `ls` reads in load order. If they ever
		// stop agreeing with the list, the listing lies about what ships.
		if !sort.StringsAreSorted(c.names) {
			t.Errorf("%s: the list is not in filename order — either renumber the files or drop the prefixes", c.dir)
		}
	}
}

// Assembly, end to end: the parts have to come back as one <style> and one
// <script>, in list order, with the closure wrapper the js/ parts are written
// against.
func TestPageAssembly(t *testing.T) {
	page := string(Page())

	if !strings.HasPrefix(page, "<!DOCTYPE html>\n<html lang=\"en\">") {
		t.Errorf("page does not start with the doctype and <html lang>:\n%.80s", page)
	}
	// renderPage (cmd/catway/page.go) splices the operator's config in here.
	if !strings.Contains(page, "</head>") {
		t.Error("no </head> — renderPage has nowhere to inject the theme and keybindings")
	}

	style := between(t, page, "<style>", "</style>")
	if style != "\n"+stylesheet {
		t.Error("the <style> is not the concatenated stylesheet")
	}
	inPage := between(t, page, "<script>", "</script>")
	if want := "\n(() => {\n" + script + "})();\n"; inPage != want {
		t.Error("the <script> is not the concatenated front-end inside its closure")
	}

	// Order, spot-checked at both ends: the closure's shared bindings are
	// declared in 01-bootstrap and the app is started in 40-boot, and swapping
	// the two would be a load-time ReferenceError in a browser we cannot run
	// here.
	first := strings.Index(inPage, "const PV = 1")
	last := strings.Index(inPage, "\n  connect();\n")
	if first < 0 || last < 0 || first > last {
		t.Error("the front-end is not in load order: the bootstrap declarations must precede the start-up calls")
	}
}

// Every id and class the front-end looks up by hand has to exist in the markup:
// a missing container is a null dereference on page load, and the components
// that hold them are now spread across five files. The list is the set of ids
// js/01-bootstrap.js resolves with getElementById at start-up, plus the section
// wrappers the fold controls need.
func TestMarkupCarriesTheIdsTheFrontEndResolves(t *testing.T) {
	page := string(Page())
	for _, id := range []string{
		"app", "sidebar", "splitter", "main", "topbar", "tabbar", "panes",
		"brand", "sb-fold",
		"sec-usage", "usage-hctl", "usage-list",
		"sec-hosts", "host-hctl", "host-list",
		"sec-workspaces", "ws-hctl", "ws-list", "ws-count", "ws-global-todo",
		"sec-panes", "pane-hctl", "pane-list",
		"sec-agents", "agent-hctl", "agent-list",
		"sec-history", "hist-hctl", "hist-list",
		"statusbar", "palhint", "pluginsbtn", "chatbtn", "gear",
		"chat", "chat-head", "chat-title", "chat-status", "chat-log",
		"chat-compose", "chat-input", "chat-ctl", "chat-stop", "chat-clear",
		"banner", "toasts",
	} {
		if !regexp.MustCompile(`\bid="` + regexp.QuoteMeta(id) + `"`).MatchString(page) {
			t.Errorf("markup is missing id=%q — the front-end resolves it at start-up", id)
		}
	}
	// Hidden-by-default sections and the disabled stop button: state the
	// front-end flips rather than sets, so it has to start right. Matched a tag
	// at a time because element writes attributes in map order, so nothing may
	// assume id comes before hidden.
	for _, c := range []struct{ tag, id, boolAttr string }{
		{"section", "sec-hosts", "hidden"},   // stays hidden while the session has one host
		{"section", "sec-history", "hidden"}, // stays hidden until the ledger has rows
		{"button", "chat-stop", "disabled"},  // nothing to cancel until a turn is in flight
	} {
		if !hasBoolAttr(page, c.tag, c.id, c.boolAttr) {
			t.Errorf("<%s id=%q> lost its %s attribute", c.tag, c.id, c.boolAttr)
		}
	}
}

// hasBoolAttr reports whether the named element carries a boolean attribute.
// element writes those as key="" (its attributes are always key/value pairs),
// which HTML5 reads as present-and-true, so both spellings are accepted.
func hasBoolAttr(page, tag, id, attr string) bool {
	for _, m := range regexp.MustCompile(`<`+tag+`\b[^>]*>`).FindAllString(page, -1) {
		if !strings.Contains(m, `id="`+id+`"`) {
			continue
		}
		return strings.Contains(m, attr+`=""`) || regexp.MustCompile(`\b`+attr+`[\s>]`).MatchString(m)
	}
	return false
}

// The logo is machine-generated and written into the page verbatim; the risk is
// that a regeneration drops one of the two hand-tuned attributes, which are on
// the <svg> precisely so a regeneration does not have to touch them.
func TestBrandMarkKeepsItsTuning(t *testing.T) {
	page := string(Page())
	for _, want := range []string{
		`class="mark"`,
		`preserveAspectRatio="none"`, // without it the squashed box letterboxes
		`stroke-width="3.6"`,         // without it the strokes fall under a device pixel
		`fill="currentColor"`,        // what makes the mark follow a live theme switch
		`<path d="M48.00,284.73`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("mark.svg lost %q", want)
		}
	}
	// #brand is an inline run, so nothing may separate the mark from the
	// wordmark — a line break there renders as a space.
	if !strings.Contains(page, "</svg>Cats Mux") {
		t.Error("whitespace crept in between the logo mark and the wordmark")
	}
}

func between(t *testing.T, s, open, close string) string {
	t.Helper()
	i := strings.Index(s, open)
	if i < 0 {
		t.Fatalf("no %s in the page", open)
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		t.Fatalf("no %s in the page", close)
	}
	return rest[:j]
}

// The flag vocabulary is written down twice — once in Go (internal/flags, which
// validates every workspace.flag / pane.flag) and once in JavaScript
// (FLAG_DEFS in js/07-workspaces.js, which draws the menu and the marks). The
// duplication is deliberate: the browser has to render a menu of kinds before
// any flag exists, and fetching six compile-time constants over the wire would
// be a message for nothing.
//
// What is not acceptable is the two lists drifting. A kind added in Go and not
// in the browser is a flag a script can set and the sidebar draws with no
// colour and no name; one added only in the browser is a menu row the server
// refuses. So the JS is parsed here and compared field by field.
func TestWebFlagVocabularyMatchesGo(t *testing.T) {
	src, err := fs.ReadFile(jsFS, "js/07-workspaces.js")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	block := regexp.MustCompile(`(?s)const FLAG_DEFS = \[(.*?)\n  \];`).FindSubmatch(src)
	if block == nil {
		t.Fatal("FLAG_DEFS not found in js/07-workspaces.js — if it moved, move this test's parse with it")
	}
	entry := regexp.MustCompile(`\{ kind: "([^"]*)", glyph: "([^"]*)", label: "([^"]*)", meaning: "([^"]*)" \}`)
	var got []string
	for _, m := range entry.FindAllStringSubmatch(string(block[1]), -1) {
		got = append(got, strings.Join(m[1:], "|"))
	}
	var want []string
	for _, d := range flags.Defs() {
		want = append(want, strings.Join([]string{string(d.Kind), d.Glyph, d.Label, d.Meaning}, "|"))
	}
	// Order matters as well as content: FLAG_DEFS is the menu, and the Go table
	// documents its order as "the ones that ask for attention first".
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("FLAG_DEFS and internal/flags disagree\n  js: %v\n  go: %v", got, want)
	}
}

// Every named kind needs a colour rule, or a flag the server happily accepts
// draws in whatever the surrounding row was using — which reads as "no flag"
// exactly as often as it reads as the wrong one.
func TestEveryFlagKindHasAColour(t *testing.T) {
	css, err := fs.ReadFile(cssFS, "css/28-flags.css")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, d := range flags.Defs() {
		if !strings.Contains(string(css), ".fk-"+string(d.Kind)+" ") {
			t.Errorf("css/28-flags.css has no rule for .fk-%s", d.Kind)
		}
	}
	// And the fallback for a glyph the user invented, which no named rule covers.
	if !strings.Contains(string(css), ".fk-custom ") {
		t.Error("css/28-flags.css has no .fk-custom rule")
	}
}
