package plugin

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/rohanthewiz/cats/wire"
)

// Update-check outcomes. They ARE the wire statuses (plugin.check_updates →
// PluginUpdateInfo.Status), aliased rather than redeclared so the host and the
// protocol cannot drift into two spellings of the same verdict.
const (
	// UpdateAvailable: upstream's ref points at a different commit than the
	// installed one. Deliberately "different", not "newer": a force-pushed
	// branch or a re-tagged release is still something `plugin update` would
	// land, and that is the question the check answers.
	UpdateAvailable = wire.PluginUpdateAvailable
	// UpToDate: upstream's ref resolves to the installed commit.
	UpToDate = wire.PluginUpdateCurrent
	// UpdateSkipped: there is nothing to compare against — a linked checkout
	// (the developer's own tree, which update refuses by design), a broken
	// entry, or an install with no git history. Not an error: the dialog shows
	// no badge for these rather than a warning.
	UpdateSkipped = wire.PluginUpdateSkipped
	// UpdateCheckFailed: the remote could not be asked (offline, auth, a
	// renamed repo). Reported per plugin so one unreachable remote never hides
	// the result for the rest.
	UpdateCheckFailed = wire.PluginUpdateError
)

// UpdateCheck is the answer for one plugin. SHAs are full; shortening is a
// display concern left to the caller. RemoteVersion is best effort — it is
// read from the upstream manifest only when the commits differ, and stays ""
// if that read fails (the "available" verdict never depends on it).
type UpdateCheck struct {
	ID            string
	Status        string
	Reason        string // why skipped / what failed; "" otherwise
	LocalSHA      string
	RemoteSHA     string
	LocalVersion  string
	RemoteVersion string
	// RemoteSubject is the upstream commit's subject line — what the update
	// would bring, in the author's words. Best effort, like RemoteVersion, and
	// read in the same step.
	RemoteSubject string
}

// CheckUpdate asks inst's recorded origin whether `plugin update` would change
// anything, without changing anything itself. It is the read-only twin of
// Update and follows the same addressing: the recorded ref if pinned, the
// remote's HEAD (default branch) otherwise.
//
// Two steps, cheapest first:
//
//  1. git ls-remote origin <ref>   → remote sha      (one round trip, no objects)
//     equal to local HEAD?          → UpToDate, done
//  2. git fetch --depth 1 origin <ref> --no-write-fetch-head
//     git show <remote sha>:cats-plugin.toml → RemoteVersion
//
// Step 2 exists purely so the dialog can say "v0.3.0 → v0.4.0 · fix the
// thing" instead of an anonymous "update available". It downloads the one upstream commit's objects
// into the clone's object store — the same objects the eventual Update fetch
// needs, so it is not wasted work — but touches no ref, no index and no
// working-tree file, and --no-write-fetch-head keeps it from clobbering the
// FETCH_HEAD a concurrent Update may be about to reset to. The installed copy
// is therefore byte-for-byte what it was before the check.
//
// ctx bounds the whole check: remotes hang (a captive portal, a dead VPN), and
// a dialog waiting on a check must get an answer, even a failed one.
func CheckUpdate(ctx context.Context, inst Installed) UpdateCheck {
	res := UpdateCheck{ID: inst.ID, LocalVersion: inst.Version}
	switch {
	case inst.Err != nil:
		res.Status, res.Reason = UpdateSkipped, "broken"
		return res
	case inst.Linked:
		res.Status, res.Reason = UpdateSkipped, "linked checkout"
		return res
	}
	if _, err := os.Stat(filepath.Join(inst.Dir, ".git")); err != nil {
		res.Status, res.Reason = UpdateSkipped, "no git history"
		return res
	}
	fail := func(err error) UpdateCheck {
		res.Status, res.Reason = UpdateCheckFailed, err.Error()
		return res
	}

	local, err := gitHead(inst.Dir)
	if err != nil {
		return fail(err)
	}
	res.LocalSHA = local

	remote, err := remoteRefSHA(ctx, inst.Dir, inst.Ref)
	if err != nil {
		return fail(err)
	}
	res.RemoteSHA = remote
	if remote == local {
		res.Status = UpToDate
		return res
	}

	res.Status = UpdateAvailable
	res.RemoteVersion, res.RemoteSubject = remoteCommitInfo(ctx, inst.Dir, inst.Ref, remote)
	return res
}

// remoteRefSHA resolves ref on origin to the COMMIT sha it points at, which is
// what gitHead reports for the installed copy and so what it must be compared
// against.
//
// ls-remote patterns are tail-matched ("main" would also match
// refs/heads/feature/main), so the output is filtered to exact refnames here
// rather than trusting the first line. Annotated tags need care: the tag ref
// itself names the tag OBJECT, and only the peeled "<tag>^{}" line names the
// commit a clone of that tag checks out — so the peeled line wins when present.
// Lightweight tags and branches have no peeled line and use their own.
func remoteRefSHA(ctx context.Context, dir, ref string) (string, error) {
	patterns := []string{"HEAD"}
	want := []string{"HEAD"}
	if ref != "" {
		patterns = []string{ref, ref + "^{}"}
		// Preference order, most specific first. A full "refs/…" ref (rare —
		// install documents branch|tag) is honored as given.
		want = []string{
			"refs/tags/" + ref + "^{}",
			"refs/tags/" + ref,
			"refs/heads/" + ref,
			ref + "^{}",
			ref,
		}
	}
	out, err := gitQuiet(ctx, dir, append([]string{"ls-remote", "origin"}, patterns...)...)
	if err != nil {
		return "", fmt.Errorf("ls-remote: %w", err)
	}
	refs := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		sha, name, ok := strings.Cut(line, "\t")
		if ok {
			refs[strings.TrimSpace(name)] = strings.TrimSpace(sha)
		}
	}
	for _, w := range want {
		if sha, ok := refs[w]; ok && sha != "" {
			return sha, nil
		}
	}
	if ref == "" {
		return "", fmt.Errorf("origin reports no HEAD")
	}
	return "", fmt.Errorf("origin has no branch or tag %q", ref)
}

// remoteCommitInfo fetches the upstream commit (see CheckUpdate for why this
// is safe on the installed clone) and reads the version out of its manifest
// plus its subject line. Any failure yields "" — both are decoration on a
// verdict already reached, so a flaky fetch must not turn "available" into
// "error".
//
// The manifest is decoded loosely (just the version key) rather than through
// LoadManifest: validation is Update's job when it actually lands the tree,
// and a check should not refuse to report a version because upstream added a
// manifest key this build does not know yet.
func remoteCommitInfo(ctx context.Context, dir, ref, sha string) (version, subject string) {
	refspec := ref
	if refspec == "" {
		refspec = "HEAD"
	}
	if _, err := gitQuiet(ctx, dir, "fetch", "--quiet", "--depth", "1", "--no-write-fetch-head", "origin", refspec); err != nil {
		return "", ""
	}
	if b, err := gitQuiet(ctx, dir, "log", "-1", "--format=%s", sha); err == nil {
		subject = strings.TrimSpace(string(b))
	}
	b, err := gitQuiet(ctx, dir, "show", sha+":"+ManifestName)
	if err != nil {
		return "", subject
	}
	var m struct {
		Version string `toml:"version"`
	}
	if toml.Unmarshal(b, &m) != nil {
		return "", subject
	}
	return m.Version, subject
}

// gitQuiet runs git in dir and returns stdout, folding stderr into the error.
// Unlike runStep it never streams — a check is data, not progress — and it
// disables every way git could stop to ask a question: no stdin, no terminal
// prompt, no askpass helper. A private repo whose credentials are not cached
// must fail fast here, because a check runs in the background of a dialog
// where nobody could ever answer the prompt.
func gitQuiet(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=")
	// BatchMode stops ssh remotes prompting for a passphrase or host-key
	// confirmation, for the same reason. A user who already set their own
	// GIT_SSH_COMMAND keeps it: their wrapper is presumably what makes the
	// remote reachable at all, and a check that broke it would only ever fail.
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	// A killed git can leave its ssh child holding the stdout pipe open, and
	// Output would then wait on that pipe long past the deadline. WaitDelay
	// caps the wait after the context fires.
	cmd.WaitDelay = 2 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("timed out")
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			// git's last stderr line is the one that says what went wrong
			// ("fatal: repository … not found"); earlier ones are hints.
			lines := strings.Split(msg, "\n")
			return nil, fmt.Errorf("%s", strings.TrimSpace(lines[len(lines)-1]))
		}
		return nil, err
	}
	return out, nil
}
