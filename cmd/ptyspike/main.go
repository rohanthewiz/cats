// Command ptyspike is the second Phase B proof-of-concept: it spawns a real
// shell in a PTY, pumps the shell's output into a go-libghostty terminal, and
// then dumps the emulated cell grid. This is the full Phase B terminal path
// (Go owns the PTY *and* the VT emulation) that today lives in the Rust server's
// src/pty + src/ghostty + src/terminal.
//
// Build requires libghostty-vt on PKG_CONFIG_PATH; see scripts/build-libghostty-vt.sh.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/creack/pty"
	libghostty "go.mitchellh.com/libghostty"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ptyspike:", err)
		os.Exit(1)
	}
}

func run() error {
	const cols, rows = 80, 24

	// Go-owned VT emulator.
	term, err := libghostty.NewTerminal(libghostty.WithSize(cols, rows))
	if err != nil {
		return fmt.Errorf("new terminal: %w", err)
	}
	defer term.Close()

	// Go-owned PTY running a real shell with a deterministic command sequence.
	// We run a non-interactive script so the spike terminates on its own.
	cmd := exec.Command("/bin/sh", "-c",
		`printf 'PTY -> go-libghostty\n'; `+
			`printf '\033[1;31mred\033[0m \033[1;34mblue\033[0m bold:\033[1mB\033[0m\n'; `+
			`printf 'cols=%s\n' "$(tput cols 2>/dev/null)"; `+
			`printf 'done\n'`)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return fmt.Errorf("start pty: %w", err)
	}
	defer func() { _ = ptmx.Close() }()

	// Pump PTY output into the VT emulator until the shell exits / EOF.
	// term.Write satisfies io.Writer, so this is a plain io.Copy.
	copyDone := make(chan error, 1)
	go func() {
		_, cErr := io.Copy(term, ptmx)
		copyDone <- cErr
	}()

	_ = cmd.Wait()
	// Give the reader a beat to drain any buffered output after exit.
	select {
	case <-copyDone:
	case <-time.After(200 * time.Millisecond):
	}

	// Dump the resulting grid via the plain-text formatter.
	f, err := libghostty.NewFormatter(term,
		libghostty.WithFormatterFormat(libghostty.FormatterFormatPlain),
		libghostty.WithFormatterTrim(true),
	)
	if err != nil {
		return fmt.Errorf("new formatter: %w", err)
	}
	defer f.Close()

	out, err := f.FormatString()
	if err != nil {
		return fmt.Errorf("format: %w", err)
	}

	fmt.Printf("=== %dx%d grid after running a real shell through the PTY ===\n", cols, rows)
	fmt.Println(out)
	fmt.Println("OK: spawned a shell PTY in Go, emulated it with go-libghostty, read the grid back.")
	return nil
}
