package worktree

import (
	"os"
	"path/filepath"
)

// Operations: the whole worktree feature expressed as one function over a
// request, because the same operation now runs in two processes.
//
// git is a subprocess acting on a filesystem, so a worktree command has to run
// on the machine whose disk holds the checkout. For a pane on this catway's own
// box that can be catway itself; for a pane on another machine it must be that
// machine's cathost. Both ends therefore need the same sequence of git
// invocations — resolve the repo root, list the checkouts, derive the default
// path, add or remove — and if each end assembled its own sequence they would
// agree until the day one of them was fixed alone. So the sequence lives here,
// the seam carries the request and the result, and the only difference between
// a local and a remote worktree command is which process calls Do.
//
// Everything a path means is resolved here too, and that is the same argument:
// "~" is the home of the user the *daemon* runs as, and whether a path is a
// directory at all is a question only the kernel that owns the disk can answer.
// A path that arrives unexpanded is deliberate, not an oversight.

// Op names one operation. It travels as a string so an unknown op from a newer
// client is a legible error rather than a silently wrong action.
type Op string

const (
	// OpList resolves Cwd's checkout and lists the repository's worktrees.
	OpList Op = "list"
	// OpCreate adds a new branch + checkout (`git worktree add -b`).
	OpCreate Op = "create"
	// OpRemove deletes a checkout folder (`git worktree remove`), never a branch.
	OpRemove Op = "remove"
	// OpStat asks whether Path is a directory on this machine — what worktree.open
	// needs before rooting a workspace there, and the one question about another
	// machine's disk that cannot be guessed.
	OpStat Op = "stat"
)

// OpRequest is one worktree operation. The fields are a union across the ops
// (Cwd anchors list/create, Path addresses remove/stat) rather than four
// message types, because the seam carries one request and one result and the op
// discriminates them — the same shape a control-API method has.
//
// Root is the *configured* worktree directory, unexpanded: it is expanded by
// whoever runs the operation, so the default "~/.cats/worktrees" means the home
// of the account the checkout will belong to.
type OpRequest struct {
	Op     Op     `json:"op"`
	Cwd    string `json:"cwd,omitempty"`    // list, create: the pane's directory
	Path   string `json:"path,omitempty"`   // create (explicit), remove, stat
	Branch string `json:"branch,omitempty"` // create
	Root   string `json:"root,omitempty"`   // create, list: the worktree root
	Force  bool   `json:"force,omitempty"`  // remove
}

// OpResult is the answer. Error is the failure text (git's stderr, as the
// dialogs show it) rather than a Go error, because it crosses a wire; an empty
// Error is success.
//
// Dirty is separate from Error because refusing to delete a checkout with
// uncommitted work is not a failure of the command — it is the point at which
// the front end escalates to a "delete anyway" confirm. The caller decides how
// to say that; this end only reports which refusal it was.
type OpResult struct {
	Error string `json:"error,omitempty"`
	Dirty bool   `json:"dirty,omitempty"`

	Checkout string  `json:"checkout,omitempty"` // list: the anchor's own checkout
	Root     string  `json:"root,omitempty"`     // list: Root, expanded here
	Entries  []Entry `json:"entries,omitempty"`  // list
	Path     string  `json:"path,omitempty"`     // create/remove/stat: the resolved path
	IsDir    bool    `json:"is_dir,omitempty"`   // stat
}

// Do runs one operation on this machine's disk and never returns a Go error:
// every failure is text for a human, because the caller may be a process on
// another box that can do nothing with a typed error but print it.
//
// Callers must run it off any goroutine that must stay responsive — git can
// take as long as a checkout takes.
func Do(req OpRequest) OpResult {
	switch req.Op {
	case OpList:
		return doList(req)
	case OpCreate:
		return doCreate(req)
	case OpRemove:
		return doRemove(req)
	case OpStat:
		return doStat(req)
	}
	return OpResult{Error: "unknown worktree operation " + string(req.Op)}
}

// doList resolves the anchor directory's checkout and lists the repository. The
// anchor's own checkout is reported separately from the entries because it is
// what marks the "current" row, and a linked worktree's root is not the main
// repository's.
func doList(req OpRequest) OpResult {
	checkout, err := RepoRoot(req.Cwd)
	if err != nil {
		return OpResult{Error: "not a git worktree: " + err.Error()}
	}
	entries, err := List(checkout)
	if err != nil {
		return OpResult{Error: err.Error()}
	}
	return OpResult{Checkout: checkout, Entries: entries, Root: ExpandTilde(req.Root)}
}

// doCreate adds a branch and its checkout. The default path keys on the *main*
// repository's folder name, which the porcelain list resolves even when the
// anchor is itself a linked worktree.
func doCreate(req OpRequest) OpResult {
	if req.Branch == "" {
		// The caller names the branch — it is the name the workspace takes and
		// the value the command reports back, so generating it here would make
		// the answer depend on which machine ran the op.
		return OpResult{Error: "worktree create: a branch name is required"}
	}
	src, err := RepoRoot(req.Cwd)
	if err != nil {
		return OpResult{Error: "not a git worktree: " + err.Error()}
	}
	path := ExpandTilde(req.Path)
	if path == "" {
		entries, err := List(src)
		if err != nil {
			return OpResult{Error: err.Error()}
		}
		path = DefaultCheckoutPath(ExpandTilde(req.Root), filepath.Base(MainPath(entries, src)), req.Branch)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return OpResult{Error: err.Error()}
	}
	if err := Run(AddCommand(src, path, req.Branch, "HEAD")); err != nil {
		return OpResult{Error: err.Error()}
	}
	return OpResult{Path: path}
}

// doRemove deletes a checkout folder. It runs from the *main* worktree because
// git refuses to remove the checkout it is running inside of.
func doRemove(req OpRequest) OpResult {
	path := ExpandTilde(req.Path)
	if path == "" {
		return OpResult{Error: "worktree remove: a checkout path is required"}
	}
	entries, err := List(path)
	if err != nil {
		return OpResult{Error: err.Error()}
	}
	if err := Run(RemoveCommand(MainPath(entries, path), path, req.Force)); err != nil {
		msg := err.Error()
		return OpResult{Path: path, Error: msg, Dirty: IsDirtyRemoveError(msg)}
	}
	return OpResult{Path: path}
}

// doStat answers whether a path is a directory here. The resolved path comes
// back with the answer so the caller roots its workspace on what was actually
// checked rather than on the "~/…" it sent.
func doStat(req OpRequest) OpResult {
	path := ExpandTilde(req.Path)
	if st, err := os.Stat(path); err != nil || !st.IsDir() {
		return OpResult{Path: path, Error: "worktree path is not a directory: " + path}
	}
	return OpResult{Path: path, IsDir: true}
}
