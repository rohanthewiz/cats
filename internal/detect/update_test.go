package detect

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// remoteManifest builds a minimal valid remote TOML manifest for codex.
func remoteManifest(version, contains string) string {
	return fmt.Sprintf(`
id = "codex"
version = %q
min_engine_version = 1
updated_at = "2026-06-10T12:00:00Z"

[[rules]]
id = "idle"
state = "idle"
contains = [%q]
`, version, contains)
}

func TestManifestVersionCompare(t *testing.T) {
	cmp := func(a, b string) int {
		t.Helper()
		av, err := parseManifestVersion(a)
		if err != nil {
			t.Fatalf("parse %q: %v", a, err)
		}
		bv, err := parseManifestVersion(b)
		if err != nil {
			t.Fatalf("parse %q: %v", b, err)
		}
		return compareManifestVersions(av, bv)
	}
	if cmp("2026.6.10.1", "2026.6.9.9") != 1 {
		t.Fatal("2026.6.10.1 should be newer")
	}
	if cmp("1.2.0", "1.2") != 0 {
		t.Fatal("trailing zeros are insignificant")
	}
	if cmp("1.2.1", "1.2") != 1 {
		t.Fatal("1.2.1 should be newer than 1.2")
	}
}

func TestManifestVersionRejectsBadSegments(t *testing.T) {
	for _, v := range []string{"", "2026.06.alpha", "2026..06", "2026.999999999999999999999999999999"} {
		if _, err := parseManifestVersion(v); err == nil {
			t.Errorf("version %q should be rejected", v)
		}
	}
}

func TestProcessAgentManifestCommitsNewer(t *testing.T) {
	dir := t.TempDir()
	content := remoteManifest("2026.06.10.3", "ready")
	commit, err := processAgentManifest(dir, "codex", content)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if commit == nil || commit.Agent != "codex" || commit.Version != "2026.06.10.3" {
		t.Fatalf("commit: %+v", commit)
	}
	got, err := os.ReadFile(remoteManifestPath(dir, "codex"))
	if err != nil || string(got) != content {
		t.Fatalf("committed content mismatch: %v", err)
	}
}

func TestProcessAgentManifestRejectsDowngradeAndTamper(t *testing.T) {
	dir := t.TempDir()
	current := remoteManifest("2026.06.10.4", "current")
	if _, err := processAgentManifest(dir, "codex", current); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Downgrade: refused, cache untouched.
	if _, err := processAgentManifest(dir, "codex", remoteManifest("2026.06.10.3", "older")); err == nil {
		t.Fatal("downgrade should be rejected")
	}
	// Same version, different content: refused.
	if _, err := processAgentManifest(dir, "codex", remoteManifest("2026.06.10.4", "changed")); err == nil {
		t.Fatal("same-version content change should be rejected")
	}
	// Same version, same content: silent no-op.
	commit, err := processAgentManifest(dir, "codex", current)
	if err != nil || commit != nil {
		t.Fatalf("same content: commit=%v err=%v", commit, err)
	}
	got, _ := os.ReadFile(remoteManifestPath(dir, "codex"))
	if string(got) != current {
		t.Fatal("cached manifest was clobbered")
	}
}

func TestParseRemoteManifestValidation(t *testing.T) {
	cases := map[string]string{
		"wrong id":       strings.Replace(remoteManifest("1.0", "x"), `id = "codex"`, `id = "claude"`, 1),
		"no version":     strings.Replace(remoteManifest("1.0", "x"), `version = "1.0"`, ``, 1),
		"engine too new": strings.Replace(remoteManifest("1.0", "x"), "min_engine_version = 1", "min_engine_version = 99", 1),
		"unknown field":  remoteManifest("1.0", "x") + "\nmystery_field = true\n",
		"no rules":       "id = \"codex\"\nversion = \"1.0\"\nmin_engine_version = 1\n",
	}
	for name, content := range cases {
		if _, err := parseRemoteManifest("codex", []byte(content)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestParseCatalogValidation(t *testing.T) {
	good := `
schema_version = 1

[[agents]]
id = "codex"
path = "codex.toml"

[[agents]]
id = "not-a-real-agent"
path = "fake.toml"
`
	entries, err := parseCatalog(good)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// The unknown agent is skipped, not fatal.
	if len(entries) != 1 || entries[0].id != "codex" || entries[0].path != "codex.toml" {
		t.Fatalf("entries: %+v", entries)
	}

	bad := map[string]string{
		"schema":      "schema_version = 9\n",
		"unsafe path": "schema_version = 1\n[[agents]]\nid = \"codex\"\npath = \"../evil.toml\"\n",
		"dup agent":   "schema_version = 1\n[[agents]]\nid = \"codex\"\npath = \"a.toml\"\n[[agents]]\nid = \"codex\"\npath = \"b.toml\"\n",
		"empty path":  "schema_version = 1\n[[agents]]\nid = \"codex\"\npath = \"\"\n",
	}
	for name, content := range bad {
		if _, err := parseCatalog(content); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// End-to-end over HTTP: catalog + manifest served, committed, status written,
// and the overlaid manifest actually drives Detect after a reload.
func TestCheckAndUpdateEndToEnd(t *testing.T) {
	dir := t.TempDir()
	manifest := remoteManifest("2026.06.10.3", "update-e2e-ready")
	mux := http.NewServeMux()
	mux.HandleFunc("/agent-detection/index.toml", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "schema_version = 1\n\n[[agents]]\nid = \"codex\"\npath = \"codex.toml\"\n")
	})
	mux.HandleFunc("/agent-detection/codex.toml", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, manifest)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, err := CheckAndUpdate(dir, srv.URL+"/agent-detection/index.toml")
	if err != nil {
		t.Fatalf("CheckAndUpdate: %v", err)
	}
	if len(out.Updated) != 1 || out.Updated[0].Agent != "codex" || out.Updated[0].Version != "2026.06.10.3" {
		t.Fatalf("updated: %+v", out.Updated)
	}
	ag := out.Status.Agents["codex"]
	if ag.LastResult != "updated" || ag.CachedVersion != "2026.06.10.3" {
		t.Fatalf("status: %+v", ag)
	}
	if _, err := os.Stat(statusPath(dir)); err != nil {
		t.Fatalf("status.json missing: %v", err)
	}

	// The committed overlay wins over the bundled manifest once loaded.
	t.Cleanup(func() { SetRemoteManifestDir("") })
	SetRemoteManifestDir(dir)
	d := Detect("codex", Input{Screen: "update-e2e-ready"})
	if d.State != StateIdle {
		t.Fatalf("remote-manifest rule did not apply: %+v", d)
	}

	// A second run is a no-op ("current"), not a re-update.
	out2, err := CheckAndUpdate(dir, srv.URL+"/agent-detection/index.toml")
	if err != nil {
		t.Fatalf("second CheckAndUpdate: %v", err)
	}
	if len(out2.Updated) != 0 || out2.Status.Agents["codex"].LastResult != "current" {
		t.Fatalf("second run: %+v", out2)
	}
}

// A broken remote overlay must fall back to the bundled manifest, not drop the
// agent.
func TestLoadManifestsBrokenRemoteFallsBack(t *testing.T) {
	dir := t.TempDir()
	if err := atomicWriteFile(remoteManifestPath(dir, "codex"), []byte("{not toml")); err != nil {
		t.Fatal(err)
	}
	m := loadManifests(dir)
	if m["codex"] == nil {
		t.Fatal("bundled codex manifest must survive a broken remote overlay")
	}
}
