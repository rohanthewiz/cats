package runbook

import (
	"strings"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
)

// The three spellings of `on:` all mean the same thing. They exist because all
// three read naturally, and the difference is punctuation rather than meaning.
func TestTriggerSpellings(t *testing.T) {
	bare := parse(t, "steps:\n  - run: pane.list\non: pane_exited\n")
	list := parse(t, "steps:\n  - run: pane.list\non: [pane_exited]\n")
	clause := parse(t, "steps:\n  - run: pane.list\non:\n  - event: pane_exited\n")
	for name, rb := range map[string]*Runbook{"bare": bare, "list": list, "clause": clause} {
		if len(rb.Triggers) != 1 || rb.Triggers[0].Event != app.EventPaneExited {
			t.Errorf("%s: triggers = %+v", name, rb.Triggers)
		}
	}
}

// An unknown event name is a load error. Without it the runbook would simply
// never fire, and a trigger that never fires produces nothing to debug from.
func TestUnknownTriggerEventIsRefused(t *testing.T) {
	parseErr(t, "steps:\n  - run: pane.list\non: host_attached\n", "is not an event")
}

// The same rule one level down: a `where:` key must name a field the event
// actually carries. `exit_cod` would otherwise be a filter that matches nothing.
func TestWhereKeyIsCheckedAgainstThePayload(t *testing.T) {
	parseErr(t, "steps:\n  - run: pane.list\non:\n  - event: pane_exited\n    where: {exit_cod: 0}\n",
		`has no payload field "exit_cod"`)
	// And the message names what it does carry, so the fix is in the error.
	_, err := Parse([]byte("steps:\n  - run: pane.list\non:\n  - event: pane_exited\n    where: {exit_cod: 0}\n"), "t", "t.yaml")
	if !strings.Contains(err.Error(), "exit_code") {
		t.Errorf("error = %v, want it to list the real fields", err)
	}
}

// A misspelled clause KEY is refused for the same reason: `wehre:` would leave a
// clause with no filter at all, which fires on every occurrence of the event.
func TestUnknownTriggerKeyIsRefused(t *testing.T) {
	parseErr(t, "steps:\n  - run: pane.list\non:\n  - event: pane_exited\n    wehre: {exit_code: 0}\n",
		"no such trigger key")
}

func TestTriggerValueShapes(t *testing.T) {
	parseErr(t, "steps:\n  - run: pane.list\non:\n  - event: pane_agent\n    where: {state: {is: blocked}}\n",
		"must be a string, number, boolean")
	parseErr(t, "steps:\n  - run: pane.list\non:\n  - event: pane_agent\n    min_interval: soon\n",
		"is not a duration")
	rb := parse(t, "steps:\n  - run: pane.list\non:\n  - event: pane_agent\n    min_interval: 90s\n")
	if rb.Triggers[0].MinInterval != 90*time.Second {
		t.Errorf("min_interval = %s", rb.Triggers[0].MinInterval)
	}
}

// A runbook triggering on its own completion is a loop with nothing between its
// turns, and it is the one loop decidable from the document alone.
func TestRunbookCannotTriggerOnRunbookFinished(t *testing.T) {
	parseErr(t, "steps:\n  - run: pane.list\non: runbook_finished\n", "loop with nothing between its turns")
}

// Two unfiltered clauses on one event would fire once — the one-run-at-a-time
// rule swallows the second — which looks exactly like a broken filter.
func TestDuplicateUnfilteredClauseIsRefused(t *testing.T) {
	parseErr(t, "steps:\n  - run: pane.list\non: [pane_exited, pane_exited]\n", "adds nothing")
	// Filtered clauses on the same event are how "blocked OR working" is written.
	rb := parse(t, `
steps:
  - run: pane.list
on:
  - event: pane_agent
    where: {state: blocked}
  - event: pane_agent
    where: {state: working}
`)
	if len(rb.Triggers) != 2 {
		t.Fatalf("triggers = %+v", rb.Triggers)
	}
}

// `{{ event.… }}` is only bound when the runbook has triggers, and the refusal
// says so rather than sending the author hunting for a missing step id.
func TestEventRefNeedsATrigger(t *testing.T) {
	parseErr(t, "steps:\n  - run: pane.focus\n    params: {pane: \"{{ event.pane }}\"}\n",
		"declares no `on:` trigger")
}

// A field no declared event carries is a typo, and it is caught at load.
func TestEventRefFieldIsChecked(t *testing.T) {
	parseErr(t, `
on: pane_exited
steps:
  - run: pane.focus
    params: {pane: "{{ event.pain }}"}
`, `no event this runbook triggers on carries a field "pain"`)

	// The check is against the UNION of the declared events, not the
	// intersection: a runbook started by either event is written once, and a
	// field only one of them carries is a run-time miss rather than a typo.
	parse(t, `
on: [pane_exited, pane_cwd]
steps:
  - run: pane.send_input
    params: {pane: "{{ event.pane }}", text: "cd {{ event.cwd }}"}
`)
}

// `event` is reserved the way `vars` is, so a reference's root is unambiguous.
func TestEventIsAReservedStepID(t *testing.T) {
	parseErr(t, "on: pane_exited\nsteps:\n  - id: event\n    run: pane.list\n", "is reserved")
}

// --- matching ---------------------------------------------------------------

func TestTriggerMatch(t *testing.T) {
	rb := parse(t, `
on:
  - event: pane_agent
    where: {state: [blocked, working], agent: claude}
steps:
  - run: pane.list
`)
	tr := rb.Triggers[0]
	cases := []struct {
		name    string
		payload map[string]any
		want    bool
	}{
		{"both match", map[string]any{"agent": "claude", "state": "blocked"}, true},
		{"other alternative", map[string]any{"agent": "claude", "state": "working"}, true},
		{"state outside the list", map[string]any{"agent": "claude", "state": "idle"}, false},
		{"other agent", map[string]any{"agent": "codex", "state": "blocked"}, false},
	}
	for _, c := range cases {
		if got := tr.Match(c.payload); got != c.want {
			t.Errorf("%s: Match = %v, want %v", c.name, got, c.want)
		}
	}
}

// The filter comes from a YAML decoder and the payload from a JSON one, and the
// two disagree about numbers: `exit_code: 0` is an int here and a float64
// there. Comparing them with == would make every numeric filter silently false.
func TestNumericFilterCrossesDecoders(t *testing.T) {
	rb := parse(t, "on:\n  - event: pane_exited\n    where: {exit_code: 0}\nsteps:\n  - run: pane.list\n")
	tr := rb.Triggers[0]
	if !tr.Match(map[string]any{"pane": float64(3), "exit_code": float64(0)}) {
		t.Error("a zero exit code did not match a filter written as 0")
	}
	if tr.Match(map[string]any{"pane": float64(3), "exit_code": float64(1)}) {
		t.Error("a non-zero exit code matched a filter written as 0")
	}
}

// EventMap fills in what omitempty drops. Without it a filter on the ordinary
// successful exit would be the one filter that could never match.
func TestEventMapKeepsZeroValues(t *testing.T) {
	m := EventMap(app.PaneExitedEvent{Pane: 7})
	if _, ok := m["exit_code"]; !ok {
		t.Fatalf("exit_code missing from %v", m)
	}
	if m["exit_code"] != float64(0) {
		t.Errorf("exit_code = %#v, want the JSON number 0", m["exit_code"])
	}
	if m["pane"] != float64(7) {
		t.Errorf("pane = %#v", m["pane"])
	}

	// The omitempty fields of a host event are present too, and empty.
	h := EventMap(app.HostLinkEvent{Host: "devbox"})
	for _, k := range []string{"host", "label", "addr", "error", "pane"} {
		if _, ok := h[k]; !ok {
			t.Errorf("%s missing from %v", k, h)
		}
	}
	if h["error"] != "" {
		t.Errorf("error = %#v, want the empty string", h["error"])
	}
}

// Every event in the vocabulary must have a payload whose fields a trigger can
// be written against — the table is what the load check consults, so a name in
// it with nothing behind it would accept any `where:` key at all.
func TestEveryEventHasAPayload(t *testing.T) {
	for _, name := range app.EventNames() {
		p, ok := app.EventPayload(name)
		if !ok || p == nil {
			t.Errorf("%s has no payload in the event table", name)
			continue
		}
		if len(EventMap(p)) == 0 {
			t.Errorf("%s's payload has no wire fields", name)
		}
	}
}
