package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
type defaultsState struct {
	Pending []pendingDefault `json:"pending"`
}

type pendingDefault struct {
	DefaultPlugin
	Attempts  int    `json:"attempts,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

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

		p.Attempts++
		inst, err := InstallHeadless(p.Source, "", out)
		o := SeedOutcome{Source: p.Source, Attempt: p.Attempts, Err: err}
		if err == nil {
			o.Installed, o.Version = true, inst.Version
		} else if p.Attempts >= maxSeedAttempts {
			o.GaveUp = true
		} else {
			p.LastError = err.Error()
			still = append(still, p)
		}
		outcomes = append(outcomes, o)
	}

	st.Pending = still
	return outcomes, writeDefaultsState(statePath, st)
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
