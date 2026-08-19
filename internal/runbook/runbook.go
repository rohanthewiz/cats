// Package runbook parses and validates a runbook: a YAML document whose steps
// are §7 commands, run in order against a live cats session.
//
// The one rule that shapes everything here is that a step IS a §7 command —
// there are no runbook-only verbs, no `sleep`, no `if`. That is not minimalism
// for its own sake: it makes "what can a runbook do?" answerable without
// reading this package, because the answer is "exactly what any client holding
// the control socket can do, and nothing else". Waiting for a shell to finish
// is pane.wait_for_output; telling somebody it finished is ui.notify. A verb
// that only a runbook could use would be a second, quieter command vocabulary,
// and the §7 table would stop being the whole surface.
//
// The second rule is that everything checkable is checked at LOAD, before a
// single step runs. A runbook is a sequence of side effects on a live desktop —
// panes get split, input gets typed — so a typo in step 4 that surfaces only
// after steps 1-3 have already happened leaves the session half-changed with no
// undo. Load() therefore rejects unknown command names, missing required
// params, references to steps that do not exist or have not run yet, and
// references to undeclared vars. What survives to run time is the set of
// failures that genuinely cannot be known earlier: a pane id that was valid
// when the file was written and is not now.
package runbook

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/rohanthewiz/cats/internal/app"
)

// Runbook is one parsed, validated document.
type Runbook struct {
	// Name is the runbook's identity — what runbook.run addresses it by. It
	// comes from the file stem unless the document names itself, the same rule
	// themes use: files are the unit of storage, names the unit of identity.
	Name        string
	Description string
	// Vars are the declared parameters and their defaults. Declaring them is
	// what makes `{{ vars.branch }}` checkable at load: an undeclared var is a
	// typo, and a typo that reaches run time types the literal text
	// "{{ vars.brnach }}" into somebody's shell.
	Vars  map[string]string
	Steps []Step
	Path  string // the file it was read from, for diagnostics
}

// Step is one §7 command invocation.
type Step struct {
	// ID, when set, binds this step's RESULT under that name for later steps:
	// `id: api` makes `{{ api.pane }}` resolve to the pane the step returned.
	// Optional, because most steps are effects with nothing worth naming.
	ID  string
	Run string // a §7 command name, validated against app.CommandNames()
	// Params is the params object as written, with `{{ ... }}` references still
	// in place. Substitution happens per-run, not at load, because a reference's
	// value is a previous step's result in THIS run.
	Params map[string]any
	// Expect, when set, is a `{{ ... }}` reference that must resolve to a
	// TRUTHY value after the step has run, or the step counts as failed.
	//
	// It exists because "the command succeeded" and "the thing happened" are
	// not the same claim, and §7 is right to keep them apart:
	// pane.wait_for_output reports a TIMEOUT as a successful call returning
	// `matched: false`, because a client asked a question and got an answer.
	// A runbook, though, is a sequence — "wait for the build, then deploy" has
	// to stop when the build never finished, and without this it would sail
	// past into the deploy having noticed nothing.
	//
	// Fixing it by teaching the engine about wait_for_output's result shape was
	// the alternative, and it is worse: the engine would then know one command
	// specially, and the next result field that means "did not happen"
	// (ledger.output's `found`) would need the same edit. A step-level
	// assertion costs one field and covers the whole class.
	Expect string
	// ContinueOnError keeps the run going past a step that failed. Off by
	// default: a runbook is a sequence, and the usual reason step 5 exists is
	// that step 4 worked.
	ContinueOnError bool
	// expectRef is Expect parsed, when set.
	expectRef *ref
	// refs are the `{{ ... }}` references reachable in Params, collected at load
	// so the validator can prove each one resolves. Kept rather than re-derived
	// so a run does no discovery work.
	refs []ref
}

// doc is the on-disk shape, kept separate from Runbook so the YAML surface can
// stay forgiving (absent sections, a step written as a bare command name) while
// the in-memory type stays exact.
type doc struct {
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Vars        map[string]string `yaml:"vars"`
	Steps       []docStep         `yaml:"steps"`
}

type docStep struct {
	ID              string         `yaml:"id"`
	Run             string         `yaml:"run"`
	Params          map[string]any `yaml:"params"`
	Expect          string         `yaml:"expect"`
	ContinueOnError bool           `yaml:"continue_on_error"`
}

// maxSteps bounds one document. It is a legibility limit rather than a resource
// one — the executor is a loop over a slice — but a "runbook" of a thousand
// steps is a program, and this package deliberately is not one.
const maxSteps = 200

// Parse reads one runbook document. stem names the runbook when the document
// does not (the file's base name without its extension).
func Parse(data []byte, stem, path string) (*Runbook, error) {
	var d doc
	if err := yaml.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	rb := &Runbook{
		Name:        strings.TrimSpace(d.Name),
		Description: strings.TrimSpace(d.Description),
		Vars:        d.Vars,
		Path:        path,
	}
	if rb.Name == "" {
		rb.Name = stem
	}
	if !nameOK(rb.Name) {
		return nil, fmt.Errorf("%s: runbook name %q must be a non-empty run of letters, digits, '-' or '_'", path, rb.Name)
	}
	if len(d.Steps) == 0 {
		return nil, fmt.Errorf("%s: a runbook needs at least one step", path)
	}
	if len(d.Steps) > maxSteps {
		return nil, fmt.Errorf("%s: %d steps exceeds the %d-step limit", path, len(d.Steps), maxSteps)
	}

	// bound is the set of names a `{{ ... }}` reference may root at, growing as
	// the walk passes each step that declares an id. Checking against it in step
	// order is what makes a FORWARD reference — `{{ later.pane }}` used before
	// `later` has run — a load error rather than an empty string at run time.
	bound := map[string]bool{}
	if len(rb.Vars) > 0 {
		bound[varsRoot] = true
	}

	for i, ds := range d.Steps {
		where := fmt.Sprintf("%s: step %d", path, i+1)
		st, err := parseStep(ds, where, bound)
		if err != nil {
			return nil, err
		}
		if st.ID != "" {
			bound[st.ID] = true
		}
		rb.Steps = append(rb.Steps, st)
	}
	return rb, nil
}

// parseStep validates one step against the §7 table and the names bound so far.
func parseStep(ds docStep, where string, bound map[string]bool) (Step, error) {
	st := Step{
		ID:              strings.TrimSpace(ds.ID),
		Run:             strings.TrimSpace(ds.Run),
		Params:          ds.Params,
		Expect:          strings.TrimSpace(ds.Expect),
		ContinueOnError: ds.ContinueOnError,
	}
	if st.Run == "" {
		return st, fmt.Errorf("%s: needs a `run:` naming a command (see `catctl commands`)", where)
	}
	spec, ok := specFor(st.Run)
	if !ok {
		return st, fmt.Errorf("%s: %q is not a command; see `catctl commands`", where, st.Run)
	}
	// runbook.run is the one command a step may not be. Everything else a
	// runbook can do, a client can already do — but a runbook that runs a
	// runbook has no bound at all, and the recursion would be discovered as a
	// wedged loop goroutine rather than as a mistake in a file.
	if st.Run == app.CmdRunbookRun {
		return st, fmt.Errorf("%s: a runbook cannot run a runbook", where)
	}
	if spec.ParamsRequired && len(st.Params) == 0 {
		return st, fmt.Errorf("%s: %s requires params", where, st.Run)
	}
	// A key the params struct has no field for would be DROPPED by the decoder,
	// not rejected — see params.go. That is the quietest failure a runbook can
	// have, so it is caught here.
	var paramsType reflect.Type
	if spec.Params != nil {
		paramsType = reflect.TypeOf(spec.Params)
	}
	if err := checkParams(st.Run, paramsType, st.Params); err != nil {
		return st, fmt.Errorf("%s: %w", where, err)
	}
	if st.ID != "" {
		if !nameOK(st.ID) {
			return st, fmt.Errorf("%s: id %q must be a run of letters, digits, '-' or '_'", where, st.ID)
		}
		if st.ID == varsRoot {
			return st, fmt.Errorf("%s: id %q is reserved for the runbook's declared vars", where, varsRoot)
		}
		if bound[st.ID] {
			return st, fmt.Errorf("%s: id %q is already used by an earlier step", where, st.ID)
		}
		if spec.Result == nil {
			return st, fmt.Errorf("%s: %s returns no data, so binding it to id %q would bind nothing", where, st.Run, st.ID)
		}
	}

	refs, err := collectRefs(st.Params)
	if err != nil {
		return st, fmt.Errorf("%s: %w", where, err)
	}
	for _, rf := range refs {
		if !bound[rf.root()] {
			return st, fmt.Errorf("%s: %s refers to %q, which no earlier step binds and no var declares",
				where, rf.text, rf.root())
		}
	}
	st.refs = refs

	if st.Expect != "" {
		// An expect is checked AFTER its own step has run, so unlike the params
		// it may name this step's own id — which is the whole point, since the
		// field it is asserting on is usually this step's own result.
		if st.ID == "" {
			return st, fmt.Errorf("%s: expect needs an `id:` on the step, since it asserts on the step's own result", where)
		}
		erefs, err := parseRefs(st.Expect)
		if err != nil {
			return st, fmt.Errorf("%s: expect: %w", where, err)
		}
		if len(erefs) != 1 || strings.TrimSpace(st.Expect) != erefs[0].text {
			return st, fmt.Errorf("%s: expect must be exactly one reference, e.g. \"{{ %s.matched }}\"", where, st.ID)
		}
		root := erefs[0].root()
		if root != st.ID && !bound[root] {
			return st, fmt.Errorf("%s: expect refers to %q, which is neither this step's id nor anything bound earlier",
				where, root)
		}
		st.expectRef = &erefs[0]
	}
	return st, nil
}

// specFor looks one command up in the §7 table.
func specFor(name string) (app.CommandSpec, bool) {
	for _, s := range app.CommandSpecs() {
		if s.Name == name {
			return s, true
		}
	}
	return app.CommandSpec{}, false
}

// nameOK accepts the identifier shape used for both a runbook's name and a
// step's id: they are addressed from a CLI argument and from inside a `{{ }}`
// reference, so anything needing quoting is rejected at the door.
func nameOK(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// --- loading a directory --------------------------------------------------------

// Broken is a runbook file that would not parse. It is reported rather than
// skipped: a runbook missing from the list with no explanation is the same
// symptom as a runbook that was never written, and the two need different fixes.
type Broken struct {
	Path string
	Err  error
}

// LoadDir reads every *.yaml / *.yml in dir. A missing directory is not an
// error — most installs have no runbooks — and neither is a broken file.
func LoadDir(dir string) (books []*Runbook, broken []Broken) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			broken = append(broken, Broken{Path: path, Err: err})
			continue
		}
		stem := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		rb, err := Parse(data, stem, path)
		if err != nil {
			broken = append(broken, Broken{Path: path, Err: err})
			continue
		}
		books = append(books, rb)
	}
	return books, broken
}

// UserDir is where the user's runbooks live, resolved exactly like the theme
// and plugin directories. "" when no config home can be resolved, in which case
// a caller reports no runbooks rather than failing.
func UserDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "cats", "runbooks")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "cats", "runbooks")
}

// Set is the result of one scan of the runbook directory: the runbooks that
// parsed, by name, plus the files that did not.
//
// It is built per call rather than cached at startup. A runbook is a file the
// user edits and then immediately runs, and a cache would make "edit, run"
// silently execute the previous version — a staleness bug whose symptom is a
// correct-looking run of the wrong steps. The cost of not caching is a readdir
// and a few kilobytes of YAML per invocation, against a command a human types.
type Set struct {
	Books  []*Runbook // sorted by name
	Broken []Broken   // sorted by path
}

// Load scans dir and returns the set, name-sorted so listings are stable.
// A name claimed by two files is a conflict: the first by path wins and the
// other is reported as broken, because silently picking one would make
// `runbook run deploy` mean different things on two machines whose directories
// happen to be ordered differently.
func Load(dir string) Set {
	books, broken := LoadDir(dir)
	sort.Slice(books, func(i, j int) bool { return books[i].Path < books[j].Path })

	seen := map[string]*Runbook{}
	var kept []*Runbook
	for _, rb := range books {
		if prev, dup := seen[rb.Name]; dup {
			broken = append(broken, Broken{
				Path: rb.Path,
				Err:  fmt.Errorf("runbook name %q is already defined by %s", rb.Name, prev.Path),
			})
			continue
		}
		seen[rb.Name] = rb
		kept = append(kept, rb)
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].Name < kept[j].Name })
	sort.Slice(broken, func(i, j int) bool { return broken[i].Path < broken[j].Path })
	return Set{Books: kept, Broken: broken}
}

// Get returns the runbook with the given name.
func (s Set) Get(name string) (*Runbook, error) {
	for _, rb := range s.Books {
		if rb.Name == name {
			return rb, nil
		}
	}
	// A name that matches a file that failed to parse gets that file's error
	// rather than "no such runbook": the user wrote the runbook, and the
	// actionable fact is why it did not load.
	for _, b := range s.Broken {
		stem := strings.TrimSuffix(filepath.Base(b.Path), filepath.Ext(b.Path))
		if stem == name {
			return nil, b.Err
		}
	}
	return nil, errors.New("no runbook named " + name)
}
