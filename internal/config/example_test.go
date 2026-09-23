package config

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateExample = flag.Bool("update-example", false, "rewrite config.example.json from Default()")

// config.example.json at the repo root is Default() as a saved file: every
// section with its default values, which is the one thing JSON can show
// without comments. This test keeps it from drifting; regenerate with
//
//	go test ./internal/config -run TestExampleJSON -update-example
func TestExampleJSON(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.json")
	if *updateExample {
		os.Remove(path)
		if err := Save(path, Default()); err != nil {
			t.Fatal(err)
		}
		os.Chmod(path, 0o644)
		return
	}
	want := filepath.Join(t.TempDir(), "config.json")
	if err := Save(want, Default()); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(want)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(a) != string(b) {
		t.Fatalf("config.example.json is stale; regenerate with -update-example")
	}
}
