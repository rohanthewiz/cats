//go:build darwin

package detect

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// ProcessExe reads the kernel's path for a live process — here the test binary
// itself, whose path os.Executable also knows — with symlinks already resolved,
// and answers "" for a pid that cannot be a process.
func TestProcessExeResolvesALiveProcess(t *testing.T) {
	want, err := os.Executable()
	if err != nil {
		t.Skip("os.Executable unavailable:", err)
	}
	if real, err := filepath.EvalSymlinks(want); err == nil {
		want = real
	}
	if got := ProcessExe(os.Getpid()); got != want {
		t.Fatalf("ProcessExe(self) = %q, want %q", got, want)
	}
	if got := ProcessExe(0); got != "" {
		t.Fatalf("ProcessExe(0) = %q, want \"\"", got)
	}
}

// ProcessArgs reads the argv a live process was started with. For a `#!`
// script the kernel exec's the interpreter with the script's path spliced in
// after it, so the path the shell exec'd is in argv even though the executable
// is the interpreter — which is what the orchestrator matches a script plugin
// by.
func TestProcessArgsNamesTheScriptBehindAnInterpreter(t *testing.T) {
	if got := ProcessArgs(os.Getpid()); !slices.Equal(got, os.Args) {
		t.Fatalf("ProcessArgs(self) = %q, want %q", got, os.Args)
	}
	if got := ProcessArgs(0); got != nil {
		t.Fatalf("ProcessArgs(0) = %q, want nil", got)
	}

	script := filepath.Join(t.TempDir(), "plug.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, "list")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	// The fork is done when Start returns; the exec may not be. Poll briefly
	// for the interpreter's argv rather than the pre-exec image's.
	deadline := time.Now().Add(2 * time.Second)
	for {
		args := ProcessArgs(cmd.Process.Pid)
		if len(args) >= 3 && args[1] == script && args[2] == "list" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ProcessArgs(script) = %q, want [<sh> %q list]", args, script)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
