//go:build ghostty

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/runbook"
)

// This file is the runbook executor: the orchestrator half of runbook.list and
// runbook.run.
//
// The whole design is one sentence — a runbook step is dispatched through the
// same app.Dispatcher a browser cmd and a catctl request go through. There is
// no runbook-specific implementation of any command, no privileged path, and no
// way for a step to reach something a client could not. Whatever a step does,
// the ordinary command did it.
//
// Everything here runs on the orchestrator loop goroutine, which owns the
// session. That is what makes the bindings map and the step cursor safe without
// a lock, and it is also the constraint the executor is shaped around: a step
// may resolve inline (pane.focus) or many milliseconds later (capture, or
// anything that round-trips to a cathost), and the chain has to handle both
// without ever blocking the loop. See advance.

// The run is bounded by app.MaxRunbookRuntime, checked BETWEEN steps rather
// than enforced by a timer, so a long step that is genuinely working is never
// cut in half.
//
// There is deliberately no per-step timeout. Every async command already
// resolves its own responder on its own timer (read/capture/wait all do), so a
// step that never resolves is a bug in that command rather than a case to
// handle here — and the ctlproto backstop already bounds the caller's wait. A
// third timer would mostly add a way for two of them to disagree about which
// step failed.

// RunbookList answers runbook.list. The directory is re-scanned on every call —
// see runbook.Set for why nothing is cached.
func (o *orch) RunbookList(r app.Responder) {
	dir := runbook.UserDir()
	if dir == "" {
		r.Fail("no config directory is resolvable, so there is nowhere for runbooks to live")
		return
	}
	set := runbook.Load(dir)

	out := make([]app.RunbookInfo, 0, len(set.Books)+len(set.Broken))
	for _, rb := range set.Books {
		out = append(out, app.RunbookInfo{
			Name:        rb.Name,
			Description: rb.Description,
			Path:        rb.Path,
			Steps:       len(rb.Steps),
			Vars:        varNames(rb),
		})
	}
	for _, b := range set.Broken {
		out = append(out, app.RunbookInfo{
			Name:  stemOf(b.Path),
			Path:  b.Path,
			Error: b.Err.Error(),
		})
	}
	r.OK(app.RunbookListResult{Dir: dir, Runbooks: out})
}

// varNames lists a runbook's declared vars in sorted order.
func varNames(rb *runbook.Runbook) []string {
	if len(rb.Vars) == 0 {
		return nil
	}
	out := make([]string, 0, len(rb.Vars))
	for k := range rb.Vars {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// stemOf is a broken file's fallback name: what its author would have addressed
// it by, since a file that did not parse has no declared name.
func stemOf(path string) string {
	base := path
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndexByte(base, '.'); i > 0 {
		base = base[:i]
	}
	return base
}

// runbookRun is one in-flight run: the parsed document, how far through it we
// are, what each completed step bound, and the caller waiting on the result.
//
// Loop-goroutine only, like everything the orchestrator holds.
type runbookRun struct {
	rb    *runbook.Runbook
	binds runbook.Bindings
	steps []app.RunbookStepResult
	r     app.Responder
	i     int       // index of the step about to run
	end   time.Time // the whole-run deadline
	// failed records that some step errored and was not tolerated. The run then
	// walks the remaining steps marking them Skipped rather than stopping dead,
	// so the result says what did not happen as well as what did.
	failed bool
}

// RunbookRun answers runbook.run: load the named runbook, then walk its steps.
func (o *orch) RunbookRun(r app.Responder, p app.RunbookRunParams) {
	dir := runbook.UserDir()
	if dir == "" {
		r.Fail("no config directory is resolvable, so there is nowhere for runbooks to live")
		return
	}
	rb, err := runbook.Load(dir).Get(p.Name)
	if err != nil {
		r.Fail(err.Error())
		return
	}

	// Vars are merged before anything runs, and an undeclared one is refused
	// rather than ignored. `--var brnach=x` silently doing nothing is the same
	// failure mode as a typo'd reference, one step further out: the run appears
	// to succeed and does the wrong thing.
	vars := map[string]any{}
	for k, v := range rb.Vars {
		vars[k] = v
	}
	for k, v := range p.Vars {
		if _, declared := rb.Vars[k]; !declared {
			r.Fail(fmt.Sprintf("runbook %s declares no var %q", rb.Name, k))
			return
		}
		vars[k] = v
	}

	run := &runbookRun{
		rb:    rb,
		binds: runbook.Bindings{"vars": vars},
		steps: make([]app.RunbookStepResult, 0, len(rb.Steps)),
		r:     r,
		end:   time.Now().Add(app.MaxRunbookRuntime),
	}
	o.advanceRunbook(run)
}

// advanceRunbook runs steps until the runbook finishes or a step goes async.
//
// The loop exists because a dispatched command may resolve either way, and the
// two cases must not be written twice. `inFlight` distinguishes them: while it
// is set, control is still inside Dispatch, so a responder call means the step
// finished INLINE and the loop should simply iterate. Once Dispatch has
// returned with the step unresolved, the responder will fire later from a
// cathost reply, and that call re-enters this function to carry the chain on.
//
// Recursion depth is therefore bounded by the number of ASYNC steps in flight,
// which is always one — never by the step count.
func (o *orch) advanceRunbook(run *runbookRun) {
	for {
		if run.i >= len(run.rb.Steps) {
			o.finishRunbook(run)
			return
		}
		if time.Now().After(run.end) {
			run.recordErr(fmt.Sprintf("the run exceeded %s", app.MaxRunbookRuntime))
			o.skipRest(run)
			o.finishRunbook(run)
			return
		}

		step := run.rb.Steps[run.i]
		params, err := runbook.Resolve(step.Params, run.binds)
		if err != nil {
			if !run.recordErr(err.Error()) {
				o.skipRest(run)
				o.finishRunbook(run)
				return
			}
			continue
		}
		raw, err := json.Marshal(params)
		if err != nil {
			// Unreachable in practice: params came from YAML and every resolved
			// value is a JSON scalar, list or object. Handled anyway, because a
			// panic here would take the loop goroutine and the whole session.
			if !run.recordErr("cannot encode params: " + err.Error()) {
				o.skipRest(run)
				o.finishRunbook(run)
				return
			}
			continue
		}
		if len(params) == 0 {
			raw = nil // no params: the dispatcher's optional-decode path
		}

		var (
			inFlight = true  // control is still inside Dispatch
			inline   = false // the responder fired before Dispatch returned
			answered = false // the responder fired at all — see below
			carryOn  = false // whether the run continues past this step
		)
		id := step.ID
		resp := funcResponder{fn: func(data any, errMsg string) {
			// A command that resolved its responder twice would otherwise
			// re-enter finishStep against the NEXT step's index and corrupt the
			// whole result. Nothing in the table does that today; the guard is
			// here because the corruption would be silent and would look like a
			// runbook bug rather than a dispatcher one.
			if answered {
				log.Printf("catway: runbook %s step %d (%s) answered twice; ignoring the second",
					run.rb.Name, run.i+1, step.Run)
				return
			}
			answered = true
			carryOn = run.finishStep(id, data, errMsg)
			if inFlight {
				inline = true
				return // the loop below picks it up
			}
			if !carryOn {
				o.skipRest(run)
				o.finishRunbook(run)
				return
			}
			o.advanceRunbook(run)
		}}

		app.NewDispatcher(o.session, o).Dispatch(step.Run, app.JSONParamDecoder{Raw: raw}, resp)
		inFlight = false
		if !inline {
			return // still pending; the responder resumes the chain
		}
		if !carryOn {
			o.skipRest(run)
			o.finishRunbook(run)
			return
		}
	}
}

// finishStep records one step's outcome and binds its result. It reports
// whether the run should continue.
func (run *runbookRun) finishStep(id string, data any, errMsg string) bool {
	step := run.rb.Steps[run.i]
	res := app.RunbookStepResult{Index: run.i + 1, ID: id, Run: step.Run, Error: errMsg}
	run.steps = append(run.steps, res)
	run.i++

	if errMsg != "" {
		if !step.ContinueOnError {
			run.failed = true
			return false
		}
		// A tolerated failure still binds, so a later step referring to this
		// one's id gets a clear "no field" error rather than a stale value from
		// nowhere.
		if id != "" {
			_ = run.binds.Bind(id, nil)
		}
		return true
	}
	if id != "" {
		if err := run.binds.Bind(id, data); err != nil {
			return run.stepFailed(err.Error(), step.ContinueOnError)
		}
	}
	// The expectation runs AFTER the bind, because what it asserts on is almost
	// always this step's own result. A command that succeeded while reporting
	// that the thing did not happen — pane.wait_for_output timing out — fails
	// the step here, which is the only place that distinction can be drawn
	// without the engine knowing one command specially.
	if err := step.CheckExpect(run.binds); err != nil {
		return run.stepFailed(err.Error(), step.ContinueOnError)
	}
	return true
}

// stepFailed records a failure onto the step just appended and reports whether
// the run continues. Used for the failures discovered AFTER the command itself
// answered OK — a bind that would not encode, an expectation that did not hold.
func (run *runbookRun) stepFailed(msg string, tolerated bool) bool {
	run.steps[len(run.steps)-1].Error = msg
	if tolerated {
		return true
	}
	run.failed = true
	return false
}

// recordErr records a failure for the step about to run — one that never
// reached the dispatcher, because its params would not resolve. Reports whether
// the run continues.
func (run *runbookRun) recordErr(msg string) bool {
	step := run.rb.Steps[run.i]
	run.steps = append(run.steps, app.RunbookStepResult{
		Index: run.i + 1, ID: step.ID, Run: step.Run, Error: msg,
	})
	run.i++
	if step.ContinueOnError {
		return true
	}
	run.failed = true
	return false
}

// skipRest marks every step the run never reached. "Did not run" and "ran fine"
// are the two answers a reader must not confuse when working out what state the
// session was left in.
func (o *orch) skipRest(run *runbookRun) {
	for ; run.i < len(run.rb.Steps); run.i++ {
		st := run.rb.Steps[run.i]
		run.steps = append(run.steps, app.RunbookStepResult{
			Index: run.i + 1, ID: st.ID, Run: st.Run, Skipped: true,
		})
	}
}

// finishRunbook resolves the caller's responder exactly once.
func (o *orch) finishRunbook(run *runbookRun) {
	if run.failed {
		log.Printf("catway: runbook %s failed at step %d", run.rb.Name, failedIndex(run.steps))
	}
	run.r.OK(app.RunbookRunResult{Name: run.rb.Name, Steps: run.steps, Failed: run.failed})
}

// failedIndex is the 1-based index of the first step that errored, for the log
// line. 0 when none did.
func failedIndex(steps []app.RunbookStepResult) int {
	for _, s := range steps {
		if s.Error != "" {
			return s.Index
		}
	}
	return 0
}
