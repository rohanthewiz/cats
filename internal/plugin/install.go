package plugin

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/creack/pty"
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
		return Installed{}, occupiedErr(dest, m.ID)
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
	abs, err := filepath.Abs(expandTilde(dir))
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
		return Installed{}, occupiedErr(entry, m.ID)
	}

	if err := runBuild(abs, m.Build, out); err != nil {
		return Installed{}, err
	}
	if err := os.Symlink(abs, entry); err != nil {
		return Installed{}, err
	}
	return load(root, m.ID)
}

// occupiedErr explains why an existing entry blocks Install/Link claiming its
// id. The interesting case is a symlink whose target is gone — a dev link left
// behind after its checkout moved or was deleted (e.g. a plugin extracted to
// its own repo). Plain "already installed" would send the user to `plugin
// list`, where a working install shows up but a broken link needs the message
// to say what is actually squatting on the id and how to clear it.
func occupiedErr(entry, id string) error {
	if fi, err := os.Lstat(entry); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if _, err := os.Stat(entry); err != nil { // Stat follows the link: target missing
			target, _ := os.Readlink(entry)
			return fmt.Errorf("plugin %q is blocked by a broken dev link (-> %s); run `catctl plugin uninstall %s` first",
				id, target, id)
		}
	}
	return fmt.Errorf("plugin %q is already installed (uninstall it first)", id)
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
		return expandTilde(source)
	}
	if parts := strings.Split(source, "/"); len(parts) == 2 && parts[0] != "" && parts[1] != "" {
		return "https://github.com/" + source + ".git"
	}
	return source
}

// expandTilde resolves a leading "~" against the current user's home directory.
// A shell normally does this before a program ever sees the argument, but a
// plugin path can arrive as raw argv with no shell in between: the plugins
// dialog spawns `catctl plugin link <path>` through tab.create, which execs the
// argv directly. Without this, "~/src/thing" would be read as a literal
// relative directory named "~" and fail with a confusing not-found.
//
// "~user" (another user's home) is deliberately left untouched — resolving it
// needs the user database, and nothing in the plugin flow asks for it. Likewise
// an unresolvable home returns the input unchanged so the caller's own
// not-found error surfaces rather than a substituted path.
func expandTilde(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

// InstallCwdEnvVar names the directory the user ran the installer from, and is
// exported to every [[build]] step.
//
// Build steps run with cmd.Dir set to the plugin root, which is right for
// building but leaves a step unable to answer "which project is the user
// actually working in?" — the plugin root is the only directory it can see.
// The host process's own working directory is that answer (catctl inherits the
// user's shell cwd), so it is passed down explicitly rather than left to a
// step to guess. A step that does not care simply ignores it.
const InstallCwdEnvVar = "CATS_PLUGIN_INSTALL_CWD"

// runBuild executes the manifest's [[build]] steps in order, in the plugin
// root, streaming output to out. Steps run with the host's environment — a
// plugin build needs the same PATH/toolchain the user has.
func runBuild(dir string, steps []BuildStep, out io.Writer) error {
	for i, st := range steps {
		if err := runBuildStep(dir, st.Command, out); err != nil {
			return fmt.Errorf("build step %d (%s): %w", i+1, strings.Join(st.Command, " "), err)
		}
	}
	return nil
}

// runBuildStep runs one [[build]] argv. It is runStep plus the two things a
// build step needs and a git invocation must not get:
//
//   - the invoking directory (InstallCwdEnvVar), so a step can offer to set
//     something up in the user's project rather than in the plugin checkout.
//   - the terminal, so a step may ask the user a question at install time —
//     but only when the host actually has one. Inherited unconditionally, a
//     step that reads stdin would hang a headless or scripted install forever
//     instead of seeing the immediate EOF it gets today.
//
// git keeps the old no-stdin behavior deliberately: with a terminal attached,
// a clone of a private repo would sit at a credential prompt inside what the
// user experiences as "catctl is installing", rather than failing fast.
func runBuildStep(dir string, argv []string, out io.Writer) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	if out != nil {
		cmd.Stdout, cmd.Stderr = out, out
	}
	if wd, err := os.Getwd(); err == nil {
		cmd.Env = append(os.Environ(), InstallCwdEnvVar+"="+wd)
	}
	if hostHasTerminal() {
		cmd.Stdin = os.Stdin
	}
	return cmd.Run()
}

// hostHasTerminal reports whether this process's stdin is an interactive
// terminal rather than a pipe, a file, or nothing.
//
// It asks the fd for its window size — a terminal-only ioctl — instead of
// checking for a character device. The distinction matters here: /dev/null is a
// character device, and it is precisely what stdin becomes when catctl itself
// is run with stdin unset. Handing that to a build step as "a terminal" would
// invite the step to prompt into something that answers instantly with EOF.
func hostHasTerminal() bool {
	_, _, err := pty.Getsize(os.Stdin)
	return err == nil
}

// runStep runs one argv in dir with output streamed to out, with no stdin —
// used for the host's own git invocations (see runBuildStep).
func runStep(dir string, argv []string, out io.Writer) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	if out != nil {
		cmd.Stdout, cmd.Stderr = out, out
	}
	return cmd.Run()
}
