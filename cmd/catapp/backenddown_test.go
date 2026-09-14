//go:build darwin

package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// overlayMarkup pulls the card markup back out of the script, decoding the JSON
// string literal the way the page's JS engine would.
func overlayMarkup(t *testing.T, js string) string {
	t.Helper()
	m := regexp.MustCompile(`var html=(".*?");var el=`).FindStringSubmatch(js)
	if m == nil {
		t.Fatalf("no html literal in %q", js)
	}
	var s string
	if err := json.Unmarshal([]byte(m[1]), &s); err != nil {
		t.Fatalf("html literal is not a JSON string: %v", err)
	}
	return s
}

func TestBackendOverlayEscapesTheDetail(t *testing.T) {
	js := backendOverlayJS("stopped", `exit </pre><script>alert(1)</script>`, true)
	if strings.Contains(js, "<script>") || strings.Contains(js, "</pre><script>") {
		t.Fatalf("raw markup from the detail reached the script: %q", js)
	}
	markup := overlayMarkup(t, js)
	if !strings.Contains(markup, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("detail not HTML-escaped in the card: %q", markup)
	}
}

func TestBackendOverlayOffersRetryOnlyWhenItCanHelp(t *testing.T) {
	if m := overlayMarkup(t, backendOverlayJS("t", "d", true)); !strings.Contains(m, "<button") {
		t.Errorf("canRetry: no button in %q", m)
	}
	if m := overlayMarkup(t, backendOverlayJS("t", "d", false)); strings.Contains(m, "<button") {
		t.Errorf("!canRetry: a button in %q", m)
	}
	if m := overlayMarkup(t, backendOverlayJS("Restarting", "", false)); strings.Contains(m, "<pre") {
		t.Errorf("empty detail still rendered a block: %q", m)
	}
}

func TestBackendClearTargetsTheOverlay(t *testing.T) {
	if !strings.Contains(backendClearJS(), `"`+backendOverlayID+`"`) {
		t.Fatalf("clear script does not name the overlay: %q", backendClearJS())
	}
}
