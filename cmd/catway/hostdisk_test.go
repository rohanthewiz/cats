//go:build ghostty

package main

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/browserproto"
)

// There is no captured-output parser here the way there is for memory — statfs
// hands back a struct, not text, so the only thing worth testing is the reading
// itself and the two ways it is allowed to fail. It is a smoke test rather than
// an assertion about the machine: no independent figure for this volume exists
// inside the process to check the answer against.

func TestHostDiskLive(t *testing.T) {
	w := hostDisk()
	switch runtime.GOOS {
	case "darwin", "linux":
		if w.Pct < 0 {
			t.Fatalf("no reading on %s", runtime.GOOS)
		}
		// Zero is excluded as well as out-of-range: a mounted volume with
		// literally nothing on it does not occur, so a 0 here means the
		// arithmetic lost the used figure rather than that the disk is empty.
		if w.Pct == 0 || w.Pct > 100 {
			t.Fatalf("pct = %v, want a plausible share", w.Pct)
		}
		if !strings.Contains(w.Detail, "/") {
			t.Fatalf("detail = %q, want used/total", w.Detail)
		}
	default:
		if w.Pct != browserproto.UsagePctUnknown {
			t.Fatalf("pct = %v on %s, want unknown", w.Pct, runtime.GOOS)
		}
	}
}

// A path that does not exist must report nothing rather than zeroes. Zeroes
// would reach the sidebar as a 0% row — a volume with infinite room — which is
// the one wrong answer worse than no row at all.
func TestDiskBytesMissingPath(t *testing.T) {
	if _, _, err := diskBytes(t.TempDir() + "/no/such/place"); err == nil {
		t.Fatal("want an error for an unstattable path")
	}
}

// The path choice, not the numbers: home when the environment has one, the root
// when it does not. Both must be stattable on a host that supports the call, so
// the fallback is exercised as a real reading rather than only as a string.
func TestHostDiskPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if got := hostDiskPath(); got != home {
			t.Fatalf("path = %q, want the home directory %q", got, home)
		}
	}
	// UserHomeDir reads $HOME on both supported systems, so emptying it forces
	// the fallback branch without touching the real environment beyond this test.
	t.Setenv("HOME", "")
	if got := hostDiskPath(); got != "/" {
		t.Fatalf("path without a home = %q, want /", got)
	}
	switch runtime.GOOS {
	case "darwin", "linux":
		if _, _, err := diskBytes("/"); err != nil {
			t.Fatalf("the fallback path is unreadable: %v", err)
		}
	}
}
