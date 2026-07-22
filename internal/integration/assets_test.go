package integration

import (
	"strings"
	"testing"
)

// TestBundledAssetsReportSessionRefs is the golden-content check ported from
// bundled_integration_assets_report_session_refs: every asset speaks the
// current session-report protocol, and the session-only hooks must not carry
// lifecycle calls.
func TestBundledAssetsReportSessionRefs(t *testing.T) {
	type check struct {
		name    string
		asset   string
		want    []string
		wantNot []string
	}
	checks := []check{
		{
			name:  "pi",
			asset: piExtensionAsset,
			want: []string{
				"agent_session_path: currentAgentSessionPath",
				"agent_session_id: currentAgentSessionId",
				"publishState(true)",
			},
		},
		{
			name:    "claude",
			asset:   claudeHookAsset,
			want:    []string{"agent_session_id", "pane.report_agent_session"},
			wantNot: []string{`"state": action`, "pane.release_agent"},
		},
		{
			name:    "codex",
			asset:   codexHookAsset,
			want:    []string{"CATS_HOOK_INPUT_FILE", "agent_session_id", "pane.report_agent_session"},
			wantNot: []string{`"state": action`, "pane.release_agent"},
		},
		{
			name:  "kimi",
			asset: kimiHookAsset,
			want: []string{
				`source = "cats:kimi"`, "agent_session_id",
				"pane.report_agent_session", `"state": action`, "pane.release_agent",
			},
		},
		{
			name:    "copilot",
			asset:   copilotHookAsset,
			want:    []string{"agent_session_id", "pane.report_agent_session"},
			wantNot: []string{`"state":`, "pane.release_agent"},
		},
		{
			name:    "droid",
			asset:   droidHookAsset,
			want:    []string{"agent_session_id", "pane.report_agent_session"},
			wantNot: []string{`"state": action`, "pane.release_agent"},
		},
		{
			name:  "opencode",
			asset: opencodePluginAsset,
			want: []string{
				"properties?.sessionID", "params.agent_session_id = sessionID",
				"pane.report_agent_session", "reportState", "pane.release_agent",
			},
		},
		{
			name:  "kilo",
			asset: kiloPluginAsset,
			want: []string{
				`SOURCE = "cats:kilo"`, `AGENT = "kilo"`,
				"pane.report_agent_session", "reportState", "pane.release_agent",
			},
		},
		{
			name:  "hermes",
			asset: hermesPluginInitAsset,
			want: []string{
				"session_id = _session_id(kwargs)", "agent_session_id",
				`pane.report_agent",`, "pane.release_agent",
			},
		},
		{
			name:    "qodercli",
			asset:   qodercliHookAsset,
			want:    []string{"CATS_HOOK_INPUT_FILE", "agent_session_id", "pane.report_agent_session"},
			wantNot: []string{`"state": action`, "pane.release_agent", "QODER_HOOK_EVENT"},
		},
		{
			name:  "cursor",
			asset: cursorHookAsset,
			want: []string{
				"CATS_INTEGRATION_ID=cursor", "conversation_id", "conversationId",
				"sessionId", "agent_session_id", "pane.report_agent_session",
				"hook_event_name", "sessionStart",
			},
			wantNot: []string{`"state":`, "pane.release_agent"},
		},
	}

	for _, c := range checks {
		for _, want := range c.want {
			if !strings.Contains(c.asset, want) {
				t.Errorf("%s asset missing %q", c.name, want)
			}
		}
		for _, wantNot := range c.wantNot {
			if strings.Contains(c.asset, wantNot) {
				t.Errorf("%s asset unexpectedly contains %q", c.name, wantNot)
			}
		}
	}
}

// TestAssetMarkersMatchExpectedVersions pins each embedded asset's
// CATS_INTEGRATION_VERSION marker to the package's expected-version
// constant: bumping one without the other would break status detection.
func TestAssetMarkersMatchExpectedVersions(t *testing.T) {
	cases := []struct {
		name     string
		asset    string
		expected int
	}{
		{"pi", piExtensionAsset, piIntegrationVersion},
		{"omp", ompExtensionAsset, ompIntegrationVersion},
		{"claude", claudeHookAsset, claudeIntegrationVersion},
		{"codex", codexHookAsset, codexIntegrationVersion},
		{"kimi", kimiHookAsset, kimiIntegrationVersion},
		{"copilot", copilotHookAsset, copilotIntegrationVersion},
		{"droid", droidHookAsset, droidIntegrationVersion},
		{"opencode", opencodePluginAsset, opencodeIntegrationVersion},
		{"kilo", kiloPluginAsset, kiloIntegrationVersion},
		{"hermes", hermesPluginInitAsset, hermesIntegrationVersion},
		{"qodercli", qodercliHookAsset, qodercliIntegrationVersion},
		{"cursor", cursorHookAsset, cursorIntegrationVersion},
	}
	for _, tc := range cases {
		version, ok := parseIntegrationVersion(tc.asset)
		if !ok {
			t.Errorf("%s asset has no parseable CATS_INTEGRATION_VERSION marker", tc.name)
			continue
		}
		if version != tc.expected {
			t.Errorf("%s asset marker v%d != expected-version constant %d", tc.name, version, tc.expected)
		}
	}
}
