//go:build darwin

package detect

import (
	"os/exec"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Spawns a PTY whose foreground process advertises argv[0]="claude" (via the
// shell's `exec -a`) over a real binary (sleep), and asserts procscan identifies
// it — exercising tcgetpgrp + process-group enumeration + argv inspection without
// needing a real agent installed.
func TestForegroundAgentIdentifiesByArgv(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "exec -a claude sleep 5")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty.Start: %v", err)
	}
	defer func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if label := ForegroundAgent(ptmx.Fd()); label == "claude" {
			return // success
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("ForegroundAgent never identified claude; got %q", ForegroundAgent(ptmx.Fd()))
}

func TestForegroundAgentPlainShellIsEmpty(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 5")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatalf("pty.Start: %v", err)
	}
	defer func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// Give it a moment to settle, then confirm a plain shell is not an agent.
	time.Sleep(200 * time.Millisecond)
	if label := ForegroundAgent(ptmx.Fd()); label != "" {
		t.Fatalf("plain shell identified as %q, want empty", label)
	}
}
