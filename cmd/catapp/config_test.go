//go:build darwin

package main

import (
	"strings"
	"testing"
)

// The preset list is what the Connect menu and the connect page are both drawn
// from, so its ordering and identity rules are the feature rather than
// bookkeeping.

// A target's display name falls back through label → host → raw URL. An unnamed
// row in a menu is unclickable in practice, so there is no empty case.
func TestRemoteTargetName(t *testing.T) {
	for _, tc := range []struct {
		target remoteTarget
		want   string
	}{
		{remoteTarget{URL: "https://home.relay.herdr.dev", Label: "home"}, "home"},
		{remoteTarget{URL: "https://home.relay.herdr.dev"}, "home.relay.herdr.dev"},
		{remoteTarget{URL: "https://box.lan:8421"}, "box.lan:8421"},
		{remoteTarget{URL: "not a url", Label: "  "}, "not a url"},
	} {
		if got := tc.target.name(); got != tc.want {
			t.Errorf("%+v: name = %q, want %q", tc.target, got, tc.want)
		}
	}
}

// Reconnecting to a known catway updates it in place. A second entry for the
// same URL would give the menu two identical rows, and re-labelling one would
// be impossible.
func TestUpsertPresetReplacesInPlace(t *testing.T) {
	var cfg appConfig
	cfg.upsertPreset(remoteTarget{URL: "https://a", Label: "a"})
	cfg.upsertPreset(remoteTarget{URL: "https://b", Label: "b"})
	cfg.upsertPreset(remoteTarget{URL: "https://a", Label: "home"})

	if len(cfg.Presets) != 2 {
		t.Fatalf("presets = %+v, want two", cfg.Presets)
	}
	// Order is insertion order and survives the update: a menu whose items move
	// when you use them is one you cannot build muscle memory for.
	if cfg.Presets[0].URL != "https://a" || cfg.Presets[1].URL != "https://b" {
		t.Fatalf("presets reordered: %+v", cfg.Presets)
	}
	if cfg.Presets[0].Label != "home" {
		t.Fatalf("label = %q, want the updated home", cfg.Presets[0].Label)
	}

	// Whitespace is trimmed on the way in, so " https://a " is not a third
	// catway that happens to look like the first.
	cfg.upsertPreset(remoteTarget{URL: "  https://a  ", Label: " office "})
	if len(cfg.Presets) != 2 || cfg.Presets[0].Label != "office" {
		t.Fatalf("padded url made a new entry: %+v", cfg.Presets)
	}
	// And an empty URL is not a target at all.
	cfg.upsertPreset(remoteTarget{Label: "nothing"})
	if len(cfg.Presets) != 2 {
		t.Fatalf("an empty url was saved: %+v", cfg.Presets)
	}
}

// Forgetting a catway leaves the window where it is: the current connection is
// a live session, and tidying a list is not a reason to end it.
func TestRemovePresetKeepsTheCurrentConnection(t *testing.T) {
	cfg := appConfig{Remote: remoteTarget{URL: "https://a"}}
	cfg.upsertPreset(remoteTarget{URL: "https://a"})
	cfg.upsertPreset(remoteTarget{URL: "https://b"})

	cfg.removePreset("https://a")
	if len(cfg.Presets) != 1 || cfg.Presets[0].URL != "https://b" {
		t.Fatalf("presets = %+v, want only b", cfg.Presets)
	}
	if cfg.Remote.URL != "https://a" {
		t.Fatalf("current connection = %q, want it untouched", cfg.Remote.URL)
	}
	// Which is exactly the state currentPreset reports as "not a saved one".
	if got := cfg.currentPreset(); got != -1 {
		t.Fatalf("currentPreset = %d, want -1 for an unsaved current URL", got)
	}
}

func TestCurrentPresetFindsTheConnectedOne(t *testing.T) {
	cfg := appConfig{Remote: remoteTarget{URL: "https://b"}}
	cfg.upsertPreset(remoteTarget{URL: "https://a"})
	cfg.upsertPreset(remoteTarget{URL: "https://b"})
	if got := cfg.currentPreset(); got != 1 {
		t.Fatalf("currentPreset = %d, want 1", got)
	}
}

// The page is assembled by hand, so the two things that would be quietly wrong
// are checked: that a preset's own text is escaped before it lands in an
// attribute, and that the cancel button appears only when there is a session to
// go back to.
func TestConnectPageEscapesAndGatesCancel(t *testing.T) {
	presets := []remoteTarget{{URL: `https://x/"><script>`, Label: `a & b`}}
	page := connectPage(presets, "https://x", false)
	if strings.Contains(page, "<script>a") || strings.Contains(page, `"><script>`) {
		t.Fatal("a preset's text reached the page unescaped")
	}
	if !strings.Contains(page, "a &amp; b") {
		t.Fatal("the label is missing from the page")
	}
	if strings.Contains(page, "catsCancel") {
		t.Fatal("cancel offered with no session to return to")
	}
	if !strings.Contains(connectPage(presets, "https://x", true), "catsCancel") {
		t.Fatal("cancel missing when there is a session to return to")
	}
	// A first run has nothing saved and must not render an empty list.
	if strings.Contains(connectPage(nil, "", false), "<ul class=\"saved\">") {
		t.Fatal("an empty saved list was rendered")
	}
}

// The menu is drawn from these names, so they must line up with the presets
// one-for-one — the index is the whole message a menu item carries back.
func TestPresetNamesTracksTheList(t *testing.T) {
	names := presetNames([]remoteTarget{{URL: "https://a", Label: "home"}, {URL: "https://b.lan"}})
	if len(names) != 2 || names[0] != "home" || names[1] != "b.lan" {
		t.Fatalf("names = %v", names)
	}
	// Non-nil for an empty list: nil is how local mode says "no Connect menu at
	// all", and an empty thin-client list still gets the menu's other item.
	if presetNames(nil) == nil {
		t.Fatal("presetNames(nil) must be non-nil; nil means local mode to the menu")
	}
}
