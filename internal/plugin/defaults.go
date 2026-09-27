package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Default plugins: the ones a fresh cats install gets without being asked.
//
// They stay ordinary plugins. The seed runs the same install `catctl plugin
// install` does, so the result is a normal clone under the plugins root, with
// update checks, uninstall, and nothing special in the dialog. What makes them
// "default" is only that cats installs them once, on a machine that has never
// had a plugin set, and never again after that. Uninstalling one is a real
// choice, and a later catway start respects it.
//
// The decision is recorded in a state file in the plugins root:
//
//	catway start ─▶ SeedDefaults
//	                  │
//	                  ├─ opted out (CATS_NO_DEFAULT_PLUGINS) ─────────────▶ nothing
//	                  │
//	                  ├─ state file present ─▶ retry whatever is still pending
//	                  │
//	                  └─ no state file
//	                       ├─ plugins root exists ─▶ existing install:
//	                       │                         record "nothing pending"
//	                       └─ no plugins root ────▶ fresh install:
//	                                                record every default pending,
//	                                                then install them
//
// Why the plugins root is the signal. It exists once anything has been
// installed or linked, so its absence means "this user has never had a plugin
// set". An upgrading user who once uninstalled cats-todo still has the root,
// and does not get it back. The state file is written *before* the first
// attempt because Install itself creates the root. Without the file, a seed
// that failed offline would leave a root behind, and the next start would
// mistake it for an existing install and never retry.

// DefaultPlugin is one plugin a fresh install seeds.
type DefaultPlugin struct {
	// ID is the manifest id, i.e. the install directory name. Knowing it up
	// front lets the seeder see that the plugin is already present without
	// cloning anything. Install only learns the id after the clone.
	ID string
	// Source is what `catctl plugin install` would be given.
	Source string
}

// Defaults is the seed set. Unpinned on purpose: a fresh install should get
// the current release, the same thing the README's install line gives.
var Defaults = []DefaultPlugin{
	{ID: "rohanthewiz.cats-todo", Source: "rohanthewiz/cats-todo"},
}

// NoDefaultsEnvVar opts a machine out of seeding when set to anything
// non-empty. It is for scripted or managed setups that want an empty plugin
// set, and for scratch servers that should not reach GitHub on start.
const NoDefaultsEnvVar = "CATS_NO_DEFAULT_PLUGINS"

// defaultsStateName is the seed's state file in the plugins root. The dot
// prefix keeps it out of List, like the install temp dirs.
const defaultsStateName = ".cats-defaults.json"

// maxSeedAttempts caps retries of one default across catway starts. A failure
// is usually transient (offline on first launch), so it is retried on the next
// start. But one that never succeeds, such as a machine with no Go toolchain
// for cats-todo's build, must not clone on every start forever. After the last
// attempt the plugin is dropped from the pending list and the user can install
// it by hand.
const maxSeedAttempts = 3

// defaultsState is the state file's shape. An empty Pending list means the
// seed is finished for good, whether by success, by giving up, or because the
// machine was judged an existing install.
//
// Failed holds the defaults the seed gave up on. The seed itself never reads
// it again; it is kept so the plugins dialog can tell the user why a default
// is missing (FailedDefaults). Without it, the one user who most needs the
// explanation, the fresh install with no Go toolchain, would find the reason
// only in daemons.log. An entry leaves Failed when the user dismisses it
// (DismissDefault) or installs and later uninstalls the plugin (Uninstall
// calls forgetDefault).
type defaultsState struct {
	Pending []pendingDefault `json:"pending"`
	Failed  []pendingDefault `json:"failed,omitempty"`
}

type pendingDefault struct {
	DefaultPlugin
	Attempts  int    `json:"attempts,omitempty"`
	LastError string `json:"last_error,omitempty"`
	// LastOutput is the tail of the failed attempt's clone/build output. The
	// error alone often names only the step ("build step 1 (sh -c …): exit
	// status 127"), while the line that explains it ("go: not found") is in
	// the output.
	LastOutput string `json:"last_output,omitempty"`
}

// defaultsMu serializes read-modify-write passes over the state file within
// one process. The seed runs on its own goroutine at catway start and holds
// the lock for its whole pass (installs included, up to a minute), so a
// dismiss sent from the dialog meanwhile waits rather than having its write
// overwritten by the seed's final one. Readers (FailedDefaults) take no lock:
// writes are temp-file + rename, so a read sees one whole version or the
// other.
//
// catctl is a separate process and is not covered. The only thing it writes
// is forgetDefault on uninstall, and a lost write there costs at most one
// stale notice, which the presence filter in FailedDefaults mostly hides.
var defaultsMu sync.Mutex

// seedOutputTailLines bounds LastOutput. A notice line and its tooltip have
// room for a few lines, and the state file should not carry a whole build log.
const seedOutputTailLines = 6

// SeedOutcome says what happened to one default on this pass, for the
// caller's log.
type SeedOutcome struct {
	Source    string
	Installed bool   // installed on this pass
	Present   bool   // already there (installed some other way); nothing done
	GaveUp    bool   // this failure was the last attempt
	Attempt   int    // which attempt this was (1-based) when an install ran
	Err       error  // the install failure, if any
	Version   string // installed version, when Installed
}

// SeedDefaults runs one seeding pass: it decides whether this machine is a
// fresh install (see the diagram above) and installs whatever defaults are
// still pending. It returns one outcome per default it acted on. A nil slice
// means nothing was due.
//
// The installs are headless (see InstallHeadless), because the caller is a
// daemon and no one is watching. out receives clone and build
// output. Pass a buffer and log its tail on failure.
//
// Safe to call on every catway start, since a finished seed costs one small
// file read. It is not safe to run concurrently with itself; catway calls it
// once per process.
func SeedDefaults(defaults []DefaultPlugin, out io.Writer) ([]SeedOutcome, error) {
	if os.Getenv(NoDefaultsEnvVar) != "" {
		return nil, nil
	}
	root, err := Root()
	if err != nil {
		return nil, err
	}
	statePath := filepath.Join(root, defaultsStateName)

	defaultsMu.Lock()
	defer defaultsMu.Unlock()

	st, err := readDefaultsState(statePath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if _, rerr := os.Lstat(root); rerr == nil {
			// Existing install. Record the verdict so this check never runs
			// again, even if the root is later emptied.
			return nil, writeDefaultsState(statePath, defaultsState{})
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, err
		}
		for _, d := range defaults {
			st.Pending = append(st.Pending, pendingDefault{DefaultPlugin: d})
		}
		if err := writeDefaultsState(statePath, st); err != nil {
			return nil, err
		}
	case err != nil:
		// A corrupt state file is not a reason to re-seed. Treating it as
		// "fresh" could reinstall something the user removed. Report it and
		// leave the file for a human.
		return nil, fmt.Errorf("default plugins: %s: %w", statePath, err)
	}

	var outcomes []SeedOutcome
	var still []pendingDefault
	for _, p := range st.Pending {
		// Already present: installed by hand, by a peer sync, or by an earlier
		// pass that crashed after the rename but before the state write. Any
		// entry, even a broken one, occupies the id, and Install would refuse
		// it anyway.
		if _, err := os.Lstat(filepath.Join(root, p.ID)); err == nil {
			outcomes = append(outcomes, SeedOutcome{Source: p.Source, Present: true})
			continue
		}

		// The attempt's output goes to the caller as before, and a copy is kept
		// here so a failure can record its tail (LastOutput).
		var attemptOut bytes.Buffer
		w := io.Writer(&attemptOut)
		if out != nil {
			w = io.MultiWriter(out, &attemptOut)
		}

		p.Attempts++
		inst, err := InstallHeadless(p.Source, "", w)
		o := SeedOutcome{Source: p.Source, Attempt: p.Attempts, Err: err}
		if err == nil {
			o.Installed, o.Version = true, inst.Version
			outcomes = append(outcomes, o)
			continue
		}
		p.LastError = err.Error()
		p.LastOutput = tailLines(attemptOut.String(), seedOutputTailLines)
		if p.Attempts >= maxSeedAttempts {
			o.GaveUp = true
			st.Failed = append(dropDefault(st.Failed, p.ID), p)
		} else {
			still = append(still, p)
		}
		outcomes = append(outcomes, o)
	}

	st.Pending = still
	return outcomes, writeDefaultsState(statePath, st)
}

// FailedDefault is a default plugin the seed tried and failed to install,
// for the plugins dialog and `catctl plugin list`.
type FailedDefault struct {
	ID       string
	Source   string
	Attempts int
	// GaveUp is false while the seed will still retry on a later catway start,
	// true once it has stopped trying.
	GaveUp bool
	Err    string // the last attempt's error
	Output string // the tail of the last attempt's clone/build output
}

// FailedDefaults lists the defaults whose seeding failed and that are still
// not installed: both the ones pending a retry (after at least one failed
// attempt) and the ones the seed gave up on. A default that is present, by
// whatever route it got there, is left out, since there is nothing left to
// explain. No state file, or the opt-out, means an empty answer.
//
// It only reads. It takes no lock and is cheap enough to run on every
// plugin.list.
func FailedDefaults() ([]FailedDefault, error) {
	if os.Getenv(NoDefaultsEnvVar) != "" {
		return nil, nil
	}
	root, err := Root()
	if err != nil {
		return nil, err
	}
	st, err := readDefaultsState(filepath.Join(root, defaultsStateName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var res []FailedDefault
	add := func(p pendingDefault, gaveUp bool) {
		if p.Attempts == 0 {
			return // planned but never tried, e.g. the first pass is still running
		}
		if _, err := os.Lstat(filepath.Join(root, p.ID)); err == nil {
			return
		}
		res = append(res, FailedDefault{
			ID: p.ID, Source: p.Source, Attempts: p.Attempts, GaveUp: gaveUp,
			Err: p.LastError, Output: p.LastOutput,
		})
	}
	for _, p := range st.Pending {
		add(p, false)
	}
	for _, p := range st.Failed {
		add(p, true)
	}
	return res, nil
}

// DismissDefault drops a default from the seed's state, pending or given up.
// It is the "I don't want this one" answer to a failure notice: the notice
// goes away and, if the seed was still going to retry, it no longer will.
// Dismissing an id the state does not hold is not an error, so a second
// window's dismiss of the same notice succeeds quietly.
func DismissDefault(id string) error {
	root, err := Root()
	if err != nil {
		return err
	}
	defaultsMu.Lock()
	defer defaultsMu.Unlock()
	return forgetDefaultLocked(filepath.Join(root, defaultsStateName), id)
}

// forgetDefault is DismissDefault for Uninstall. Uninstalling a default is
// the same choice as dismissing it, and it closes a gap: a default installed
// by hand while still pending, then uninstalled before the next catway start,
// would otherwise be seeded straight back in.
func forgetDefault(root, id string) error {
	defaultsMu.Lock()
	defer defaultsMu.Unlock()
	return forgetDefaultLocked(filepath.Join(root, defaultsStateName), id)
}

func forgetDefaultLocked(statePath, id string) error {
	st, err := readDefaultsState(statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil // never seeded here; nothing to forget
	}
	if err != nil {
		return fmt.Errorf("default plugins: %s: %w", statePath, err)
	}
	pending, failed := dropDefault(st.Pending, id), dropDefault(st.Failed, id)
	if len(pending) == len(st.Pending) && len(failed) == len(st.Failed) {
		return nil // not a default this machine was seeding; leave the file alone
	}
	st.Pending, st.Failed = pending, failed
	return writeDefaultsState(statePath, st)
}

// dropDefault returns ps without the entry for id. It builds a new slice so
// the caller can compare lengths to learn whether anything was removed.
func dropDefault(ps []pendingDefault, id string) []pendingDefault {
	var out []pendingDefault
	for _, p := range ps {
		if p.ID != id {
			out = append(out, p)
		}
	}
	return out
}

// tailLines keeps the last n lines of s, with surrounding blank space trimmed.
func tailLines(s string, n int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func readDefaultsState(path string) (defaultsState, error) {
	var st defaultsState
	b, err := os.ReadFile(path)
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

// writeDefaultsState writes via temp file + rename, so a crash mid-write
// leaves the old state rather than a truncated file. A truncated file would
// read as corrupt and stall the seed (see SeedDefaults).
func writeDefaultsState(path string, st defaultsState) error {
	if st.Pending == nil {
		st.Pending = []pendingDefault{} // "pending": [] reads plainer than null
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
