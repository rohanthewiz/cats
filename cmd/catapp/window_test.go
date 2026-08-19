//go:build darwin

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// The Mac app's half of multi-window is mostly Objective-C, which has no Go
// test. What can be tested is the part that decides things: the URL a window
// opens on, and the restore list's round trip through app.json.

func TestWindowURLCarriesTheWorkspace(t *testing.T) {
	m := &winManager{base: "http://127.0.0.1:8421"}

	// No workspace ⇒ the bare UI. The server reads an omitted Init.Workspace as
	// "the primary view", which is what ⌘N means: another window on what I am
	// already doing.
	if got, want := m.windowURL(""), "http://127.0.0.1:8421/"; got != want {
		t.Errorf("windowURL(\"\") = %q, want %q", got, want)
	}
	if got, want := m.windowURL("w2"), "http://127.0.0.1:8421/?ws=w2"; got != want {
		t.Errorf("windowURL(w2) = %q, want %q", got, want)
	}
	// A trailing slash on the base must not produce "//".
	m2 := &winManager{base: "https://box.example/"}
	if got, want := m2.windowURL("w7"), "https://box.example/?ws=w7"; got != want {
		t.Errorf("windowURL with a trailing slash = %q, want %q", got, want)
	}
	// Workspace ids are server-generated ("w2"), but the escape is what keeps a
	// hand-edited app.json from building a URL that means something else.
	if got, want := m.windowURL("a b&c"), "http://127.0.0.1:8421/?ws=a+b%26c"; got != want {
		t.Errorf("windowURL escaping = %q, want %q", got, want)
	}
}

func TestRestorePlan(t *testing.T) {
	m := &winManager{base: "http://127.0.0.1:8421"}

	// Nothing saved (a first run) is one window, not none.
	if got := m.restorePlan(nil); len(got) != 1 || got[0].URL != "http://127.0.0.1:8421/" {
		t.Fatalf("empty restore = %+v, want one window on the primary view", got)
	}

	saved := []savedWindow{
		{Workspace: "w1", X: 10, Y: 20, W: 1200, H: 800},
		{Workspace: "w2", X: 1210, Y: 20, W: 900, H: 700},
		{Workspace: "w-gone", W: 640, H: 480},
	}
	plan := m.restorePlan(saved)
	if len(plan) != 3 {
		t.Fatalf("restore plan has %d windows, want 3", len(plan))
	}
	if plan[1].URL != "http://127.0.0.1:8421/?ws=w2" || plan[1].Frame.X != 1210 {
		t.Errorf("second window = %+v, want w2 at its own frame", plan[1])
	}
	// A workspace that no longer exists still opens: the server falls back to
	// the primary view rather than erroring, and a user's window layout should
	// survive them cleaning up projects.
	if plan[2].URL != "http://127.0.0.1:8421/?ws=w-gone" {
		t.Errorf("a vanished workspace was dropped or rewritten: %+v", plan[2])
	}
}

// The restore list rides app.json beside the mode and the presets. A window
// layout is client state, exactly as the preset list is — catway persists
// nothing about windows at all.
func TestAppConfigWindowsRoundTrip(t *testing.T) {
	in := appConfig{
		Mode:    "local",
		Windows: []savedWindow{{Workspace: "w1", X: 1, Y: 2, W: 3, H: 4}, {W: 100, H: 200}},
	}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out appConfig
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out.Windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(out.Windows))
	}
	if out.Windows[0] != in.Windows[0] || out.Windows[1] != in.Windows[1] {
		t.Fatalf("windows round-tripped as %+v, want %+v", out.Windows, in.Windows)
	}
	// A window with no workspace (opened on the primary view) must not carry an
	// empty "workspace" key that a hand-edit could mistake for a real id.
	if got := string(data); !strings.Contains(got, `{"x":0,"y":0,"w":100,"h":200}`) {
		t.Errorf("a window on the primary view should serialise without a workspace: %s", got)
	}
}

// A config from before windows existed still loads — the field is omitempty on
// the way out and absent on the way in.
func TestAppConfigWithoutWindows(t *testing.T) {
	var out appConfig
	if err := json.Unmarshal([]byte(`{"mode":"remote","remote":{"url":"https://x"}}`), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Windows != nil {
		t.Fatalf("windows = %+v, want nil for a pre-windows config", out.Windows)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "windows") {
		t.Errorf("an empty window list should not be written: %s", data)
	}
}
