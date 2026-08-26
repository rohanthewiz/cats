// Package config is catway's optional YAML configuration file. It is a second
// source of settings alongside the command-line flags: for the server settings
// the precedence is flag > config > built-in default (main.go applies the flag
// layer via flag.Visit); the front-end settings (theme colours and copy-mode
// keybindings) have no flags and come from the config alone, baked into the
// served page.
//
// A missing file is not an error — every field has a default (Default), so an
// empty or absent config yields the same behaviour catway had before configs
// existed. Absent scalar keys keep their defaults; the keybinding map merges
// key-wise, so a config that rebinds one action keeps the defaults for the
// rest. The theme section stores only choices (a theme name + sparse colour
// overrides) — the full palette is resolved against internal/theme at render
// time, so there are no colour defaults here to merge against.
//
// House style (matching the rest of the repo): stdlib errors/fmt and prefixed
// log messages, no serr.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/rohanthewiz/cats/internal/gwtls"
)

// EnvVar overrides the config file path (after an explicit --config flag, before
// the default location).
const EnvVar = "CATS_CONFIG"

// Config is the whole catway configuration file.
type Config struct {
	Server      Server      `yaml:"server"`
	Hosts       []Host      `yaml:"hosts,omitempty"`
	Persistence Persistence `yaml:"persistence"`
	Panes       Panes       `yaml:"panes"`
	Theme       Theme       `yaml:"theme"`
	Keybindings Keybindings `yaml:"keybindings"`
	Worktrees   Worktrees   `yaml:"worktrees"`
	Push        Push        `yaml:"push"`
	Editor      Editor      `yaml:"editor"`
	Ledger      Ledger      `yaml:"ledger"`
	Runbooks    Runbooks    `yaml:"runbooks"`
}

// --- hosts --------------------------------------------------------------------

// LocalHostID is the id of the host catway always has: the cathost reached over
// server.cathost_socket. It is SYNTHESIZED rather than configured (see
// EffectiveHosts), so a config with no hosts: block still describes exactly one
// host, and every pane that names no host belongs to it. A hosts: entry may
// claim the id to override its address or label.
const LocalHostID = "local"

// Address schemes a host's addr may use.
const (
	HostUnix = "unix" // unix://path — a local socket, or an `ssh -L` forward of a remote one
	HostTCP  = "tcp"  // tcp://host:port — cleartext, so loopback binds only
	HostTLS  = "tls"  // tls://host:port — cathost's own transport (Phase 4)
)

// Host is one cathost this catway attaches to. Panes carry a host id, so the
// roster is what turns "the daemon" into "this pane's machine".
//
// Addr is scheme://target:
//
//	unix:///tmp/cats-cathost.sock   the local daemon — and, forwarded over
//	                                `ssh -L /tmp/box.sock:/tmp/cats.sock`, a
//	                                genuinely remote one with no new protocol
//	tcp://127.0.0.1:8422            cleartext; only sane on a loopback bind
//	tls://devbox:8422               cathost's native remote transport
//
// Token/TokenFile authenticate to a cathost that requires one and Fingerprint
// pins its self-signed certificate; both are inert until the transport that
// uses them exists. TokenFile is the better of the pair — the settings modal
// rewrites this file wholesale on every config.set, so a literal token lives on
// in a file that is easy to commit by accident (the same reasoning that keeps
// Push's credential out of the config entirely).
type Host struct {
	ID          string `yaml:"id"`
	Label       string `yaml:"label,omitempty"` // display name; "" ⇒ the id
	Addr        string `yaml:"addr"`
	Token       string `yaml:"token,omitempty"`
	TokenFile   string `yaml:"token_file,omitempty"`
	Fingerprint string `yaml:"fingerprint,omitempty"` // pinned TLS cert SHA-256
	// Default marks the host new panes land on when nothing names one — and the
	// host a pane whose recorded host has vanished falls back to. At most one
	// entry may set it; with none set, the local host is the default.
	Default bool `yaml:"default,omitempty"`
	// ControlRelay lets panes on this host reach the control API — in-pane
	// catctl, cats-todo, plugin binaries — by relaying it through that machine's
	// cathost.
	//
	// Off by default, and it is the one host setting that is a trust decision
	// rather than a connection detail. The control API can create panes, run
	// commands in them, read any pane's contents on ANY host, rewrite this
	// config and attach or detach cathosts. Turning this on for a host says:
	// anything that can open a unix socket on that machine may do all of that.
	//
	// There is deliberately no partial version. A caller holding the control
	// socket can type `pbpaste` into a local pane with pane.send_input and read
	// the answer back with pane.capture, so a denylist of the "sensitive"
	// methods would gate nothing it does not already have by a longer route —
	// the same argument ctlproto.MethodClipboardRead already makes about the
	// local socket. Enable it for a machine you trust as much as the one running
	// catway, and leave it off otherwise.
	ControlRelay bool `yaml:"control_relay,omitempty"`
}

// DisplayLabel is the host's human name: its label, or its id when unlabelled.
func (h Host) DisplayLabel() string {
	if h.Label != "" {
		return h.Label
	}
	return h.ID
}

// Transport splits Addr into its scheme and target. It is deliberately lenient
// about an empty unix path — a catway started with no cathost socket at all
// (tests, a probe run) should fail at dial time like any other unreachable
// socket, not refuse to build its roster. Validate is the strict half, applied
// to what an operator actually wrote in the file.
func (h Host) Transport() (scheme, target string, err error) {
	i := strings.Index(h.Addr, "://")
	if i < 0 {
		return "", "", fmt.Errorf("addr %q: want scheme://target (unix://path, tcp://host:port, tls://host:port)", h.Addr)
	}
	scheme, target = h.Addr[:i], h.Addr[i+3:]
	switch scheme {
	case HostUnix:
		return scheme, target, nil
	case HostTCP, HostTLS:
		if _, _, err := net.SplitHostPort(target); err != nil {
			return "", "", fmt.Errorf("addr %q: %s needs host:port", h.Addr, scheme)
		}
		return scheme, target, nil
	}
	return "", "", fmt.Errorf("addr %q: unknown scheme %q", h.Addr, scheme)
}

// hostIDRe bounds host ids to what can travel unescaped everywhere one appears:
// a JSON field, a session file, a CSS/DOM id in the sidebar, a catctl argument.
var hostIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// validateHosts checks the roster as written: unique, well-formed ids, a
// parseable address each, at most one default, and no ambiguous credential.
func (c Config) validateHosts() error {
	seen := make(map[string]bool, len(c.Hosts))
	defaults := 0
	for i, h := range c.Hosts {
		if h.ID == "" {
			return fmt.Errorf("hosts[%d]: id is required", i)
		}
		if !hostIDRe.MatchString(h.ID) {
			return fmt.Errorf("hosts[%d]: id %q: want letters, digits, '.', '_' or '-'", i, h.ID)
		}
		if seen[h.ID] {
			return fmt.Errorf("hosts: duplicate id %q", h.ID)
		}
		seen[h.ID] = true
		if h.Addr == "" {
			return fmt.Errorf("hosts.%s: addr is required", h.ID)
		}
		scheme, target, err := h.Transport()
		if err != nil {
			return fmt.Errorf("hosts.%s: %w", h.ID, err)
		}
		if target == "" {
			return fmt.Errorf("hosts.%s: addr %q: empty target", h.ID, h.Addr)
		}
		if scheme != HostUnix && h.ID == LocalHostID {
			// Not a hard rule of the protocol — a warning would do — but an id
			// that means "this machine" pointing at another one is the kind of
			// thing that makes every later error message lie.
			return fmt.Errorf("hosts.%s: the local host must use a unix:// address", h.ID)
		}
		if h.Token != "" && h.TokenFile != "" {
			return fmt.Errorf("hosts.%s: set token or token_file, not both", h.ID)
		}
		if h.Default {
			defaults++
		}
	}
	if defaults > 1 {
		return errors.New("hosts: at most one host may be marked default")
	}
	return nil
}

// EffectiveHosts is the roster catway actually attaches to: the configured
// hosts with the local one synthesized in front unless the file already claims
// that id, and exactly one entry marked Default.
//
// The synthesis is what keeps single-host users at zero config: with no hosts:
// block the result is one unix host on server.cathost_socket, which is what
// catway has always dialed. The default is the explicitly marked host, else the
// local one, else the first — so "which machine does a new pane land on" always
// has an answer, and it is the historical one until somebody says otherwise.
//
// The receiver's slice is never mutated: the result is a fresh slice of copies,
// because the Default flags are normalized on it.
func (c Config) EffectiveHosts(cathostSocket string) []Host {
	return EffectiveHosts(cathostSocket, c.Hosts)
}

// EffectiveHosts is the package-level form, for callers holding a roster rather
// than a whole Config (catway's single-host constructors).
func EffectiveHosts(cathostSocket string, hosts []Host) []Host {
	out := make([]Host, 0, len(hosts)+1)
	local := false
	for _, h := range hosts {
		if h.ID == LocalHostID {
			local = true
		}
	}
	if !local {
		out = append(out, Host{ID: LocalHostID, Label: localHostLabel(), Addr: HostUnix + "://" + cathostSocket})
	}
	out = append(out, hosts...)

	def := -1
	for i, h := range out {
		if h.Default {
			def = i
			break
		}
	}
	if def < 0 {
		for i, h := range out {
			if h.ID == LocalHostID {
				def = i
				break
			}
		}
	}
	if def < 0 && len(out) > 0 {
		def = 0
	}
	for i := range out {
		out[i].Default = i == def
	}
	return out
}

// localHostLabel names the local host after the machine, short form: on a LAN
// the roster reads "studio · devbox", not "local · devbox". Falls back to the
// id when the hostname is unavailable, which is also what an unlabelled host
// displays as.
func localHostLabel() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return LocalHostID
	}
	if i := strings.Index(h, "."); i > 0 {
		h = h[:i] // studio.local → studio
	}
	return h
}

// Push is the outbound push-notification bridge (internal/push): when catway
// emits a notify — an agent needs attention, or finished — it also POSTs to
// this webhook, so a phone with its screen off gets a real system push instead
// of a toast on a screen nobody is looking at. Any ntfy-shaped endpoint works
// (ntfy.sh or a self-hosted instance).
//
// The credential is deliberately absent, for a sharper reason than the one
// behind Server.Password. config.set marshals this whole struct back to disk,
// so a token *field* would mean the settings modal silently writes the user's
// secret into a file they may well commit — even for a user who carefully
// supplied it in the environment. Set CATS_PUSH_TOKEN instead. (An ntfy topic
// URL is itself a capability, so someone who wants file-only config can embed
// credentials there — their choice, not our default.)
type Push struct {
	Enabled bool   `yaml:"enabled"`
	URL     string `yaml:"url,omitempty"` // e.g. https://ntfy.sh/cats-7f3a91
	// Kinds are the notify kinds forwarded to the phone. The default is
	// "attention" only: "finished" fires on every completion of every agent,
	// and a bridge that pushes those is how its owner learns to ignore it.
	Kinds []string `yaml:"kinds,omitempty"`
	// Priority maps a notify kind onto the endpoint's priority value. Note the
	// default tops out at "high", never ntfy's "urgent"/5 — that bypasses Do Not
	// Disturb on Android, and a blocked agent is not a 3am emergency.
	Priority map[string]string `yaml:"priority,omitempty"`
	// ClickURL is the deep-link base a notification tap opens; the pane's public
	// handle is appended ("cats://pane/" + "w1:p3"). Empty ⇒ no click action.
	ClickURL string `yaml:"click_url,omitempty"`
	// MinInterval debounces per (pane, kind) as a Go duration: an agent flapping
	// between working and blocked while a tool retries must not vibrate the
	// phone every few seconds.
	MinInterval string `yaml:"min_interval,omitempty"`
	// Actions turns the notification's buttons on. An "attention" push then
	// carries the agent's own menu (read off the pane's screen) as tappable
	// choices, and tapping one answers the prompt.
	//
	// It is opt-in, and separately from the topic URL, because it is the only
	// INBOUND surface in this file: everything else here is catway posting out.
	// Turning it on means a request arriving from the internet, carrying a token
	// the notification server has also seen, can type into a terminal. That is
	// worth deciding on purpose.
	Actions bool `yaml:"actions,omitempty"`
	// ActionURL is the base catway is reachable at FROM THE PHONE — scheme,
	// host and port, no trailing path. The action endpoint is appended.
	//
	// It cannot be derived: catway knows the address it bound (often
	// 127.0.0.1, or a Tailscale name, or nothing routable at all) but not the
	// one a phone on another network would use to come back. Required when
	// Actions is set, because buttons pointing nowhere are worse than no
	// buttons — they look like they worked.
	ActionURL string `yaml:"action_url,omitempty"`
}

// Interval is the parsed MinInterval; an empty value means no debounce.
func (p Push) Interval() (time.Duration, error) {
	if p.MinInterval == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(p.MinInterval)
	if err != nil {
		return 0, fmt.Errorf("min_interval %q: %w", p.MinInterval, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("min_interval %q: must not be negative", p.MinInterval)
	}
	return d, nil
}

// KindSet is MinInterval's companion: the forwarded kinds as a set, ready for
// internal/push. Validate has already rejected unknown names.
func (p Push) KindSet() map[string]bool {
	out := make(map[string]bool, len(p.Kinds))
	for _, k := range p.Kinds {
		out[k] = true
	}
	return out
}

// Server mirrors the network/auth flags. Password is deliberately absent — a
// shared secret belongs in the environment (CATS_PASSWORD) or a flag, never a
// config file that is easy to commit by accident.
type Server struct {
	Addr          string `yaml:"addr"`
	CathostSocket string `yaml:"cathost_socket"`
	ControlSocket string `yaml:"control_socket"` // "" ⇒ ctlproto resolves env/default
	HookSocket    string `yaml:"hook_socket"`    // agent hook-report API socket
	Auth          string `yaml:"auth"`           // "password" | "none"
	SessionTTL    string `yaml:"session_ttl"`    // a Go duration string, e.g. "24h"
	TLS           TLS    `yaml:"tls"`
	// AllowedOrigins are extra WebSocket Origins accepted beyond same-origin
	// (see gwauth.OriginOK): full origins or bare host[:port] authorities. Needed
	// when a reverse proxy or relay serves the UI under a host that differs from
	// the catway's own Host header. Empty ⇒ strict same-origin only. omitempty
	// keeps an unset list out of a saved file, so it round-trips as nil (not [])
	// and stays equal to the default.
	AllowedOrigins []string `yaml:"allowed_origins,omitempty"`
}

// Persistence is session persistence & restore (WS3): the model snapshot that
// survives a catway restart and the scrollback seeds that survive a cathost
// daemon loss.
type Persistence struct {
	// Enabled turns persistence on (the default): the session model is saved on
	// every mutation and restored at startup.
	Enabled bool `yaml:"enabled"`
	// StateDir overrides where session.json/history.json live ("" ⇒
	// $XDG_STATE_HOME/cats, falling back to ~/.local/state/cats).
	StateDir string `yaml:"state_dir"`
	// HistoryLines bounds the scrollback captured per pane for cold-restore
	// seeds (0 = the whole buffer).
	HistoryLines int `yaml:"history_lines"`
	// ResumeAgents relaunches supported AI-agent panes into their native
	// conversation sessions on a cold restore (cats's
	// session.resume_agents_on_restore, default true). Requires official
	// integrations that report session refs over the hook API.
	ResumeAgents bool `yaml:"resume_agents"`
}

// TLS is the HTTPS configuration. Enabled alone uses an auto self-signed cert;
// Cert+Key provide operator PEMs (and imply Enabled).
type TLS struct {
	Enabled bool   `yaml:"enabled"`
	Cert    string `yaml:"cert"`
	Key     string `yaml:"key"`
	// SANs are extra subject alternative names for the auto-generated
	// certificate — a LAN DNS name, or the hostname a relay will front — on top
	// of the loopback/hostname/interface set gwtls discovers. Each entry is an IP
	// literal or a bare DNS name; Validate rejects anything else.
	//
	// Ignored when Cert/Key name operator PEMs: those are whatever they are.
	// Changing this list re-mints the certificate, which changes the fingerprint
	// a client may have pinned, so it takes effect at restart rather than on
	// server.reload_config. omitempty keeps an unset list out of a saved file so
	// it round-trips as nil, matching AllowedOrigins.
	SANs []string `yaml:"sans,omitempty"`
}

// Theme is the front-end appearance. Name selects a named theme (a built-in,
// a user theme file, or a plugin-shipped one — see internal/theme); "" means
// the default. Colors are per-key overrides layered ON TOP of the named
// theme's palette (CSS custom-property names without the leading "--"), and
// Font, when set, overrides the theme's font stack. The config file stores
// only the user's choices; the full effective palette is resolved at render
// time, so this package needs no knowledge of what themes exist.
type Theme struct {
	Name   string            `yaml:"name,omitempty"`
	Colors map[string]string `yaml:"colors,omitempty"`
	Font   string            `yaml:"font,omitempty"`
}

// Keybindings maps a front-end action to the keyboard keys that trigger it. Only
// copy-mode is configurable today; keys are DOM KeyboardEvent.key values
// ("ArrowLeft", "h", "Escape", …).
type Keybindings struct {
	CopyMode map[string][]string `yaml:"copy_mode"`
}

// Worktrees configures the git-worktree feature (WS8 dialogs): where new
// checkouts land. Directory may start with "~" — expanded where used, not here,
// so the stored config stays portable.
type Worktrees struct {
	Directory string `yaml:"directory"`
}

// Panes configures the pane lifecycle — specifically the one part of it that
// acts without being asked.
//
// A pane whose child exits is deliberately KEPT: the chrome turns red and the
// last screen stays put, because the build output or stack trace that preceded
// the exit is usually why anyone is looking. But nothing used to take it away
// again either, so a session left running for days silted up with dead panes.
// The reaper closes one once it is old enough to be scenery rather than
// something still being read.
type Panes struct {
	// ReapExited is how long a pane is kept after its child exits, as a Go
	// duration string. Empty, "0", "off" or "never" keeps corpses forever —
	// the behaviour before the reaper existed, and the reason this is a
	// duration rather than a bool: turning it off and setting it to a week are
	// the same knob.
	//
	// The session's last pane is never reaped whatever this says; a terminal
	// that tidies itself out of existence is not a tidy terminal.
	ReapExited string `yaml:"reap_exited"`
}

// ReapExitedAfter parses ReapExited. 0 means "never reap", which is what the
// off-switch spellings and an absent value resolve to.
func (p Panes) ReapExitedAfter() (time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(p.ReapExited)) {
	case "", "0", "off", "never", "none":
		return 0, nil
	}
	d, err := time.ParseDuration(p.ReapExited)
	if err != nil {
		return 0, fmt.Errorf("reap_exited %q: %w", p.ReapExited, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("reap_exited %q: must not be negative", p.ReapExited)
	}
	return d, nil
}

// Ledger configures the command history — one durable record per command a
// shell ran in any pane, on any host.
//
// It is on by default and costs nothing until a shell actually reports: the
// scanning is a subscription each cathost only honours while asked, and a shell
// with no OSC 133 integration installed produces no marks at all. What the
// switch really controls is whether cats asks, and therefore whether any pane
// pays for the scan.
type Ledger struct {
	Enabled bool `yaml:"enabled"`
	// Retention is how many records are kept; the oldest go first. It is a count
	// rather than an age because it is the bound that keeps a query's backward
	// scan honest — an age bound would let a quiet month and a frantic week
	// differ by three orders of magnitude in how much a listing walks.
	Retention int `yaml:"retention,omitempty"`
}

// Runbooks configures the runbook engine — specifically the only part of it
// that acts without being asked.
//
// There is no switch for runbooks themselves: `runbook.run` is a command like
// any other, and a file in the runbook directory is somebody's own automation.
// Triggers are different because an `on:` clause runs steps nobody typed, so
// there has to be one place that turns all of it off at once — a runaway, a
// shared machine, a session where somebody wants to read a runbook before it
// starts acting.
//
// On by default all the same: declaring `on:` in the document IS the opt-in,
// and requiring a second one in a different file would only mean the feature
// appears broken the first time it is used. The two files belong to the same
// person and live in the same directory.
type Runbooks struct {
	Triggers bool `yaml:"triggers"`
}

// Editor is what cats knows about editors, which is deliberately almost
// nothing: a set of agent labels that mark a pane as one, and an argv that
// starts one. There is no editor integration behind this — pane.open_file emits
// an event on the control stream and the editor, already a client of that
// stream from inside its pane, acts on it.
//
// Agents is a list because a session can hold more than one kind of editor, and
// because the label is whatever that editor reports over the hook API — it is
// the editor's name for itself, not a cats-side registry.
type Editor struct {
	Agents  []string `yaml:"agents,omitempty"`
	Command []string `yaml:"command,omitempty"`
	// Spawn allows pane.open_file to start an editor when none is running. On
	// by default: "click a path and it opens" is the whole point, and a
	// request that silently does nothing because no editor happened to be open
	// is the worst of the three outcomes.
	Spawn bool `yaml:"spawn"`
}

// TTL parses SessionTTL into a duration.
func (s Server) TTL() (time.Duration, error) {
	d, err := time.ParseDuration(s.SessionTTL)
	if err != nil {
		return 0, fmt.Errorf("session_ttl %q: %w", s.SessionTTL, err)
	}
	return d, nil
}

// --- defaults ----------------------------------------------------------------

// The default palette and font used to live here as defaultColors/defaultFont;
// they moved to internal/theme (the cats-green built-in and theme.DefaultFont)
// when named themes arrived. The config's theme section now stores only the
// user's *choices* — a theme name plus sparse overrides — so its defaults are
// simply empty: no name (⇒ the default theme) and no overrides.

// defaultCopyMode is the copy-mode action → keys table. Its keys are the full
// set of known copy-mode actions; Validate rejects any others. Keep in sync with
// copyModeKey in cmd/catway/web/js/23-copymode.js.
var defaultCopyMode = map[string][]string{
	"move-left":  {"ArrowLeft", "h"},
	"move-right": {"ArrowRight", "l"},
	"move-up":    {"ArrowUp", "k"},
	"move-down":  {"ArrowDown", "j"},
	"line-start": {"0", "Home"},
	"line-end":   {"$", "End"},
	"top":        {"g"},
	"bottom":     {"G"},
	"select":     {"v"},
	"rect":       {"r"},
	"yank":       {"y", "Enter"},
	"exit":       {"Escape", "q"},
}

// Default is the configuration catway uses with no config file. Every call
// returns fresh maps so callers can mutate the result without affecting the
// package globals or each other.
func Default() Config {
	return Config{
		Server: Server{
			Addr:          ":8421",
			CathostSocket: "/tmp/cats-cathost.sock",
			ControlSocket: "",
			HookSocket:    "/tmp/cats-hooks.sock",
			Auth:          "password",
			SessionTTL:    "24h",
		},
		Persistence: Persistence{Enabled: true, HistoryLines: 2000, ResumeAgents: true},
		// Four hours is "since before lunch": long enough that a pane still
		// worth reading is still there, short enough that a week-long session
		// is not a graveyard.
		Panes:       Panes{ReapExited: "4h"},
		Theme:       Theme{Colors: map[string]string{}},
		Keybindings: Keybindings{CopyMode: cloneKeyMap(defaultCopyMode)},
		Worktrees:   Worktrees{Directory: "~/.cats/worktrees"},
		// ced is the editor cats is named alongside and the only one that
		// speaks this protocol today. Naming it here rather than leaving the
		// list empty means the feature works out of the box for the setup it
		// was built for, and the list is the extension point for anything else
		// that reports itself over the hook API.
		Editor:   Editor{Agents: []string{"ced"}, Command: []string{"ced"}, Spawn: true},
		Ledger:   Ledger{Enabled: true},
		Runbooks: Runbooks{Triggers: true},
		// Off by default, but with the shape filled in: a saved config then shows
		// the operator the feature exists and what its knobs are, and the values
		// round-trip equal to this default.
		Push: Push{
			Kinds:       []string{PushKindAttention},
			Priority:    map[string]string{PushKindAttention: "high", PushKindFinished: "low"},
			MinInterval: "60s",
		},
	}
}

// --- loading -----------------------------------------------------------------

// Load resolves the config path (override flag > CATS_CONFIG > default location)
// and returns the merged, validated configuration plus the path consulted. A
// missing file at the default location yields Default with no error; a missing
// file at an explicitly requested path (flag or env) is an error, since the user
// named a file that isn't there.
func Load(override string) (Config, string, error) {
	path, explicit := resolvePath(override)
	if path == "" {
		return Default(), "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			if explicit {
				return Default(), path, fmt.Errorf("config file %s not found", path)
			}
			return Default(), path, nil
		}
		return Default(), path, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg, err := parse(data)
	if err != nil {
		return Default(), path, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, path, nil
}

// parse decodes YAML onto a defaults copy (so absent scalars keep their
// defaults) and merges the theme/keybinding maps key-wise (which unmarshal
// would otherwise replace wholesale — and for theme.colors the merge base is
// deliberately empty, normalizing an absent map to a non-nil one), then
// validates.
func parse(data []byte) (Config, error) {
	cfg := Default()
	if len(bytes.TrimSpace(data)) == 0 {
		return cfg, nil // empty document ⇒ pure defaults (goccy would zero the struct)
	}
	defColors, defKeys := cfg.Theme.Colors, cfg.Keybindings.CopyMode
	cfg.Theme.Colors, cfg.Keybindings.CopyMode = nil, nil
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Default(), err
	}
	cfg.Theme.Colors = mergeStrMap(defColors, cfg.Theme.Colors)
	cfg.Keybindings.CopyMode = mergeKeyMap(defKeys, cfg.Keybindings.CopyMode)
	if err := cfg.Validate(); err != nil {
		return Default(), err
	}
	return cfg, nil
}

// --- saving ------------------------------------------------------------------

// Save writes cfg as YAML to path, creating parent directories. The Config
// struct is the whole schema, so marshalling it writes the complete file; any
// comments in a hand-written config are lost on the first save (accepted — the
// settings modal owns the file from then on). Callers validate first.
func Save(path string, cfg Config) error {
	if path == "" {
		return errors.New("save config: empty path")
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("save config %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("save config %s: %w", path, err)
	}
	return nil
}

// Validate checks the enum/duration fields and the keybinding action names so a
// bad config fails loudly (at startup or on reload) instead of silently.
func (c Config) Validate() error {
	switch c.Server.Auth {
	case "password", "none":
	default:
		return fmt.Errorf("server.auth %q: want \"password\" or \"none\"", c.Server.Auth)
	}
	if _, err := c.Server.TTL(); err != nil {
		return fmt.Errorf("server.%w", err)
	}
	if (c.Server.TLS.Cert == "") != (c.Server.TLS.Key == "") {
		return errors.New("server.tls: cert and key must be set together")
	}
	// Fail at load, not at first HTTPS connect: a mistyped SAN would otherwise
	// surface months later as an unexplained browser trust warning.
	if _, _, err := gwtls.ParseSANs(c.Server.TLS.SANs); err != nil {
		return fmt.Errorf("server.tls.sans: %w", err)
	}
	if err := c.validateHosts(); err != nil {
		return err
	}
	if c.Persistence.HistoryLines < 0 {
		return fmt.Errorf("persistence.history_lines %d: must be >= 0", c.Persistence.HistoryLines)
	}
	if _, err := c.Panes.ReapExitedAfter(); err != nil {
		return fmt.Errorf("panes.%w", err)
	}
	for action, keys := range c.Keybindings.CopyMode {
		if _, ok := defaultCopyMode[action]; !ok {
			return fmt.Errorf("keybindings.copy_mode: unknown action %q", action)
		}
		if len(keys) == 0 {
			return fmt.Errorf("keybindings.copy_mode.%s: needs at least one key", action)
		}
	}
	if err := c.Push.Validate(); err != nil {
		return fmt.Errorf("push.%w", err)
	}
	return nil
}

// Notify kinds, mirroring browserproto's. Duplicated as plain strings so this
// package stays free of the wire types (config is imported by catctl, which
// links neither).
const (
	PushKindAttention = "attention"
	PushKindFinished  = "finished"
	// PushKindInfo is the kind ui.notify defaults to — anything a plugin, an
	// agent hook or a runbook raises for itself. It is accepted here so an
	// operator CAN forward it, and left out of the default Kinds so a plugin
	// that narrates its own progress cannot start vibrating a phone merely by
	// existing.
	PushKindInfo = "info"
)

// pushPriorities are the values ntfy accepts. Checked eagerly so a typo fails at
// startup rather than silently downgrading every notification.
var pushPriorities = map[string]bool{
	"min": true, "low": true, "default": true, "high": true, "urgent": true,
	"1": true, "2": true, "3": true, "4": true, "5": true,
}

// Validate checks the push section. The URL is only required when the bridge is
// enabled — a disabled section with a half-filled URL is a work in progress, not
// an error.
//
// Exported (unlike the other sections' checks) because main re-validates after
// the flag layer: --push-url can turn the bridge on over a config that left it
// off, so the file-time check is not the last word.
func (p Push) Validate() error {
	if _, err := p.Interval(); err != nil {
		return err
	}
	for _, k := range p.Kinds {
		if k != PushKindAttention && k != PushKindFinished && k != PushKindInfo {
			return fmt.Errorf("kinds: unknown notify kind %q", k)
		}
	}
	for k, v := range p.Priority {
		if k != PushKindAttention && k != PushKindFinished && k != PushKindInfo {
			return fmt.Errorf("priority: unknown notify kind %q", k)
		}
		if !pushPriorities[v] {
			return fmt.Errorf("priority.%s %q: not an ntfy priority", k, v)
		}
	}
	if !p.Enabled {
		return nil
	}
	if p.URL == "" {
		return errors.New("url: required when push is enabled")
	}
	u, err := url.Parse(p.URL)
	if err != nil {
		return fmt.Errorf("url %q: %w", p.URL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("url %q: want an http or https URL", p.URL)
	}
	if u.Host == "" {
		return fmt.Errorf("url %q: missing host", p.URL)
	}
	if p.Actions {
		if p.ActionURL == "" {
			return errors.New("action_url: required when push.actions is set (the address a phone reaches this catway at)")
		}
		au, err := url.Parse(p.ActionURL)
		if err != nil {
			return fmt.Errorf("action_url %q: %w", p.ActionURL, err)
		}
		if (au.Scheme != "http" && au.Scheme != "https") || au.Host == "" {
			return fmt.Errorf("action_url %q: want an http or https base URL", p.ActionURL)
		}
	}
	return nil
}

// ActionBase is ActionURL without its trailing slash, so a caller can join a
// path onto it without producing a double slash — which some reverse proxies
// treat as a different route, and every notification client renders verbatim.
func (p Push) ActionBase() string { return strings.TrimRight(p.ActionURL, "/") }

// resolvePath picks the config path: an explicit override (flag) wins, then
// CATS_CONFIG, then the default location. explicit reports whether the path came
// from the flag or env (so a missing file there is an error, not silent
// defaults).
func resolvePath(override string) (path string, explicit bool) {
	if override != "" {
		return override, true
	}
	if v := os.Getenv(EnvVar); v != "" {
		return v, true
	}
	return DefaultPath(), false
}

// DefaultPath is $XDG_CONFIG_HOME/cats/config.yaml, falling back to
// ~/.config/cats/config.yaml (the conventional location for a dev CLI tool, on
// macOS too). Returns "" if neither the env var nor a home dir is available.
// Exported so config.set can create the file when no config was in use yet.
func DefaultPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "cats", "config.yaml")
}

// --- map helpers -------------------------------------------------------------

func cloneStrMap(m map[string]string) map[string]string { return maps.Clone(m) }

func cloneKeyMap(m map[string][]string) map[string][]string {
	out := make(map[string][]string, len(m))
	for k, v := range m {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// mergeStrMap overlays over onto base (base is not mutated).
func mergeStrMap(base, over map[string]string) map[string]string {
	out := cloneStrMap(base)
	maps.Copy(out, over)
	return out
}

// mergeKeyMap overlays over onto base per action (a present action's key list
// replaces the default's; absent actions keep their defaults).
func mergeKeyMap(base, over map[string][]string) map[string][]string {
	out := cloneKeyMap(base)
	for k, v := range over {
		out[k] = append([]string(nil), v...)
	}
	return out
}
