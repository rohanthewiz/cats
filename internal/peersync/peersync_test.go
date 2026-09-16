package peersync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTranslatePath(t *testing.T) {
	cases := []struct{ p, from, to, want string }{
		{"/Users/me/projs/x", "/Users/me", "/home/me", "/home/me/projs/x"},
		{"/Users/me", "/Users/me", "/home/me", "/home/me"},
		{"/Users/me2/projs/x", "/Users/me", "/home/me", "/Users/me2/projs/x"}, // prefix, not a path component
		{"/opt/shared", "/Users/me", "/home/me", "/opt/shared"},
		{"/Users/me/projs/x", "", "/home/me", "/Users/me/projs/x"},
		{"", "/Users/me", "/home/me", ""},
	}
	for _, c := range cases {
		if got := TranslatePath(c.p, c.from, c.to); got != c.want {
			t.Errorf("TranslatePath(%q, %q, %q) = %q, want %q", c.p, c.from, c.to, got, c.want)
		}
	}
}

func TestWantRoundTrip(t *testing.T) {
	w := Want{Workspaces: true, Plugins: true}
	if got := ParseWant(w.String()); got != w {
		t.Fatalf("ParseWant(%q) = %+v, want %+v", w.String(), got, w)
	}
	if ParseWant("bogus, todos").String() != "todos" {
		t.Fatalf("unknown words should be ignored")
	}
	if (Want{}).Any() {
		t.Fatalf("empty want must not be Any")
	}
}

func row(id, title string, kv ...any) map[string]any {
	r := map[string]any{"id": id, "title": title, "prompt": "p-" + title, "done": false, "created": "2026-01-01T00:00:00Z"}
	for i := 0; i+1 < len(kv); i += 2 {
		r[kv[i].(string)] = kv[i+1]
	}
	return r
}

func TestMergeTodosRules(t *testing.T) {
	dir := t.TempDir()
	local := []map[string]any{
		row("a", "same"),
		row("b", "open here", "priority", "high"),
		row("c", "conflict here"),
		row("d", "text twin"),
	}
	incoming := []map[string]any{
		row("a", "same"), // identical → unchanged
		row("b", "open here", "done", true, "schedule", map[string]any{"at": "x"}), // finished there → completed here, schedule dropped
		row("c", "conflict THERE"),                                 // same id, both open, differs → conflict, local kept
		row("zz", "text twin"),                                     // same text, other id → dupe
		row("e", "new one", "schedule", map[string]any{"at": "x"}), // new → added without schedule
	}
	out, res := mergeTodos(local, incoming, nil, dir)
	if res.Unchanged != 1 || res.Completed != 1 || res.Conflicts != 1 || res.Dupes != 1 || res.Added != 1 {
		t.Fatalf("result %+v", res)
	}
	if len(out) != 5 {
		t.Fatalf("got %d rows, want 5", len(out))
	}
	if out[1]["done"] != true || out[1]["schedule"] != nil {
		t.Fatalf("completion did not propagate cleanly: %v", out[1])
	}
	if out[1]["priority"] != nil {
		// The finished record replaces the local one wholesale; priority was
		// only set locally, so it is gone — the documented "more finished wins".
		t.Logf("note: local-only field dropped on completion, as designed: %v", out[1])
	}
	if out[2]["title"] != "conflict here" {
		t.Fatalf("conflict must keep the local row, got %v", out[2])
	}
	if out[4]["id"] != "e" || out[4]["schedule"] != nil {
		t.Fatalf("added row wrong: %v", out[4])
	}
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "conflict here") {
		t.Fatalf("conflict note missing: %v", res.Notes)
	}
	// Idempotent: merging the same bundle again changes nothing.
	out2, res2 := mergeTodos(out, incoming, nil, dir)
	if res2.changed() || len(out2) != len(out) {
		t.Fatalf("second merge changed things: %+v", res2)
	}
}

func TestMergeTodosAttachments(t *testing.T) {
	dir := t.TempDir()
	incoming := []map[string]any{
		row("i", "with image", "images", []any{"images/i/shot.png", "images/i/missing.png", "../../etc/passwd"}),
	}
	files := map[string][]byte{"images/i/shot.png": []byte("png")}
	out, res := mergeTodos(nil, incoming, files, dir)
	if res.Added != 1 || res.NoFiles != 1 {
		t.Fatalf("result %+v", res)
	}
	imgs := out[0]["images"].([]any)
	if len(imgs) != 1 || imgs[0] != "images/i/shot.png" {
		t.Fatalf("kept images = %v", imgs)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "images", "i", "shot.png")); err != nil || string(b) != "png" {
		t.Fatalf("attachment not written: %v %q", err, b)
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "..", "etc", "passwd")); err == nil {
		t.Fatalf("traversal path must never be written")
	}
}

func TestSafeAttachmentPath(t *testing.T) {
	for _, bad := range []string{"", "/abs", "images/../x", "other/x.png", "images/a/../../x", "images\\a\\x"} {
		if _, ok := safeAttachmentPath(bad); ok {
			t.Errorf("%q accepted", bad)
		}
	}
	if got, ok := safeAttachmentPath("images/ab/x.png"); !ok || got != "images/ab/x.png" {
		t.Errorf("good path refused: %q %v", got, ok)
	}
}

// fakeLocal is a Local over a temp home with a few folders.
type fakeLocal struct {
	home    string
	wss     []WorkspaceEntry
	created []WorkspaceEntry
}

func (f *fakeLocal) Instance() Instance { return Instance{Hostname: "here", Home: f.home, OS: "test"} }
func (f *fakeLocal) Workspaces() ([]WorkspaceEntry, error) {
	return append([]WorkspaceEntry(nil), f.wss...), nil
}
func (f *fakeLocal) CreateWorkspaces(es []WorkspaceEntry) []error {
	f.created = append(f.created, es...)
	f.wss = append(f.wss, es...)
	return make([]error, len(es))
}

func TestApplyWorkspacesAndTodos(t *testing.T) {
	home := t.TempDir()
	t.Setenv(todoConfigDirEnv, filepath.Join(home, "global"))
	os.MkdirAll(filepath.Join(home, "projs", "a"), 0o755)
	os.MkdirAll(filepath.Join(home, "projs", "b"), 0o755)
	local := &fakeLocal{home: home, wss: []WorkspaceEntry{{Cwd: filepath.Join(home, "projs", "a")}}}

	theirHome := "/Users/them"
	b := Bundle{
		Schema:   Schema,
		Instance: Instance{Hostname: "there", Home: theirHome},
		Want:     Want{Workspaces: true, Todos: true},
		Workspaces: []WorkspaceEntry{
			{Cwd: theirHome + "/projs/a"},              // exists, already a workspace
			{Cwd: theirHome + "/projs/b", Name: "bee"}, // exists, new
			{Cwd: theirHome + "/projs/c"},              // no such folder
			{Cwd: "/opt/elsewhere"},                    // outside home, absent
		},
		Global: &Backlog{Todos: []map[string]any{row("g1", "global one")}},
		Backlogs: []Backlog{
			{Cwd: theirHome + "/projs/b", Todos: []map[string]any{row("b1", "b one")}},
			{Cwd: theirHome + "/projs/c", Todos: []map[string]any{row("c1", "c one")}},
		},
	}
	rep := Apply(local, b, Want{Workspaces: true, Todos: true})
	if rep.Error != "" {
		t.Fatalf("error: %s", rep.Error)
	}
	if len(local.created) != 1 || local.created[0].Cwd != filepath.Join(home, "projs", "b") || local.created[0].Name != "bee" {
		t.Fatalf("created %+v", local.created)
	}
	statuses := map[string]Status{}
	for _, it := range rep.Items {
		statuses[it.Kind+":"+it.Name] = it.Status
	}
	want := map[string]Status{
		"workspace:a (" + filepath.Join(home, "projs", "a") + ")":   StatusUnchanged,
		"workspace:bee (" + filepath.Join(home, "projs", "b") + ")": StatusSynced,
		"workspace:c (" + filepath.Join(home, "projs", "c") + ")":   StatusSkipped,
		"workspace:elsewhere (/opt/elsewhere)":                      StatusSkipped,
		"todos:global":                                              StatusSynced,
		"todos:b (" + filepath.Join(home, "projs", "b") + ")":       StatusSynced,
		"todos:c (" + filepath.Join(home, "projs", "c") + ")":       StatusSkipped,
	}
	for k, v := range want {
		if statuses[k] != v {
			t.Errorf("%s: got %q, want %q (items: %+v)", k, statuses[k], v, rep.Items)
		}
	}
	rows, err := readTodos(filepath.Join(home, "projs", "b", todoDirName, todoFileName))
	if err != nil || len(rows) != 1 || rows[0]["id"] != "b1" {
		t.Fatalf("project backlog not created/merged: %v %v", rows, err)
	}
	g, _ := readTodos(filepath.Join(home, "global", todoFileName))
	if len(g) != 1 {
		t.Fatalf("global backlog: %v", g)
	}

	// Collect on this side now offers the merged backlogs and both workspaces.
	mine, err := Collect(local, Want{Workspaces: true, Todos: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(mine.Workspaces) != 2 || mine.Global == nil || len(mine.Backlogs) != 1 {
		t.Fatalf("collect: %d ws, global=%v, %d backlogs", len(mine.Workspaces), mine.Global != nil, len(mine.Backlogs))
	}
	// Applying our own bundle back is a no-op.
	again := Apply(local, mine, Want{Workspaces: true, Todos: true})
	s, _, _, f := again.Counts()
	if s != 0 || f != 0 {
		t.Fatalf("self-apply changed things: %+v", again.Items)
	}
}

func TestApplyRespectsWantAndSchema(t *testing.T) {
	local := &fakeLocal{home: t.TempDir()}
	b := Bundle{Schema: Schema, Want: Want{Workspaces: true}, Workspaces: []WorkspaceEntry{{Cwd: local.home}}}
	rep := Apply(local, b, Want{Todos: true}) // asked for todos only: the workspace must not be touched
	if len(rep.Items) != 0 || len(local.created) != 0 {
		t.Fatalf("applied outside want: %+v", rep.Items)
	}
	b.Schema = 99
	if rep := Apply(local, b, Want{Workspaces: true}); rep.Error == "" {
		t.Fatalf("schema mismatch must be an error")
	}
}

func TestRender(t *testing.T) {
	r := SyncReport{Peer: "home", Direction: DirectionBoth, Want: Want{Todos: true}, Remote: Instance{Hostname: "mini", User: "me"},
		Pulled: &ApplyReport{Items: []Item{
			{Kind: KindTodos, Name: "global", Status: StatusSynced, Detail: "2 added"},
			{Kind: KindWorkspace, Name: "x", Status: StatusSkipped, Detail: "no such folder on this machine"},
			{Kind: KindPlugin, Name: "p", Status: StatusUnchanged},
		}},
		Pushed: &ApplyReport{Error: "boom"},
	}
	lines := r.Render()
	joined := strings.Join(lines, "\n")
	for _, want := range []string{"sync with home (me@mini)", "here: 1 synced, 1 unchanged, 1 skipped, 0 failed", "skipped   workspace x — no such folder", "on home: 0 synced", "error: boom"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "unchanged plugin") {
		t.Errorf("unchanged items should not be listed:\n%s", joined)
	}
}

// TestClientRoundTrip runs Sync against a fake peer served by httptest, to
// pin the wire shape and the bearer/refusal handling.
func TestClientRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv(todoConfigDirEnv, filepath.Join(home, "global"))
	local := &fakeLocal{home: home}
	theirs := Bundle{Schema: Schema, Instance: Instance{Hostname: "peer", Home: "/x"}, Want: Want{Todos: true},
		Global: &Backlog{Todos: []map[string]any{row("p1", "from peer")}}}
	var gotApply Bundle
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer s3cret" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case PathBundle:
			if r.URL.Query().Get("want") != "todos" {
				t.Errorf("want query = %q", r.URL.Query().Get("want"))
			}
			json.NewEncoder(w).Encode(theirs)
		case PathApply:
			json.NewDecoder(r.Body).Decode(&gotApply)
			json.NewEncoder(w).Encode(ApplyReport{Instance: theirs.Instance, Items: []Item{{Kind: KindTodos, Name: "global", Status: StatusSynced}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "s3cret", "")
	if err != nil {
		t.Fatal(err)
	}
	rep := Sync(context.Background(), local, c, "peer", Want{Todos: true}, DirectionBoth)
	if rep.Error != "" {
		t.Fatalf("sync error: %s", rep.Error)
	}
	if rep.Pulled == nil || rep.Pushed == nil || rep.Remote.Hostname != "peer" {
		t.Fatalf("report %+v", rep)
	}
	if len(gotApply.Global.Todos) != 1 || gotApply.Global.Todos[0]["id"] != "p1" {
		t.Fatalf("pushed bundle should carry the just-merged global row: %+v", gotApply.Global)
	}

	// The auth guard's answer to a wrong credential on a non-/ws path is a
	// redirect to the login page; the client must see the 302, not follow it.
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/login", http.StatusFound)
	}))
	defer redir.Close()
	rc, _ := NewClient(redir.URL, "x", "")
	if rep := Sync(context.Background(), local, rc, "peer", Want{Todos: true}, DirectionPull); !strings.Contains(rep.Error, "refused the credential") {
		t.Fatalf("redirect error = %q", rep.Error)
	}

	bad, _ := NewClient(srv.URL, "wrong", "")
	if rep := Sync(context.Background(), local, bad, "peer", Want{Todos: true}, DirectionPull); !strings.Contains(rep.Error, "refused the credential") {
		t.Fatalf("bad token error = %q", rep.Error)
	}
	if rep := Sync(context.Background(), local, c, "peer", Want{}, DirectionPull); rep.Error == "" {
		t.Fatalf("empty want must be refused")
	}
}
