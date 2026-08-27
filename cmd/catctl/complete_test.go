package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/plugin"
)

// values pulls just the inserted text out of a candidate list.
func values(cands []candidate) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.value)
	}
	return out
}

// complete runs the engine over a command line. The last element is the word
// under the cursor, exactly as a shell hands it over.
func complete(words ...string) ([]string, string) {
	cands, directive := completeCatctl(words)
	return values(cands), directive
}

// The tests below deliberately avoid the live (control-socket) kinds: those
// depend on a running server, and a test that quietly passes because nothing
// answered would assert nothing at all. What is testable without a server is
// the routing — which slot of which verb asks for what — and that is covered by
// TestArgKindsMatchSynopsis plus the static kinds here.

func TestCompleteTopLevel(t *testing.T) {
	got, dir := complete("")
	if dir != dirNoFiles {
		t.Errorf("directive = %q, want %q", dir, dirNoFiles)
	}
	for _, want := range []string{"focus", "split", "plugin", "integration", "completion", "help"} {
		if !slices.Contains(got, want) {
			t.Errorf("bare completion missing %q", want)
		}
	}
	// The raw §7 table is the escape hatch, not the everyday path: it stays out
	// of an empty <TAB> and appears as soon as a prefix is typed.
	if slices.Contains(got, "pane.split") {
		t.Error("bare completion should not list raw §7 methods")
	}
	if got, _ := complete("pane."); !slices.Contains(got, "pane.split") {
		t.Errorf("typing a prefix should reach raw methods, got %v", got)
	}
}

func TestCompleteVerbArgs(t *testing.T) {
	cases := []struct {
		name  string
		words []string
		want  []string
	}{
		{"split direction", []string{"split", ""}, []string{"h", "v"}},
		{"swap direction", []string{"swap", ""}, []string{"left", "right", "up", "down"}},
		{"cycle direction", []string{"cycle", ""}, []string{"next", "prev"}},
		{"prefix filters", []string{"focus-dir", "l"}, []string{"left"}},
		// A flag before the verb must not shift the slot the operand lands in.
		{"global flag skipped", []string{"--json", "swap", ""}, []string{"left", "right", "up", "down"}},
		{"flag with a value skipped", []string{"--socket", "/tmp/x", "swap", ""}, []string{"left", "right", "up", "down"}},
		// Past the declared slots there is nothing to say.
		{"beyond the arg list", []string{"swap", "left", ""}, nil},
		{"unknown verb", []string{"nonesuch", ""}, nil},
	}
	for _, tc := range cases {
		got, _ := complete(tc.words...)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: complete(%v) = %v, want %v", tc.name, tc.words, got, tc.want)
		}
	}

	// split's second slot is a live pane, so what comes back depends on whether a
	// server is running — but it must never be the direction list a second time.
	got, _ := complete("split", "h", "")
	if slices.Contains(got, "h") || slices.Contains(got, "v") {
		t.Errorf("split's second slot offered the direction list again: %v", got)
	}
}

func TestCompleteFlags(t *testing.T) {
	got, _ := complete("--")
	for _, want := range []string{"--socket", "--params", "--json"} {
		if !slices.Contains(got, want) {
			t.Errorf("flag completion missing %q (got %v)", want, got)
		}
	}
	// A path-shaped flag's value hands off to the shell's own file completion.
	if _, dir := complete("--socket", ""); dir != dirFiles {
		t.Errorf("--socket value directive = %q, want %q", dir, dirFiles)
	}
	// …but the flag's value must not be mistaken for the verb.
	if got, _ := complete("--socket", "/tmp/x", "spl"); !slices.Equal(got, []string{"split"}) {
		t.Errorf("after a flag value, got %v, want [split]", got)
	}
}

func TestCompleteFamilies(t *testing.T) {
	if got, _ := complete("plugin", ""); !slices.Contains(got, "run") || !slices.Contains(got, "link") {
		t.Errorf("plugin subcommands = %v", got)
	}
	if _, dir := complete("plugin", "link", ""); dir != dirDirs {
		t.Errorf("plugin link directive = %q, want %q", dir, dirDirs)
	}
	if got, _ := complete("plugin", "install", "--"); !slices.Equal(got, []string{"--ref"}) {
		t.Errorf("plugin install flags = %v, want [--ref]", got)
	}
	// Each subcommand offers only its own flag: --all belongs to run, --ref to
	// install, and neither should surface on the other.
	if got, _ := complete("plugin", "run", "--"); !slices.Equal(got, []string{"--all"}) {
		t.Errorf("plugin run flags = %v, want [--all]", got)
	}
	if got, _ := complete("integration", "install", ""); !slices.Contains(got, "claude") {
		t.Errorf("integration targets = %v, want to include claude", got)
	}
	if got, _ := complete("completion", ""); !slices.Equal(got, []string{"bash", "zsh", "fish"}) {
		t.Errorf("completion shells = %v", got)
	}
	if got, _ := complete("probe", "--u"); !slices.Equal(got, []string{"--url"}) {
		t.Errorf("probe flags = %v, want [--url]", got)
	}
	// The word right after --url is its value, not another flag.
	if got, _ := complete("probe", "--url", ""); len(got) != 0 {
		t.Errorf("probe --url <value> = %v, want nothing to offer", got)
	}
	// Once the value is given, the next word is a flag slot again.
	if got, _ := complete("probe", "--url", "ws://x/ws", "--s"); !slices.Equal(got, []string{"--script"}) {
		t.Errorf("probe after a complete flag pair = %v, want [--script]", got)
	}
}

// A family is handed the raw tail after its verb, because its own flags are not
// in the global table: stripping --ref there would drop the flag and leave its
// value behind to be counted as an operand, shifting every later slot.
func TestFirstPositional(t *testing.T) {
	cases := []struct {
		words []string
		want  int
		ok    bool
	}{
		{[]string{"plugin", "install"}, 0, true},
		{[]string{"--json", "plugin"}, 1, true},
		{[]string{"--socket", "/tmp/s", "plugin"}, 2, true},
		{[]string{"--socket=/tmp/s", "plugin"}, 1, true},
		{[]string{"--json"}, 0, false},
		{nil, 0, false},
	}
	for _, tc := range cases {
		got, ok := firstPositional(tc.words, globalFlags)
		if got != tc.want || ok != tc.ok {
			t.Errorf("firstPositional(%v) = %d, %v; want %d, %v", tc.words, got, ok, tc.want, tc.ok)
		}
	}

	// The value of a value-taking flag is never mistaken for the verb, even when
	// it is spelled like one.
	if got, _ := complete("--socket", "plugin", "spl"); !slices.Equal(got, []string{"split"}) {
		t.Errorf("a flag value spelled like a verb = %v, want [split]", got)
	}
}

// Every <pane>/<num>/<id> in a synopsis should have a live candidate source
// behind it, and nothing else should claim one. This is the check that keeps the
// args table honest as verbs are added: the synopsis is what a reader trusts.
func TestArgKindsMatchSynopsis(t *testing.T) {
	// The placeholders each kind is allowed to sit under. Most are a single
	// spelling; a workspace is written out in full where the abbreviation would
	// be ambiguous (`tabs [workspace]` beside `ws <id>`).
	placeholder := map[argKind][]string{
		argPane:         {"pane"},
		argTab:          {"num"},
		argWorkspace:    {"id", "workspace"},
		argDirection:    {"left|right|up|down"},
		argSplitDir:     {"h|v"},
		argCycleDir:     {"prev"},
		argTheme:        {"name"},
		argDetachHost:   {"id"},
		argRunbook:      {"name"},
		argRecordAction: {"start|stop|cancel|status"},
		argFlagKind:     {"kind"},
	}
	for _, sc := range subcommands {
		// Operands in the synopsis, in order: <x> and [x] alike.
		var operands []string
		for _, f := range strings.Fields(sc.synopsis)[1:] {
			operands = append(operands, strings.Trim(f, "<>[]"))
		}
		if len(sc.args) > len(operands) {
			t.Errorf("%s: %d arg kinds for %d operands in %q", sc.verb, len(sc.args), len(operands), sc.synopsis)
			continue
		}
		for i, k := range sc.args {
			if k == argNone {
				continue
			}
			want, ok := placeholder[k]
			if !ok {
				t.Errorf("%s: arg %d has an unnamed kind %v", sc.verb, i+1, k)
				continue
			}
			if got := strings.TrimSuffix(operands[i], "..."); !slices.Contains(want, got) {
				t.Errorf("%s: arg %d completes one of %v but the synopsis calls it %q (%s)",
					sc.verb, i+1, want, got, sc.synopsis)
			}
		}
		for i, o := range operands {
			o = strings.TrimSuffix(o, "...")
			if o != "pane" && o != "num" {
				continue
			}
			if sc.argAt(i) == argNone {
				t.Errorf("%s: operand %d is <%s> but completes nothing (%s)", sc.verb, i+1, o, sc.synopsis)
			}
		}
	}
}

func TestSanitizeDesc(t *testing.T) {
	// A live pane title is arbitrary text; a tab or newline in it would read as a
	// new candidate on the wire.
	if got := sanitizeDesc("a\tb\nc  d"); got != "a b c d" {
		t.Errorf("sanitizeDesc = %q, want %q", got, "a b c d")
	}
	long := sanitizeDesc(strings.Repeat("é", 200))
	if r := []rune(long); len(r) != 72 || r[71] != '…' {
		t.Errorf("sanitizeDesc truncation = %d runes ending %q", len(r), string(r[len(r)-1]))
	}
}

// --all is positionless and must not be counted as an operand: after it, the
// next word is still the plugin id and the one after is still the action. This
// is the whole reason the flag is declared in pluginFlags rather than merely
// parsed in plugin.go — a flag the completer does not know about eats the slot
// it sits in, and `plugin run --all <TAB>` would then offer the *actions* of a
// plugin named "--all".
func TestCompletePluginRunAll(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	t.Setenv(plugin.DirEnvVar, root)
	dir := filepath.Join(root, "acme.demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `
id = "acme.demo"
version = "0.1.0"

[[actions]]
id = "ui"
title = "open the UI"
command = ["./bin/demo"]
`
	if err := os.WriteFile(filepath.Join(dir, plugin.ManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	// Slot 1 with the flag ahead of it: still the plugin id.
	if got, _ := complete("plugin", "run", "--all", ""); !slices.Contains(got, "acme.demo") {
		t.Errorf("id slot after --all = %v, want to include acme.demo", got)
	}
	// Slot 2: still the action, and the flag may trail as well as lead.
	if got, _ := complete("plugin", "run", "--all", "acme.demo", ""); !slices.Contains(got, "ui") {
		t.Errorf("action slot after --all = %v, want to include ui", got)
	}
	if got, _ := complete("plugin", "run", "acme.demo", "--all", ""); !slices.Contains(got, "ui") {
		t.Errorf("action slot with a trailing --all = %v, want to include ui", got)
	}
}

// A plugin's static entry is served by catctl itself: subcommands in the first
// word position, flags wherever the word starts with a dash.
func TestCompleteForBinaryStatic(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	t.Setenv(plugin.DirEnvVar, root)
	dir := filepath.Join(root, "acme.demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `
id = "acme.demo"
version = "0.1.0"

[[completions]]
binary = "demo"
subcommands = ["add", "list"]
flags = ["--force"]
`
	if err := os.WriteFile(filepath.Join(dir, plugin.ManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	bc, ok := plugin.LookupCompletion("demo")
	if !ok {
		t.Fatal("the plugin's completion entry was not found")
	}
	if bc.Completion.Dynamic() {
		t.Fatal("a subcommands/flags entry must not be treated as dynamic")
	}
	if !slices.Equal(bc.Completion.Subcommands, []string{"add", "list"}) {
		t.Errorf("subcommands = %v", bc.Completion.Subcommands)
	}
}

// The generated scripts must name this binary and register every plugin-declared
// command. They are shell code we cannot run here for every shell, so assert the
// contract each one depends on.
func TestCompletionScripts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugins")
	t.Setenv(plugin.DirEnvVar, root)
	dir := filepath.Join(root, "acme.demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `
id = "acme.demo"
version = "0.1.0"

[[completions]]
binary = "demo-tool"
command = ["./bin/demo", "__complete"]
`
	if err := os.WriteFile(filepath.Join(dir, plugin.ManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ name, body, register string }{
		{"bash", bashBody, bashRegister},
		{"zsh", zshBody, zshRegister},
		{"fish", fishBody, fishRegister},
	} {
		s := script(tc.body, tc.register)
		if strings.Contains(s, exePlaceholder) {
			t.Errorf("%s: the executable placeholder survived into the script", tc.name)
		}
		if !strings.Contains(s, shellQuote(self())) {
			t.Errorf("%s: script does not name this binary", tc.name)
		}
		if !strings.Contains(s, "demo-tool") {
			t.Errorf("%s: the plugin's command was not registered", tc.name)
		}
		// A function name derived from the command must be a legal shell
		// identifier — the dash in demo-tool is the case that would break it.
		if tc.name != "fish" && !strings.Contains(s, "_catctl_for_demo_tool") {
			t.Errorf("%s: registration function name not folded, got:\n%s", tc.name, s)
		}
	}

	// zsh only: compdef does not exist until compinit has run, and plenty of
	// .zshrc files never run it. Every registration must go through
	// __cats_register, which either calls compdef or queues for the flush —
	// a bare top-level compdef is the "command not found" at every prompt.
	s := script(zshBody, zshRegister)
	for line := range strings.SplitSeq(s, "\n") {
		if strings.HasPrefix(line, "compdef ") {
			t.Errorf("zsh: unguarded top-level %q; register via __cats_register", line)
		}
	}
	if !strings.Contains(s, "__cats_register _catctl catctl") ||
		!strings.Contains(s, "__cats_register _catctl_for_demo_tool demo-tool") {
		t.Errorf("zsh: registrations do not go through __cats_register, got:\n%s", s)
	}
}

// A completer that says too much cannot flood the shell.
func TestCappedWriter(t *testing.T) {
	w := &cappedWriter{max: 4}
	n, err := w.Write([]byte("abcdefgh"))
	if err != nil || n != 8 {
		t.Fatalf("Write = %d, %v; want the full length and no error", n, err)
	}
	if w.buf.String() != "abcd" {
		t.Errorf("buffered %q, want %q", w.buf.String(), "abcd")
	}
}
