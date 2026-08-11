package detect

import (
	"slices"
	"testing"
)

func TestManifestsCompile(t *testing.T) {
	m := ensureManifests()
	for _, label := range []string{"claude", "codex", "pi", "copilot", "agy", "gemini"} {
		if m[label] == nil {
			t.Errorf("manifest for %q did not load", label)
		}
	}
}

func TestDetectClaude(t *testing.T) {
	cases := []struct {
		name      string
		in        Input
		wantState State
		wantIdle  bool
		wantBlock bool
		wantWork  bool
	}{
		{
			name:      "osc_title_working_braille",
			in:        Input{OscTitle: "⠁ building the thing"},
			wantState: StateWorking,
			wantWork:  true,
		},
		{
			// Claude Code's title spinner moved off the braille dots to the
			// half-filled circles (◐◓◑◒). Matching only braille made every
			// working pane fall through to the idle fallback.
			name:      "osc_title_working_half_circle",
			in:        Input{OscTitle: "◑ Investigate lost agent status"},
			wantState: StateWorking,
			wantWork:  true,
		},
		{
			name:      "osc_title_working_quadrant_circle",
			in:        Input{OscTitle: "◵ building the thing"},
			wantState: StateWorking,
			wantWork:  true,
		},
		{
			name:      "osc_title_idle",
			in:        Input{OscTitle: "✳ ready"},
			wantState: StateIdle,
			wantIdle:  true,
		},
		{
			name:      "live_prompt_box_idle",
			in:        Input{Screen: "──────────\n❯ ask me something\n──────────"},
			wantState: StateIdle,
			wantIdle:  true,
		},
		{
			name: "bash_permission_blocked",
			in: Input{Screen: "Do you want to proceed?\n" +
				"bash command\n" +
				"1. Yes\n" +
				"2. No"},
			wantState: StateBlocked,
			wantBlock: true,
		},
		{
			name:      "no_match_falls_back_idle",
			in:        Input{Screen: "just some plain output\nnothing special here"},
			wantState: StateIdle,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect("claude", tc.in)
			if got.State != tc.wantState {
				t.Errorf("state = %q, want %q", got.State, tc.wantState)
			}
			if got.VisibleIdle != tc.wantIdle || got.VisibleBlocker != tc.wantBlock || got.VisibleWorking != tc.wantWork {
				t.Errorf("visible flags = (idle=%v block=%v work=%v), want (idle=%v block=%v work=%v)",
					got.VisibleIdle, got.VisibleBlocker, got.VisibleWorking, tc.wantIdle, tc.wantBlock, tc.wantWork)
			}
		})
	}
}

func TestDetectPiWorking(t *testing.T) {
	got := Detect("pi", Input{Screen: "Working..."})
	if got.State != StateWorking || !got.VisibleWorking {
		t.Errorf("pi working = %+v, want working+visible", got)
	}
}

func TestDetectUnknownAndFallback(t *testing.T) {
	if got := Detect("", Input{Screen: "anything"}); got.State != StateUnknown {
		t.Errorf("empty label = %q, want unknown", got.State)
	}
	// Known agent, empty screen, no rule → idle fallback.
	if got := Detect("codex", Input{}); got.State != StateIdle {
		t.Errorf("codex fallback = %q, want idle", got.State)
	}
}

// The overlay is a hotfix channel, not a permanent override: a cached remote
// manifest older than the bundled one must not shadow it. The failure this
// guards against is silent — a stale overlay's rules simply stop matching, and
// every pane reads as the idle fallback.
func TestLoadManifestsPrefersNewerOfBundledAndRemote(t *testing.T) {
	// A rule the bundled codex manifest does not have, so "did the overlay win?"
	// is answerable by detection alone.
	const marker = "overlay marker line"
	write := func(t *testing.T, dir, version string) {
		t.Helper()
		path := remoteManifestPath(dir, "codex")
		if err := atomicWriteFile(path, []byte(remoteManifest(version, marker))); err != nil {
			t.Fatalf("write overlay: %v", err)
		}
	}

	t.Run("older overlay is ignored", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "2026.06.10.2") // bundled codex is 2026.06.10.3
		m := loadManifests(dir)
		if m["codex"] == nil {
			t.Fatal("codex manifest missing entirely")
		}
		if matchedMarker(m, marker) {
			t.Fatal("older overlay replaced the bundled manifest")
		}
	})

	t.Run("newer overlay wins", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "2026.06.11.1")
		m := loadManifests(dir)
		if !matchedMarker(m, marker) {
			t.Fatal("newer overlay did not replace the bundled manifest")
		}
	})
}

// matchedMarker reports whether codex's loaded manifest carries the overlay's
// marker rule — i.e. whether the overlay is the one in force.
func matchedMarker(m map[string]*compiledManifest, marker string) bool {
	cm := m["codex"]
	if cm == nil {
		return false
	}
	for i := range cm.rules {
		if slices.Contains(cm.rules[i].gate.contains, marker) {
			return true
		}
	}
	return false
}
