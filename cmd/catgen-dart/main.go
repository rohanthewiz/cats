// Command catgen-dart generates the cats mobile client's Dart wire layer from
// the Go definitions that already exist in this repo, so the two can never
// drift silently.
//
// # Why generated, and why hybrid
//
// The protocol is 39 message shapes and 47 commands with their params and
// results. Hand-porting that to Dart is ~2500 lines nobody would keep current;
// the first field added to PaneFrame would put the phone quietly out of sync
// with the server, and the failure mode is a decode that succeeds with a
// missing value rather than an error anyone sees.
//
// Neither reflection nor go/ast suffices alone:
//
//	reflect  →  field set, JSON tags, omitempty, embedding, ALIASES, Go kinds
//	go/ast   →  doc comments, const-block names and values
//
// The vocabulary and the messages share package wire, whose embedded structs
// and typed constants a pure AST walk would have to chase by hand; and the doc
// comments — the best documentation in this repo — are invisible to reflect.
// So: both.
//
// # The drift gate
//
// The output is committed under testdata/golden and diffed by this package's
// own test, which runs in `make check`'s untagged `test` target. Adding a field
// to a wire struct without regenerating fails the CATS build, not the app's —
// the same rigor TestCommandSpecsRouted applies to the command table.
//
// # Usage
//
//	go run ./cmd/catgen-dart -out ../cats-mobile/packages/catsproto/lib/src/generated
//	go run ./cmd/catgen-dart -out <dir> -flutter-root "$FLUTTER_ROOT"   # also keys.g.dart
//	go run ./cmd/catgen-dart -check                                     # golden diff only
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/rohanthewiz/cats/wire"
)

const modPath = "github.com/rohanthewiz/cats"

// Package import paths, spelled once.
const (
	pkgWire = modPath + "/wire"
	pkgOrch = modPath + "/internal/orchestration"
)

// sourceDirs are the package directories go/ast reads, repo-root relative.
var sourceDirs = []string{"wire", "internal/orchestration"}

func main() {
	out := flag.String("out", "", "directory to write the generated Dart into")
	flutterRoot := flag.String("flutter-root", "", "Flutter SDK root; enables keys.g.dart")
	check := flag.Bool("check", false, "print the generated files' names and sizes without writing")
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fail(err)
	}
	files, err := generate(root, *flutterRoot)
	if err != nil {
		fail(err)
	}
	if *check || *out == "" {
		for _, name := range sortedKeys(files) {
			fmt.Printf("%-16s %6d bytes\n", name, len(files[name]))
		}
		if *out == "" && !*check {
			fail(fmt.Errorf("-out is required (or pass -check)"))
		}
		return
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}
	for _, name := range sortedKeys(files) {
		path := filepath.Join(*out, name)
		if err := os.WriteFile(path, []byte(files[name]), 0o644); err != nil {
			fail(err)
		}
		fmt.Printf("wrote %s (%d bytes)\n", path, len(files[name]))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "catgen-dart:", err)
	os.Exit(1)
}

// repoRoot walks up from the working directory to the module root, so the
// generator works from `go run ./cmd/catgen-dart` at the top and from `go test`
// inside the package directory alike.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			if strings.Contains(string(b), "module "+modPath) {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod for %s above the working directory", modPath)
		}
		dir = parent
	}
}

// generate produces every output file, keyed by filename. flutterRoot may be
// empty, in which case keys.g.dart is not regenerated — its input is a Flutter
// SDK data file that changes only on an SDK upgrade, so requiring Flutter to
// build cats would be a real cost for no drift protection. The committed table
// is still validated on every run: see TestGeneratedKeyCodesParse.
func generate(root, flutterRoot string) (map[string]string, error) {
	src, err := loadSource(root, modPath, sourceDirs)
	if err != nil {
		return nil, err
	}
	reg := newRegistry(src)

	wireRoots, err := messageRoots(src, reg)
	if err != nil {
		return nil, err
	}
	cmdRoots, err := commandRoots(reg)
	if err != nil {
		return nil, err
	}
	// Both root sets are in; the file tags are final. Check them before a single
	// byte is emitted, so a broken split fails here rather than in whatever tool
	// eventually reads the output.
	if err := reg.checkFileDeps(); err != nil {
		return nil, err
	}

	files := map[string]string{}
	defer func() {
		// Every emitter ends its last block with a blank line. Dart files end in
		// exactly one newline, so trim it once here rather than in five places.
		for name, body := range files {
			files[name] = strings.TrimRight(body, "\n") + "\n"
		}
	}()
	if files["codec.g.dart"], err = emitCodec(); err != nil {
		return nil, err
	}
	if files["wire.g.dart"], err = emitWire(src, reg, wireRoots); err != nil {
		return nil, err
	}
	if files["commands.g.dart"], err = emitCommands(src, reg, cmdRoots); err != nil {
		return nil, err
	}
	if files["attrs.g.dart"], err = emitAttrs(src, reg); err != nil {
		return nil, err
	}
	if flutterRoot != "" {
		if files["keys.g.dart"], err = emitKeys(flutterRoot); err != nil {
			return nil, err
		}
	}
	return files, nil
}

// --- roots ---------------------------------------------------------------------

// message is one wire message: its Dart class and the direction it travels.
type message struct {
	class *classDef
	up    bool
}

// messageRoots discovers every message type WITHOUT a hand-written list, by
// walking wire's Type const block and asking the real decoders what
// each value decodes to. A type added to the block and wired into DecodeDown
// therefore reaches Dart with no edit here — and one added to the block but
// never routed fails generation, which is the same bidirectional check
// TestCommandSpecsRouted makes for commands.
func messageRoots(src *source, reg *registry) ([]message, error) {
	names := src.groups[pkgWire+".MsgWelcome"]
	if len(names) == 0 {
		return nil, fmt.Errorf("wire's Type const block was not found; " +
			"did the first constant stop being MsgWelcome?")
	}
	var out []message
	for _, name := range names {
		c := src.consts[pkgWire+"."+name]
		if c == nil {
			return nil, fmt.Errorf("wire.%s did not resolve to a literal", name)
		}
		disc := strings.Trim(c.dart, "'")
		envelope := []byte(fmt.Sprintf(`{"t":%q}`, disc))

		var v any
		up := false
		if down, err := wire.DecodeDown(envelope); err == nil {
			v = down
		} else if u, err := wire.DecodeUp(envelope); err == nil {
			v, up = u, true
		} else {
			return nil, fmt.Errorf("wire.%s (%q) is in the Type block but neither "+
				"DecodeUp nor DecodeDown routes it", name, disc)
		}
		reg.curFile = fileWire
		cls, err := reg.ensure(reflect.TypeOf(v).Elem(), disc)
		if err != nil {
			return nil, err
		}
		out = append(out, message{class: cls, up: up})
	}
	return out, nil
}

// commandCall is one §7 command with its Dart-side shapes resolved.
type commandCall struct {
	spec   wire.CommandSpec
	dart   string // Dart method / constant name
	params *classDef
	result *classDef
}

// commandRoots resolves wire.CommandSpecs() — the same table `catctl commands`
// prints — into Dart classes. The table is the point: 47 hand-written call
// sites become 47 generated typed methods, and the "silently dropped without an
// id" rule arrives as a documented property rather than a bug hunt.
func commandRoots(reg *registry) ([]commandCall, error) {
	specs := wire.CommandSpecs()
	out := make([]commandCall, 0, len(specs))
	reg.curFile = fileCommands
	for _, spec := range specs {
		call := commandCall{spec: spec, dart: commandIdent(spec.Name)}
		if spec.Params != nil {
			c, err := reg.ensure(reflect.TypeOf(spec.Params), "")
			if err != nil {
				return nil, fmt.Errorf("%s params: %w", spec.Name, err)
			}
			call.params = c
		}
		if spec.Result != nil {
			c, err := reg.ensure(reflect.TypeOf(spec.Result), "")
			if err != nil {
				return nil, fmt.Errorf("%s result: %w", spec.Name, err)
			}
			call.result = c
		}
		out = append(out, call)
	}
	return out, nil
}

// commandIdent turns a wire command name into a Dart identifier:
// "pane.wait_for_output" → paneWaitForOutput.
func commandIdent(name string) string {
	return lowerCamel(strings.ReplaceAll(name, ".", "_"))
}

// --- wire.g.dart ------------------------------------------------------------------

func emitWire(src *source, reg *registry, msgs []message) (string, error) {
	e := &emitter{src: src, reg: reg}
	e.header("The WS9 browser-facing protocol: every message shape, both directions.",
		[]string{"wire/{proto,up,down}.go"})

	e.p("// ignore_for_file: deprecated_member_use_from_same_package")
	e.nl()
	e.p("import 'dart:convert';")
	e.p("import 'dart:typed_data';")
	e.nl()
	e.p("import 'codec.g.dart';")
	e.nl()
	e.p("// A note for the app: Key, Theme, Title and Image also name Flutter widgets.")
	e.p("// That collision is intentional — these are the SERVER's names, and renaming")
	e.p("// them here would put the wire and the code that reads it out of step. Import")
	e.p("// this package with a prefix in Flutter code:")
	e.p("//")
	e.p("//     import 'package:catsproto/catsproto.dart' as cats;")
	e.nl()

	if err := e.topConst(pkgWire, "ProtocolVersion", "kProtocolVersion", "int",
		"Sent in `init.v` and echoed in `welcome.v`. catway requires EXACT equality\n"+
			"and closes the socket on a mismatch, so a bump means \"app update required\"\n"+
			"— never a silent degrade."); err != nil {
		return "", err
	}
	if err := e.topConst(pkgWire, "UsagePctUnknown", "kUsagePctUnknown", "double",
		"Render a window at this percentage as TEXT (its `detail`), never as a bar at\n"+
			"zero: a missing denominator must read as missing, not as \"nothing spent\"."); err != nil {
		return "", err
	}

	groups := []constGroup{
		{class: "MsgType", pkg: pkgWire, first: "MsgWelcome", strip: "Msg", typ: "String",
			doc: "Every `t` discriminator, both directions. Unknown values must be IGNORED\n" +
				"rather than treated as errors — that rule is what lets the server add a\n" +
				"message type without a version bump."},
		{class: "KeyKind", pkg: pkgWire, first: "KeyDown", strip: "Key", typ: "String",
			doc: "Key event kinds (`key.kind`)."},
		{class: "Mods", pkg: pkgWire, first: "ModShift", strip: "Mod", typ: "int",
			doc: "Modifier bits for `key.mods` / `mouse.mods`."},
		{class: "MouseKind", pkg: pkgWire, first: "MouseDown", strip: "Mouse", typ: "String",
			doc: "Pointer event kinds (`mouse.kind`)."},
		{class: "MouseButton", pkg: pkgWire, first: "BtnLeft", strip: "Btn", typ: "int",
			doc: "Pointer buttons (`mouse.btn`)."},
		{class: "AgentState", pkg: pkgWire, first: "AgentIdle", strip: "Agent", typ: "String",
			doc: "Agent activity states. The roster groups by these, and `seen: false`\n" +
				"renders as \"Done\" — the \"your agent finished while you were away\" signal."},
		{class: "Caps", pkg: pkgWire, first: "CapViewer", strip: "Cap", typ: "String",
			doc: "Optional server behaviours, advertised in `welcome.caps`.\n" +
				"\n" +
				"Check these rather than assuming. A server that ignored `key.pane` looks\n" +
				"exactly like one that honoured it, and guessing wrong puts keystrokes in\n" +
				"somebody else's pane."},
	}
	for _, g := range groups {
		if err := e.constGroup(g); err != nil {
			return "", err
		}
	}

	// Array stand-ins first: the message classes reference them.
	for _, ac := range reg.usedArrayClasses(fileWire) {
		e.arrayClass(ac)
	}

	var wireClasses []*classDef
	for _, c := range reg.order {
		if cl := reg.classes[c]; cl.file == fileWire {
			wireClasses = append(wireClasses, cl)
		}
	}
	for _, c := range sortedClasses(wireClasses) {
		e.class(c)
	}

	emitDecoder(e, msgs, false)
	emitDecoder(e, msgs, true)
	return e.b.String(), nil
}

// emitDecoder writes decodeDown / decodeUp: the switch a client folds its
// session state through.
func emitDecoder(e *emitter, msgs []message, up bool) {
	name, dir, who := "decodeDown", "server → client", "server"
	if up {
		name, dir, who = "decodeUp", "client → server", "client"
	}
	e.p("/// Decodes one %s message into its class.", dir)
	e.p("///")
	e.p("/// Returns null for an unrecognised `t`. That is the protocol's rule, not a")
	e.p("/// convenience: a %s is free to add message types within a version, and a", who)
	e.p("/// client that threw on the first unknown one would drop the whole socket.")
	e.p("Object? %s(Map<String, Object?> j) {", name)
	e.p("  switch (j['t']) {")
	for _, m := range msgs {
		if m.up != up {
			continue
		}
		e.p("    case %s.type:", m.class.dart)
		if m.class.deprecated != "" {
			e.p("      // ignore: deprecated_member_use_from_same_package")
		}
		e.p("      return %s.fromJson(j);", m.class.dart)
	}
	e.p("  }")
	e.p("  return null;")
	e.p("}")
	e.nl()
}

// --- commands.g.dart --------------------------------------------------------------

func emitCommands(src *source, reg *registry, calls []commandCall) (string, error) {
	e := &emitter{src: src, reg: reg}
	e.header("The §7 command vocabulary: names, params, results, and a typed client.",
		[]string{"wire/vocab.go (wire.CommandSpecs)"})

	e.p("import 'dart:convert';")
	e.p("import 'dart:typed_data';")
	e.nl()
	e.p("import 'codec.g.dart';")
	// commands.g.dart depends on wire.g.dart, and the dependency can only point
	// this way. A class is written wherever it was FIRST reached, and every wire
	// root is discovered before the first command root — so a type used by both
	// (LedgerEntry, NotifyAction) always lands in wire, and a wire class can
	// never reference a commands class. That ordering is what makes this import
	// safe rather than the first half of a cycle; checkFileDeps enforces it.
	e.p("import 'wire.g.dart';")
	e.nl()
	e.p("// ignore_for_file: unused_import")
	e.nl()

	groups := []constGroup{
		{class: "SplitDir", pkg: pkgWire, first: "SplitH", strip: "Split", typ: "String",
			doc: "Split directions (`pane.split`)."},
		{class: "NavDir", pkg: pkgWire, first: "DirLeft", strip: "Dir", typ: "String",
			doc: "Cardinal directions (`pane.focus_direction`, `pane.swap`)."},
	}
	for _, g := range groups {
		if err := e.constGroup(g); err != nil {
			return "", err
		}
	}

	// Command names.
	e.p("/// Every §7 command name. The same vocabulary `catctl commands` prints and")
	e.p("/// the browser's `cmd` envelope carries — one table, two front ends.")
	e.p("///")
	e.p("/// Named CmdName rather than Cmd because `Cmd` is already the wire class for")
	e.p("/// the command ENVELOPE (wire.g.dart), and both live in one library.")
	e.p("abstract final class CmdName {")
	for i, c := range calls {
		if i > 0 {
			e.nl()
		}
		e.p("  static const String %s = %s;", c.dart, dartString(c.spec.Name))
	}
	e.p("}")
	e.nl()

	for _, ac := range reg.usedArrayClasses(fileCommands) {
		e.arrayClass(ac)
	}

	var cmdClasses []*classDef
	for _, name := range reg.order {
		if cl := reg.classes[name]; cl.file == fileCommands {
			cmdClasses = append(cmdClasses, cl)
		}
	}
	for _, c := range sortedClasses(cmdClasses) {
		e.class(c)
	}

	emitCommandSpecs(e, calls)
	emitCommandMixin(e, calls)
	return e.b.String(), nil
}

func emitCommandSpecs(e *emitter, calls []commandCall) {
	e.p("/// One §7 command as data, mirroring wire.CommandSpec.")
	e.p("class CommandSpec {")
	e.p("  const CommandSpec(")
	e.p("    this.name, {")
	e.p("    this.paramsRequired = false,")
	e.p("    this.replyRequired = false,")
	e.p("  });")
	e.nl()
	e.p("  final String name;")
	e.nl()
	e.p("  /// The command fails with \"bad params\" when called with none. The rest")
	e.p("  /// decode optionally, where absent params mean the zero value — a")
	e.p("  /// meaningful call (`pane.close` closes the focused pane).")
	e.p("  final bool paramsRequired;")
	e.nl()
	e.p("  /// The server SILENTLY DROPS this command when the caller cannot receive a")
	e.p("  /// result — a `cmd` sent with no `id`. It exists only to produce data, so")
	e.p("  /// an answer with nowhere to go is no answer. CatsCommands always sends an")
	e.p("  /// id, so this is a fact about the protocol, not a hazard of this client.")
	e.p("  final bool replyRequired;")
	e.p("}")
	e.nl()
	e.p("/// The full vocabulary, in the order `catctl commands` prints it.")
	e.p("const List<CommandSpec> kCommandSpecs = <CommandSpec>[")
	for _, c := range calls {
		var opts []string
		if c.spec.ParamsRequired {
			opts = append(opts, "paramsRequired: true")
		}
		if c.spec.ReplyRequired {
			opts = append(opts, "replyRequired: true")
		}
		if len(opts) == 0 {
			e.p("  CommandSpec(%s),", dartString(c.spec.Name))
			continue
		}
		e.p("  CommandSpec(%s, %s),", dartString(c.spec.Name), strings.Join(opts, ", "))
	}
	e.p("];")
	e.nl()
}

func emitCommandMixin(e *emitter, calls []commandCall) {
	e.p("/// What a generated command method needs from the connection.")
	e.p("///")
	e.p("/// Named `invoke`, not `call`: a Dart class with a `call` method becomes")
	e.p("/// callable as a function, and a connection object that can be invoked like")
	e.p("/// a closure is a surprise nobody asked for.")
	e.p("abstract interface class CatsCommandTransport {")
	e.p("  /// Sends [name] with [params], correlated by a generated id, and completes")
	e.p("  /// with the reply's `data` (null when the command returns none).")
	e.p("  ///")
	e.p("  /// Throws on an `ok: false` reply, on timeout, and on disconnect — every")
	e.p("  /// pending call must fail when the socket dies, or a leaked completer is a")
	e.p("  /// spinner that never stops.")
	e.p("  Future<Object?> invoke(String name, Object? params);")
	e.p("}")
	e.nl()
	e.p("/// The §7 vocabulary as typed methods. Mix into a CatsCommandTransport.")
	e.p("mixin CatsCommands implements CatsCommandTransport {")
	for i, c := range calls {
		if i > 0 {
			e.nl()
		}
		emitCommandMethod(e, c)
	}
	e.p("}")
	e.nl()
}

func emitCommandMethod(e *emitter, c commandCall) {
	// Signature.
	ret := "void"
	if c.result != nil {
		ret = c.result.dart
	}
	arg, pass := "", "null"
	switch {
	case c.params == nil:
		// parameterless
	case c.spec.ParamsRequired:
		arg = c.params.dart + " params"
		pass = "params.toJson()"
	default:
		arg = "[" + c.params.dart + "? params]"
		pass = "params?.toJson()"
	}

	e.p("  /// `%s`", c.spec.Name)
	if c.params != nil && !c.spec.ParamsRequired {
		e.p("  ///")
		e.p("  /// Params are optional: absent means the zero value, which is a real call.")
	}
	if c.spec.ReplyRequired {
		e.p("  ///")
		e.p("  /// Reply-gated server-side: a `cmd` with no id is dropped without running.")
		e.p("  /// This method always correlates, so it always runs.")
	}
	if c.result == nil {
		e.p("  Future<void> %s(%s) async {", c.dart, arg)
		e.p("    await invoke(CmdName.%s, %s);", c.dart, pass)
		e.p("  }")
		return
	}
	e.p("  Future<%s> %s(%s) async =>", ret, c.dart, arg)
	e.p("      %s.fromJson(asObj(await invoke(CmdName.%s, %s)));", ret, c.dart, pass)
}

// --- attrs.g.dart -------------------------------------------------------------

func emitAttrs(src *source, reg *registry) (string, error) {
	e := &emitter{src: src, reg: reg}
	e.header("The cell attribute bitmask carried in `cell.m`.",
		[]string{"internal/orchestration/protocol.go (the ratatui Modifier bits)"})
	e.p("// These constants are UNEXPORTED in Go — the reason this generator parses")
	e.p("// the source rather than reflecting over it. Retyping six bit patterns by")
	e.p("// hand is exactly the kind of transcription the renderer cannot afford to")
	e.p("// get wrong: a misplaced bit is not a crash, it is italics where there")
	e.p("// should be underline, forever.")
	e.nl()
	err := e.constGroup(constGroup{
		class: "CellAttr", pkg: pkgOrch, first: "modBold", strip: "mod", typ: "int",
		doc: "Attribute bits for `cell.m`. Test with `cell.m & CellAttr.bold != 0`.\n" +
			"\n" +
			"Reversed is a SWAP of fg and bg at paint time, not a colour of its own —\n" +
			"resolve it after the frame defaults have been applied, or a reversed cell\n" +
			"with omitted colours swaps two zeros and disappears.",
	})
	if err != nil {
		return "", err
	}
	return e.b.String(), nil
}

// --- codec.g.dart ---------------------------------------------------------------

// emitCodec writes the total-decoding helpers every generated fromJson calls.
// They are generated rather than hand-written in cats-mobile so the whole
// package regenerates as one unit — a helper's contract and the code that calls
// it cannot end up in different repos at different revisions.
func emitCodec() (string, error) {
	e := &emitter{}
	e.header("Decoding helpers shared by the generated wire and command classes.",
		[]string{"cmd/catgen-dart/main.go (emitCodec)"})
	e.p("%s", codecBody)
	return e.b.String(), nil
}

const codecBody = `import 'dart:convert';
import 'dart:typed_data';

// Every helper here is TOTAL: a missing key, a null, or a value of the wrong
// type yields the zero rather than throwing. That is not laziness about
// validation — it is the protocol's own rule turned into code. catway may add
// fields within a protocol version (§0.3 was deliberately additive precisely so
// old clients survive), and a phone that threw on one unexpected shape would
// drop the socket and every pane with it.
//
// Where a value's absence is MEANINGFUL rather than merely tolerated, the
// generated field is nullable and the *OrNull helpers are used instead.

String asString(Object? v) => v is String ? v : '';

String? asStringOrNull(Object? v) => v is String ? v : null;

int asInt(Object? v) => v is num ? v.toInt() : 0;

int? asIntOrNull(Object? v) => v is num ? v.toInt() : null;

double asDouble(Object? v) => v is num ? v.toDouble() : 0;

double? asDoubleOrNull(Object? v) => v is num ? v.toDouble() : null;

bool asBool(Object? v) => v is bool && v;

bool? asBoolOrNull(Object? v) => v is bool ? v : null;

/// A nested JSON object. Returns an empty map rather than null so a caller can
/// hand it straight to a generated fromJson, which fills in zeros.
Map<String, Object?> asObj(Object? v) =>
    v is Map ? v.cast<String, Object?>() : const <String, Object?>{};

/// Go's []byte is base64 on the wire (encoding/json's own rule).
Uint8List asBytes(Object? v) {
  if (v is! String || v.isEmpty) return Uint8List(0);
  try {
    return base64Decode(v);
  } on FormatException {
    return Uint8List(0);
  }
}

/// The lists are fixed-length: a decoded frame is read, never appended to, and
/// a growable list would allocate a second backing array for nothing. The one
/// place growth matters — the cell grid — is a typed array in the renderer, not
/// this.
List<T> asList<T>(Object? v, T Function(Object?) item) =>
    v is List ? List<T>.generate(v.length, (i) => item(v[i]), growable: false) : const [];

Map<String, T> asMap<T>(Object? v, T Function(Object?) item) {
  if (v is! Map) return const {};
  final out = <String, T>{};
  v.forEach((k, val) {
    if (k is String) out[k] = item(val);
  });
  return out;
}

/// Decodes one WebSocket text frame into its JSON object. Returns null for
/// anything that is not a JSON object — malformed input is a message to drop,
/// not a socket to close.
Map<String, Object?>? decodeFrame(String text) {
  try {
    final v = jsonDecode(text);
    return v is Map ? v.cast<String, Object?>() : null;
  } on FormatException {
    return null;
  }
}
`

// --- shared file tagging -------------------------------------------------------

// Which generated file a class belongs to. Assigned at discovery: a class is
// written wherever it was first reached, and commands.g.dart imports nothing
// from wire.g.dart because the two root sets share no types.
const (
	fileWire     = "wire"
	fileCommands = "commands"
)
