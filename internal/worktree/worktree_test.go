package worktree

import (
	"reflect"
	"testing"
)

// Generated branch slugs are worktree-namespaced and stable per seed (the Rust
// vectors, so both implementations name branches identically).
func TestGeneratedBranchSlug(t *testing.T) {
	for _, tc := range []struct {
		seed int64
		want string
	}{
		{0, "worktree/brave-river-0000"},
		{9, "worktree/calm-cloud-0009"},
	} {
		if got := GeneratedBranchSlug(tc.seed); got != tc.want {
			t.Errorf("GeneratedBranchSlug(%d) = %q, want %q", tc.seed, got, tc.want)
		}
	}
}

// Branch names become filesystem-safe folder names: lowercased, non-alnum runs
// collapsed to "-", edges trimmed, fallback when nothing survives.
func TestBranchToPathSlug(t *testing.T) {
	for _, tc := range []struct{ branch, want string }{
		{"worktree/brave-river", "worktree-brave-river"},
		{"issue/137 Worktree Spaces", "issue-137-worktree-spaces"},
		{"///", "worktree"},
		{"", "worktree"},
	} {
		if got := BranchToPathSlug(tc.branch); got != tc.want {
			t.Errorf("BranchToPathSlug(%q) = %q, want %q", tc.branch, got, tc.want)
		}
	}
}

func TestDefaultCheckoutPath(t *testing.T) {
	got := DefaultCheckoutPath("/home/me/.herdr/worktrees", "herdr", "worktree/brave-river")
	if want := "/home/me/.herdr/worktrees/herdr/worktree-brave-river"; got != want {
		t.Fatalf("DefaultCheckoutPath = %q, want %q", got, want)
	}
}

// The porcelain parser handles branch refs, detached/bare/prunable markers, and
// blank-line separation (the Rust test fixture).
func TestParseWorktreeListPorcelain(t *testing.T) {
	out := "worktree /repo/main\nHEAD abc\nbranch refs/heads/main\n\n" +
		"worktree /repo/issue\nHEAD def\nbranch refs/heads/worktree/issue\n\n" +
		"worktree /repo/detached\nHEAD fed\ndetached\nprunable stale\n\n"
	want := []Entry{
		{Path: "/repo/main", Branch: "main"},
		{Path: "/repo/issue", Branch: "worktree/issue"},
		{Path: "/repo/detached", IsDetached: true, IsPrunable: true},
	}
	if got := ParseWorktreeListPorcelain([]byte(out)); !reflect.DeepEqual(got, want) {
		t.Fatalf("parse = %+v, want %+v", got, want)
	}

	bare := "worktree /repo/bare.git\nbare\n"
	if got := ParseWorktreeListPorcelain([]byte(bare)); len(got) != 1 || !got[0].IsBare {
		t.Fatalf("bare parse = %+v", got)
	}
	if got := ParseWorktreeListPorcelain(nil); len(got) != 0 {
		t.Fatalf("empty parse = %+v", got)
	}
}

// Dirty-remove detection needs both git substrings, so the locked-worktree hint
// does not escalate the confirm.
func TestIsDirtyRemoveError(t *testing.T) {
	for _, tc := range []struct {
		msg  string
		want bool
	}{
		{"fatal: '/w/herdr' contains modified or untracked files, use --force to delete it", true},
		{"fatal: '/w/herdr' is a missing but already registered worktree", false},
		{"fatal: '/w/herdr' contains a locked worktree, use --force only if you know why", false},
	} {
		if got := IsDirtyRemoveError(tc.msg); got != tc.want {
			t.Errorf("IsDirtyRemoveError(%q) = %v, want %v", tc.msg, got, tc.want)
		}
	}
}

// The command builders emit the exact git invocations; remove never names the
// branch (the checkout goes, the branch stays).
func TestCommandBuilders(t *testing.T) {
	add := AddCommand("/repo/herdr", "/w/herdr/worktree-brave-river", "worktree/brave-river", "HEAD")
	if add.Program != "git" || !reflect.DeepEqual(add.Args, []string{
		"-C", "/repo/herdr", "worktree", "add", "-b", "worktree/brave-river",
		"/w/herdr/worktree-brave-river", "HEAD",
	}) {
		t.Fatalf("AddCommand = %+v", add)
	}

	rm := RemoveCommand("/repo/herdr", "/w/herdr/issue-137", false)
	if !reflect.DeepEqual(rm.Args, []string{"-C", "/repo/herdr", "worktree", "remove", "/w/herdr/issue-137"}) {
		t.Fatalf("RemoveCommand = %+v", rm)
	}
	rmf := RemoveCommand("/repo/herdr", "/w/herdr/issue-137", true)
	if !reflect.DeepEqual(rmf.Args, []string{"-C", "/repo/herdr", "worktree", "remove", "--force", "/w/herdr/issue-137"}) {
		t.Fatalf("forced RemoveCommand = %+v", rmf)
	}

	ls := ListCommand("/repo/herdr")
	if !reflect.DeepEqual(ls.Args, []string{"-C", "/repo/herdr", "worktree", "list", "--porcelain"}) {
		t.Fatalf("ListCommand = %+v", ls)
	}
}

func TestExpandTilde(t *testing.T) {
	t.Setenv("HOME", "/home/me")
	for _, tc := range []struct{ in, want string }{
		{"~/.herdr/worktrees", "/home/me/.herdr/worktrees"},
		{"~", "/home/me"},
		{"/tmp/worktrees", "/tmp/worktrees"},
		{"~backup", "~backup"}, // not a home reference
	} {
		if got := ExpandTilde(tc.in); got != tc.want {
			t.Errorf("ExpandTilde(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
