package wire

import (
	"reflect"
	"testing"
)

// These tests guard the §7 command table (commandSpecs) as the machine-readable
// truth about the vocabulary: cmd/catgen-dart emits a typed client from it, and
// the phone imports this package directly, so a wrong or missing entry is not a
// stale comment — it is a client method that does not exist, or one whose
// signature lies about what comes back.
//
// Only the checks that need nothing but the table live here. The ones that read
// the dispatcher's source or drive the real dispatcher are in
// internal/app/command_vocab_test.go, beside the code they exercise.

func TestCommandSpecsShape(t *testing.T) {
	for _, spec := range commandSpecs {
		if spec.Name == "" {
			t.Fatalf("commandSpecs has an entry with no name")
		}
		for _, f := range []struct {
			what string
			v    any
		}{{"Params", spec.Params}, {"Result", spec.Result}} {
			if f.v == nil {
				continue
			}
			if k := reflect.TypeOf(f.v).Kind(); k != reflect.Struct {
				t.Errorf("%s: %s is a %s, want a struct value or nil", spec.Name, f.what, k)
			}
		}
		if spec.ReplyRequired && spec.Result == nil {
			t.Errorf("%s: ReplyRequired with no Result — a command dropped for want of a reply channel must have something to say", spec.Name)
		}
	}
}

// CommandNames is derived from the table, and CommandSpecs hands out a copy:
// a caller reordering what it got back must not reorder the vocabulary.
func TestCommandSpecsCopyAndNames(t *testing.T) {
	names := CommandNames()
	specs := CommandSpecs()
	if len(names) != len(specs) {
		t.Fatalf("CommandNames has %d entries, CommandSpecs %d", len(names), len(specs))
	}
	for i := range specs {
		if names[i] != specs[i].Name {
			t.Fatalf("order drift at %d: name %q, spec %q", i, names[i], specs[i].Name)
		}
	}

	first := specs[0].Name
	specs[0] = CommandSpec{Name: "clobbered"}
	if got := CommandSpecs()[0].Name; got != first {
		t.Fatalf("CommandSpecs shares its backing array: first entry is now %q, want %q", got, first)
	}
}

// --- behaviour: the two flags describe what Dispatch actually does ------------
