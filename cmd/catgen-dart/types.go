package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// This file is the reflect half of the hybrid: the field set, the JSON tags and
// omitempty, embedded-struct flattening, and the Go kinds. It resolves type
// ALIASES for free, which is why reflection is here at all —
// The vocabulary and the messages share package wire now, and a pure
// go/ast walk would have to chase every one of them by hand.
//
// The output is a registry of classDefs: a Dart-shaped description that the
// emitter turns into source without ever touching reflect again.

// classDef is one Dart class to emit.
type classDef struct {
	dart   string       // Dart class name
	key    string       // "<pkgpath>.<GoName>", the identity used for overrides
	rt     reflect.Type // the Go struct
	doc    string
	disc   string // the JSON "t" value when this is a message envelope; "" otherwise
	fields []fieldDef
	embeds []embedDef
	// file is the generated file this class lands in, assigned at discovery: a
	// class is written wherever it was first reached. The two root sets share no
	// types, so nothing is arbitrary about the split in practice.
	file string
	// deprecated, when non-empty, is the reason emitted as @Deprecated. Picked up
	// from Go's own "Deprecated:" doc convention, or declared here as a Dart-side
	// policy (Resize, which a viewer must never send).
	deprecated string
}

// fieldDef is one JSON-visible field, already flattened out of any embedding.
type fieldDef struct {
	dart      string       // Dart field name (lowerCamel of the JSON tag)
	json      string       // the wire key
	rt        reflect.Type // the field's type, pointer already stripped
	ptr       bool         // the Go field was a pointer ⇒ the Dart field is nullable
	omitempty bool
	doc       string
	// embed names the embedded Dart class this field was flattened out of, so
	// the emitter can group the constructor arguments back together.
	embed string
}

// embedDef records one embedded struct that was flattened. The emitter turns it
// into a getter (`PaneMeta get paneMeta`) so a caller can still pass the group
// around as a unit — Go's embedding is a wire-level flattening, not a modelling
// decision, and re-losing the grouping in Dart would be a downgrade.
type embedDef struct {
	dart   string // the embedded class's Dart name
	getter string // lowerCamel accessor
	fields []fieldDef
}

// registry accumulates every class reachable from the roots, and enforces that
// two Go types never land on the same Dart name.
type registry struct {
	src     *source
	classes map[string]*classDef // by Dart name
	byKey   map[string]*classDef // by "<pkgpath>.<GoName>"
	order   []string             // Dart names, discovery order
	curFile string               // which generated file newly-discovered classes land in
}

func newRegistry(src *source) *registry {
	return &registry{src: src, classes: map[string]*classDef{}, byKey: map[string]*classDef{}}
}

// dartNameOverrides renames a generated class. Two reasons, both hard failures
// otherwise: a clash with dart:core (Error), and two distinct Go types that
// once shared a name across packages (the layout-chrome WorkspaceInfo/
// TabInfo versus the workspace.list/tab.list rows, since renamed WorkspaceEntry/
// TabEntry in Go, which is why those two entries are gone).
//
// Nothing here is cosmetic. `ensure` fails the generator on any unlisted clash,
// so this table can only ever shrink by deleting a Go type, never by neglect.
var dartNameOverrides = map[string]string{
	"github.com/rohanthewiz/cats/wire.Error":  "ErrorMsg",
	"github.com/rohanthewiz/cats/wire.Record": "RecordMsg",
}

// dartDeprecated marks a class deprecated on the Dart side only. Resize is the
// whole point: layer 3 of the four-layer "never resize the desktop" guard is
// analysis_options.yaml promoting deprecated_member_use to an error, which only
// bites if the generator says so — Go has no reason to deprecate Resize, since
// the browser sends it legitimately.
var dartDeprecated = map[string]string{
	"github.com/rohanthewiz/cats/wire.Resize": "" +
		"A cats mobile client is a viewer: it never reshapes the session grid, " +
		"because the session has one grid shared by every connection and a phone " +
		"announcing its own size would reflow the desktop's panes. CatsConnection " +
		"refuses to send this.",
}

// reservedDart is the set of Dart names a generated class may not take. dart:core
// is implicitly imported into every library, so shadowing one of these produces
// code that compiles but misbehaves at a distance (`on Error catch` resolving to
// a wire message). Object's own members are here too: a FIELD named hashCode or
// runtimeType would not compile at all.
var reservedDart = map[string]bool{
	"BigInt": true, "Comparable": true, "DateTime": true, "Deprecated": true,
	"Duration": true, "Enum": true, "Error": true, "Exception": true, "Expando": true,
	"Function": true, "Future": true, "Invocation": true, "Iterable": true,
	"Iterator": true, "List": true, "Map": true, "MapEntry": true, "Match": true,
	"Never": true, "Null": true, "Object": true, "Pattern": true, "Record": true,
	"RegExp": true, "Runes": true, "Set": true, "Sink": true, "StackTrace": true,
	"Stopwatch": true, "Stream": true, "String": true, "StringBuffer": true,
	"StringSink": true, "Symbol": true, "Type": true, "Uri": true,
	// Object members — illegal as field names.
	"hashCode": true, "runtimeType": true, "toString": true, "noSuchMethod": true,
	// Reserved words that could plausibly arrive as a JSON tag.
	"class": true, "const": true, "default": true, "extends": true,
	"final": true, "for": true, "if": true, "in": true, "new": true,
	"return": true, "super": true, "switch": true, "this": true, "throw": true,
	"true": true, "false": true, "null": true, "var": true, "void": true, "while": true,
	"with": true,
	// Names the generator itself emits. A Go type that took one of these would
	// produce a duplicate declaration in the same Dart library — a compile error
	// far from its cause, so it is caught here instead.
	"AgentState": true, "Caps": true, "CatsCommands": true, "CatsCommandTransport": true,
	"CellAttr": true, "CmdName": true, "CommandSpec": true, "KeyKind": true,
	"Mods": true, "MouseButton": true, "MouseKind": true, "MsgType": true, "NavDir": true,
	"Rect": true, "RowCol": true, "SplitDir": true,
}

var rawMessageType = reflect.TypeOf(json.RawMessage(nil))

// ensure registers rt (a struct) and everything it reaches, returning the class.
// disc is the JSON "t" value when rt is a message envelope.
func (r *registry) ensure(rt reflect.Type, disc string) (*classDef, error) {
	key := typeKey(rt)
	if c, ok := r.byKey[key]; ok {
		if disc != "" && c.disc == "" {
			c.disc = disc
		}
		return c, nil
	}

	name := rt.Name()
	if o, ok := dartNameOverrides[key]; ok {
		name = o
	}
	if reservedDart[name] {
		return nil, fmt.Errorf("%s maps to Dart %q, which dart:core already defines — "+
			"add a dartNameOverrides entry", key, name)
	}
	if prev, ok := r.classes[name]; ok {
		return nil, fmt.Errorf("%s and %s both map to Dart class %q — "+
			"add a dartNameOverrides entry for one of them", prev.key, key, name)
	}

	td := r.src.types[key]
	c := &classDef{dart: name, key: key, rt: rt, disc: disc, file: r.curFile}
	if td != nil {
		c.doc = td.doc
	}
	if reason, ok := dartDeprecated[key]; ok {
		c.deprecated = reason
	} else if why, ok := goDeprecation(c.doc); ok {
		c.deprecated = why
	}

	// Register before descending: a struct that reached itself (none today, but
	// a recursive tree type would) must find the in-progress class, not recurse.
	r.classes[name] = c
	r.byKey[key] = c
	r.order = append(r.order, name)

	if err := r.collectFields(c, rt, ""); err != nil {
		return nil, err
	}
	return c, nil
}

// collectFields walks one struct's JSON-visible fields, flattening embedded
// structs exactly as encoding/json does. embed is the Dart name of the embedded
// class currently being flattened ("" at the top level).
func (r *registry) collectFields(c *classDef, rt reflect.Type, embed string) error {
	td := r.src.types[typeKey(rt)]
	for i := range rt.NumField() {
		f := rt.Field(i)
		if f.PkgPath != "" { // unexported: invisible to encoding/json
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		omitempty := strings.Contains(","+opts+",", ",omitempty,")

		if f.Anonymous && name == "" {
			// Embedded struct with no tag: encoding/json promotes its fields.
			sub, err := r.ensure(f.Type, "")
			if err != nil {
				return err
			}
			before := len(c.fields)
			if err := r.collectFields(c, f.Type, sub.dart); err != nil {
				return err
			}
			c.embeds = append(c.embeds, embedDef{
				dart:   sub.dart,
				getter: lowerFirst(sub.dart),
				fields: append([]fieldDef(nil), c.fields[before:]...),
			})
			continue
		}
		if name == "" {
			name = f.Name
		}

		// The "t" discriminator is not a field in Dart: it is a fixed property of
		// the class, and making a caller set it is an invitation to set it wrong.
		if name == "t" && f.Name == "T" && embed == "" {
			continue
		}

		ft, ptr := f.Type, false
		if ft.Kind() == reflect.Pointer {
			ft, ptr = ft.Elem(), true
		}
		if err := r.reach(ft); err != nil {
			return fmt.Errorf("%s.%s: %w", c.dart, f.Name, err)
		}

		dartField := lowerCamel(name)
		if reservedDart[dartField] {
			return fmt.Errorf("%s.%s: JSON key %q maps to Dart identifier %q, which is reserved",
				c.dart, f.Name, name, dartField)
		}
		doc := ""
		if td != nil {
			doc = td.fields[f.Name]
		}
		c.fields = append(c.fields, fieldDef{
			dart: dartField, json: name, rt: ft, ptr: ptr,
			omitempty: omitempty, doc: doc, embed: embed,
		})
	}
	return nil
}

// reach registers any struct types nested inside t (slice/map elements included)
// so the emitter finds a class for every field it must decode.
func (r *registry) reach(t reflect.Type) error {
	switch {
	case t == rawMessageType, isByteSlice(t), namedArray(t) != nil:
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		_, err := r.ensure(t, "")
		return err
	case reflect.Slice, reflect.Array:
		return r.reach(deref(t.Elem()))
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return fmt.Errorf("map key %s is not a string; JSON objects are string-keyed", t.Key())
		}
		return r.reach(deref(t.Elem()))
	}
	return nil
}

// --- fixed-size arrays ---------------------------------------------------------

// arrayClass names the components of a fixed-size Go array so it becomes a real
// Dart class instead of a List. `panes[i].rect[2]` is unreadable and
// `anchor[0]` is worse — one is a width, the other a row, and neither index says
// so. The wire form stays the array; only the Dart surface gains names.
type arrayClass struct {
	dart       string
	components []string
	doc        string
	numeric    bool // true ⇒ int components
}

// arrayClasses is keyed by reflect.Type.String(), which is stable and covers
// both the named case (wire.Rect) and the unnamed one ([2]uint32 in
// ReadParams, where there is no Go type to hang a name on).
var arrayClasses = map[string]*arrayClass{
	"wire.Rect": {
		dart:       "Rect",
		components: []string{"x", "y", "w", "h"},
		numeric:    true,
		doc: "A cell rectangle. On the wire it is the bare array [x, y, w, h];\n" +
			"the names exist because rect[2] does not say \"width\".",
	},
	"[2]uint32": {
		dart:       "RowCol",
		components: []string{"row", "col"},
		numeric:    true,
		doc: "A screen-buffer coordinate. On the wire it is the bare array\n" +
			"[row, col], in ABSOLUTE buffer coordinates — row counts from the top\n" +
			"of scrollback, not from the top of the viewport. Derive it from the\n" +
			"pane's Scroll.",
	},
}

// namedArray reports the arrayClass for t, or nil when t is not one.
func namedArray(t reflect.Type) *arrayClass {
	if t.Kind() != reflect.Array {
		return nil
	}
	ac := arrayClasses[t.String()]
	if ac != nil && len(ac.components) != t.Len() {
		return nil // shape changed in Go; fall through and fail loudly at emit
	}
	return ac
}

// usedArrayClasses returns the array classes reached by the classes of one
// generated file, in a stable order. Scoping by file matters: RowCol is reached
// only from ReadParams and Rect only from the layout chrome, so an unscoped
// answer would define both in both files and neither would compile.
//
// Placement follows the same first-reached rule as a class: an array class used
// by BOTH files is emitted only in wire, because commands.g.dart imports
// wire.g.dart and a second declaration of Rect would be a duplicate, not a
// second copy. Nothing shares one today (Rect is wire-only, RowCol
// commands-only), which is exactly why the rule has to be written down — the
// breakage would arrive with the first shared field and read as a Dart error
// nowhere near its cause.
func (r *registry) usedArrayClasses(file string) []*arrayClass {
	seen := map[string]*arrayClass{}
	for _, c := range r.classes {
		if c.file != file {
			continue
		}
		for _, f := range c.fields {
			if ac := namedArray(f.rt); ac != nil {
				seen[ac.dart] = ac
			}
			if f.rt.Kind() == reflect.Slice || f.rt.Kind() == reflect.Map {
				if ac := namedArray(deref(f.rt.Elem())); ac != nil {
					seen[ac.dart] = ac
				}
			}
		}
	}
	if file != fileWire {
		// Subtract whatever wire already declares; see the placement note above.
		for _, c := range r.classes {
			if c.file != fileWire {
				continue
			}
			for _, f := range c.fields {
				if ac := namedArray(f.rt); ac != nil {
					delete(seen, ac.dart)
				}
				if f.rt.Kind() == reflect.Slice || f.rt.Kind() == reflect.Map {
					if ac := namedArray(deref(f.rt.Elem())); ac != nil {
						delete(seen, ac.dart)
					}
				}
			}
		}
	}
	out := make([]*arrayClass, 0, len(seen))
	for _, name := range sortedKeys(seen) {
		out = append(out, seen[name])
	}
	return out
}

// checkFileDeps enforces the one invariant the commands→wire import rests on:
// no class in wire.g.dart may reference a class in commands.g.dart.
//
// Discovery order already guarantees it — messageRoots runs to completion
// before commandRoots, so every type reachable from a wire message is tagged
// `wire` before a command is looked at, and a shared type is therefore always
// on the wire side. This function exists because that guarantee lives in the
// ORDER OF TWO CALLS in generate(), which a future edit could swap without any
// visible signal. The failure it would cause is an import cycle in Dart,
// reported by a tool cats does not run.
func (r *registry) checkFileDeps() error {
	for _, name := range r.order {
		c := r.classes[name]
		if c.file != fileWire {
			continue
		}
		for _, f := range c.fields {
			for _, ref := range r.structRefs(f.rt) {
				if ref.file == fileWire {
					continue
				}
				return fmt.Errorf("wire class %s references %s, which is emitted into "+
					"%s.g.dart — wire.g.dart cannot import it without a cycle. "+
					"Discovery order (messageRoots before commandRoots) is supposed to "+
					"make this impossible; check generate()", c.dart, ref.dart, ref.file)
			}
		}
	}
	return nil
}

// structRefs returns the classes a field's type mentions: the struct itself, or
// the element type of a slice/map. Pointers are already stripped by the caller
// for the top level, not for elements.
func (r *registry) structRefs(t reflect.Type) []*classDef {
	if namedArray(t) != nil || t == rawMessageType || isByteSlice(t) {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		if c, ok := r.byKey[typeKey(t)]; ok {
			return []*classDef{c}
		}
	case reflect.Slice, reflect.Array, reflect.Map:
		return r.structRefs(deref(t.Elem()))
	}
	return nil
}

// --- helpers -------------------------------------------------------------------

func typeKey(t reflect.Type) string { return t.PkgPath() + "." + t.Name() }

func deref(t reflect.Type) reflect.Type {
	if t.Kind() == reflect.Pointer {
		return t.Elem()
	}
	return t
}

func isByteSlice(t reflect.Type) bool {
	return t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 && t != rawMessageType
}

// goDeprecation extracts the reason from Go's "Deprecated:" doc convention.
func goDeprecation(doc string) (string, bool) {
	idx := strings.Index(doc, "Deprecated:")
	if idx < 0 {
		return "", false
	}
	return strings.Join(strings.Fields(doc[idx+len("Deprecated:"):]), " "), true
}

// lowerCamel turns a snake_case JSON key into a Dart identifier: def_fg → defFg,
// cell_w_px → cellWPx.
func lowerCamel(s string) string {
	parts := strings.Split(s, "_")
	var b strings.Builder
	for i, p := range parts {
		if p == "" {
			continue
		}
		if i == 0 {
			b.WriteString(p)
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(p[1:])
	}
	return b.String()
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
