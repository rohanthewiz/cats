package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/browserproto"
)

// goldenDir holds the exact bytes cats-mobile's packages/catsproto ships. The
// files are committed here rather than only in the app repo so the drift gate
// bites in the repo where the drift HAPPENS: a field added to PaneFrame is a
// cats commit, and this test makes that commit fail its own `make check` rather
// than quietly leaving the phone a version behind until somebody notices a
// missing value on a screen.
const goldenDir = "testdata/golden"

// keysFile is generated from a Flutter SDK data file, so it is regenerated only
// when the SDK is on hand. See emitKeys for why that is the right trade.
const keysFile = "keys.g.dart"

func TestGoldenIsUpToDate(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	// FLUTTER_ROOT, when the environment has one, extends the check to the key
	// table too. Without it the committed table stands, and
	// TestGeneratedKeyCodesParse (internal/inputenc, ghostty-tagged) is what
	// keeps it honest.
	files, err := generate(root, os.Getenv("FLUTTER_ROOT"))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	entries, err := os.ReadDir(goldenDir)
	if err != nil {
		t.Fatal(err)
	}
	onDisk := map[string]bool{}
	for _, e := range entries {
		onDisk[e.Name()] = true
	}

	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(goldenDir, name))
		if err != nil {
			t.Errorf("%s is missing from the golden: %v\n%s", name, err, regenHint)
			continue
		}
		delete(onDisk, name)
		if string(got) != want {
			t.Errorf("%s is stale.\n%s\n%s", name, firstDifference(string(got), want), regenHint)
		}
	}
	for name := range onDisk {
		if name == keysFile && os.Getenv("FLUTTER_ROOT") == "" {
			continue // not regenerated this run, by design
		}
		t.Errorf("%s is in the golden but the generator no longer emits it", name)
	}
}

// TestGeneratorIsDeterministic runs the whole pipeline twice. Map iteration is
// random in Go and this generator walks several maps, so a single missed sort
// would turn the golden diff into a coin flip that fails on some CI runs and
// not others — the most expensive kind of flake, because the natural response
// is to re-run rather than to look.
func TestGeneratorIsDeterministic(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	first, err := generate(root, "")
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		again, err := generate(root, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(again) != len(first) {
			t.Fatalf("file set differs between runs: %d then %d", len(first), len(again))
		}
		for name, want := range first {
			if again[name] != want {
				t.Fatalf("%s differs between two runs of the same generator:\n%s",
					name, firstDifference(want, again[name]))
			}
		}
	}
}

// TestEveryCommandReachesDart pins the property the whole commands file exists
// for: app.CommandSpecs is the one vocabulary, and every entry in it must
// arrive on the phone as a typed method. A command added to the Go table and
// forgotten here would be invisible rather than broken.
func TestEveryCommandReachesDart(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatal(err)
	}
	files, err := generate(root, "")
	if err != nil {
		t.Fatal(err)
	}
	src := files["commands.g.dart"]

	for _, spec := range app.CommandSpecs() {
		ident := commandIdent(spec.Name)
		if !strings.Contains(src, "  static const String "+ident+" = '"+spec.Name+"';") {
			t.Errorf("command %q has no CmdName.%s constant", spec.Name, ident)
		}
		if !strings.Contains(src, "invoke(CmdName."+ident+",") {
			t.Errorf("command %q has no CatsCommands method", spec.Name)
		}
	}
}

// TestReservedNamesAreEnforced proves the collision guard is live rather than
// decorative: without it, a Go type named Error would shadow dart:core's and
// the failure would surface as a confusing analyzer message in another repo.
func TestReservedNamesAreEnforced(t *testing.T) {
	reg := newRegistry(&source{types: map[string]*typeDoc{}})
	// Stand a different Go type on the name browserproto.Welcome wants, then let
	// the real one try to claim it.
	reg.classes["Welcome"] = &classDef{dart: "Welcome", key: "example.com/other.Welcome"}
	_, err := reg.ensure(reflect.TypeFor[browserproto.Welcome](), "")
	if err == nil {
		t.Fatal("a second Go type mapping to Welcome was accepted; the guard is not wired")
	}
	if !strings.Contains(err.Error(), "dartNameOverrides") {
		t.Errorf("the error does not say how to fix it: %v", err)
	}
}

const regenHint = "regenerate with:\n" +
	"    go run ./cmd/catgen-dart -out cmd/catgen-dart/testdata/golden\n" +
	"and copy the same output into cats-mobile/packages/catsproto/lib/src/generated."

// firstDifference reports the first differing line with a little context, so a
// 69 KB file's failure is readable instead of a wall of Dart.
func firstDifference(got, want string) string {
	gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := range max(len(gl), len(wl)) {
		g, w := lineAt(gl, i), lineAt(wl, i)
		if g == w {
			continue
		}
		return "first difference at line " + strconv.Itoa(i+1) + ":\n" +
			"  golden: " + g + "\n" +
			"  fresh:  " + w
	}
	return "files differ only in trailing content"
}

func lineAt(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return "<end of file>"
}
