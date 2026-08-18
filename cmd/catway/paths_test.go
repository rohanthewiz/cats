//go:build ghostty

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/orchestration"
	"github.com/rohanthewiz/cats/internal/pathpick"
)

// pathpick.Merge keeps cdx's ranking in front, fills in behind it with the
// session's own directories, and drops duplicates and anything that is not a
// directory — a remembered path can be deleted between the recording and the
// picker. It lives in pathpick because the machine that owns the disk is the one
// that has to do the stat'ing, and that is no longer always this one.
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

	got := pathpick.Merge(
		[]string{hot, shared, gone, file, ""},
		[]string{live, shared + "/", live, "relative/dir"},
	)
	want := []string{hot, shared, live}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pathpick.Merge = %v, want %v", got, want)
	}
}

// The picker shows pathpick.ListError's text under a field while the user is
// still typing, so the common misses read as sentences rather than as syscall
// wrappers.
func TestListErr(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "nope")
	if _, _, err := pathpick.Subdirs(missing); pathpick.ListError(missing, err) != "no such directory: "+missing {
		t.Errorf("missing dir: %q", pathpick.ListError(missing, err))
	}

	file := filepath.Join(root, "f")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pathpick.Subdirs(file); pathpick.ListError(file, err) != "not a directory: "+file {
		t.Errorf("file: %q", pathpick.ListError(file, err))
	}
}

// liveCwdsOn is the session half of the recents list: every workspace identity
// and live pane cwd on ONE host, deduplicated, with no empty entries for panes
// whose cwd has not been reported yet. Host-scoped because these paths are
// handed to whichever machine is answering, and stat'ed there.
func TestLiveCwds(t *testing.T) {
	o, err := newOrch("", "/base")
	if err != nil {
		t.Fatalf("newOrch: %v", err)
	}
	dir := t.TempDir()
	if _, err := o.session.CreateWorkspaceAt(dir); err != nil {
		t.Fatalf("CreateWorkspaceAt: %v", err)
	}

	got := o.liveCwdsOn(o.defaultHost)
	seen := map[string]int{}
	for _, d := range got {
		if d == "" {
			t.Fatalf("liveCwdsOn includes an empty entry: %v", got)
		}
		seen[d]++
	}
	if seen[dir] != 1 {
		t.Fatalf("liveCwdsOn = %v, want the new workspace's cwd %q exactly once", got, dir)
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

// A host whose cathost cannot list its own directories answers with the reason
// rather than with THIS machine's directories, which would be a drop-down of
// paths that do not exist where the pane is. A listing error, not a failed
// command: the picker shows those inline and keeps taking keystrokes.
func TestPathListRefusesAHostThatCannotList(t *testing.T) {
	o, localPane, remotePane, _, _ := twoHostOrch(t)

	r := &hostGuardResponder{}
	o.StartPathList(r, app.PathListParams{Pane: &remotePane, Dir: "~/src"})
	if !r.ok {
		t.Fatalf("path.list on an unlistable host should resolve with a reason, not fail: %+v", r)
	}
	res, isRes := r.data.(app.PathListResult)
	if !isRes || res.Exists || !strings.Contains(res.Error, "cannot list directories") {
		t.Fatalf("path.list result = %+v", r.data)
	}

	// The local pane goes down the ordinary (asynchronous) path — nothing is
	// resolved inline, which is how this test tells the guard did not fire.
	r = &hostGuardResponder{}
	o.StartPathList(r, app.PathListParams{Pane: &localPane, Dir: "/"})
	if r.ok || r.fail {
		t.Fatalf("a local path.list must not resolve synchronously: %+v", r)
	}
}

// A capable host is asked, and its answer comes back as the command's result.
// This is the whole point of the capability: the picker completes a path on the
// machine the pane is on rather than being switched off with an apology.
func TestPathListRoundTripsToARemoteHost(t *testing.T) {
	o, _, remotePane, _, pdRemote := twoHostOrch(t)
	go o.run() // the reply comes back through the loop, as it does in a live session
	d := o.hosts[testRemoteHost]
	d.setFeatures([]string{orchestration.FeatureListDir})

	r := &hostGuardResponder{}
	syncPost(o, func() {
		o.StartPathList(r, app.PathListParams{Pane: &remotePane, Dir: "~/src", Recents: true})
	})
	if r.ok || r.fail {
		t.Fatalf("a remote path.list must not resolve before the daemon answers: %+v", r)
	}

	payload := pdRemote.expect(t, orchestration.MsgRequestListDir)
	var req orchestration.RequestListDir
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if req.PaneID != remotePane {
		t.Errorf("request pane = %d, want the anchor pane %d", req.PaneID, remotePane)
	}
	// Unexpanded on purpose: "~" is the REMOTE user's home, and expanding it
	// here would send a path from this machine's account.
	if req.Dir != "~/src" {
		t.Errorf("request dir = %q, want the untouched %q", req.Dir, "~/src")
	}
	if !req.Recents {
		t.Error("the recents flag did not travel")
	}

	d.dispatch(orchestration.MsgDirListing, mustJSON(t, orchestration.NewDirListing(remotePane, pathpick.Listing{
		Dir: "/home/remote/src", Home: "/home/remote", Exists: true, Dirs: []string{"a", "b"},
	})))
	syncPost(o, func() {}) // the dispatch's closure is ahead of this one

	res, isRes := r.data.(app.PathListResult)
	if !r.ok || !isRes {
		t.Fatalf("no result after the daemon answered: %+v", r)
	}
	if !res.Exists || res.Dir != "/home/remote/src" || res.Home != "/home/remote" {
		t.Fatalf("result = %+v, want the remote machine's answer verbatim", res)
	}
	if len(res.Dirs) != 2 {
		t.Fatalf("dirs = %v, want the two the daemon listed", res.Dirs)
	}
}

// The host param overrides the anchor pane's host, because the new-workspace
// dialog picks a host before anything exists there: without it, a path typed for
// devbox would be completed against the local disk.
func TestPathListHostParamOverridesTheAnchor(t *testing.T) {
	o, localPane, _, _, pdRemote := twoHostOrch(t)
	o.hosts[testRemoteHost].setFeatures([]string{orchestration.FeatureListDir})

	r := &hostGuardResponder{}
	o.StartPathList(r, app.PathListParams{Pane: &localPane, Host: testRemoteHost, Dir: "src"})
	payload := pdRemote.expect(t, orchestration.MsgRequestListDir)
	var req orchestration.RequestListDir
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	// And the local pane's cwd is NOT sent as the anchor: a relative path
	// resolved against a directory from another filesystem is worse than no
	// anchor at all, which the answering machine resolves to its own home.
	if req.Base != "" {
		t.Fatalf("base = %q, want none — the anchor pane is on another machine", req.Base)
	}

	// An id nobody is attached to is refused with a reason rather than silently
	// answered by the local machine.
	r = &hostGuardResponder{}
	o.StartPathList(r, app.PathListParams{Host: "ghost", Dir: "/"})
	res, isRes := r.data.(app.PathListResult)
	if !r.ok || !isRes || !strings.Contains(res.Error, "unknown host") {
		t.Fatalf("unknown host result = %+v", r.data)
	}
}
