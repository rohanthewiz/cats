package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// The default config is self-consistent: it validates, its TTL parses, and it
// carries the full theme + copy-mode tables.
func TestDefaultValid(t *testing.T) {
	d := Default()
	if err := d.Validate(); err != nil {
		t.Fatalf("Default should validate: %v", err)
	}
	if ttl, err := d.Server.TTL(); err != nil || ttl != 24*time.Hour {
		t.Fatalf("default TTL = %v, %v; want 24h", ttl, err)
	}
	// The theme section defaults to "no choices": no name (⇒ the default
	// theme) and an empty-but-present overrides map.
	if d.Theme.Name != "" || d.Theme.Colors == nil || len(d.Theme.Colors) != 0 {
		t.Fatalf("default theme should be empty choices, got %+v", d.Theme)
	}
	if len(d.Keybindings.CopyMode) != len(defaultCopyMode) {
		t.Fatalf("default copy_mode has %d actions, want %d", len(d.Keybindings.CopyMode), len(defaultCopyMode))
	}
	// Default returns independent maps — mutating one result must not affect
	// another (guards the shared package-global globals).
	d.Theme.Colors["bg"] = "#000000"
	if Default().Theme.Colors["bg"] == "#000000" {
		t.Fatal("Default must return fresh maps, not shared globals")
	}
}

// An empty file yields exactly the defaults.
func TestParseEmpty(t *testing.T) {
	got, err := parse([]byte(""))
	if err != nil {
		t.Fatalf("parse empty: %v", err)
	}
	if !reflect.DeepEqual(got, Default()) {
		t.Fatalf("empty config should equal Default()")
	}
}

// A partial config overrides only what it names: one scalar, one color, one
// keybinding — everything else keeps its default. tls.enabled:false (a zero
// value) must survive, i.e. absent keys keep defaults but present keys win even
// when zero.
func TestParsePartialMerge(t *testing.T) {
	yaml := `
server:
  addr: "127.0.0.1:9000"
  auth: none
theme:
  name: darcula
  colors:
    bg: "#000000"
keybindings:
  copy_mode:
    yank: ["y", "c"]
`
	got, err := parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Server.Addr != "127.0.0.1:9000" || got.Server.Auth != "none" {
		t.Fatalf("server overrides not applied: %+v", got.Server)
	}
	// Untouched scalars keep defaults.
	if got.Server.CathostSocket != Default().Server.CathostSocket {
		t.Fatalf("cathost socket should keep default, got %q", got.Server.CathostSocket)
	}
	// The named color lands as an override; the map stays sparse (the full
	// palette is resolved against the theme at render time, not here).
	if got.Theme.Colors["bg"] != "#000000" || len(got.Theme.Colors) != 1 {
		t.Fatalf("theme overrides = %v, want just bg", got.Theme.Colors)
	}
	if got.Theme.Name != "darcula" {
		t.Fatalf("theme.name = %q", got.Theme.Name)
	}
	// Rebound action wins; sibling actions keep defaults.
	if !reflect.DeepEqual(got.Keybindings.CopyMode["yank"], []string{"y", "c"}) {
		t.Fatalf("yank rebind not applied: %v", got.Keybindings.CopyMode["yank"])
	}
	if !reflect.DeepEqual(got.Keybindings.CopyMode["exit"], defaultCopyMode["exit"]) {
		t.Fatalf("exit should keep default, got %v", got.Keybindings.CopyMode["exit"])
	}
}

// Validation rejects bad enum/duration/keybinding values.
func TestValidateRejects(t *testing.T) {
	cases := map[string]string{
		"bad auth":       "server:\n  auth: maybe\n",
		"bad ttl":        "server:\n  session_ttl: \"neverish\"\n",
		"unknown action": "keybindings:\n  copy_mode:\n    teleport: [\"t\"]\n",
		"empty key list": "keybindings:\n  copy_mode:\n    yank: []\n",
		"lone tls cert":  "server:\n  tls:\n    cert: /x.pem\n",
		// A SAN typo would otherwise surface months later as an unexplained
		// browser trust warning, long after anyone connects it to this line.
		"tls san as url":   "server:\n  tls:\n    sans: [\"https://cats.lan\"]\n",
		"tls san hostport": "server:\n  tls:\n    sans: [\"cats.lan:8421\"]\n",

		"push enabled without url": "push:\n  enabled: true\n",
		"push non-http url":        "push:\n  enabled: true\n  url: ftp://ntfy.sh/t\n",
		"push hostless url":        "push:\n  enabled: true\n  url: \"https://\"\n",
		"push unknown kind":        "push:\n  kinds: [\"attention\", \"exploded\"]\n",
		"push unknown prio kind":   "push:\n  priority:\n    exploded: high\n",
		"push bad priority":        "push:\n  priority:\n    attention: LOUD\n",
		"push bad interval":        "push:\n  min_interval: \"soonish\"\n",
		"push negative interval":   "push:\n  min_interval: \"-30s\"\n",
	}
	for name, yaml := range cases {
		if _, err := parse([]byte(yaml)); err == nil {
			t.Errorf("%s: expected parse error", name)
		}
	}
}

// Load reads an explicit path and merges it; a missing explicit path is an
// error; a syntactically empty file is defaults.
func TestLoad(t *testing.T) {
	dir := t.TempDir()

	// Present, valid file → merged.
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("server:\n  addr: \":7000\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, gotPath, err := Load(path)
	if err != nil {
		t.Fatalf("Load present: %v", err)
	}
	if got.Server.Addr != ":7000" || gotPath != path {
		t.Fatalf("Load present: addr=%q path=%q", got.Server.Addr, gotPath)
	}

	// Missing explicit path → error (the operator named a file that isn't there).
	if _, _, err := Load(filepath.Join(dir, "nope.yaml")); err == nil {
		t.Fatal("Load of a missing explicit path should error")
	}

	// CATS_CONFIG env resolves the path when no override is given.
	t.Setenv(EnvVar, path)
	got, _, err = Load("")
	if err != nil || got.Server.Addr != ":7000" {
		t.Fatalf("Load via env: addr=%q err=%v", got.Server.Addr, err)
	}
}

// Save writes a loadable file that round-trips to the same config (the settings
// modal's persist path), creating parent directories as needed.
func TestSaveRoundtrip(t *testing.T) {
	cfg := Default()
	cfg.Theme.Colors["bg"] = "#101010"
	cfg.Theme.Font = "monospace"
	cfg.Keybindings.CopyMode["yank"] = []string{"y", "c"}
	cfg.Worktrees.Directory = "/tmp/wt"

	path := filepath.Join(t.TempDir(), "nested", "config.yaml")
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, gotPath, err := Load(path)
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if gotPath != path {
		t.Fatalf("Load path = %q, want %q", gotPath, path)
	}
	if !reflect.DeepEqual(got, cfg) {
		t.Fatalf("roundtrip mismatch:\n got  %+v\n want %+v", got, cfg)
	}

	if err := Save("", cfg); err == nil {
		t.Fatal("Save with an empty path should error")
	}
}

// The push bridge is off by default but arrives shaped, and a config that names
// kinds replaces the default list rather than merging into it (slices are
// unmarshalled wholesale — so "kinds: [finished]" means finished ONLY, which is
// what an operator writing that line intends).
func TestParsePush(t *testing.T) {
	def := Default().Push
	if def.Enabled {
		t.Fatal("push must default to disabled")
	}
	if !reflect.DeepEqual(def.Kinds, []string{PushKindAttention}) {
		t.Fatalf("default push.kinds = %v, want just attention", def.Kinds)
	}
	// "info" is ui.notify's kind, so anything holding the control socket can
	// raise one. It must be configurable and must NOT be on by default: a
	// plugin narrating its own progress cannot be allowed to reach a phone
	// until the operator says so.
	if err := (Push{Enabled: true, URL: "https://ntfy.sh/x", Kinds: []string{PushKindInfo}}).Validate(); err != nil {
		t.Fatalf("push.kinds must accept %q: %v", PushKindInfo, err)
	}
	if slices.Contains(def.Kinds, PushKindInfo) {
		t.Fatalf("default push.kinds forwards %q: %v", PushKindInfo, def.Kinds)
	}
	if d, err := def.Interval(); err != nil || d != time.Minute {
		t.Fatalf("default push.min_interval = %v (err %v), want 1m", d, err)
	}

	cfg, err := parse([]byte("push:\n" +
		"  enabled: true\n" +
		"  url: https://ntfy.sh/cats-7f3a91\n" +
		"  kinds: [\"attention\", \"finished\"]\n" +
		"  click_url: \"cats://pane/\"\n" +
		"  min_interval: 15s\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := cfg.Push
	if !got.Enabled || got.URL != "https://ntfy.sh/cats-7f3a91" || got.ClickURL != "cats://pane/" {
		t.Fatalf("push = %+v", got)
	}
	if d, _ := got.Interval(); d != 15*time.Second {
		t.Fatalf("push.min_interval = %v, want 15s", d)
	}
	set := got.KindSet()
	if !set[PushKindAttention] || !set[PushKindFinished] {
		t.Fatalf("push.KindSet() = %v", set)
	}

	// A narrowing override replaces, and the priority map keeps its defaults for
	// the kind the operator didn't mention.
	cfg, err = parse([]byte("push:\n  kinds: [\"finished\"]\n"))
	if err != nil {
		t.Fatalf("parse narrowing: %v", err)
	}
	if set := cfg.Push.KindSet(); set[PushKindAttention] || !set[PushKindFinished] {
		t.Fatalf("narrowed push.KindSet() = %v, want finished only", set)
	}

	// An empty interval means no debounce, and must not be an error.
	if d, err := (Push{}).Interval(); err != nil || d != 0 {
		t.Fatalf("empty min_interval = %v (err %v), want 0", d, err)
	}
}

// The worktrees block defaults to ~/.cats/worktrees and overrides cleanly.
func TestParseWorktrees(t *testing.T) {
	if got := Default().Worktrees.Directory; got != "~/.cats/worktrees" {
		t.Fatalf("default worktrees.directory = %q", got)
	}
	got, err := parse([]byte("worktrees:\n  directory: /tmp/checkouts\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Worktrees.Directory != "/tmp/checkouts" {
		t.Fatalf("worktrees.directory override = %q", got.Worktrees.Directory)
	}
}

// The persistence block: absent keys keep the on-by-default behaviour; present
// keys override; a negative history_lines is rejected.
func TestParsePersistence(t *testing.T) {
	got, err := parse([]byte("persistence:\n  state_dir: /tmp/state\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !got.Persistence.Enabled || got.Persistence.StateDir != "/tmp/state" || got.Persistence.HistoryLines != 2000 {
		t.Fatalf("got %+v", got.Persistence)
	}

	got, err = parse([]byte("persistence:\n  enabled: false\n  history_lines: 500\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Persistence.Enabled || got.Persistence.HistoryLines != 500 {
		t.Fatalf("got %+v", got.Persistence)
	}

	if _, err := parse([]byte("persistence:\n  history_lines: -1\n")); err == nil {
		t.Fatal("negative history_lines should be rejected")
	}

	got, err = parse([]byte("persistence:\n  resume_agents: false\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Persistence.ResumeAgents || !got.Persistence.Enabled {
		t.Fatalf("got %+v", got.Persistence)
	}
}

// The shipped example config must always parse against the current schema and
// keep the defaults it documents.
func TestExampleConfigParses(t *testing.T) {
	data, err := os.ReadFile("../../config.example.yaml")
	if err != nil {
		t.Fatalf("read example config: %v", err)
	}
	got, err := parse(data)
	if err != nil {
		t.Fatalf("example config does not parse: %v", err)
	}
	want := Default()
	// Server holds a slice (AllowedOrigins) now, so it is not != comparable;
	// DeepEqual covers all three sub-structs uniformly.
	if !reflect.DeepEqual(got.Server, want.Server) ||
		!reflect.DeepEqual(got.Persistence, want.Persistence) ||
		!reflect.DeepEqual(got.Worktrees, want.Worktrees) {
		t.Fatalf("example config drifted from defaults:\n got %+v\nwant %+v",
			got, want)
	}
}

// --- hosts -------------------------------------------------------------------

// With no hosts: block there is still exactly one host — "local", on the
// cathost socket, and the default. This is the shape every existing single-host
// session gets, so it is the one that must never change.
func TestEffectiveHostsSynthesizesLocal(t *testing.T) {
	got := Default().EffectiveHosts("/tmp/cats-cathost.sock")
	if len(got) != 1 {
		t.Fatalf("hosts = %+v; want exactly one", got)
	}
	h := got[0]
	if h.ID != LocalHostID || h.Addr != "unix:///tmp/cats-cathost.sock" || !h.Default {
		t.Fatalf("synthesized local host = %+v", h)
	}
	if h.DisplayLabel() == "" {
		t.Fatal("the local host must carry a display label (the machine name)")
	}
}

// A configured host joins the synthesized local one, in file order, and the
// local host stays the default while nothing claims it.
func TestEffectiveHostsAppendsConfigured(t *testing.T) {
	c := Default()
	c.Hosts = []Host{{ID: "devbox", Addr: "unix:///tmp/devbox.sock"}}
	got := c.EffectiveHosts("/tmp/local.sock")
	if len(got) != 2 || got[0].ID != LocalHostID || got[1].ID != "devbox" {
		t.Fatalf("hosts = %+v; want [local devbox]", got)
	}
	if !got[0].Default || got[1].Default {
		t.Fatalf("local should be the default: %+v", got)
	}
	// The input roster must not be touched — EffectiveHosts normalizes Default
	// flags, and doing that in place would rewrite the caller's config.
	if c.Hosts[0].Default {
		t.Fatal("EffectiveHosts mutated the config's own host entry")
	}
}

// An explicit default wins, and a hosts: entry that claims the "local" id
// replaces the synthesized one instead of colliding with it.
func TestEffectiveHostsDefaultAndOverride(t *testing.T) {
	c := Default()
	c.Hosts = []Host{
		{ID: LocalHostID, Label: "this box", Addr: "unix:///tmp/other.sock"},
		{ID: "devbox", Addr: "unix:///tmp/devbox.sock", Default: true},
	}
	got := c.EffectiveHosts("/tmp/ignored.sock")
	if len(got) != 2 {
		t.Fatalf("hosts = %+v; want two (no synthesized duplicate)", got)
	}
	if got[0].ID != LocalHostID || got[0].Addr != "unix:///tmp/other.sock" || got[0].Label != "this box" {
		t.Fatalf("configured local host should win: %+v", got[0])
	}
	if got[0].Default || !got[1].Default {
		t.Fatalf("devbox should be the default: %+v", got)
	}
}

func TestHostTransport(t *testing.T) {
	cases := []struct {
		addr, scheme, target string
		wantErr              bool
	}{
		{addr: "unix:///tmp/a.sock", scheme: "unix", target: "/tmp/a.sock"},
		{addr: "unix://", scheme: "unix", target: ""}, // lenient: fails at dial, not at build
		{addr: "tcp://127.0.0.1:8422", scheme: "tcp", target: "127.0.0.1:8422"},
		{addr: "tls://devbox:8422", scheme: "tls", target: "devbox:8422"},
		{addr: "tls://devbox", wantErr: true}, // no port
		{addr: "ssh://devbox", wantErr: true}, // unknown scheme
		{addr: "/tmp/a.sock", wantErr: true},  // no scheme
	}
	for _, c := range cases {
		scheme, target, err := Host{ID: "h", Addr: c.addr}.Transport()
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: want error, got %s/%s", c.addr, scheme, target)
			}
			continue
		}
		if err != nil || scheme != c.scheme || target != c.target {
			t.Errorf("%q = %q/%q, %v; want %q/%q", c.addr, scheme, target, err, c.scheme, c.target)
		}
	}
}

// A malformed roster must fail at load, where the operator is looking, rather
// than as a pane that never spawns.
func TestValidateHostsRejects(t *testing.T) {
	cases := []struct {
		name  string
		hosts []Host
	}{
		{"no id", []Host{{Addr: "unix:///tmp/a.sock"}}},
		{"bad id", []Host{{ID: "dev box", Addr: "unix:///tmp/a.sock"}}},
		{"duplicate id", []Host{
			{ID: "dev", Addr: "unix:///tmp/a.sock"},
			{ID: "dev", Addr: "unix:///tmp/b.sock"},
		}},
		{"no addr", []Host{{ID: "dev"}}},
		{"empty target", []Host{{ID: "dev", Addr: "unix://"}}},
		{"bad scheme", []Host{{ID: "dev", Addr: "ssh://dev"}}},
		{"remote local", []Host{{ID: LocalHostID, Addr: "tls://elsewhere:8422"}}},
		{"two credentials", []Host{{ID: "dev", Addr: "unix:///tmp/a.sock", Token: "t", TokenFile: "f"}}},
		{"two defaults", []Host{
			{ID: "a", Addr: "unix:///tmp/a.sock", Default: true},
			{ID: "b", Addr: "unix:///tmp/b.sock", Default: true},
		}},
	}
	for _, c := range cases {
		cfg := Default()
		cfg.Hosts = c.hosts
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: should not validate", c.name)
		}
	}
}

// The good case, through the real YAML path: a hosts: block parses into the
// roster and survives validation.
func TestParseHosts(t *testing.T) {
	got, err := parse([]byte(`
hosts:
  - id: devbox
    label: "devbox (ssh)"
    addr: "unix:///tmp/devbox.sock"
    default: true
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got.Hosts) != 1 {
		t.Fatalf("hosts = %+v", got.Hosts)
	}
	h := got.Hosts[0]
	if h.ID != "devbox" || h.Addr != "unix:///tmp/devbox.sock" || !h.Default || h.DisplayLabel() != "devbox (ssh)" {
		t.Fatalf("host = %+v", h)
	}
}

// push.actions is the only INBOUND surface in the config, and buttons that
// point nowhere are worse than no buttons — they look like they worked. So the
// switch requires an address a phone can come back to, and that address has to
// be a real http(s) base.
func TestPushActionsRequireAnActionURL(t *testing.T) {
	base := Push{Enabled: true, URL: "https://ntfy.sh/cats-7f3a91"}

	on := base
	on.Actions = true
	if err := on.Validate(); err == nil {
		t.Fatal("push.actions with no action_url validated")
	} else if !strings.Contains(err.Error(), "action_url") {
		t.Fatalf("refusal does not name the missing key: %v", err)
	}

	bad := on
	bad.ActionURL = "cats.example"
	if err := bad.Validate(); err == nil {
		t.Fatal("a schemeless action_url validated")
	}

	ok := on
	ok.ActionURL = "https://cats.example/"
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid action_url refused: %v", err)
	}
	// The base is joined onto, so its trailing slash must not survive into a
	// double-slash path that a proxy may route somewhere else entirely.
	if got := ok.ActionBase(); got != "https://cats.example" {
		t.Errorf("ActionBase() = %q", got)
	}

	// With actions off the field is ignored, so an old config that set one
	// while experimenting still starts.
	off := base
	off.ActionURL = "not a url"
	if err := off.Validate(); err != nil {
		t.Fatalf("action_url checked while actions are off: %v", err)
	}
}
