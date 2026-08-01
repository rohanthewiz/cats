package theme

import "maps"

// The built-in palettes. cats-green is fully hand-authored so it reproduces the
// pre-themes rendering byte-for-byte (its values are the ones index.html and
// config.defaultColors historically carried); the rest author the keys where
// taste matters — surfaces, accent, state colors — and let Normalize derive the
// long tail (washes, terminal defaults, strong/soft text) so each palette stays
// readable at a glance.
//
// Order here is presentation order in the settings modal's picker.

// builtins returns fresh copies (maps and all) so callers can mutate freely.
func builtins() []Theme {
	src := []Theme{
		{
			Name: DefaultName, Label: "Cats Green", Dark: true,
			Colors: map[string]string{
				// The original GoNotes-style dark green: warm gray-green
				// surfaces, green accent.
				"bg": "#1f2420", "fg": "#d6ddd6", "accent": "#4db380", "accent-dim": "#3d4a43",
				"panel": "#242a25", "panel2": "#2b322c", "line": "#38403a", "muted": "#9db0a2",
				"chrome": "#2b322c", "chrome-focus": "#3a4a3f", "heading": "#6cbf8d",
				// Workspace group headers sit a tier below the section titles:
				// a greenish brown that separates them from both the green
				// headings above and the muted pane rows below.
				"ws-heading": "#a09a62",
				"todo":       "#f0dfa0", "ok": "#6ac47a", "warn": "#e0b64e", "err": "#e57373",
				"done": "#00ccf5",
				// The formerly-hardcoded stylesheet colors, preserved exactly.
				"fg-strong": "#e8e8e8", "fg-soft": "#dddddd", "fg-bright": "#ffffff",
				"chrome-fg": "#e6efe7", "chrome-fg-dim": "#cfe0d3", "accent-fg": "#0e1a13",
				"err-bg": "#2a1c1c", "err-fg": "#ffd7d7",
				"hover":    "rgba(255,255,255,.16)",
				"sel-fill": "rgba(88,204,140,0.30)", "cm-cursor": "rgba(122,220,170,0.95)",
				"scroll-thumb": "rgba(122,220,170,0.6)", "scroll-thumb-idle": "rgba(255,255,255,0.25)",
				"term-fg": "#d6ddd6", "term-bg": "#1f2420",
			},
		},
		{
			Name: "darcula", Label: "Darcula", Dark: true,
			Colors: map[string]string{
				// JetBrains' classic: neutral charcoal, orange keywords-accent,
				// the string-green / number-blue as state colors.
				"bg": "#2b2b2b", "fg": "#a9b7c6", "accent": "#cc7832", "accent-dim": "#5a4a38",
				"panel": "#313335", "panel2": "#3c3f41", "line": "#45494b", "muted": "#8c8c8c",
				"chrome": "#3c3f41", "chrome-focus": "#4e5254", "heading": "#ffc66b",
				"ws-heading": "#a89a68", // the editor's olive annotation color, dimmed
				"todo":       "#bbb529", "ok": "#629755", "warn": "#ffc66b", "err": "#ff6b68",
				"done": "#6897bb", "accent-fg": "#2b1d0e",
			},
		},
		{
			Name: "tokyo-night", Label: "Tokyo Night", Dark: true,
			Colors: map[string]string{
				"bg": "#1a1b26", "fg": "#c0caf5", "accent": "#7aa2f7", "accent-dim": "#3b4261",
				"panel": "#16161e", "panel2": "#1f2335", "line": "#292e42", "muted": "#565f89",
				"chrome": "#1f2335", "chrome-focus": "#33467c", "heading": "#bb9af7",
				"ws-heading": "#a89372", // the palette's one warm tone, taken down to a sign-lit tan
				"todo":       "#ff9e64", "ok": "#9ece6a", "warn": "#e0af68", "err": "#f7768e",
				"done": "#7dcfff", "accent-fg": "#15161e", "fg-soft": "#a9b1d6",
			},
		},
		{
			Name: "solarized-dark", Label: "Solarized Dark", Dark: true,
			Colors: map[string]string{
				"bg": "#002b36", "fg": "#93a1a1", "accent": "#268bd2", "accent-dim": "#1a4a5e",
				"panel": "#073642", "panel2": "#0a4652", "line": "#12454f", "muted": "#586e75",
				"chrome": "#0a4652", "chrome-focus": "#0f5666", "heading": "#2aa198",
				"ws-heading": "#b09a55", // solarized yellow, softened toward the base tones
				"todo":       "#cb4b16", "ok": "#859900", "warn": "#b58900", "err": "#dc322f",
				"done": "#6c71c4", "accent-fg": "#fdf6e3", "fg-strong": "#eee8d5",
			},
		},
		{
			Name: "solarized-light", Label: "Solarized Light", Dark: false,
			Colors: map[string]string{
				"bg": "#fdf6e3", "fg": "#657b83", "accent": "#268bd2", "accent-dim": "#9cc3dd",
				"panel": "#eee8d5", "panel2": "#e4ddc8", "line": "#d3cbb7", "muted": "#93a1a1",
				"chrome": "#e4ddc8", "chrome-focus": "#d9d2bc", "heading": "#cb4b16",
				// Light theme: the earth tone has to go dark, not dim, to stay a
				// heading on paper — solarized yellow driven down to olive brown.
				"ws-heading": "#6f5f2a",
				"todo":       "#b58900", "ok": "#859900", "warn": "#b58900", "err": "#dc322f",
				"done": "#6c71c4", "accent-fg": "#fdf6e3",
				"fg-strong": "#073642", "fg-bright": "#002b36",
				"err-bg": "#f6d7cd", "err-fg": "#a4321f",
			},
		},
		{
			Name: "super-warm", Label: "Super Warm", Dark: true,
			Colors: map[string]string{
				// Cozy ember room: brown-black surfaces, amber accent, muted
				// teal as the one cool pop (done) so it stays legible as "new".
				"bg": "#201a14", "fg": "#e8dcc8", "accent": "#e0a458", "accent-dim": "#4a3d2c",
				"panel": "#262019", "panel2": "#2e2620", "line": "#3e342a", "muted": "#a89a80",
				"chrome": "#2e2620", "chrome-focus": "#4a3d30", "heading": "#d4915e",
				"ws-heading": "#a8845c", // the ember heading, one step back from the fire
				"todo":       "#f0d090", "ok": "#a8c080", "warn": "#e8b04e", "err": "#e57360",
				"done": "#7ec8c0", "accent-fg": "#201505",
			},
		},
		{
			Name: "cool-blue", Label: "Cool Blue", Dark: true,
			Colors: map[string]string{
				// Nordic: slate blue-grays, frost cyan accent, aurora states.
				"bg": "#262b36", "fg": "#d8dee9", "accent": "#88c0d0", "accent-dim": "#3f4d59",
				"panel": "#2e3440", "panel2": "#3b4252", "line": "#434c5e", "muted": "#7b88a1",
				"chrome": "#3b4252", "chrome-focus": "#4c566a", "heading": "#81a1c1",
				"ws-heading": "#ab9683", // driftwood: the aurora orange pulled most of the way to gray
				"todo":       "#d08770", "ok": "#a3be8c", "warn": "#ebcb8b", "err": "#bf616a",
				"done": "#b48ead", "accent-fg": "#16242a",
			},
		},
		{
			Name: "dark-game", Label: "Dark Game", Dark: true,
			Colors: map[string]string{
				// Arcade CRT: near-black violet surfaces, phosphor-green accent,
				// magenta headings, neon state colors.
				"bg": "#0e0e14", "fg": "#d0d0e0", "accent": "#39ff14", "accent-dim": "#1d4a1d",
				"panel": "#14141c", "panel2": "#1b1b26", "line": "#2c2c3c", "muted": "#7a7a95",
				"chrome": "#1b1b26", "chrome-focus": "#2a2a44", "heading": "#ff2d95",
				"ws-heading": "#b39a4e", // the other CRT phosphor: amber, at rest
				"todo":       "#ffe14d", "ok": "#5fe86a", "warn": "#ffb020", "err": "#ff3860",
				"done": "#00e5ff", "accent-fg": "#041008",
			},
		},
		{
			Name: "dark-city", Label: "Dark City", Dark: true,
			Colors: map[string]string{
				// Night skyline: deep indigo, neon purple accent, signage pinks
				// and teals.
				"bg": "#12101c", "fg": "#d8d4ee", "accent": "#a277ff", "accent-dim": "#3d3260",
				"panel": "#171526", "panel2": "#1e1b30", "line": "#302b48", "muted": "#8580a8",
				"chrome": "#1e1b30", "chrome-focus": "#35305a", "heading": "#f694ff",
				"ws-heading": "#a8936f", // sodium streetlight under the neon
				"todo":       "#ffe073", "ok": "#5df0a6", "warn": "#ffb454", "err": "#ff6767",
				"done": "#82e2ff", "accent-fg": "#140a24",
			},
		},
		{
			Name: "corporate", Label: "Corporate", Dark: false,
			Colors: map[string]string{
				// Clean office light: cool gray surfaces, restrained blue
				// accent, ledger-toned states.
				"bg": "#f7f8fa", "fg": "#333a45", "accent": "#2f6fd0", "accent-dim": "#a8c4ea",
				"panel": "#eef0f4", "panel2": "#e4e7ed", "line": "#d0d5de", "muted": "#6b7684",
				"chrome": "#e4e7ed", "chrome-focus": "#d3dae6", "heading": "#3b5bab",
				// Light theme: dark olive brown, the ledger amber taken down far
				// enough to hold a heading against the near-white panel.
				"ws-heading": "#6f6a4e",
				"todo":       "#9a6700", "ok": "#1e8e5a", "warn": "#b07800", "err": "#cc3d3d",
				"done": "#0e8a9e", "accent-fg": "#ffffff",
				"fg-strong": "#14181f", "fg-bright": "#000000",
				"err-bg": "#f5dada", "err-fg": "#a02c2c",
			},
		},
	}
	out := make([]Theme, len(src))
	for i, t := range src {
		t.Source = SourceBuiltin
		t.Colors = cloneColors(t.Colors)
		out[i] = t
	}
	return out
}

func cloneColors(m map[string]string) map[string]string { return maps.Clone(m) }

// BuiltIns returns the built-in themes in presentation order, fresh copies.
func BuiltIns() []Theme { return builtins() }
