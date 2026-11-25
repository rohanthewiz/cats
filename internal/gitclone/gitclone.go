// Package gitclone turns "a repository someone typed" into a checkout on disk:
// the spelling rules for a repo spec (Resolve) and the non-interactive clone
// itself (Clone). It backs workspace.create's Repo field — a workspace opened
// on a project that is not on this machine yet.
package gitclone

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ghSegment is one half of a GitHub "owner/repo" shorthand. Deliberately the
// character set GitHub itself allows, so a filesystem path ("./x", "~/x",
// "a b/c") can never be mistaken for one.
var ghSegment = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

// Resolve turns a typed repo spec into the URL git clones and the directory
// name the checkout takes (git's own "humanish" name: the last path segment,
// minus ".git").
//
// The convenience is GitHub: a bare "owner/repo" becomes
// https://github.com/owner/repo.git, and "github.com/owner/repo" — what is
// left of a URL copied without its scheme — gets the scheme back. Everything
// else passes through untouched: a URL with a scheme, an scp-style
// "git@host:owner/repo", or a filesystem path, so other forges, ssh remotes
// and local repositories keep working. A user who wants their GitHub clones
// over ssh says so once in git's own config (url.<base>.insteadOf) rather than
// in every dialog.
func Resolve(spec string) (url, name string, err error) {
	s := strings.TrimSpace(spec)
	if s == "" {
		return "", "", errors.New("no repository given")
	}
	// A leading "-" would be read by git as an option. Clone also passes "--",
	// but refusing here gives the error a name instead of git's usage text.
	if strings.HasPrefix(s, "-") {
		return "", "", fmt.Errorf("not a repository: %q", spec)
	}
	switch {
	case strings.Contains(s, "://"), isSCPLike(s):
		url = s
	case isPathLike(s):
		// No shell stands between a dialog field and git, so "~" is expanded
		// here or not at all.
		if home, herr := os.UserHomeDir(); herr == nil && (s == "~" || strings.HasPrefix(s, "~/")) {
			s = filepath.Join(home, s[1:])
		}
		url = s
	default:
		parts := strings.Split(strings.TrimSuffix(s, "/"), "/")
		if len(parts) == 3 && strings.EqualFold(parts[0], "github.com") {
			parts = parts[1:]
		}
		if len(parts) != 2 || !ghSegment.MatchString(parts[0]) || !ghSegment.MatchString(parts[1]) {
			return "", "", fmt.Errorf("not a repository: %q (want owner/repo for GitHub, or a git URL)", spec)
		}
		url = "https://github.com/" + parts[0] + "/" + strings.TrimSuffix(parts[1], ".git") + ".git"
	}
	name = humanish(url)
	if name == "" {
		return "", "", fmt.Errorf("cannot tell what to call a checkout of %q", spec)
	}
	return url, name, nil
}

// isSCPLike reports git's scp-style remote, "[user@]host:path" — a colon
// before any slash.
func isSCPLike(s string) bool {
	colon := strings.IndexByte(s, ':')
	if colon <= 0 {
		return false
	}
	slash := strings.IndexByte(s, '/')
	return slash < 0 || colon < slash
}

func isPathLike(s string) bool {
	return strings.HasPrefix(s, "/") || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "~")
}

// humanish is the directory name `git clone` would pick for a URL.
func humanish(url string) string {
	s := strings.TrimRight(url, "/")
	s = strings.TrimSuffix(s, ".git")
	s = strings.TrimRight(s, "/")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	if s == "." || s == ".." {
		return ""
	}
	return s
}

// Clone clones url into dest, which must not exist yet (an empty directory is
// allowed, as it is for git). It never stops to ask a question — no terminal
// prompt, no askpass, ssh in batch mode — because it runs behind a dialog
// where nobody could answer: a private repository whose credentials are not
// already cached fails with git's own message instead of hanging.
//
// A failed or cancelled clone leaves nothing behind when dest was created
// here; a directory that was there before is never removed.
func Clone(ctx context.Context, url, dest string) error {
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git is required to clone a repository: %w", err)
	}
	existed := false
	if entries, err := os.ReadDir(dest); err == nil {
		if len(entries) > 0 {
			return fmt.Errorf("already exists: %s", dest)
		}
		existed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		if _, serr := os.Stat(dest); serr == nil {
			return fmt.Errorf("already exists: %s", dest) // a file, not a directory
		}
		return fmt.Errorf("%s: %w", dest, err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("cannot create directory: %w", err)
	}

	cmd := exec.CommandContext(ctx, "git", "clone", "--", url, dest)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=")
	// A user's own GIT_SSH_COMMAND is presumably what makes their remote
	// reachable at all, so it is kept rather than overridden.
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	// A killed git can leave an ssh child holding the pipe open; cap the wait.
	cmd.WaitDelay = 2 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if !existed {
			_ = os.RemoveAll(dest)
		}
		if ctx.Err() != nil {
			return errors.New("clone timed out")
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			// git's last stderr line says what went wrong; earlier ones are
			// progress and hints.
			lines := strings.Split(msg, "\n")
			return errors.New(strings.TrimSpace(lines[len(lines)-1]))
		}
		return err
	}
	return nil
}
