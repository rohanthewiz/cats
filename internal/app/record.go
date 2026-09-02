package app

import (
	"github.com/rohanthewiz/cats/wire"

	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

// This file is the recorder seam: what Dispatch tells an armed macro recorder
// (runbook.record), and how a params or result field says what it is.
//
// The hook is in Dispatch and NOWHERE else. It is the one choke point every
// caller already funnels through — a browser `cmd`, catctl, the control relay,
// a plugin binary, a runbook's own steps, a trigger's firing — so a recorder
// placed there records the VOCABULARY rather than one client's use of it.
// Anywhere else would be a second vocabulary that starts drifting the day it is
// written. That it also catches runbook steps is a feature, not an accident:
// recording while a runbook runs should produce a runbook that does the same
// thing.
//
//	Dispatch(name, dec, r)
//	   │  spec.Recorded?          no ──► the switch, untouched
//	   ▼ yes
//	 rec.Begin(name) ──► seq, reserving this command's PLACE in the recording
//	   │
//	   ├─ dec wrapped: keeps the caller's raw params
//	   └─ r   wrapped: OK ──► rec.Commit(seq, params, result)
//	                   Fail ► rec.Abort(seq)
//
// The slot is reserved on the way IN and filled on the way out, so a command
// that resolves late — a worktree create, a file put to another machine — keeps
// the position it was called in rather than the one it finished in. Recording
// on completion alone would silently reorder a macro against the sequence the
// user actually performed.

// Recorder is the armed macro recorder. It is loop-goroutine state like
// everything else the dispatcher touches, so implementations need no locking.
//
// Begin returns 0 when nothing is being recorded, which is the only check
// Dispatch makes: "am I recording" and "reserve a slot" are one question asked
// once, so there is no window in which the answer changes between them.
type Recorder interface {
	Begin(cmd string) int64
	// Commit fills a reserved slot with what the command was called with and
	// what it returned. params is the caller's own JSON — see recordDecoder for
	// why the raw form rather than the decoded struct — and is nil for a
	// command called with none. result is the value passed to Responder.OK,
	// which is nil for the commands that return nothing.
	Commit(seq int64, params json.RawMessage, result any)
	// Abort releases a slot whose command failed. A slot that is neither
	// committed nor aborted (a command that never resolves its responder) is
	// simply never emitted — see the recorder's own drop rule.
	Abort(seq int64)
}

// recorderBackend is the optional half of the Backend seam: a backend that can
// record implements it, and one that cannot — every test fake, catgen, any
// future front-end — is unaffected. It is an optional interface rather than a
// Backend method because the recorder is not something a dispatcher NEEDS in
// order to run a command, and widening the seam would make every fake in the
// tree implement a method it has no use for.
type recorderBackend interface {
	// Recorder returns the backend's recorder. It may return nil, which reads
	// the same as not implementing this interface at all.
	Recorder() Recorder
}

// rawParamDecoder is the optional half of ParamDecoder: a decoder that can hand
// back the bytes it decoded from. Every decoder in the tree today is
// JSONParamDecoder, which can.
type rawParamDecoder interface {
	RawParams() []byte
}

// recordDecoder passes a decode through and keeps the caller's params.
//
// It keeps the RAW form in preference to marshalling the decoded struct back,
// and the difference is not cosmetic. A struct field with `omitempty` that was
// explicitly sent as its zero value — `submit: false`, `offset: 0` — marshals
// back to nothing, so a recording built from the decoded struct would quietly
// drop exactly the fields whose zero value was the point. That is the same bug
// `runbook.EventMap` exists to prevent one layer out, and the same fix: read
// the shape the caller actually sent.
type recordDecoder struct {
	inner ParamDecoder
	raw   json.RawMessage
}

func (d *recordDecoder) Decode(v any) error {
	if err := d.inner.Decode(v); err != nil {
		return err
	}
	if rp, ok := d.inner.(rawParamDecoder); ok {
		d.raw = append(json.RawMessage(nil), rp.RawParams()...)
		return nil
	}
	// Fallback for a decoder with no raw form (there is none today). It loses
	// omitempty zero values, as above, which is why it is the fallback and not
	// the rule.
	if b, err := json.Marshal(v); err == nil {
		d.raw = b
	}
	return nil
}

// recordResponder commits the recorder's slot on success and releases it on
// failure, then passes the result on to the real caller unchanged.
//
// The done flag is not defensive bookkeeping: a responder is storable in a
// pending round trip, and a command that answered twice would otherwise write
// one slot twice. The dispatcher has its own guard against that; this one keeps
// the recording honest if a future command loses it.
type recordResponder struct {
	inner Responder
	rec   Recorder
	dec   *recordDecoder
	seq   int64
	done  bool
}

func (r *recordResponder) WantsReply() bool { return r.inner.WantsReply() }

func (r *recordResponder) OK(data any) {
	if !r.done {
		r.done = true
		r.rec.Commit(r.seq, r.dec.raw, data)
	}
	r.inner.OK(data)
}

func (r *recordResponder) Fail(errMsg string) {
	if !r.done {
		r.done = true
		r.rec.Abort(r.seq)
	}
	r.inner.Fail(errMsg)
}

// --- field classification ----------------------------------------------------

// The classifications a params or result field can carry, in the `cats` struct
// tag beside its `json` one. Anything untagged is ClassPlain: recorded as sent.
//
// The default HAS to be "recorded". A recorder that dropped fields it did not
// recognise would emit runbooks that quietly fail to reproduce what was done,
// which is a worse failure than any it could prevent — the run appears to work
// and does something else. Which means the safety comes from somewhere else:
// TestRecordedParamsAreClassified fails the build when a params field of a
// recorded command is not accounted for, so the omission stops a build rather
// than writing a secret into a file on disk.
const (
	ClassPlain = "plain"
	// ClassSecret marks a field whose VALUE must not be written down, even into
	// a file the user just asked for: a credential. The emitter turns it into a
	// declared var with no default, so the runbook keeps its shape and asks for
	// the value when it runs.
	//
	// It is deliberately narrow. pane.send_input's text is the most private
	// field in this vocabulary and is NOT secret, because it is also the one a
	// macro exists to replay; the protection for it is that recording is armed
	// at all. Secret is for values that are useless to a replay and dangerous on
	// disk, which is credentials and nothing else so far.
	ClassSecret = "secret"
	// ClassPaneHandle / ClassWorkspaceHandle mark a field carrying a live
	// session handle. A recorded handle cannot be replayed as a literal — pane 7
	// tomorrow is somebody else's terminal — so the emitter rewrites each one
	// into a reference to the step that produced it, and refuses the recording
	// when nothing did.
	ClassPaneHandle      = "handle=pane"
	ClassWorkspaceHandle = "handle=workspace"
)

// ParamField is one top-level wire field of a command's params.
//
// Classification is top-level only, which is a real limit rather than an
// oversight: no nested field needs one today, and
// TestRecordedParamsAreClassified refuses a `cats` tag found deeper, so the day
// one does the walk gets extended instead of silently ignoring it.
type ParamField struct {
	Name  string // the JSON key, which is what a runbook step writes
	Class string
}

// ParamFields reports every top-level wire field of a command's params, sorted
// by name. An unknown command, or one taking no params, yields nothing.
func ParamFields(cmd string) []ParamField {
	spec, ok := specByName[cmd]
	if !ok || spec.Params == nil {
		return nil
	}
	var out []ParamField
	for name, f := range wireFields(reflect.TypeOf(spec.Params)) {
		out = append(out, ParamField{Name: name, Class: classOf(f)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ResultHandles reports which top-level wire fields of a command's result
// produce a session handle, and of which kind — the other half of the rewrite,
// since a handle in a params field is only replayable if some earlier step
// returned it.
func ResultHandles(cmd string) map[string]string {
	spec, ok := specByName[cmd]
	if !ok || spec.Result == nil {
		return nil
	}
	out := map[string]string{}
	for name, f := range wireFields(reflect.TypeOf(spec.Result)) {
		if c := classOf(f); c == ClassPaneHandle || c == ClassWorkspaceHandle {
			out[name] = c
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// RecordedCommand reports whether Dispatch offers this command to a recorder.
func RecordedCommand(cmd string) bool {
	spec, ok := specByName[cmd]
	return ok && spec.Recorded
}

// Spec looks one command up in the §7 table.
func Spec(cmd string) (CommandSpec, bool) {
	spec, ok := specByName[cmd]
	return spec, ok
}

// specByName indexes the table for the per-command lookups above, which happen
// once per dispatched command and would otherwise be a linear scan of seventy
// entries on the session's one goroutine.
var specByName = func() map[string]CommandSpec {
	m := make(map[string]CommandSpec, len(wire.CommandSpecs()))
	for _, s := range wire.CommandSpecs() {
		m[s.Name] = s
	}
	return m
}()

// classOf reads a field's classification.
func classOf(f reflect.StructField) string {
	switch tag := strings.TrimSpace(f.Tag.Get("cats")); tag {
	case "":
		return ClassPlain
	default:
		return tag
	}
}

// wireFields maps a struct's JSON keys to their fields, flattening embedded
// structs the way encoding/json does. Pointers are followed so an optional
// params struct (`*ConfigTheme`) is walked as the object it stands for.
func wireFields(rt reflect.Type) map[string]reflect.StructField {
	out := map[string]reflect.StructField{}
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		for t != nil && t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t == nil || t.Kind() != reflect.Struct {
			return
		}
		for i := range t.NumField() {
			f := t.Field(i)
			if f.PkgPath != "" { // unexported: invisible to encoding/json
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			if f.Anonymous && name == "" {
				walk(f.Type)
				continue
			}
			if name == "" {
				name = f.Name
			}
			out[name] = f
		}
	}
	walk(rt)
	return out
}
