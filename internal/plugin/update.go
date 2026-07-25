package plugin

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Update refreshes an installed plugin in place and reports whether anything
// changed. This is the payoff for Install keeping .git and writing the
// provenance JSON: the recorded origin already knows where to fetch from, and
// the recorded ref knows what to stay pinned to ("" = the remote's default
// branch, addressed as the remote's HEAD so it never needs resolving locally).
//
// fetch --depth 1 + reset --hard FETCH_HEAD is used instead of git pull on
// purpose: pull breaks on the detached HEAD a tag-pinned clone leaves behind,
// and a plain merge would trip over force-pushed release branches. Fetching
// the ref and hard-resetting to it handles branches, tags, and rewritten
// history identically — the installed copy is a cache of upstream, not a
// working tree anyone edits, so discarding local state is correct.
//
// When the fetched commit equals the current one the build is skipped and
// updated=false — re-running builds on a no-op update would make a periodic
// "update everything" loop needlessly expensive.
func Update(id string, out io.Writer) (inst Installed, updated bool, err error) {
	if _, err := exec.LookPath("git"); err != nil {
		return Installed{}, false, fmt.Errorf("git is required to update plugins: %w", err)
	}
	inst, err = Get(id)
	if err != nil {
		return Installed{}, false, err
	}
	// A linked plugin's directory is the developer's own checkout — resetting
	// it to upstream could destroy their uncommitted work, so update refuses.
	if inst.Linked {
		return Installed{}, false, fmt.Errorf(
			"plugin %q is linked — update the checkout at %s yourself, then re-run catctl plugin link to rebuild", id, inst.Dir)
	}
	if _, err := os.Stat(filepath.Join(inst.Dir, ".git")); err != nil {
		return Installed{}, false, fmt.Errorf("plugin %q has no git history to update from (reinstall it)", id)
	}

	oldSha, err := gitHead(inst.Dir)
	if err != nil {
		return Installed{}, false, err
	}

	refspec := inst.Ref
	if refspec == "" {
		refspec = "HEAD" // the remote's default branch, whatever it is named
	}
	if err := runStep(inst.Dir, []string{"git", "fetch", "--depth", "1", "origin", refspec}, out); err != nil {
		return Installed{}, false, fmt.Errorf("fetch %s for %s: %w", refspec, id, err)
	}
	if err := runStep(inst.Dir, []string{"git", "reset", "--hard", "FETCH_HEAD"}, out); err != nil {
		return Installed{}, false, fmt.Errorf("reset to fetched %s: %w", refspec, err)
	}

	newSha, err := gitHead(inst.Dir)
	if err != nil {
		return Installed{}, false, err
	}
	if newSha == oldSha {
		return inst, false, nil
	}

	// The refreshed tree gets the same scrutiny a fresh install would; any
	// failure rolls the tree back to oldSha so a bad upstream release cannot
	// leave the installed copy broken. Build artifacts are untracked, so the
	// previous version's binaries survive both the reset forward and back.
	rollback := func(cause error) (Installed, bool, error) {
		if rbErr := runStep(inst.Dir, []string{"git", "reset", "--hard", oldSha}, out); rbErr != nil {
			return Installed{}, false, fmt.Errorf("%w (and rollback to %s failed: %v — reinstall the plugin)", cause, oldSha, rbErr)
		}
		return Installed{}, false, fmt.Errorf("%w (previous version restored)", cause)
	}

	m, err := LoadManifest(inst.Dir)
	if err != nil {
		return rollback(fmt.Errorf("updated manifest is invalid: %w", err))
	}
	// The install dir is keyed by id; an upstream id change would make the
	// entry lie about what it contains, so it is a refusal, not a migration.
	if m.ID != id {
		return rollback(fmt.Errorf("update changed the plugin id from %q to %q — uninstall and reinstall instead", id, m.ID))
	}
	if !m.currentPlatformSupported() {
		return rollback(fmt.Errorf("updated plugin %s no longer supports this platform (supports: %s)",
			id, strings.Join(m.Platforms, ", ")))
	}
	if err := runBuild(inst.Dir, m.Build, out); err != nil {
		return rollback(err)
	}

	fresh, err := Get(id)
	if err != nil {
		return Installed{}, false, err
	}
	return fresh, true, nil
}

// gitHead returns dir's current commit sha. Unlike runStep (which streams),
// this captures output — the sha is data the updater compares, not progress
// the user watches.
func gitHead(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = dir
	b, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("rev-parse HEAD in %s: %w", dir, err)
	}
	return strings.TrimSpace(string(b)), nil
}
