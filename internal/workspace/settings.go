package workspace

import (
	"fmt"
	"strings"
)

// Settings are a workspace's user-chosen preferences: knobs that change how
// cats treats the project behind the workspace, as opposed to the workspace's
// own state (its name, lock, flag, sleep). The zero value is "every setting at
// its default", and that is what every workspace created before settings
// existed restores to.
//
// One struct rather than more loose fields on Workspace because the settings
// travel as a group — one dialog edits them, one command patches them, one
// snapshot key holds them — and a new setting should cost one field here, not
// a field plus a setter plus a snapshot line plus a restore line.
//
// Each field's zero value must mean "the default", so that an unset setting
// never has to be told apart from one set to the default. That is also what
// lets the snapshot omit the whole block for an untouched workspace.
type Settings struct {
	// DevBranch is the project's development (trunk) branch: the branch the
	// sidebar's sync dot compares against its remote. "" = automatic, which
	// is main, or master where that is still the name (see gitsync.Resolve).
	//
	// Set it for a project whose shared branch is called something else —
	// develop, trunk, a release line — where the automatic guess would compare
	// the wrong branch, or find none and leave the dot uncoloured.
	DevBranch string `json:"dev_branch,omitempty"`
}

// IsZero reports whether every setting is at its default. It is what
// encoding/json's omitzero consults, so an untouched workspace snapshots
// byte-identically to one written before settings existed.
func (s Settings) IsZero() bool { return s == Settings{} }

// SetSettings replaces the workspace's settings wholesale. Validation is the
// caller's (NormalizeDevBranch), since the session is the layer that turns a
// command's patch into a full Settings value and refuses a bad one.
func (w *Workspace) SetSettings(s Settings) {
	w.Settings = s
}

// maxBranchLen bounds a configured branch name. Git itself has no hard limit,
// but a ref is a file path on most backends, and a name longer than a path
// component is a paste accident rather than a branch.
const maxBranchLen = 255

// NormalizeDevBranch trims a user-supplied branch name and checks it against
// git's ref-name rules, returning the cleaned name ("" stays "", meaning
// automatic).
//
// The rules are those of `git check-ref-format --branch`, re-stated here
// rather than forked out to: the check runs on the command path, inside the
// loop goroutine, and a subprocess there would stall every window for the
// length of a fork. They matter for more than tidiness — the name is spliced
// into "refs/heads/<name>" for for-each-ref and ls-remote, and a name that git
// would refuse as a branch could otherwise be read as a pattern or an option
// by one of those commands.
//
// A leading "refs/heads/" is accepted and stripped, since it is the form a
// user copying from `git for-each-ref` output would paste.
func NormalizeDevBranch(name string) (string, error) {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "refs/heads/")
	if name == "" {
		return "", nil
	}
	bad := func(why string) (string, error) {
		return "", fmt.Errorf("invalid branch name %q: %s", name, why)
	}
	if len(name) > maxBranchLen {
		return bad(fmt.Sprintf("longer than %d bytes", maxBranchLen))
	}
	// Character rules first: a single bad byte is the most common mistake (a
	// space in a pasted name) and the clearest message.
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			return bad("contains a control character")
		case r == ' ':
			return bad("contains a space")
		case strings.ContainsRune(`~^:?*[\`, r):
			return bad(fmt.Sprintf("contains %q", r))
		}
	}
	// Structural rules, in the order git documents them.
	switch {
	case strings.HasPrefix(name, "-"):
		// The one with teeth: a leading dash is how an option is spelled.
		return bad("starts with '-'")
	case name == "@":
		return bad("is '@'")
	case strings.Contains(name, ".."):
		return bad("contains '..'")
	case strings.Contains(name, "@{"):
		return bad("contains '@{'")
	case strings.Contains(name, "//"):
		return bad("contains '//'")
	case strings.HasPrefix(name, "/"), strings.HasSuffix(name, "/"):
		return bad("starts or ends with '/'")
	case strings.HasSuffix(name, "."):
		return bad("ends with '.'")
	}
	// Per-component rules: each slash-separated part is a path component of
	// the ref file, so git forbids the forms a filesystem would treat
	// specially.
	for part := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(part, ".") {
			return bad("has a component starting with '.'")
		}
		if strings.HasSuffix(part, ".lock") {
			return bad("has a component ending in '.lock'")
		}
	}
	return name, nil
}
