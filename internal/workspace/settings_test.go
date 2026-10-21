package workspace

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNormalizeDevBranch(t *testing.T) {
	ok := map[string]string{
		"":                   "", // automatic
		"   ":                "",
		"develop":            "develop",
		"  develop\t":        "develop",
		"refs/heads/develop": "develop", // pasted from for-each-ref
		"release/2.0":        "release/2.0",
		"feature/x-y_z":      "feature/x-y_z",
		"main":               "main", // explicit main is allowed: it drops the master fallback
		"v1.2":               "v1.2",
		"user@host":          "user@host", // '@' alone is fine; only "@" and "@{" are not
		"ünïcode":            "ünïcode",
		"a.b/c.d":            "a.b/c.d",
		"refs/heads/refs/x":  "refs/x", // one prefix stripped, not all
		"trailing-dash-":     "trailing-dash-",
	}
	for in, want := range ok {
		got, err := NormalizeDevBranch(in)
		if err != nil || got != want {
			t.Errorf("NormalizeDevBranch(%q) = %q, %v; want %q", in, got, err, want)
		}
	}

	bad := []string{
		"-x", "--upload-pack=evil", // option-shaped
		"has space", "tab\there", "nul\x00", "del\x7f",
		"a~b", "a^b", "a:b", "a?b", "a*b", "a[b", `a\b`,
		"@", "a..b", "a@{1}", "a//b", "/a", "a/", "a.",
		".hidden", "a/.hidden", "x.lock", "a/x.lock/b",
		strings.Repeat("x", maxBranchLen+1),
	}
	for _, in := range bad {
		if got, err := NormalizeDevBranch(in); err == nil {
			t.Errorf("NormalizeDevBranch(%q) = %q, want an error", in, got)
		}
	}
}

// Settings survive the snapshot, and an untouched workspace writes no
// "settings" key at all — so every session file from before settings existed
// restores, and re-saves, byte-identically.
func TestSnapshotSettings(t *testing.T) {
	if b, err := json.Marshal(Snapshot{ID: "w1"}); err != nil || strings.Contains(string(b), "settings") {
		t.Fatalf("default settings serialised as %s (%v), want no settings key", b, err)
	}

	snap := Snapshot{ID: "w1", Settings: Settings{DevBranch: "develop"}}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"settings":{"dev_branch":"develop"}`) {
		t.Fatalf("snapshot JSON = %s, want the dev branch under settings", b)
	}
	var back Snapshot
	if err := json.Unmarshal(b, &back); err != nil || back.Settings.DevBranch != "develop" {
		t.Fatalf("round trip: %+v (%v)", back.Settings, err)
	}
}
