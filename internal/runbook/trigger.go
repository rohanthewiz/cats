package runbook

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
)

// This file is the `on:` clause: what makes a runbook run itself when something
// happens in the session, rather than only when somebody asks.
//
// The design rule is the same one that shapes the steps. A step is a §7 command
// and nothing else; a trigger is a control-API EVENT and nothing else. There is
// no runbook-only notion of "when a build finishes" or "when the branch
// changes" — if a runbook can react to it, some client subscribed to
// events.subscribe could already have reacted to it, by a longer route. That
// keeps "what can start a runbook?" answerable from the event table
// (app.EventNames) instead of from this package.
//
// Everything a trigger declares is checked at LOAD, for the reason the whole
// package checks at load: the alternative to a refusal is a filter that
// silently never matches, and a trigger that never fires produces no output at
// all to debug from. `where: {exit_cod: 0}` is the exact shape of that failure,
// and it is refused here.

// eventRoot is the reserved reference root for the event that fired a triggered
// run: `{{ event.pane }}`. Like varsRoot it is a name a step id may not take, so
// a reference's root stays unambiguous.
const eventRoot = "event"

// Trigger is one `on:` clause: an event name, optional equality filters on that
// event's payload, and an optional floor on how often it may fire.
type Trigger struct {
	// Event is a name from app.EventNames().
	Event string
	// Where filters on the event's payload by exact match. Every entry must
	// hold for the trigger to fire, and a value written as a list holds when any
	// of its entries matches — `state: [blocked, idle]` is one clause, not two
	// runbooks. An empty Where fires on every occurrence of the event.
	Where map[string]any
	// MinInterval is the shortest gap between two firings of THIS trigger. Zero
	// means no floor.
	//
	// It is a throttle, not a safety device — the loop protections in the
	// executor are that — and it exists because some events are simply frequent.
	// pane_cwd fires on every `cd`, and a runbook that reacts to "I moved to a
	// new repo" wants the settled answer, not one run per path component of a
	// deep `cd`.
	MinInterval time.Duration

	// payloadFields is the event payload's wire field set, kept from the load
	// check so Match never reflects over anything.
	payloadFields map[string]bool
}

// docTrigger is the on-disk shape of one clause. The bare-string form
// (`on: [pane_exited]`) is normalised into it before this is used.
type docTrigger struct {
	Event       string         `yaml:"event"`
	Where       map[string]any `yaml:"where"`
	MinInterval string         `yaml:"min_interval"`
}

// maxTriggers bounds one document's `on:` list. A runbook that reacts to twenty
// different events is not one runbook.
const maxTriggers = 8

// parseTriggers normalises and validates a document's `on:` value.
//
// The value may be a single event name, a single clause, or a list of either,
// because all three read naturally and the difference is punctuation rather
// than meaning.
func parseTriggers(on any, where string) ([]Trigger, error) {
	if on == nil {
		return nil, nil
	}
	items, err := triggerItems(on, where)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("%s: `on:` is present but names no event", where)
	}
	if len(items) > maxTriggers {
		return nil, fmt.Errorf("%s: %d `on:` clauses exceeds the %d-clause limit", where, len(items), maxTriggers)
	}

	out := make([]Trigger, 0, len(items))
	seen := map[string]bool{}
	for i, it := range items {
		t, err := parseTrigger(it, fmt.Sprintf("%s: on[%d]", where, i))
		if err != nil {
			return nil, err
		}
		// Two clauses on the same event with no filters would fire the runbook
		// twice for one occurrence — which the one-run-at-a-time rule would then
		// silently swallow, making it look like a filter bug. Refused instead.
		// Filtered clauses on the same event are fine and are how "blocked OR
		// exited" is written.
		if len(t.Where) == 0 {
			if seen[t.Event] {
				return nil, fmt.Errorf("%s: on[%d]: %s is already triggered on with no `where:`, so this clause adds nothing",
					where, i, t.Event)
			}
			seen[t.Event] = true
		}
		out = append(out, t)
	}
	return out, nil
}

// triggerItems flattens the three accepted spellings of `on:` into a list.
func triggerItems(on any, where string) ([]docTrigger, error) {
	switch v := on.(type) {
	case string:
		return []docTrigger{{Event: v}}, nil
	case map[string]any:
		dt, err := toDocTrigger(v, where)
		if err != nil {
			return nil, err
		}
		return []docTrigger{dt}, nil
	case []any:
		out := make([]docTrigger, 0, len(v))
		for i, e := range v {
			switch ev := e.(type) {
			case string:
				out = append(out, docTrigger{Event: ev})
			case map[string]any:
				dt, err := toDocTrigger(ev, fmt.Sprintf("%s: on[%d]", where, i))
				if err != nil {
					return nil, err
				}
				out = append(out, dt)
			default:
				return nil, fmt.Errorf("%s: on[%d] must be an event name or a clause with `event:`, not %T", where, i, e)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s: `on:` must be an event name, a clause, or a list of either, not %T", where, on)
	}
}

// toDocTrigger reads one clause out of the generic YAML tree. It is hand-rolled
// rather than re-unmarshalled because the tree is already decoded, and because
// an unknown key here deserves the same refusal an unknown param gets — a
// misspelled `wehre:` would otherwise become a clause with no filter, which
// fires on everything.
func toDocTrigger(m map[string]any, where string) (docTrigger, error) {
	var dt docTrigger
	for _, k := range sortedMapKeys(m) {
		switch k {
		case "event":
			s, ok := m[k].(string)
			if !ok {
				return dt, fmt.Errorf("%s: event must be an event name, not %T", where, m[k])
			}
			dt.Event = strings.TrimSpace(s)
		case "where":
			w, ok := m[k].(map[string]any)
			if !ok {
				return dt, fmt.Errorf("%s: where must be a mapping of payload field to value, not %T", where, m[k])
			}
			dt.Where = w
		case "min_interval":
			s, ok := m[k].(string)
			if !ok {
				return dt, fmt.Errorf("%s: min_interval must be a duration string like \"30s\", not %T", where, m[k])
			}
			dt.MinInterval = strings.TrimSpace(s)
		default:
			return dt, fmt.Errorf("%s: no such trigger key %q; a clause takes event, where, min_interval", where, k)
		}
	}
	return dt, nil
}

// parseTrigger validates one clause against the event vocabulary.
func parseTrigger(dt docTrigger, where string) (Trigger, error) {
	t := Trigger{Event: dt.Event, Where: dt.Where}
	if t.Event == "" {
		return t, fmt.Errorf("%s: needs an `event:` naming a control-API event (%s)", where, strings.Join(app.EventNames(), ", "))
	}
	payload, ok := app.EventPayload(t.Event)
	if !ok {
		return t, fmt.Errorf("%s: %q is not an event; the vocabulary is %s", where, t.Event, strings.Join(app.EventNames(), ", "))
	}
	// A runbook triggering on its own completion is a loop with a period of one
	// run. The executor's protections would catch it, but as a rate limit that
	// trips after several runs have already changed the session — and this one
	// case is decidable from the document alone.
	if t.Event == app.EventRunbookFinished {
		return t, fmt.Errorf("%s: a runbook cannot trigger on %s — that is a loop with nothing between its turns",
			where, app.EventRunbookFinished)
	}

	fields := payloadFieldSet(payload)
	t.payloadFields = fields
	for _, k := range sortedMapKeys(t.Where) {
		if !fields[k] {
			return t, fmt.Errorf("%s: %s has no payload field %q; it carries %s",
				where, t.Event, k, strings.Join(sortedSet(fields), ", "))
		}
		if err := checkWhereValue(t.Where[k]); err != nil {
			return t, fmt.Errorf("%s: where.%s: %w", where, k, err)
		}
	}

	if dt.MinInterval != "" {
		d, err := time.ParseDuration(dt.MinInterval)
		if err != nil {
			return t, fmt.Errorf("%s: min_interval %q is not a duration (try \"30s\", \"5m\"): %w", where, dt.MinInterval, err)
		}
		if d < 0 {
			return t, fmt.Errorf("%s: min_interval %q is negative", where, dt.MinInterval)
		}
		t.MinInterval = d
	}
	return t, nil
}

// checkWhereValue refuses a filter value that could not equal anything a
// payload field holds. A mapping is the interesting case: `where: {pane: {id: 3}}`
// is somebody reaching for a path expression, and matching it would need a
// notion of structural equality this package has no use for.
func checkWhereValue(v any) error {
	switch t := v.(type) {
	case nil:
		return fmt.Errorf("an empty value matches nothing; drop the key to match every value")
	case []any:
		if len(t) == 0 {
			return fmt.Errorf("an empty list matches nothing; drop the key to match every value")
		}
		for _, e := range t {
			if !isScalar(e) {
				return fmt.Errorf("a list of alternatives may hold only strings, numbers and booleans, not %T", e)
			}
		}
		return nil
	default:
		if !isScalar(v) {
			return fmt.Errorf("must be a string, number, boolean, or a list of those — not %T", v)
		}
		return nil
	}
}

func isScalar(v any) bool {
	switch v.(type) {
	case string, bool:
		return true
	default:
		_, ok := numOf(v)
		return ok
	}
}

// Match reports whether an event occurrence satisfies this trigger's filters.
// payload is the event's JSON tree — the same generic shape a step's result is
// bound as, so a field is compared under the name a subscriber would see.
func (t Trigger) Match(payload map[string]any) bool {
	for k, want := range t.Where {
		got, ok := payload[k]
		if !ok {
			// The field is in the payload STRUCT (checked at load) but absent
			// from this occurrence's JSON, which means `omitempty` dropped its
			// zero value. Compare against the zero rather than failing: a
			// runbook filtering `exit_code: 0` is asking about the ordinary
			// case, and it would otherwise be the one case that never matches.
			got = nil
		}
		if !valueMatches(want, got) {
			return false
		}
	}
	return true
}

// valueMatches is equality for one filter entry: any-of for a list, exact
// otherwise.
func valueMatches(want, got any) bool {
	if list, ok := want.([]any); ok {
		for _, w := range list {
			if scalarEqual(w, got) {
				return true
			}
		}
		return false
	}
	return scalarEqual(want, got)
}

// scalarEqual compares a YAML-decoded filter value against a JSON-decoded
// payload value.
//
// The two sides arrive from different decoders and disagree about numbers: YAML
// gives `exit_code: 0` as an int, encoding/json gives the payload's 0 as a
// float64. Comparing with == would make every numeric filter false, and it
// would do so silently — the trigger simply never fires. So numbers are
// compared as float64 and everything else by value. Absent (nil) matches the
// zero of whichever type the filter names, which is the omitempty case Match
// leans on.
func scalarEqual(want, got any) bool {
	if wn, ok := numOf(want); ok {
		if got == nil {
			return wn == 0
		}
		gn, ok := numOf(got)
		return ok && wn == gn
	}
	switch w := want.(type) {
	case string:
		if got == nil {
			return w == ""
		}
		g, ok := got.(string)
		return ok && w == g
	case bool:
		if got == nil {
			return !w
		}
		g, ok := got.(bool)
		return ok && w == g
	}
	return false
}

// numOf widens any numeric Go value the two decoders can produce.
func numOf(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// payloadFieldSet is an event payload's wire field names, flattened the way
// encoding/json flattens embedded structs — the same walk a step's params get.
func payloadFieldSet(payload any) map[string]bool {
	out := map[string]bool{}
	if payload == nil {
		return out
	}
	for k := range jsonFields(reflect.TypeOf(payload)) {
		out[k] = true
	}
	return out
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TriggerEvents lists the distinct event names a runbook triggers on, sorted.
// It is what a listing shows and what the executor indexes by.
func (rb *Runbook) TriggerEvents() []string {
	if len(rb.Triggers) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, t := range rb.Triggers {
		if !seen[t.Event] {
			seen[t.Event] = true
			out = append(out, t.Event)
		}
	}
	sort.Strings(out)
	return out
}

// MatchTriggers returns the first clause that matches this occurrence, or nil.
// The first rather than all of them: a runbook runs once per event whatever the
// document says, and the clause is returned only so its MinInterval can be
// applied to the right one.
func (rb *Runbook) MatchTriggers(event string, payload map[string]any) (int, *Trigger) {
	for i := range rb.Triggers {
		t := &rb.Triggers[i]
		if t.Event == event && t.Match(payload) {
			return i, t
		}
	}
	return -1, nil
}

// EventMap renders an event payload as the generic tree a trigger filters
// against and `{{ event.… }}` resolves into.
//
// It is built by reflection over the struct rather than by a round trip through
// encoding/json, for one reason: `omitempty`. Marshalling PaneExitedEvent{Pane:
// 3} drops exit_code entirely, so `where: {exit_code: 0}` — a filter on the
// ordinary successful exit — would be the one filter that could never match, and
// `{{ event.exit_code }}` would be a run-time "no such field" on exactly the
// runs it was written for. Every field the load check validated against is
// therefore present here with its zero value, and the map a runbook sees has
// exactly the shape the event table documents.
//
// The VALUES still go through encoding/json individually, so a field's type is
// the JSON one (a number is a float64, a nested struct a map) and matches what a
// step's bound result looks like.
func EventMap(payload any) map[string]any {
	out := map[string]any{}
	if payload == nil {
		return out
	}
	rv := reflect.ValueOf(payload)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return out
		}
		rv = rv.Elem()
	}
	rt := rv.Type()
	if rt.Kind() != reflect.Struct {
		return out
	}
	for name, f := range jsonFields(rt) {
		// FieldByName rather than FieldByIndex: jsonFields flattens embedded
		// structs, so a promoted field's Index is relative to the type it was
		// declared in, not to rt. No event payload embeds anything today, which
		// is exactly why the wrong one would go unnoticed.
		fv := rv.FieldByName(f.Name)
		if !fv.IsValid() {
			continue
		}
		raw, err := json.Marshal(fv.Interface())
		if err != nil {
			continue // a field that will not encode cannot be filtered on either
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			continue
		}
		out[name] = v
	}
	return out
}
