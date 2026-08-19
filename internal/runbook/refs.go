package runbook

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// This file is the `{{ ... }}` reference machinery: finding references in a
// step's params at load, and replacing them with real values at run.
//
// It is deliberately not a template language. There is no arithmetic, no
// conditional, no function call — a reference is a dotted path into a value
// some earlier step returned, or into the runbook's declared vars, and that is
// the entire grammar. Anything richer would want an evaluator, an evaluator
// wants errors at evaluation time, and errors at evaluation time are exactly
// what this package is built to avoid: by the time step 4 evaluates, steps 1-3
// have already changed the user's session.

// varsRoot is the reserved reference root for the runbook's declared vars:
// `{{ vars.branch }}`. It is a name a step id may not take, so a reference's
// root is unambiguous without any escaping.
const varsRoot = "vars"

// ref is one `{{ ... }}` occurrence found in a params tree.
type ref struct {
	text string   // the reference as written, for error messages
	path []string // the dotted path, e.g. ["api", "pane"]
}

func (r ref) root() string { return r.path[0] }

// --- finding references ----------------------------------------------------------

// collectRefs walks a params tree and returns every reference in it. Order is
// unspecified; the caller only checks membership.
func collectRefs(v any) ([]ref, error) {
	var out []ref
	err := walkStrings(v, func(s string) error {
		rs, err := parseRefs(s)
		if err != nil {
			return err
		}
		out = append(out, rs...)
		return nil
	})
	return out, err
}

// walkStrings visits every string in a decoded YAML tree. Map keys are not
// visited: a reference in a key would name a WIRE FIELD, and the set of wire
// fields is fixed by the command's params struct, so computing one could only
// ever produce a field the server ignores.
func walkStrings(v any, fn func(string) error) error {
	switch t := v.(type) {
	case string:
		return fn(t)
	case []any:
		for _, e := range t {
			if err := walkStrings(e, fn); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, k := range sortedKeys(t) {
			if err := walkStrings(t[k], fn); err != nil {
				return err
			}
		}
	case map[any]any:
		// goccy/go-yaml decodes into map[string]any for string keys, but a
		// document with a non-string key still lands here. Such a key cannot
		// name a wire field, so the value is walked and the key ignored.
		for _, e := range t {
			if err := walkStrings(e, fn); err != nil {
				return err
			}
		}
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Sorting keeps error messages deterministic when a step has more than one
	// bad reference; the caller reports the first.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// parseRefs extracts every `{{ ... }}` from one string.
//
// An unclosed `{{` is an error rather than literal text. The alternative —
// treating it as text — would type a half-written reference into a shell, and
// the whole point of load-time validation is that a mistake in a runbook is
// caught before anything runs.
func parseRefs(s string) ([]ref, error) {
	var out []ref
	for i := 0; ; {
		open := strings.Index(s[i:], "{{")
		if open < 0 {
			return out, nil
		}
		open += i
		close := strings.Index(s[open:], "}}")
		if close < 0 {
			return nil, fmt.Errorf("unclosed reference in %q", s)
		}
		close += open
		r, err := parseRef(strings.TrimSpace(s[open+2 : close]))
		if err != nil {
			return nil, err
		}
		out = append(out, r)
		i = close + 2
	}
}

// parseRef turns the inside of a `{{ }}` into a path.
func parseRef(body string) (ref, error) {
	if body == "" {
		return ref{}, fmt.Errorf("empty reference {{ }}")
	}
	parts := strings.Split(body, ".")
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			return ref{}, fmt.Errorf("reference {{ %s }} has an empty path segment", body)
		}
	}
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return ref{text: "{{ " + body + " }}", path: parts}, nil
}

// --- resolving references --------------------------------------------------------

// Bindings is the value each reference root resolves against during one run:
// every completed step's result under its id, plus "vars".
//
// Results are stored as the generic JSON tree a round trip through
// encoding/json produces, not as the typed structs the dispatcher returned.
// That is what lets a reference use the WIRE field name — `{{ api.pane }}`, the
// name the user sees in `catctl commands` and in every other client — rather
// than the Go field name, which no runbook author has any reason to know.
type Bindings map[string]any

// Bind records a step's result under its id.
func (b Bindings) Bind(id string, result any) error {
	if result == nil {
		b[id] = map[string]any{}
		return nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("cannot bind %s: %w", id, err)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("cannot bind %s: %w", id, err)
	}
	b[id] = v
	return nil
}

// Resolve returns a copy of a step's params with every reference replaced.
//
// The substitution has two modes, and the distinction is what makes references
// usable at all. A string that is EXACTLY one reference is replaced by the
// referenced VALUE with its type intact — `pane: "{{ api.pane }}"` sends the
// number 3, not the string "3", which matters because Pane is a uint32 on the
// wire and a string there is a decode error. A reference embedded in longer
// text is stringified and interpolated, because the surrounding characters
// prove the author wanted text.
func Resolve(params map[string]any, b Bindings) (map[string]any, error) {
	if len(params) == 0 {
		return nil, nil
	}
	v, err := resolveValue(params, b)
	if err != nil {
		return nil, err
	}
	out, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("params resolved to %T, not an object", v)
	}
	return out, nil
}

func resolveValue(v any, b Bindings) (any, error) {
	switch t := v.(type) {
	case string:
		return resolveString(t, b)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			r, err := resolveValue(e, b)
			if err != nil {
				return nil, err
			}
			out[i] = r
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			r, err := resolveValue(e, b)
			if err != nil {
				return nil, err
			}
			out[k] = r
		}
		return out, nil
	default:
		return v, nil
	}
}

func resolveString(s string, b Bindings) (any, error) {
	trimmed := strings.TrimSpace(s)
	// Whole-string form: one reference and nothing else, so the value keeps its
	// type. Checked before the interpolating path because the two produce
	// different Go values for the same input.
	if strings.HasPrefix(trimmed, "{{") && strings.HasSuffix(trimmed, "}}") &&
		strings.Index(trimmed, "}}") == len(trimmed)-2 {
		r, err := parseRef(strings.TrimSpace(trimmed[2 : len(trimmed)-2]))
		if err != nil {
			return nil, err
		}
		return lookup(r, b)
	}

	var sb strings.Builder
	for i := 0; ; {
		open := strings.Index(s[i:], "{{")
		if open < 0 {
			sb.WriteString(s[i:])
			return sb.String(), nil
		}
		open += i
		close := strings.Index(s[open:], "}}")
		if close < 0 {
			return nil, fmt.Errorf("unclosed reference in %q", s)
		}
		close += open
		sb.WriteString(s[i:open])
		r, err := parseRef(strings.TrimSpace(s[open+2 : close]))
		if err != nil {
			return nil, err
		}
		val, err := lookup(r, b)
		if err != nil {
			return nil, err
		}
		sb.WriteString(stringify(val))
		i = close + 2
	}
}

// lookup walks one reference's path through the bindings.
func lookup(r ref, b Bindings) (any, error) {
	var cur any = map[string]any(b)
	for i, seg := range r.path {
		switch t := cur.(type) {
		case map[string]any:
			v, ok := t[seg]
			if !ok {
				return nil, fmt.Errorf("%s: %s has no field %q", r.text, pathTo(r.path, i), seg)
			}
			cur = v
		case []any:
			idx, err := strconv.Atoi(seg)
			if err != nil {
				return nil, fmt.Errorf("%s: %s is a list, so %q must be an index", r.text, pathTo(r.path, i), seg)
			}
			if idx < 0 || idx >= len(t) {
				return nil, fmt.Errorf("%s: %s has %d entries, so index %d is out of range", r.text, pathTo(r.path, i), len(t), idx)
			}
			cur = t[idx]
		default:
			return nil, fmt.Errorf("%s: %s is not an object, so it has no field %q", r.text, pathTo(r.path, i), seg)
		}
	}
	return cur, nil
}

// pathTo names the prefix of a path that was walked successfully, so an error
// says which hop failed rather than only that the whole reference did.
func pathTo(path []string, i int) string {
	if i == 0 {
		return "the runbook"
	}
	return strings.Join(path[:i], ".")
}

// stringify renders a resolved value for interpolation into surrounding text.
// A number arrives from encoding/json as a float64, so an integral one is
// printed without its ".0" — `{{ api.pane }}` inside a shell command must
// produce "3", not "3.0".
func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return ""
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		return string(raw)
	}
}

// --- expectations ----------------------------------------------------------------

// CheckExpect evaluates a step's `expect:` against the bindings and reports why
// it failed, or nil if it held.
//
// Truthiness is spelled out rather than borrowed from a language's notion of it,
// because the values arriving here come from encoding/json and a runbook author
// is looking at YAML. A number is false only at zero, a string only when empty,
// a list or object only when empty — and a missing field is not "false", it is
// the lookup error the caller already gets, because asserting on a field that
// does not exist is a mistake in the runbook rather than a failed run.
func (s Step) CheckExpect(b Bindings) error {
	if s.expectRef == nil {
		return nil
	}
	v, err := lookup(*s.expectRef, b)
	if err != nil {
		return fmt.Errorf("expect %s: %w", s.Expect, err)
	}
	if truthy(v) {
		return nil
	}
	return fmt.Errorf("expect %s was %s", s.Expect, describe(v))
}

func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return true
	}
}

// describe renders a failed expectation's actual value for the error. A list or
// object is reported by its emptiness rather than its contents: the contents
// could be a screenful, and the fact that matters is that it was empty.
func describe(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case []any:
		return "an empty list"
	case map[string]any:
		_ = t
		return "an empty object"
	case string:
		return `""`
	default:
		return stringify(v)
	}
}
