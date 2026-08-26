package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/cmd/catway/web"
	"github.com/rohanthewiz/cats/internal/buildinfo"
	"github.com/rohanthewiz/cats/internal/config"
)

const baseHead = "<!DOCTYPE html><html><head><title>x</title></head><body>hi</body></html>"

// renderPage injects the theme style and keybindings script, both before
// </head>, and preserves the rest of the document.
func TestRenderPageInjects(t *testing.T) {
	out := string(renderPage([]byte(baseHead), config.Default()))

	for _, want := range []string{
		`<style id="cats-config-theme">`,
		`--bg:#1f2420;`,
		`window.__catsKeys=`,
		`"copyMode"`,
		"<body>hi</body>", // original body intact
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
	// Both injections land before </head>.
	head := strings.Index(out, "</head>")
	if head < 0 {
		t.Fatal("no </head> in output")
	}
	if strings.Index(out, "cats-config-theme") > head || strings.Index(out, "__catsKeys") > head {
		t.Fatal("injections must precede </head>")
	}
}

// A rebound keybinding from config reaches the injected script.
func TestRenderPageReboundKey(t *testing.T) {
	cfg := config.Default()
	cfg.Keybindings.CopyMode["yank"] = []string{"c"}
	out := string(renderPage([]byte(baseHead), cfg))
	if !strings.Contains(out, `"yank":["c"]`) {
		t.Errorf("rebound yank not in injected keys:\n%s", out)
	}
}

// CSS values that try to break out of <style> into markup are stripped.
func TestRenderPageSanitizesTheme(t *testing.T) {
	cfg := config.Default()
	cfg.Theme.Colors["bg"] = "#000</style><script>alert(1)</script>"
	cfg.Theme.Colors["ev;il"] = "red}body{display:none"
	out := string(renderPage([]byte(baseHead), cfg))

	if strings.Contains(out, "<script>alert(1)") {
		t.Error("theme value broke out of <style>")
	}
	if strings.Contains(out, "}body{display:none") {
		t.Error("theme value injected an extra CSS rule")
	}
}

// A keybinding key value containing </script> can't close the injected script —
// json.Marshal escapes it.
func TestRenderPageEscapesKeys(t *testing.T) {
	cfg := config.Default()
	cfg.Keybindings.CopyMode["yank"] = []string{"</script><script>alert(1)</script>"}
	out := string(renderPage([]byte(baseHead), cfg))
	if strings.Contains(out, "</script><script>alert(1)") {
		t.Error("keybinding value broke out of <script>")
	}
}

// The build badge rides along when the binary carries a git identity, and is
// omitted entirely when it doesn't (test binaries are stamped by the toolchain's
// VCS setting, so which branch runs depends on the build — assert both shapes).
func TestRenderPageBuildScript(t *testing.T) {
	out := string(renderPage([]byte(baseHead), config.Default()))
	block := buildScript()

	if block == "" {
		if strings.Contains(out, "__catsBuild") {
			t.Error("unknown build must not inject a badge")
		}
		return
	}
	if !strings.Contains(out, `<script id="cats-build">window.__catsBuild={`) {
		t.Errorf("build script not injected:\n%s", block)
	}
	if !strings.Contains(block, `"hash":"`) || strings.Contains(block, `"hash":""`) {
		t.Errorf("build script has no hash: %s", block)
	}
	if head := strings.Index(out, "</head>"); strings.Index(out, "__catsBuild") > head {
		t.Error("build script must precede </head>")
	}
}

// The home directory rides along so the front end can draw paths prompt-style.
// A host with no resolvable home injects nothing and the page draws full paths,
// so both shapes are legal — what must hold is that it lands before </head> and
// that its value is a JSON string rather than a bare path pasted into JS.
func TestRenderPageHomeScript(t *testing.T) {
	out := string(renderPage([]byte(baseHead), config.Default()))
	block := homeScript()

	if block == "" {
		if strings.Contains(out, "__catsHome") {
			t.Error("no home must not inject a value")
		}
		return
	}
	if !strings.Contains(out, `<script id="cats-home">window.__catsHome="`) {
		t.Errorf("home script not injected:\n%s", block)
	}
	if head := strings.Index(out, "</head>"); strings.Index(out, "__catsHome") > head {
		t.Error("home script must precede </head>")
	}
}

// A commit subject holding markup can't close the injected <script> —
// json.Marshal's HTML escaping keeps it inert.
func TestBuildScriptEscapesSubject(t *testing.T) {
	if got := jsonBuildBlock(t, "</script><script>alert(1)</script>"); strings.Contains(got, "</script><script>alert(1)") {
		t.Errorf("subject broke out of <script>: %s", got)
	}
}

// jsonBuildBlock renders a build block for an arbitrary subject, mirroring what
// buildScript emits (buildinfo's own values are fixed at link time).
func jsonBuildBlock(t *testing.T, subject string) string {
	t.Helper()
	data, err := json.Marshal(buildinfo.Info{Hash: "abc1234", Subject: subject})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return "<script id=\"cats-build\">window.__catsBuild=" + string(data) + ";</script>\n"
}

// With no </head>, the injection still lands (prepended) so settings take effect.
func TestRenderPageNoHead(t *testing.T) {
	out := string(renderPage([]byte("<body>only</body>"), config.Default()))
	if !strings.Contains(out, "cats-config-theme") || !strings.Contains(out, "<body>only</body>") {
		t.Fatalf("no-head render dropped content:\n%s", out)
	}
}

// The page is what turns a URL into a window: "?ws=w2" has to reach the server
// as Init.Workspace, or a second window silently mirrors the first. Asserted on
// the source because there is no headless browser here — and the failure it
// guards is a rename of one field on one line.
func TestPageForwardsWorkspaceQueryInInit(t *testing.T) {
	page := string(web.Page())
	for _, want := range []string{
		`.get("ws")`,                // it reads the query parameter
		`workspace: urlWorkspace()`, // …and sends it on the init message
		`function openWindow(`,      // …and can open another window on a workspace
		`case "clients":`,           // …and reads the per-view census
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing %q — multi-window depends on it", want)
		}
	}
	// The init send must carry it: a helper defined but never wired is exactly
	// the shape of this bug.
	init := strings.Index(page, `t: "init"`)
	if init < 0 {
		t.Fatal("no init send in the page")
	}
	if ws := strings.Index(page[init:], "workspace: urlWorkspace()"); ws < 0 || ws > 400 {
		t.Fatal("the init message does not carry the window's workspace")
	}
}

// A wide grapheme is drawn from the cell it is anchored in but spills into the
// next one, which the VT grid leaves as a blank spacer carrying the same
// background. Painted cell by cell, that spacer's background rect landed after
// the glyph and erased its right half — a 🍏 in a highlighted list row rendered
// as a green sliver, and only in a highlighted row, since a spacer at the
// default background paints nothing at all.
//
// The fix is to paint every background first and every glyph second, so the
// renderer never has to know which cells are spacers. Asserted on the source
// because there is no headless canvas here, and the failure it guards is
// someone folding the two loops back into one for the tidiness of it.
func TestPagePaintsBackgroundsBeforeGlyphs(t *testing.T) {
	draw := drawFuncSource(t, string(web.Page()))

	const gridLoop = "for (let y = 0; y < p.H; y++)"
	first := strings.Index(draw, gridLoop)
	if first < 0 {
		t.Fatal("draw() no longer walks the grid")
	}
	second := strings.Index(draw[first+len(gridLoop):], gridLoop)
	if second < 0 {
		t.Fatal("draw() walks the grid once — backgrounds and glyphs share a pass, so a wide glyph's spacer will clip it")
	}
	second += first + len(gridLoop)

	// The background rect belongs to the first pass and the glyph to the second.
	bg := strings.Index(draw, "ctx.fillRect(x * cellW, y * cellH, cellW + 0.5, cellH + 0.5)")
	if bg < 0 {
		t.Fatal("no cell-background rect in draw()")
	}
	if bg > second {
		t.Error("the cell background is painted in the glyph pass — a wide glyph's spacer will clip it")
	}
	if glyph := strings.Index(draw, "ctx.fillText("); glyph < second {
		t.Error("a glyph is drawn in the background pass — later backgrounds will paint over it")
	}
}

// drawFuncSource returns the body of the page's draw() function, brace-matched,
// so the assertions above cannot be satisfied by an unrelated part of the page.
func drawFuncSource(t *testing.T, page string) string {
	t.Helper()
	start := strings.Index(page, "function draw(p) {")
	if start < 0 {
		t.Fatal("draw() not found in the page")
	}
	depth := 0
	for i := start + strings.Index(page[start:], "{"); i < len(page); i++ {
		switch page[i] {
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				return page[start : i+1]
			}
		}
	}
	t.Fatal("draw() is unbalanced")
	return ""
}
