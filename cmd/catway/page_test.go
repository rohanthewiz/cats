package main

import (
	"encoding/json"
	"strings"
	"testing"

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
