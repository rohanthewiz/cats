//go:build ghostty

package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/pathpick"
)

// mergeDirs keeps cdx's ranking in front, fills in behind it with the session's
// own directories, and drops duplicates and anything that is not a directory —
// a remembered path can be deleted between the recording and the picker.
func TestMergeDirs(t *testing.T) {
	root := t.TempDir()
	mk := func(name string) string {
		p := filepath.Join(root, name)
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	hot, shared, live := mk("hot"), mk("shared"), mk("live")
	gone := filepath.Join(root, "gone")
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	got := mergeDirs(
		[]string{hot, shared, gone, file, ""},
		[]string{live, shared + "/", live, "relative/dir"},
	)
	want := []string{hot, shared, live}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeDirs = %v, want %v", got, want)
	}
}

// The picker shows listErr's text under a field while the user is still typing,
// so the common misses read as sentences rather than as syscall wrappers.
func TestListErr(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "nope")
	if _, _, err := pathpick.Subdirs(missing); listErr(missing, err) != "no such directory: "+missing {
		t.Errorf("missing dir: %q", listErr(missing, err))
	}

	file := filepath.Join(root, "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pathpick.Subdirs(file); listErr(file, err) != "not a directory: "+file {
		t.Errorf("file: %q", listErr(file, err))
	}
}

// liveCwds is the session half of the recents list: every workspace identity and
// live pane cwd, deduplicated, with no empty entries for panes whose cwd has not
// been reported yet.
func TestLiveCwds(t *testing.T) {
	o, err := newOrch("", "/base")
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	dir := t.TempDir()
	if _, err := o.session.CreateWorkspaceAt(dir); err != nil {
		t.Fatalf("CreateWorkspaceAt: %v", err)
	}

	got := o.liveCwds()
	seen := map[string]int{}
	for _, d := range got {
		if d == "" {
			t.Fatalf("liveCwds includes an empty entry: %v", got)
		}
		seen[d]++
	}
	if seen[dir] != 1 {
		t.Fatalf("liveCwds = %v, want the new workspace's cwd %q exactly once", got, dir)
	}
}

// The anchor a relative start path resolves against is the focused pane's
// directory — the same inheritance new tabs and splits use.
func TestAnchorPaneCwd(t *testing.T) {
	o, err := newOrch("", "/base")
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	dir := t.TempDir()
	if _, err := o.session.CreateWorkspaceAt(dir); err != nil {
		t.Fatalf("CreateWorkspaceAt: %v", err)
	}
	if got := o.anchorPaneCwd(nil); canonPath(got) != canonPath(dir) {
		t.Fatalf("anchorPaneCwd = %q, want %q", got, dir)
	}
	// An unknown pane id falls back rather than answering "" — a picker with no
	// anchor could not resolve a relative path at all.
	var unknown uint32 = 9999
	if got := o.anchorPaneCwd(&unknown); !strings.HasPrefix(got, "/") {
		t.Fatalf("anchorPaneCwd(unknown) = %q, want an absolute fallback", got)
	}
}

// path.list lists this machine's disk. Anchored on a remote pane it would offer
// a drop-down of directories that do not exist where the pane is, so it answers
// with the reason instead — a listing error, not a failed command, because the
// picker shows those inline and keeps taking keystrokes.
func TestPathListRefusesRemotePanes(t *testing.T) {
	o, localPane, remotePane, _, _ := twoHostOrch(t)

	r := &hostGuardResponder{}
	o.StartPathList(r, app.PathListParams{Pane: &remotePane, Dir: "~/src"})
	if !r.ok {
		t.Fatalf("remote path.list should resolve with a reason, not fail: %+v", r)
	}
	res, isRes := r.data.(app.PathListResult)
	if !isRes || res.Exists || !strings.Contains(res.Error, "local-host only") {
		t.Fatalf("remote path.list result = %+v", r.data)
	}

	// The local pane goes down the ordinary (asynchronous) path — nothing is
	// resolved inline, which is how this test tells the guard did not fire.
	r = &hostGuardResponder{}
	o.StartPathList(r, app.PathListParams{Pane: &localPane, Dir: "/"})
	if r.ok || r.fail {
		t.Fatalf("a local path.list must not resolve synchronously: %+v", r)
	}
}
