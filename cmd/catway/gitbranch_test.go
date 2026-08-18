//go:build ghostty

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rohanthewiz/cats/internal/orchestration"
)

// writeFile writes content at path, creating parents. Fixtures here are the
// on-disk shapes git itself produces, since reading them is the whole feature.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The branch badge is resolved by reading this machine's .git files against the
// pane's cwd. For a pane on another host that cwd belongs to another
// filesystem, where the answer is either nothing or — worse — a real branch
// from a same-named checkout here. So a remote pane resolves no branch at all,
// and one that carried a branch before its host changed drops it.
func TestRefreshPaneBranchSkipsRemotePanes(t *testing.T) {
	o, localPane, remotePane, _, _ := twoHostOrch(t)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")

	remote := o.panes[remotePane]
	remote.cwd, remote.branch = root, "stale"
	o.refreshPaneBranch(remote)
	if remote.branch != "" {
		t.Fatalf("remote pane branch = %q, want it cleared", remote.branch)
	}
	if remote.branchBusy {
		t.Fatal("a remote pane must not start a branch read")
	}

	// The local pane still resolves — the guard is about the host, not about
	// branches going away once a second host exists.
	local := o.panes[localPane]
	local.cwd = root
	o.refreshPaneBranch(local)
	if !local.branchBusy {
		t.Fatal("a local pane should have started a branch read")
	}
}

// From protocol v3 the daemon resolves its own panes' branches, so catway must
// stop reading HEAD for them — including for the local host, whose cathost is
// v3 too. Two readers would race, and the loser (this one) is reading a
// filesystem that only coincidentally matches the pane's.
func TestRefreshPaneBranchDefersToAV3Host(t *testing.T) {
	o, localPane, _, _, _ := twoHostOrch(t)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/main\n")

	rt := o.panes[localPane]
	rt.cwd = root
	o.hosts[o.defaultHost].setPeerVersion(orchestration.ProtocolVersion)

	o.refreshPaneBranch(rt)
	if rt.branchBusy {
		t.Fatal("catway started its own branch read for a pane whose host resolves branches")
	}
	if rt.branch != "" {
		t.Fatalf("branch = %q, want it to come from the host, not from here", rt.branch)
	}

	// A v2 host cannot resolve anything, and a v2 host is always this machine:
	// the fallback stays live for exactly that case.
	o.hosts[o.defaultHost].setPeerVersion(orchestration.MinProtocolVersion)
	o.refreshPaneBranch(rt)
	if !rt.branchBusy {
		t.Fatal("a pane on a v2 host should still be resolved locally")
	}
}

// The host's answer is the one that reaches the browser — for a remote pane it
// is the only answer there is, since nothing on this machine can name that
// directory.
func TestApplyPaneBranchFromHost(t *testing.T) {
	o, _, remotePane, _, _ := twoHostOrch(t)

	rt := o.panes[remotePane]
	o.hosts[testRemoteHost].setPeerVersion(orchestration.ProtocolVersion)
	o.applyPaneBranch(remotePane, "feature/remote-dream")
	if rt.branch != "feature/remote-dream" {
		t.Fatalf("branch = %q, want the host's answer", rt.branch)
	}
	// The local sweep still runs over every pane; it must leave this one alone
	// rather than clearing a label it is in no position to have an opinion on.
	o.refreshPaneBranch(rt)
	if rt.branch != "feature/remote-dream" {
		t.Fatalf("branch = %q after a local sweep, want the host's answer kept", rt.branch)
	}

	o.applyPaneBranch(remotePane, "")
	if rt.branch != "" {
		t.Fatalf("branch = %q, want the host's clear to land", rt.branch)
	}
	o.applyPaneBranch(99999, "nowhere") // an unknown pane is simply ignored
}

// When the host holding a remote pane goes away, its branch goes with it: the
// label described a checkout on a machine nobody can currently reach, and a
// badge that keeps asserting it is worse than one that says nothing. The host
// re-states it on reconnect (resyncPane replays pane_branch).
func TestRemoteBranchDropsWhenItsHostGoesAway(t *testing.T) {
	o, _, remotePane, _, _ := twoHostOrch(t)

	rt := o.panes[remotePane]
	o.hosts[testRemoteHost].setPeerVersion(orchestration.ProtocolVersion)
	o.applyPaneBranch(remotePane, "feature/remote-dream")

	o.hosts[testRemoteHost].setConn(nil)
	o.refreshPaneBranch(rt)
	if rt.branch != "" {
		t.Fatalf("branch = %q, want it dropped once its host is unreachable", rt.branch)
	}
	if rt.branchBusy {
		t.Fatal("a remote pane must never start a branch read on this machine")
	}
}
