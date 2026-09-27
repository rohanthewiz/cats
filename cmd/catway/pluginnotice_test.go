//go:build ghostty

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/plugin"
	"github.com/rohanthewiz/cats/wire"
)

// noticeOrch is the smallest orch plugin_notice needs: one connected window
// to receive broadcasts, and a mailbox for the off-loop reads to post into.
func noticeOrch() (*orch, *client) {
	c := &client{out: make(chan []byte, 8)}
	return &orch{conns: map[*client]struct{}{c: {}}, mailbox: make(chan func(), 8)}, c
}

// sentNotices decodes every plugin_notice waiting on c's queue, in order.
func sentNotices(t *testing.T, c *client) [][]string {
	t.Helper()
	var got [][]string
	for {
		select {
		case b := <-c.out:
			var m wire.PluginNotice
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatalf("decode %s: %v", b, err)
			}
			if m.T != wire.MsgPluginNotice {
				continue
			}
			if m.FailedDefaults == nil {
				t.Fatalf("plugin_notice sent a null list, want []: %s", b)
			}
			got = append(got, m.FailedDefaults)
		default:
			return got
		}
	}
}

// waitPosted runs the one closure an off-loop read posts, standing in for the
// loop, and fails if none arrives in time.
func waitPosted(t *testing.T, o *orch) {
	t.Helper()
	select {
	case fn := <-o.mailbox:
		fn()
	case <-time.After(2 * time.Second):
		t.Fatal("the read posted nothing")
	}
}

// The toolbar mark is a broadcast of disk state: a read that differs from what
// windows were told is sent once, an unchanged read is not re-sent, and the
// plugin appearing under the plugins root (a catctl install in some tab)
// retracts it with an empty list rather than silence.
func TestPluginNoticeFollowsTheSeedState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	t.Setenv(plugin.DirEnvVar, root)
	t.Setenv(plugin.NoDefaultsEnvVar, "")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	state := `{"pending":[],"failed":[{"ID":"rohanthewiz.cats-todo","Source":"rohanthewiz/cats-todo",` +
		`"attempts":3,"last_error":"build step 1: exit status 127"}]}`
	if err := os.WriteFile(filepath.Join(root, ".cats-defaults.json"), []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}

	o, c := noticeOrch()
	if got := o.pluginNoticeMsg(); got.FailedDefaults == nil || len(got.FailedDefaults) != 0 {
		t.Fatalf("before any read the notice is %+v, want an empty list", got)
	}

	// The end of the seed pass: the failure is read and broadcast.
	o.refreshPluginNotice()
	waitPosted(t, o)
	if got := sentNotices(t, c); len(got) != 1 || !slices.Equal(got[0], []string{"rohanthewiz.cats-todo"}) {
		t.Fatalf("after the seed: notices %q, want one naming cats-todo", got)
	}

	// Another read of the same file (the hourly update check): nothing new.
	ids, ok := readFailedDefaultIDs()
	if !ok {
		t.Fatal("readFailedDefaultIDs failed on a good state file")
	}
	o.setFailedDefaults(ids)
	if got := sentNotices(t, c); len(got) != 0 {
		t.Fatalf("an unchanged read re-broadcast: %q", got)
	}

	// Installed by hand since: the presence filter hides it, and the mark is
	// retracted explicitly.
	if err := os.MkdirAll(filepath.Join(root, "rohanthewiz.cats-todo"), 0o755); err != nil {
		t.Fatal(err)
	}
	o.refreshPluginNotice()
	waitPosted(t, o)
	if got := sentNotices(t, c); len(got) != 1 || len(got[0]) != 0 {
		t.Fatalf("after the install: notices %q, want one empty list", got)
	}
}

// A state file that cannot be read keeps the last answer: a transient error
// is not news that the plugin got installed, so the mark must not blink off.
func TestPluginNoticeKeepsTheMarkOnAReadError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	t.Setenv(plugin.DirEnvVar, root)
	t.Setenv(plugin.NoDefaultsEnvVar, "")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".cats-defaults.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	o, c := noticeOrch()
	o.setFailedDefaults([]string{"rohanthewiz.cats-todo"})
	sentNotices(t, c) // the setup's own broadcast

	if _, ok := readFailedDefaultIDs(); ok {
		t.Fatal("a corrupt state file read as ok")
	}
	o.refreshPluginNotice()
	// The failed read posts nothing; give it the time a successful one would
	// have taken to show up, then check nothing did.
	select {
	case fn := <-o.mailbox:
		fn()
		t.Fatal("a failed read posted an update")
	case <-time.After(100 * time.Millisecond):
	}
	if got := sentNotices(t, c); len(got) != 0 {
		t.Fatalf("a failed read changed the notice: %q", got)
	}
	if !slices.Equal(o.failedDefaults, []string{"rohanthewiz.cats-todo"}) {
		t.Fatalf("a failed read dropped the mark: %q", o.failedDefaults)
	}
}
