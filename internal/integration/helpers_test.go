package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// testHome points HOME at a fresh temp dir and blanks every per-target env
// override so a developer's real environment cannot leak into a test.
// t.Setenv also serializes these tests, standing in for Rust's env lock.
func testHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, envVar := range []string{
		piCodingAgentDirEnvVar, claudeConfigDirEnvVar, codexHomeEnvVar,
		kimiCodeHomeEnvVar, copilotHomeEnvVar, qodercliConfigDirEnvVar,
		cursorConfigDirEnvVar,
	} {
		t.Setenv(envVar, "")
	}
	return home
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path, content string) {
	t.Helper()
	mustMkdirAll(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(mustReadFile(t, path)), &parsed); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return parsed
}

// jsonAt walks string keys and int indexes; nil when any step is missing.
func jsonAt(v any, path ...any) any {
	for _, step := range path {
		switch key := step.(type) {
		case string:
			m, ok := v.(map[string]any)
			if !ok {
				return nil
			}
			v, ok = m[key]
			if !ok {
				return nil
			}
		case int:
			arr, ok := v.([]any)
			if !ok || key < 0 || key >= len(arr) {
				return nil
			}
			v = arr[key]
		}
	}
	return v
}

func jsonStringAt(t *testing.T, v any, path ...any) string {
	t.Helper()
	s, ok := jsonAt(v, path...).(string)
	if !ok {
		t.Fatalf("expected string at %v, got %T (%v)", path, jsonAt(v, path...), jsonAt(v, path...))
	}
	return s
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("mode of %s = %o, want %o", path, info.Mode().Perm(), want)
	}
}

// fakeBinary drops an executable shell script named `name` into a fresh dir
// and puts only that dir on PATH.
func fakeBinary(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	t.Setenv("PATH", dir)
}
