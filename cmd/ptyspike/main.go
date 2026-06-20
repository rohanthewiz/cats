//go:build ghostty

// Command ptyspike is the Phase B proof-of-concept for the full Go terminal
// path: it spawns a real shell in a PTY and pumps the shell's output through the
// internal/terminal Emulator (go-libghostty under the hood), then renders the
// resulting Snapshot. This is the path that, in Phase B, replaces the Rust
// server's src/pty + src/ghostty + src/terminal for a pane.
//
// Build requires libghostty-vt on PKG_CONFIG_PATH and -tags ghostty;
// see scripts/build-libghostty-vt.sh.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/creack/pty"
	"github.com/rohanthewiz/herdr-web/internal/terminal"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ptyspike:", err)
		os.Exit(1)
	}
}

func run() error {
	const cols, rows = 80, 24

	// Go-owned VT emulator (Phase B terminal runtime).
	emu, err := terminal.New(cols, rows)
	if err != nil {
		return fmt.Errorf("new emulator: %w", err)
	}
	defer emu.Close()

	// Go-owned PTY running a real shell with a deterministic command sequence.
	cmd := exec.Command("/bin/sh", "-c",
		`printf 'PTY -> internal/terminal\n'; `+
			`printf '\033[1;31mred\033[0m \033[1;34mblue\033[0m bold:\033[1mB\033[0m\n'; `+
			`printf 'cols=%s\n' "$(tput cols 2>/dev/null)"; `+
			`printf 'done\n'`)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return fmt.Errorf("start pty: %w", err)
	}
	defer func() { _ = ptmx.Close() }()

	// Pump PTY output into the emulator until the shell exits / EOF. The
	// Emulator is an io.Writer, so this is a plain io.Copy.
	copyDone := make(chan error, 1)
	go func() {
		_, cErr := io.Copy(emu, ptmx)
		copyDone <- cErr
	}()

	_ = cmd.Wait()
	select {
	case <-copyDone:
	case <-time.After(200 * time.Millisecond):
	}

	snap, err := emu.Snapshot()
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}

	fmt.Printf("=== %dx%d grid after running a real shell through the Emulator ===\n", snap.Cols, snap.Rows)
	for y := uint16(0); y < snap.Rows; y++ {
		line := renderRow(snap, y)
		if line == "" {
			continue
		}
		fmt.Printf("row %2d | %s\n", y, line)
	}
	fmt.Printf("cursor: (%d,%d) visible=%v\n", snap.Cursor.X, snap.Cursor.Y, snap.Cursor.Visible)
	fmt.Println("OK: shell PTY -> internal/terminal.Emulator -> Snapshot, all in Go.")
	return nil
}

// renderRow returns the row text, annotating cells that carry an explicit color.
func renderRow(s *terminal.Snapshot, y uint16) string {
	text := ""
	colored := ""
	for x := uint16(0); x < s.Cols; x++ {
		c := s.At(x, y)
		ch := c.Rune
		if ch == "" {
			ch = " "
		}
		text += ch
		if c.Fg != nil && c.Rune != "" {
			colored += fmt.Sprintf(" %q#%02x%02x%02x", c.Rune, c.Fg.R, c.Fg.G, c.Fg.B)
		}
	}
	// trim trailing blanks
	i := len(text)
	for i > 0 && text[i-1] == ' ' {
		i--
	}
	text = text[:i]
	if text == "" {
		return ""
	}
	if colored != "" {
		return fmt.Sprintf("%-40q fg:%s", text, colored)
	}
	return fmt.Sprintf("%q", text)
}
