//go:build unix

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// withLock runs fn holding an exclusive advisory lock on a hidden sidecar
// (".config.json.lock" beside config.json, so a listing of the config dir
// shows only the files a user edits). The
// lock is on a sidecar rather than the config itself because writes replace
// the config by rename: a lock on the old inode would guard a file that no
// longer exists at that path.
//
// Advisory is enough — both writers (catway and the Mac app) are ours — and
// flock is released by the kernel if the holder dies, so a crash mid-save can
// never wedge the next one.
func withLock(path string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("config lock: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("config lock: %w", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("config lock: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck // Close releases it anyway
	return fn()
}
