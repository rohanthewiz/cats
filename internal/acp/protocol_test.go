package acp

import (
	"encoding/json"
	"testing"
)

// The permission fixtures mirror the shapes GitHub Copilot CLI 1.0.77 sends,
// captured from a live probe — if these tests fail after editing the outcome
// builders, it is the builders that are wrong.

func TestParsePermission(t *testing.T) {
	raw := json.RawMessage(`{
		"sessionId":"s-1",
		"toolCall":{"toolCallId":"tc-9","title":"Run: go test ./...","kind":"execute","status":"pending"},
		"options":[
			{"optionId":"allow","name":"Allow","kind":"allow_once"},
			{"optionId":"allow-session","name":"Always allow","kind":"allow_always"},
			{"optionId":"deny","name":"Deny","kind":"reject_once"}
		]}`)
	sid, title, kind, opts := ParsePermission(raw)
	if sid != "s-1" || title != "Run: go test ./..." || kind != "execute" {
		t.Fatalf("header fields: %q %q %q", sid, title, kind)
	}
	if len(opts) != 3 || opts[0].OptionID != "allow" || opts[2].Kind != "reject_once" {
		t.Fatalf("options: %+v", opts)
	}
}

func TestParsePermissionGarbage(t *testing.T) {
	sid, title, kind, opts := ParsePermission(json.RawMessage(`not json`))
	if sid != "" || title != "" || kind != "" || opts != nil {
		t.Fatalf("garbage should yield zero values, got %q %q %q %+v", sid, title, kind, opts)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestOutcomeShapes(t *testing.T) {
	// The doubly-nested outcome object is what the schema defines; a single
	// level is silently ignored by copilot and the turn hangs.
	if got := mustJSON(t, SelectedOutcome("allow")); got != `{"outcome":{"outcome":"selected","optionId":"allow"}}` {
		t.Fatalf("selected: %s", got)
	}
	if got := mustJSON(t, CancelledOutcome()); got != `{"outcome":{"outcome":"cancelled"}}` {
		t.Fatalf("cancelled: %s", got)
	}
}

func TestRejectOutcomePreference(t *testing.T) {
	opts := []PermOption{
		{OptionID: "always-no", Kind: "reject_always"},
		{OptionID: "once-no", Kind: "reject_once"},
		{OptionID: "yes", Kind: "allow_once"},
	}
	if got := mustJSON(t, RejectOutcome(opts)); got != `{"outcome":{"outcome":"selected","optionId":"once-no"}}` {
		t.Fatalf("should prefer reject_once: %s", got)
	}
	if got := mustJSON(t, RejectOutcome([]PermOption{{OptionID: "a", Kind: "allow_once"}})); got != `{"outcome":{"outcome":"cancelled"}}` {
		t.Fatalf("no reject option should fall back to cancelled: %s", got)
	}
}

func TestContentText(t *testing.T) {
	if got := ContentText(json.RawMessage(`{"type":"text","text":"hello"}`)); got != "hello" {
		t.Fatalf("got %q", got)
	}
	if got := ContentText(json.RawMessage(`{"type":"image","data":"..."}`)); got != "" {
		t.Fatalf("non-text should be empty, got %q", got)
	}
	if got := ContentText(json.RawMessage(`bogus`)); got != "" {
		t.Fatalf("garbage should be empty, got %q", got)
	}
}

func TestNewSessionParamsMarshalsEmptyServers(t *testing.T) {
	// mcpServers is required by the schema: [] is valid, null is not.
	got := mustJSON(t, NewSessionParams{Cwd: "/w", MCPServers: []any{}})
	if got != `{"cwd":"/w","mcpServers":[]}` {
		t.Fatalf("got %s", got)
	}
}
