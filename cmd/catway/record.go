//go:build ghostty

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/runbook"
)

// This file is the armed macro recorder: the orchestrator half of
// runbook.record.
//
// Armed, not always on. The alternative — a durable journal of every dispatched
// command, from which a time range is sliced — is strictly more powerful and
// strictly worse to own: it is a permanent record of every parameter of every
// command, including every chat message and every keystroke, kept by default,
// on a machine somebody else may administer. Armed, nothing exists unless
// somebody asked for it, the recording lives in memory until it is given a
// name, and the whole privacy question shrinks to "what goes in the file the
// user just asked for". The always-on journal can still be built on top of this
// recorder later, at which point its retention and its redaction inherit a
// working implementation rather than a design.
//
// Everything here runs on the orchestrator loop goroutine, which is also the
// goroutine Dispatch (and therefore Begin/Commit/Abort) runs on. That is what
// makes the step slice safe without a lock.

// maxRecordedSteps bounds what one recording holds in memory. It is far above
// the 200-step limit a runbook DOCUMENT has (internal/runbook), because the two
// bound different things: this one stops a recording somebody forgot about from
// growing without limit, and the emitter refuses the over-long document with
// the message that explains it.
const maxRecordedSteps = 1000

// maxRecordedBytes is the same backstop for size rather than count — one
// file.put of a large file is a single step carrying its whole payload.
const maxRecordedBytes = 4 << 20

// recStep is one reserved slot. The slot is taken when the command starts and
// filled when it succeeds, so a command that resolves late — a worktree create,
// a file put to another machine — keeps the position it was CALLED in. Emitting
// in completion order would silently reorder a macro against the sequence the
// user performed.
type recStep struct {
	seq    int64
	cmd    string
	done   bool // Commit arrived; a slot that never completes is never emitted
	params map[string]any
	result map[string]any
}

// macroRecorder is one session's recorder: at most one recording at a time.
//
// One at a time is not a simplification. Two overlapping recordings would both
// capture the same commands — there is one session and one command stream — so
// the second would be a copy of the first with a different start point, and the
// question "which one am I in" would have no answer a user could act on.
type macroRecorder struct {
	on    bool
	seq   int64
	steps []recStep
	bytes int
	// full is why capture stopped early, "" while the recording is healthy. It
	// is reported by status and by stop rather than raised as an error at the
	// moment it happens, because the command that overflowed the recording is a
	// command the user was running for its own sake and it must not fail.
	full string
	// anchorPane / anchorWorkspace are what was focused when recording started.
	// The emitter turns references to them into references to the pane and
	// workspace the RUNBOOK is started from, which is what lets the common
	// recording — the one that works in a pane that already existed — be
	// emitted at all.
	anchorPane      uint32
	anchorWorkspace string
	startedAt       time.Time
}

// Recorder gives the dispatcher this session's recorder. It is allocated on
// first ask rather than at construction so every orch built by a test that
// never records carries nothing.
func (o *orch) Recorder() app.Recorder {
	if o.macro == nil {
		o.macro = &macroRecorder{}
	}
	return o.macro
}

// Begin reserves a slot. Returning 0 is how "not recording" is spelled, and it
// is the only check Dispatch makes.
func (m *macroRecorder) Begin(cmd string) int64 {
	if m == nil || !m.on {
		return 0
	}
	if len(m.steps) >= maxRecordedSteps {
		m.full = fmt.Sprintf("the recording reached its %d-step ceiling and stopped capturing", maxRecordedSteps)
		return 0
	}
	if m.bytes >= maxRecordedBytes {
		m.full = fmt.Sprintf("the recording reached its %d MiB ceiling and stopped capturing", maxRecordedBytes>>20)
		return 0
	}
	m.seq++
	m.steps = append(m.steps, recStep{seq: m.seq, cmd: cmd})
	return m.seq
}

// Commit fills a slot with what the command was called with and what it
// returned.
//
// A params blob that will not decode, or a result that will not marshal, drops
// the slot rather than recording half of it: a step in a macro with its params
// missing would replay as a different command (pane.close with no pane closes
// the focused one), which is precisely the class of silent wrongness this whole
// phase exists to avoid.
func (m *macroRecorder) Commit(seq int64, params json.RawMessage, result any) {
	st := m.slot(seq)
	if st == nil {
		return
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &st.params); err != nil {
			m.drop(seq)
			return
		}
	}
	if result != nil {
		raw, err := json.Marshal(result)
		if err != nil {
			m.drop(seq)
			return
		}
		if err := json.Unmarshal(raw, &st.result); err != nil {
			m.drop(seq)
			return
		}
	}
	st.done = true
	m.bytes += len(params)
}

// Abort releases the slot of a command that failed. A macro is a replay of what
// happened, and a command that reported an error did not happen.
func (m *macroRecorder) Abort(seq int64) { m.drop(seq) }

func (m *macroRecorder) slot(seq int64) *recStep {
	// From the end: a command almost always resolves before the next one is
	// dispatched, so the slot being filled is the last one taken.
	for i := len(m.steps) - 1; i >= 0; i-- {
		if m.steps[i].seq == seq {
			return &m.steps[i]
		}
	}
	return nil
}

func (m *macroRecorder) drop(seq int64) {
	for i := range m.steps {
		if m.steps[i].seq == seq {
			m.steps = append(m.steps[:i], m.steps[i+1:]...)
			return
		}
	}
}

// recorded returns the completed slots, in call order, as the emitter's shape.
// Slots that were never committed are skipped — a command still in flight when
// the recording stopped did not finish inside it.
func (m *macroRecorder) recorded() []runbook.Recorded {
	out := make([]runbook.Recorded, 0, len(m.steps))
	for _, st := range m.steps {
		if !st.done {
			continue
		}
		out = append(out, runbook.Recorded{Run: st.cmd, Params: st.params, Result: st.result})
	}
	return out
}

// commandNames lists what has been captured, for status.
func (m *macroRecorder) commandNames() []string {
	out := make([]string, 0, len(m.steps))
	for _, st := range m.steps {
		if st.done {
			out = append(out, st.cmd)
		}
	}
	return out
}

func (m *macroRecorder) doneCount() int { return len(m.commandNames()) }

// RunbookRecord answers runbook.record. The dispatcher has already checked that
// the action is one of the four words and that a stop names a file.
func (o *orch) RunbookRecord(r app.Responder, p app.RunbookRecordParams) {
	m, _ := o.Recorder().(*macroRecorder)

	switch p.Action {
	case app.RecordStart:
		if m.on {
			r.Fail(fmt.Sprintf("already recording: %d commands since %s. Stop it with a name, or cancel it",
				m.doneCount(), m.startedAt.Format(time.Kitchen)))
			return
		}
		*m = macroRecorder{on: true, startedAt: time.Now()}
		if id, ok := o.session.FocusedPane(); ok {
			m.anchorPane = uint32(id)
		}
		m.anchorWorkspace = o.session.Info().ActiveWorkspace
		r.OK(m.status(app.RecordStart))

	case app.RecordStatus:
		r.OK(m.status(app.RecordStatus))

	case app.RecordCancel:
		if !m.on {
			r.Fail("nothing is being recorded")
			return
		}
		*m = macroRecorder{}
		r.OK(m.status(app.RecordCancel))

	case app.RecordStop:
		if !m.on {
			r.Fail("nothing is being recorded")
			return
		}
		o.stopRecording(r, m, p)
	}
}

// stopRecording emits the recording and writes it. A failure here leaves the
// recording ARMED: the two ways stop fails are a name that already exists and a
// handle that cannot be rewritten, and throwing the work away on either would
// punish a typo with the loss of everything that was recorded.
func (o *orch) stopRecording(r app.Responder, m *macroRecorder, p app.RunbookRecordParams) {
	dir := runbook.UserDir()
	if dir == "" {
		r.Fail("no config directory is resolvable, so there is nowhere for runbooks to live")
		return
	}
	out, err := runbook.Emit(m.recorded(), runbook.EmitOptions{
		Name:            p.Name,
		Description:     p.Description,
		AnchorPane:      m.anchorPane,
		AnchorWorkspace: m.anchorWorkspace,
	})
	if err != nil {
		r.Fail(err.Error() + " The recording is still running.")
		return
	}
	path := filepath.Join(dir, p.Name+".yaml")
	if _, statErr := os.Stat(path); statErr == nil && !p.Overwrite {
		r.Fail(fmt.Sprintf("a runbook named %s already exists at %s; pass overwrite to replace it. The recording is still running.",
			p.Name, path))
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		r.Fail("cannot create " + dir + ": " + err.Error())
		return
	}
	// 0600: a recording carries whatever was typed into the panes it captured,
	// which is a stronger reason for the tighter mode than a hand-written
	// runbook has.
	if err := os.WriteFile(path, out, 0o600); err != nil {
		r.Fail("cannot write " + path + ": " + err.Error())
		return
	}
	res := m.status(app.RecordStop)
	res.Name = p.Name
	res.Path = path
	*m = macroRecorder{}
	res.Recording = false
	r.OK(res)
}

// status describes the recorder from the outside.
func (m *macroRecorder) status(action string) app.RunbookRecordResult {
	return app.RunbookRecordResult{
		Action:    action,
		Recording: m.on,
		Steps:     m.doneCount(),
		Commands:  m.commandNames(),
		Note:      m.full,
	}
}
