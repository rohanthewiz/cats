package plugin

// The cats bin directory: one PATH entry for every plugin-provided tool.
//
// A plugin's binaries live under its own install dir, which keeps installs
// atomic and uninstalls a single RemoveAll — but leaves the tools unreachable
// from the user's shell. Rather than putting each plugin dir on PATH (a PATH
// that grows per plugin, and lingers after uninstall), the host maintains a
// symlink farm:
//
//	~/.cats/bin/<name> -> <plugins-root>/<id>/<declared bin path>
//
// with a single guarded PATH prepend emitted by `catctl shellinit` and by
// shellenv for spawned panes. Links always target the *stable* plugins-root
// path — for a dev-linked plugin the target traverses the <root>/<id> symlink
// rather than pointing into the checkout directly. That buys three things:
// ownership is a plain prefix test on the link target (no manifest needed to
// clean up a broken plugin), a re-linked or moved checkout never invalidates
// the farm, and rebuilds need no re-linking.
//
// ~/.cats (not ~/.config/cats, not ~/.local/state/cats) because the bin dir is
// user-facing like the worktree checkouts already there: something the user's
// own shell touches, neither configuration nor server state.

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// BinDirEnvVar overrides the bin directory — the same test/scratch escape
// hatch CATS_PLUGINS_DIR provides for the plugins root.
const BinDirEnvVar = "CATS_BIN_DIR"

// BinDir resolves the cats bin directory: $CATS_BIN_DIR > ~/.cats/bin.
func BinDir() (string, error) {
	if dir := os.Getenv(BinDirEnvVar); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	return filepath.Join(home, ".cats", "bin"), nil
}

// SyncBinLinks reconciles the bin directory with inst's declared bin entries:
// owned links that no longer match the manifest are removed, missing ones are
// created. Called after install, link, and update — unconditionally on update,
// so a plugin installed before this feature existed heals on its next refresh.
//
// The farm is a convenience, not the install: every problem here (a foreign
// file squatting on a name, an unwritable dir) is reported to out and skipped
// rather than failing an otherwise-complete operation. Only being unable to
// resolve the directories at all is an error.
func SyncBinLinks(inst Installed, out io.Writer) error {
	root, err := Root()
	if err != nil {
		return err
	}
	binDir, err := BinDir()
	if err != nil {
		return err
	}

	// Desired state: base name -> stable target under the plugins root.
	// Manifest validation already bounded the paths and base names.
	desired := map[string]string{}
	for _, b := range inst.Bin {
		rel, err := localRel(b)
		if err != nil {
			continue // unvalidated caller; skip rather than link outside the tree
		}
		desired[filepath.Base(rel)] = filepath.Join(root, inst.ID, rel)
	}

	// Nothing declared and nothing to clean: don't create the directory just
	// to leave it empty.
	if len(desired) == 0 {
		if _, err := os.Lstat(binDir); err != nil {
			return nil
		}
	} else if err := os.MkdirAll(binDir, 0o755); err != nil {
		return err
	}

	ownPrefix := filepath.Join(root, inst.ID) + string(filepath.Separator)
	entries, err := os.ReadDir(binDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		link := filepath.Join(binDir, name)
		target, lerr := os.Readlink(link)
		if lerr != nil || !strings.HasPrefix(target, ownPrefix) {
			// Not a symlink, or someone else's (another plugin's, or a file the
			// user put there). If it shadows a name we want, say so — silently
			// skipping would look like a link that never got created.
			if _, want := desired[name]; want && out != nil {
				fmt.Fprintf(out, "warning: %s exists and is not owned by %s; leaving it alone\n", link, inst.ID)
			}
			delete(desired, name)
			continue
		}
		if want, ok := desired[name]; ok && want == target {
			delete(desired, name) // already correct
			continue
		}
		// Ours, but stale: the manifest dropped or renamed the entry.
		if rmErr := os.Remove(link); rmErr != nil && out != nil {
			fmt.Fprintf(out, "warning: cannot remove stale link %s: %v\n", link, rmErr)
		}
	}

	for name, target := range desired {
		if err := os.Symlink(target, filepath.Join(binDir, name)); err != nil && out != nil {
			fmt.Fprintf(out, "warning: cannot link %s into %s: %v\n", name, binDir, err)
		}
	}
	return nil
}

// RemoveBinLinks removes every link in the bin directory owned by id. It works
// from link targets alone — no manifest read — so a plugin whose manifest is
// broken or gone still cleans up, which is exactly the state Uninstall is
// often called to fix.
func RemoveBinLinks(id string) error {
	root, err := Root()
	if err != nil {
		return err
	}
	binDir, err := BinDir()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(binDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	ownPrefix := filepath.Join(root, id) + string(filepath.Separator)
	for _, e := range entries {
		link := filepath.Join(binDir, e.Name())
		if target, lerr := os.Readlink(link); lerr == nil && strings.HasPrefix(target, ownPrefix) {
			if rmErr := os.Remove(link); rmErr != nil {
				err = rmErr
			}
		}
	}
	return err
}
