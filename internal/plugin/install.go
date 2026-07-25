package plugin

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Install clones source (an "owner/repo" GitHub shorthand or a full git URL /
// local path), validates its manifest, runs its build steps, and moves it into
// the plugins root under the manifest's id. ref pins a branch or tag ("" =
// default branch). out receives build/clone output (pass os.Stdout for a CLI).
//
// The clone lands in a dot-prefixed temp dir *inside the plugins root* — same
// filesystem, so the final os.Rename is atomic, and a crash mid-install leaves
// only an invisible directory rather than a half-usable plugin. The .git dir
// is kept: it is the provenance a future `plugin update` would pull on.
func Install(source, ref string, out io.Writer) (Installed, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return Installed{}, fmt.Errorf("git is required to install plugins: %w", err)
	}
	url := resolveSourceURL(source)

	root, err := Root()
	if err != nil {
		return Installed{}, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Installed{}, err
	}

	tmp, err := os.MkdirTemp(root, ".installing-")
	if err != nil {
		return Installed{}, err
	}
	defer os.RemoveAll(tmp) // no-op after the success rename

	// --depth 1 keeps installs fast; --branch covers both branches and tags,
	// which is what a plugin release ref is. (A bare commit sha would need a
	// full clone + checkout — not supported until someone actually needs it.)
	args := []string{"clone", "--depth", "1"}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, url, tmp)
	if err := runStep(root, append([]string{"git"}, args...), out); err != nil {
		return Installed{}, fmt.Errorf("clone %s: %w", url, err)
	}

	m, err := LoadManifest(tmp)
	if err != nil {
		return Installed{}, err
	}
	if !m.currentPlatformSupported() {
		return Installed{}, fmt.Errorf("plugin %s does not support this platform (supports: %s)",
			m.ID, strings.Join(m.Platforms, ", "))
	}

	// The destination is keyed by manifest id, which is only known post-clone —
	// so the already-installed check necessarily happens here, not up front.
	dest := filepath.Join(root, m.ID)
	if _, err := os.Lstat(dest); err == nil {
		return Installed{}, fmt.Errorf("plugin %q is already installed (uninstall it first)", m.ID)
	}

	if err := runBuild(tmp, m.Build, out); err != nil {
		return Installed{}, err
	}

	meta, _ := json.MarshalIndent(sourceMeta{Source: source, URL: url, Ref: ref}, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, sourceMetaName), append(meta, '\n'), 0o644); err != nil {
		return Installed{}, err
	}

	if err := os.Rename(tmp, dest); err != nil {
		return Installed{}, err
	}
	return load(root, m.ID)
}

// Link registers a local checkout as a plugin without copying it: the plugins
// root gains a symlink <root>/<id> → dir. The build still runs — a fresh
// checkout has no ./bin yet, and running it here makes `link` sufficient on
// its own, matching `herdr plugin link .`.
func Link(dir string, out io.Writer) (Installed, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Installed{}, err
	}
	m, err := LoadManifest(abs)
	if err != nil {
		return Installed{}, err
	}
	if !m.currentPlatformSupported() {
		return Installed{}, fmt.Errorf("plugin %s does not support this platform (supports: %s)",
			m.ID, strings.Join(m.Platforms, ", "))
	}

	root, err := Root()
	if err != nil {
		return Installed{}, err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Installed{}, err
	}

	entry := filepath.Join(root, m.ID)
	if fi, err := os.Lstat(entry); err == nil {
		// Re-linking the same checkout is idempotent (and re-runs the build —
		// the reason a developer re-links); anything else is a conflict.
		if fi.Mode()&os.ModeSymlink != 0 {
			if target, _ := os.Readlink(entry); target == abs {
				if err := runBuild(abs, m.Build, out); err != nil {
					return Installed{}, err
				}
				return load(root, m.ID)
			}
		}
		return Installed{}, fmt.Errorf("plugin %q is already installed (uninstall it first)", m.ID)
	}

	if err := runBuild(abs, m.Build, out); err != nil {
		return Installed{}, err
	}
	if err := os.Symlink(abs, entry); err != nil {
		return Installed{}, err
	}
	return load(root, m.ID)
}

// resolveSourceURL turns the CLI's source argument into something git clone
// accepts. Only the bare "owner/repo" shorthand is rewritten (to GitHub, like
// herdr); anything with a scheme, an scp-style git@ prefix, or a filesystem
// path shape passes through untouched so non-GitHub hosts and local repos
// keep working.
func resolveSourceURL(source string) string {
	if strings.Contains(source, "://") || strings.HasPrefix(source, "git@") {
		return source
	}
	if strings.HasPrefix(source, "/") || strings.HasPrefix(source, ".") || strings.HasPrefix(source, "~") {
		return source
	}
	if parts := strings.Split(source, "/"); len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return "https://github.com/" + source + ".git"
	}
	return source
}

// runBuild executes the manifest's [[build]] steps in order, in the plugin
// root, streaming output to out. Steps run with the host's environment — a
// plugin build needs the same PATH/toolchain the user has.
func runBuild(dir string, steps []BuildStep, out io.Writer) error {
	for i, st := range steps {
		if err := runStep(dir, st.Command, out); err != nil {
			return fmt.Errorf("build step %d (%s): %w", i+1, strings.Join(st.Command, " "), err)
		}
	}
	return nil
}

// runStep runs one argv in dir with output streamed to out.
func runStep(dir string, argv []string, out io.Writer) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	if out != nil {
		cmd.Stdout, cmd.Stderr = out, out
	}
	return cmd.Run()
}
