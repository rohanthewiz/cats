// Package startdir picks the directory a new shell starts in.
//
// "/" is never a useful answer: a GUI launch (Finder, `open`, launchd) hands
// the process "/" as its cwd, and a terminal that opens at the filesystem root
// helps nobody. Every launcher and every restored snapshot funnels its
// candidate directories through Usable, so the fall back to $HOME is decided
// once here instead of being re-derived (and forgotten) per entry point.
package startdir

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Usable returns the first candidate that is a directory worth opening a shell
// in, falling back to the user's home directory, and finally to "" — let the
// child inherit whatever the parent had — when even home is unresolvable.
func Usable(candidates ...string) string {
	for _, c := range candidates {
		if usable(c) {
			return c
		}
	}
	return Home()
}

// Home is the user's home directory, "" when it cannot be determined.
func Home() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// Resolve normalizes a start path a user typed: environment references and a
// leading "~" expand, a relative path resolves against base (the caller's own
// default directory), and the result must be an existing directory. A typo is
// an error the caller reports rather than a silent fallback — landing somewhere
// unexpected is worse than being told the path is wrong. Empty input is "no
// opinion": it returns "" with no error, and the caller applies its default.
//
// Unlike Usable, an explicit "/" is honoured: a user who types the filesystem
// root means it.
func Resolve(path, base string) (string, error) {
	p, err := expand(path, base)
	if err != nil || p == "" {
		return p, err
	}
	fi, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("no such directory: %s", p)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", p, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("not a directory: %s", p)
	}
	return p, nil
}

// ResolveOrCreate is Resolve for a directory the user has asked to bring into
// existence: same expansion, but a path that does not exist is created (parents
// included) instead of rejected. It stays a distinct entry point rather than a
// flag on Resolve because creating is a side effect the caller must opt into
// explicitly — a typo under Resolve is an error, never a new directory.
// A path that exists but is not a directory is still an error: a file cannot
// be made into one.
func ResolveOrCreate(path, base string) (string, error) {
	p, err := expand(path, base)
	if err != nil || p == "" {
		return p, err
	}
	fi, err := os.Stat(p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(p, 0o755); err != nil {
			return "", fmt.Errorf("cannot create directory: %w", err)
		}
	case err != nil:
		return "", fmt.Errorf("%s: %w", p, err)
	case !fi.IsDir():
		return "", fmt.Errorf("not a directory: %s", p)
	}
	return p, nil
}

// expand is the shared normalization half of Resolve/ResolveOrCreate: env and
// "~" expansion, relative-against-base resolution, and cleaning — everything
// short of touching the filesystem. Empty input stays "" (the caller's "no
// opinion" state).
func expand(path, base string) (string, error) {
	p := os.ExpandEnv(strings.TrimSpace(path))
	if p == "" {
		return "", nil
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home := Home()
		if home == "" {
			return "", fmt.Errorf("cannot expand %q: no home directory", path)
		}
		p = filepath.Join(home, strings.TrimPrefix(p[1:], "/"))
	}
	if !filepath.IsAbs(p) {
		if base == "" {
			return "", fmt.Errorf("relative path %q with no directory to resolve it against", path)
		}
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p), nil
}

// usable reports whether p is an existing directory other than the filesystem
// root — the shape of every start directory we are willing to inherit.
func usable(p string) bool {
	if p == "" || p == "/" {
		return false
	}
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
