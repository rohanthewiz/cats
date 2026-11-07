package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/ctlproto"
	"github.com/rohanthewiz/cats/internal/integration"
)

// fakePaneList serves pane.list on a throwaway unix socket with the given rows,
// counting the calls it answers. One request per connection, the shape
// ctlproto.Call speaks. The socket lives under a short temp dir because a unix
// socket path is capped at ~104 bytes on macOS, which t.TempDir() can exceed.
func fakePaneList(t *testing.T, rows []app.PaneInfo) (sock string, calls *int) {
	t.Helper()
	dir, err := os.MkdirTemp("", "cc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock = filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	calls = new(int)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			var req ctlproto.Request
			line, _ := bufio.NewReader(c).ReadBytes('\n')
			_ = json.Unmarshal(line, &req)
			*calls++
			data, _ := json.Marshal(app.PaneListResult{Panes: rows})
			resp, _ := json.Marshal(ctlproto.Response{ID: req.ID, OK: req.Method == app.CmdPaneList, Data: data})
			_, _ = c.Write(append(resp, '\n'))
			c.Close()
		}
	}()
	return sock, calls
}

// invokingPane is what makes `plugin run --right` mean "beside the pane I typed
// this in" rather than "beside whatever the server has focused".
func TestInvokingPane(t *testing.T) {
	rows := []app.PaneInfo{{Pane: 7, Handle: "w1:p1"}, {Pane: 12, Handle: "w1:p2"}}

	t.Run("public handle resolves through pane.list", func(t *testing.T) {
		sock, _ := fakePaneList(t, rows)
		t.Setenv(integration.CatsPaneIDEnvVar, "w1:p2")
		if got := invokingPane(sock); got == nil || *got != 12 {
			t.Fatalf("invokingPane = %v, want 12", got)
		}
	})

	t.Run("raw form needs no round trip", func(t *testing.T) {
		sock, calls := fakePaneList(t, rows)
		t.Setenv(integration.CatsPaneIDEnvVar, "p_33")
		if got := invokingPane(sock); got == nil || *got != 33 {
			t.Fatalf("invokingPane = %v, want 33", got)
		}
		if *calls != 0 {
			t.Fatalf("p_<raw> made %d calls, want 0", *calls)
		}
	})

	// Every miss leaves the split on the focused pane (nil), never an error.
	for name, env := range map[string]string{
		"unset":                "",
		"handle not in view":   "w9:p4",
		"malformed raw handle": "p_x",
	} {
		t.Run(name, func(t *testing.T) {
			sock, _ := fakePaneList(t, rows)
			t.Setenv(integration.CatsPaneIDEnvVar, env)
			if got := invokingPane(sock); got != nil {
				t.Fatalf("invokingPane = %d, want nil", *got)
			}
		})
	}

	t.Run("dead socket", func(t *testing.T) {
		t.Setenv(integration.CatsPaneIDEnvVar, "w1:p2")
		if got := invokingPane(filepath.Join(t.TempDir(), "none")); got != nil {
			t.Fatalf("invokingPane = %d, want nil", *got)
		}
	})
}

// The placement flags are rejected before any plugin lookup or socket dial, so
// these run with neither.
func TestPluginRunPlacementUsage(t *testing.T) {
	for name, args := range map[string][]string{
		"right and below":    {"x", "--right", "--below"},
		"all with right":     {"x", "--all", "--right"},
		"all with pane":      {"x", "--all", "--pane", "3"},
		"pane without value": {"x", "--pane"},
		"pane not a number":  {"x", "--pane", "w1:p2"},
	} {
		t.Run(name, func(t *testing.T) {
			if code := pluginRun(args, ""); code != 2 {
				t.Fatalf("pluginRun(%v) = %d, want 2 (usage)", args, code)
			}
		})
	}
}
