package runbook

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// This file checks a step's params against the command's params STRUCT at load
// time, so a misspelled key is refused rather than dropped.
//
// It exists because of how JSON decoding fails. encoding/json ignores a key it
// has no field for, so `timeout_secs` where the struct says `timeout_ms` is not
// an error anywhere in the system — the command runs with the field at its zero
// value and reports success. In a client that is a bug the author sees the
// moment they read the output. In a runbook it is a step that appears to work
// and silently did something else, three steps before the one that matters.
//
// The check is deliberately one-directional: unknown keys are refused, missing
// ones are not. "Params are optional unless the command says otherwise" is the
// dispatcher's rule (CommandSpec.ParamsRequired), and duplicating a per-field
// notion of requiredness here would be a second, quieter contract.

var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

// checkParams verifies every key in params names a field of the command's
// params struct. paramsType is the reflect.Type of CommandSpec.Params; a nil
// type means the command takes none.
func checkParams(cmd string, paramsType reflect.Type, params map[string]any) error {
	if len(params) == 0 {
		return nil
	}
	if paramsType == nil {
		return fmt.Errorf("%s takes no params", cmd)
	}
	return checkObject(cmd, paramsType, params, "")
}

// checkObject walks one object against one struct type. where is the dotted
// path to this object within the params ("" at the top level), so an error in a
// nested object says which one.
func checkObject(cmd string, rt reflect.Type, obj map[string]any, where string) error {
	rt = deref(rt)
	if rt.Kind() != reflect.Struct {
		return nil // not an object on the Go side; nothing to check keys against
	}
	fields := jsonFields(rt)

	for _, k := range sortedMapKeys(obj) {
		f, ok := fields[k]
		if !ok {
			return fmt.Errorf("%s has no param %q%s; it takes %s",
				cmd, k, atPath(where), nameList(fields))
		}
		if err := checkValue(cmd, f.Type, obj[k], join(where, k)); err != nil {
			return err
		}
	}
	return nil
}

// checkValue recurses into a value whose Go type is itself structured.
func checkValue(cmd string, ft reflect.Type, v any, where string) error {
	ft = deref(ft)
	switch {
	case ft == rawMessageType:
		// A raw-JSON field is a passthrough by design: the server has not
		// decided its shape, so neither can this.
		return nil
	case ft.Kind() == reflect.Map:
		// The keys ARE data (a config map, a var set), so there is no field set
		// to check them against. Values still are.
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		for _, k := range sortedMapKeys(m) {
			if err := checkValue(cmd, ft.Elem(), m[k], join(where, k)); err != nil {
				return err
			}
		}
		return nil
	case ft.Kind() == reflect.Struct:
		obj, ok := v.(map[string]any)
		if !ok {
			return nil // a scalar where an object was expected: the decoder's error to give
		}
		return checkObject(cmd, ft, obj, where)
	case ft.Kind() == reflect.Slice || ft.Kind() == reflect.Array:
		list, ok := v.([]any)
		if !ok {
			return nil
		}
		for i, e := range list {
			if err := checkValue(cmd, ft.Elem(), e, fmt.Sprintf("%s.%d", where, i)); err != nil {
				return err
			}
		}
		return nil
	}
	return nil
}

// jsonFields maps a struct's wire keys to their fields, flattening embedded
// structs exactly as encoding/json does — PaneMeta's fields are promoted into
// PaneInfo on the wire, so a runbook naming one of them is naming a real key.
func jsonFields(rt reflect.Type) map[string]reflect.StructField {
	out := map[string]reflect.StructField{}
	var walk func(reflect.Type)
	walk = func(t reflect.Type) {
		t = deref(t)
		if t.Kind() != reflect.Struct {
			return
		}
		for i := range t.NumField() {
			f := t.Field(i)
			if f.PkgPath != "" { // unexported: invisible to encoding/json
				continue
			}
			tag := f.Tag.Get("json")
			name, _, _ := strings.Cut(tag, ",")
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

// nameList renders the accepted keys for an error message. A command with a
// large params struct is truncated: the message exists to catch a typo, and
// twenty names past the tenth do not help anyone find theirs.
func nameList(fields map[string]reflect.StructField) string {
	names := make([]string, 0, len(fields))
	for k := range fields {
		names = append(names, k)
	}
	sort.Strings(names)
	const max = 12
	if len(names) > max {
		return strings.Join(names[:max], ", ") + fmt.Sprintf(", … (%d more)", len(names)-max)
	}
	return strings.Join(names, ", ")
}

func sortedMapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func deref(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func join(where, key string) string {
	if where == "" {
		return key
	}
	return where + "." + key
}

func atPath(where string) string {
	if where == "" {
		return ""
	}
	return " at " + where
}
