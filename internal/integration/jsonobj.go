package integration

// Order-preserving JSON. Settings files (claude/copilot/droid/qodercli
// settings.json, codex/cursor hooks.json) belong to the user: install must
// mutate only the cats hook entries and rewrite everything else — unrelated
// keys, and their original order — untouched. encoding/json's map[string]any
// loses order, so JSON documents are decoded off the token stream into a small
// tree whose objects remember insertion order.
//
// Value vocabulary (mirrors serde_json::Value):
//
//	nil, bool, string, json.Number   — scalars (numbers keep their raw text)
//	int64                            — numbers this package creates (timeouts)
//	[]any                            — arrays
//	*jsonObject                      — objects, key order preserved
//
// Marshalling is pretty-printed with 2-space indent like Rust's
// serde_json::to_string_pretty, with no trailing newline.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// jsonObject is a JSON object that iterates in insertion order.
type jsonObject struct {
	keys []string
	vals map[string]any
}

func newJSONObject() *jsonObject {
	return &jsonObject{vals: make(map[string]any)}
}

func (o *jsonObject) Get(key string) (any, bool) {
	v, ok := o.vals[key]
	return v, ok
}

// Set inserts or replaces; a new key is appended at the end (map entry
// semantics, matching serde_json's preserve_order Map).
func (o *jsonObject) Set(key string, v any) {
	if _, ok := o.vals[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.vals[key] = v
}

func (o *jsonObject) Delete(key string) {
	if _, ok := o.vals[key]; !ok {
		return
	}
	delete(o.vals, key)
	for i, k := range o.keys {
		if k == key {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

// parseJSONDocument decodes one complete JSON document, rejecting trailing
// content, into the order-preserving value tree.
func parseJSONDocument(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeJSONValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected content after top-level JSON value")
	}
	return v, nil
}

func decodeJSONValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return decodeJSONFrom(dec, tok)
}

func decodeJSONFrom(dec *json.Decoder, tok json.Token) (any, error) {
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil // nil, bool, string, or json.Number
	}
	switch delim {
	case '{':
		obj := newJSONObject()
		for {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			if d, ok := keyTok.(json.Delim); ok && d == '}' {
				return obj, nil
			}
			key, ok := keyTok.(string)
			if !ok {
				return nil, fmt.Errorf("invalid object key token %v", keyTok)
			}
			val, err := decodeJSONValue(dec)
			if err != nil {
				return nil, err
			}
			obj.Set(key, val)
		}
	case '[':
		arr := []any{}
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			if d, ok := tok.(json.Delim); ok && d == ']' {
				return arr, nil
			}
			val, err := decodeJSONFrom(dec, tok)
			if err != nil {
				return nil, err
			}
			arr = append(arr, val)
		}
	default:
		return nil, fmt.Errorf("unexpected delimiter %v", delim)
	}
}

// marshalJSONPretty renders the value tree with 2-space indentation, matching
// serde_json::to_string_pretty (no trailing newline; empty containers stay on
// one line).
func marshalJSONPretty(v any) []byte {
	var buf bytes.Buffer
	writeJSONValue(&buf, v, 0)
	return buf.Bytes()
}

func writeJSONValue(buf *bytes.Buffer, v any, indent int) {
	switch val := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if val {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		writeJSONString(buf, val)
	case json.Number:
		buf.WriteString(val.String())
	case int64:
		buf.WriteString(strconv.FormatInt(val, 10))
	case []any:
		if len(val) == 0 {
			buf.WriteString("[]")
			return
		}
		buf.WriteString("[\n")
		for i, item := range val {
			buf.WriteString(strings.Repeat("  ", indent+1))
			writeJSONValue(buf, item, indent+1)
			if i < len(val)-1 {
				buf.WriteByte(',')
			}
			buf.WriteByte('\n')
		}
		buf.WriteString(strings.Repeat("  ", indent))
		buf.WriteByte(']')
	case *jsonObject:
		if len(val.keys) == 0 {
			buf.WriteString("{}")
			return
		}
		buf.WriteString("{\n")
		for i, key := range val.keys {
			buf.WriteString(strings.Repeat("  ", indent+1))
			writeJSONString(buf, key)
			buf.WriteString(": ")
			writeJSONValue(buf, val.vals[key], indent+1)
			if i < len(val.keys)-1 {
				buf.WriteByte(',')
			}
			buf.WriteByte('\n')
		}
		buf.WriteString(strings.Repeat("  ", indent))
		buf.WriteByte('}')
	default:
		// Unreachable by construction; render defensively via encoding/json.
		b, err := json.Marshal(val)
		if err != nil {
			buf.WriteString("null")
			return
		}
		buf.Write(b)
	}
}

// writeJSONString escapes like serde_json: quote, backslash, the short control
// escapes, and \u00xx for remaining control characters. Notably it does NOT
// HTML-escape < > & the way encoding/json's default encoder does.
func writeJSONString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(buf, `\u%04x`, r)
			} else {
				buf.WriteRune(r)
			}
		}
	}
	buf.WriteByte('"')
}

// jsonString returns the string form of a value the way Rust's Value::as_str
// does: only genuine JSON strings match.
func jsonString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func objField(entry any, key string) (any, bool) {
	obj, ok := entry.(*jsonObject)
	if !ok {
		return nil, false
	}
	return obj.Get(key)
}

func objFieldString(entry any, key string) (string, bool) {
	v, ok := objField(entry, key)
	if !ok {
		return "", false
	}
	return jsonString(v)
}
