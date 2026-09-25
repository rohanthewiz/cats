// Package config is catway's optional JSON configuration file
// (~/.config/cats/config.json; an older config.yaml is migrated on first
// load — see migrateLegacy). It is a second
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
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
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
	Server      Server      `yaml:"server" json:"server"`
	Hosts       []Host      `yaml:"hosts,omitempty" json:"hosts,omitempty"`
	Peers       []Peer      `yaml:"peers,omitempty" json:"peers,omitempty"`
	Persistence Persistence `yaml:"persistence" json:"persistence"`
	Panes       Panes       `yaml:"panes" json:"panes"`
	Theme       Theme       `yaml:"theme" json:"theme"`
	Keybindings Keybindings `yaml:"keybindings" json:"keybindings"`
	Worktrees   Worktrees   `yaml:"worktrees" json:"worktrees"`
	Push        Push        `yaml:"push" json:"push"`
	Editor      Editor      `yaml:"editor" json:"editor"`
	Ledger      Ledger      `yaml:"ledger" json:"ledger"`
	Runbooks    Runbooks    `yaml:"runbooks" json:"runbooks"`
	// UI holds the front-end preferences that used to live only in each
	// browser's localStorage (font size, sidebar width). They sit in the file
	// so a preference follows the user between browsers and the Mac app; the
	// page is still seeded from localStorage first, so an unset field changes
	// nothing for an existing client.
	UI UI `yaml:"ui,omitempty" json:"ui,omitempty"`
}

// UI is the front-end preference section. Zero means "unset — the client's own
// default (or its last local value) wins", which is why every field is
// omitempty and there is no Default() entry for it: a config that says nothing
// about the UI must not pin every client to one font size.
type UI struct {
	// FontPx is the terminal font size in CSS px (the ⌘+/⌘- zoom level).
	FontPx int `yaml:"font_px,omitempty" json:"font_px,omitempty"`
	// SidebarWidth is the sidebar's dragged width in CSS px.
	SidebarWidth int `yaml:"sidebar_width,omitempty" json:"sidebar_width,omitempty"`
}

// UI bounds, mirroring the front end's own clamps (01-bootstrap.js FONT_MIN /
// FONT_MAX, 38-sidebar.js SBW_MIN). Checked here so a hand-edit that would be
// silently clamped away in the browser fails loudly at load instead.
const (
	UIFontMin         = 9
	UIFontMax         = 32
	UISidebarWidthMin = 150
)

// validate checks the UI section; zero fields are "unset" and always valid.
func (u UI) validate() error {
	if u.FontPx != 0 && (u.FontPx < UIFontMin || u.FontPx > UIFontMax) {
		return fmt.Errorf("ui.font_px %d: want %d..%d (or 0 for the client default)", u.FontPx, UIFontMin, UIFontMax)
	}
	if u.SidebarWidth != 0 && u.SidebarWidth < UISidebarWidthMin {
		return fmt.Errorf("ui.sidebar_width %d: want >= %d (or 0 for the client default)", u.SidebarWidth, UISidebarWidthMin)
	}
	return nil
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
	ID          string `yaml:"id" json:"id"`
	Label       string `yaml:"label,omitempty" json:"label,omitempty"` // display name; "" ⇒ the id
	Addr        string `yaml:"addr" json:"addr"`
	Token       string `yaml:"token,omitempty" json:"token,omitempty"`
	TokenFile   string `yaml:"token_file,omitempty" json:"token_file,omitempty"`
	Fingerprint string `yaml:"fingerprint,omitempty" json:"fingerprint,omitempty"` // pinned TLS cert SHA-256
	// Default marks the host new panes land on when nothing names one — and the
	// host a pane whose recorded host has vanished falls back to. At most one
	// entry may set it; with none set, the local host is the default.
	Default bool `yaml:"default,omitempty" json:"default,omitempty"`
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
	ControlRelay bool `yaml:"control_relay,omitempty" json:"control_relay,omitempty"`
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
	Enabled bool   `yaml:"enabled" json:"enabled"`
	URL     string `yaml:"url,omitempty" json:"url,omitempty"` // e.g. https://ntfy.sh/cats-7f3a91
	// Kinds are the notify kinds forwarded to the phone. The default is
	// "attention" only: "finished" fires on every completion of every agent,
	// and a bridge that pushes those is how its owner learns to ignore it.
	Kinds []string `yaml:"kinds,omitempty" json:"kinds,omitempty"`
	// Priority maps a notify kind onto the endpoint's priority value. Note the
	// default tops out at "high", never ntfy's "urgent"/5 — that bypasses Do Not
	// Disturb on Android, and a blocked agent is not a 3am emergency.
	Priority map[string]string `yaml:"priority,omitempty" json:"priority,omitempty"`
	// ClickURL is the deep-link base a notification tap opens; the pane's public
	// handle is appended ("cats://pane/" + "w1:p3"). Empty ⇒ no click action.
	ClickURL string `yaml:"click_url,omitempty" json:"click_url,omitempty"`
	// MinInterval debounces per (pane, kind) as a Go duration: an agent flapping
	// between working and blocked while a tool retries must not vibrate the
	// phone every few seconds.
	MinInterval string `yaml:"min_interval,omitempty" json:"min_interval,omitempty"`
	// Actions turns the notification's buttons on. An "attention" push then
	// carries the agent's own menu (read off the pane's screen) as tappable
	// choices, and tapping one answers the prompt.
	//
	// It is opt-in, and separately from the topic URL, because it is the only
	// INBOUND surface in this file: everything else here is catway posting out.
	// Turning it on means a request arriving from the internet, carrying a token
	// the notification server has also seen, can type into a terminal. That is
	// worth deciding on purpose.
	Actions bool `yaml:"actions,omitempty" json:"actions,omitempty"`
	// ActionURL is the base catway is reachable at FROM THE PHONE — scheme,
	// host and port, no trailing path. The action endpoint is appended.
	//
	// It cannot be derived: catway knows the address it bound (often
	// 127.0.0.1, or a Tailscale name, or nothing routable at all) but not the
	// one a phone on another network would use to come back. Required when
	// Actions is set, because buttons pointing nowhere are worse than no
	// buttons — they look like they worked.
	ActionURL string `yaml:"action_url,omitempty" json:"action_url,omitempty"`
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
	Addr          string `yaml:"addr" json:"addr"`
	CathostSocket string `yaml:"cathost_socket" json:"cathost_socket"`
	ControlSocket string `yaml:"control_socket" json:"control_socket"` // "" ⇒ ctlproto resolves env/default
	HookSocket    string `yaml:"hook_socket" json:"hook_socket"`       // agent hook-report API socket
	Auth          string `yaml:"auth" json:"auth"`                     // "password" | "none"
	SessionTTL    string `yaml:"session_ttl" json:"session_ttl"`       // a Go duration string, e.g. "24h"
	TLS           TLS    `yaml:"tls" json:"tls"`
	// AllowedOrigins are extra WebSocket Origins accepted beyond same-origin
	// (see gwauth.OriginOK): full origins or bare host[:port] authorities. Needed
	// when a reverse proxy or relay serves the UI under a host that differs from
	// the catway's own Host header. Empty ⇒ strict same-origin only. omitempty
	// keeps an unset list out of a saved file, so it round-trips as nil (not [])
	// and stays equal to the default.
	AllowedOrigins []string `yaml:"allowed_origins,omitempty" json:"allowed_origins,omitempty"`
}

// Persistence is session persistence & restore (WS3): the model snapshot that
// survives a catway restart and the scrollback seeds that survive a cathost
// daemon loss.
type Persistence struct {
	// Enabled turns persistence on (the default): the session model is saved on
	// every mutation and restored at startup.
	Enabled bool `yaml:"enabled" json:"enabled"`
	// StateDir overrides where session.json/history.json live ("" ⇒
	// $XDG_STATE_HOME/cats, falling back to ~/.local/state/cats).
	StateDir string `yaml:"state_dir" json:"state_dir"`
	// HistoryLines bounds the scrollback captured per pane for cold-restore
	// seeds (0 = the whole buffer).
	HistoryLines int `yaml:"history_lines" json:"history_lines"`
	// ResumeAgents relaunches supported AI-agent panes into their native
	// conversation sessions on a cold restore (cats's
	// session.resume_agents_on_restore, default true). Requires official
	// integrations that report session refs over the hook API.
	ResumeAgents bool `yaml:"resume_agents" json:"resume_agents"`
}

// TLS is the HTTPS configuration. Enabled alone uses an auto self-signed cert;
// Cert+Key provide operator PEMs (and imply Enabled).
type TLS struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Cert    string `yaml:"cert" json:"cert"`
	Key     string `yaml:"key" json:"key"`
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
	SANs []string `yaml:"sans,omitempty" json:"sans,omitempty"`
}

// Theme is the front-end appearance. Name selects a named theme (a built-in,
// a user theme file, or a plugin-shipped one — see internal/theme); "" means
// the default. Colors are per-key overrides layered ON TOP of the named
// theme's palette (CSS custom-property names without the leading "--"), and
// Font, when set, overrides the theme's font stack. The config file stores
// only the user's choices; the full effective palette is resolved at render
// time, so this package needs no knowledge of what themes exist.
type Theme struct {
	Name   string            `yaml:"name,omitempty" json:"name,omitempty"`
	Colors map[string]string `yaml:"colors,omitempty" json:"colors,omitempty"`
	Font   string            `yaml:"font,omitempty" json:"font,omitempty"`
}

// Keybindings maps a front-end action to the keyboard keys that trigger it. Only
// copy-mode is configurable today; keys are DOM KeyboardEvent.key values
// ("ArrowLeft", "h", "Escape", …).
type Keybindings struct {
	CopyMode map[string][]string `yaml:"copy_mode" json:"copy_mode"`
}

// Worktrees configures the git-worktree feature (WS8 dialogs): where new
// checkouts land. Directory may start with "~" — expanded where used, not here,
// so the stored config stays portable.
type Worktrees struct {
	Directory string `yaml:"directory" json:"directory"`
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
// defaultAutocloseExited is the countdown a cleanly exited pane gets when the
// config says nothing. Both spellings exist because the value is needed as a
// duration by AutocloseExitedAfter's absent-value case and as the string that
// Default() writes into a fresh config file, and they must not drift.
const (
	defaultAutocloseExited    = 10 * time.Second
	defaultAutocloseExitedStr = "10s"
)

// defaultAgentRefresh is the agent-pane re-read period when the config says
// nothing, spelled twice for the same reason defaultAutocloseExited is.
// minAgentRefresh is the floor: each sweep reads a transcript tail per agent
// pane and, when anything moved, re-broadcasts the agents rollup to every
// client, so a typo like "1ms" would turn a background refresh into a busy
// loop. Below the per-pane read throttle (20s, catway's modelRefreshInterval)
// a shorter sweep buys nothing anyway.
const (
	defaultAgentRefresh    = time.Minute
	defaultAgentRefreshStr = "1m"
	minAgentRefresh        = 10 * time.Second
)

type Panes struct {
	// ReapExited is how long a pane is kept after its child exits, as a Go
	// duration string. Empty, "0", "off" or "never" keeps corpses forever —
	// the behaviour before the reaper existed, and the reason this is a
	// duration rather than a bool: turning it off and setting it to a week are
	// the same knob.
	//
	// The session's last pane is never reaped whatever this says; a terminal
	// that tidies itself out of existence is not a tidy terminal.
	ReapExited string `yaml:"reap_exited" json:"reap_exited"`

	// AutocloseExited is the short countdown a pane gets when its child exits
	// CLEANLY (status 0), as a Go duration string; the same off-switch
	// spellings as ReapExited disable it. It is the tidy-up end of the same
	// idea the reaper serves, at the other end of the timescale: reap_exited
	// is the sweep that stops a long session silting up, this is the pane
	// closing itself the moment you stop needing it.
	//
	// Only a clean exit qualifies. A pane that died non-zero is showing a
	// stack trace or a failed build — the very thing dead panes are kept for —
	// and sweeping that away on a timer would destroy the output at
	// exactly the moment it became interesting. A `exit`ed shell has nothing
	// left to say.
	//
	// The countdown is visible in the pane header and cancellable from it
	// (pane.keep), so the ten seconds are a chance to say "no", not a deadline
	// to race. The default is sized to NOTICING, not to reading: long enough
	// to see the countdown appear and hit ✕ (or enter copy mode, which also
	// keeps the pane), not long enough to skim a plugin run's git or build
	// output. Output worth reading is worth keeping, and keeping it is one
	// click — sizing the default for the slowest reader instead would leave
	// every ordinary `exit`ed shell sitting around for its sake.
	AutocloseExited string `yaml:"autoclose_exited" json:"autoclose_exited"`

	// AgentRefresh is how often every agent pane's model line — the model, its
	// effort and, for claude, how full the context window is — is re-read from
	// the agent's transcript while the pane sits in one state, as a Go duration
	// string. State changes (working → waiting, and so on) trigger a read of
	// their own regardless; this only bounds how stale a pane that is not
	// changing state can get. The off-switch spellings disable the sweep,
	// leaving state changes as the only trigger.
	//
	// It lives under panes rather than a section of its own because it is a
	// property of the pane rows the sidebar draws, and the panes tab is where
	// someone looking for "why does that number lag" would look.
	AgentRefresh string `yaml:"agent_refresh" json:"agent_refresh"`
}

// AgentRefreshEvery parses AgentRefresh. 0 means "no periodic sweep" (the
// off-switch spellings); an absent value takes the default, like
// AutocloseExitedAfter, so a config written before this knob existed keeps the
// behaviour it had. Values under minAgentRefresh are refused rather than
// clamped, so the saved file never says something the server is not doing.
func (p Panes) AgentRefreshEvery() (time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(p.AgentRefresh)) {
	case "0", "off", "never", "none":
		return 0, nil
	case "":
		return defaultAgentRefresh, nil
	}
	d, err := time.ParseDuration(p.AgentRefresh)
	if err != nil {
		return 0, fmt.Errorf("agent_refresh %q: %w", p.AgentRefresh, err)
	}
	if d < minAgentRefresh {
		return 0, fmt.Errorf("agent_refresh %q: must be at least %s (or off)", p.AgentRefresh, minAgentRefresh)
	}
	return d, nil
}

// AutocloseExitedAfter parses AutocloseExited. 0 means "never auto-close",
// which — as with ReapExitedAfter — is what the off-switch spellings resolve
// to. An ABSENT value does not: it takes the default from Default(), since a
// config file written before this knob existed should still get the behaviour.
func (p Panes) AutocloseExitedAfter() (time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(p.AutocloseExited)) {
	case "0", "off", "never", "none":
		return 0, nil
	case "":
		return defaultAutocloseExited, nil
	}
	d, err := time.ParseDuration(p.AutocloseExited)
	if err != nil {
		return 0, fmt.Errorf("autoclose_exited %q: %w", p.AutocloseExited, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("autoclose_exited %q: must not be negative", p.AutocloseExited)
	}
	return d, nil
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
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Retention is how many records are kept; the oldest go first. It is a count
	// rather than an age because it is the bound that keeps a query's backward
	// scan honest — an age bound would let a quiet month and a frantic week
	// differ by three orders of magnitude in how much a listing walks.
	Retention int `yaml:"retention,omitempty" json:"retention,omitempty"`
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
	Triggers bool `yaml:"triggers" json:"triggers"`
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
	Agents  []string `yaml:"agents,omitempty" json:"agents,omitempty"`
	Command []string `yaml:"command,omitempty" json:"command,omitempty"`
	// Spawn allows pane.open_file to start an editor when none is running. On
	// by default: "click a path and it opens" is the whole point, and a
	// request that silently does nothing because no editor happened to be open
	// is the worst of the three outcomes.
	Spawn bool `yaml:"spawn" json:"spawn"`
}

// TTL parses SessionTTL into a duration.
func (s Server) TTL() (time.Duration, error) {
	d, err := time.ParseDuration(s.SessionTTL)
	if err != nil {
		return 0, fmt.Errorf("session_ttl %q: %w", s.SessionTTL, err)
	}
	return d, nil
}

// --- peers --------------------------------------------------------------------

// Peer is another catway whose backend this one can synchronize with
// (peer.sync). A peer is NOT a host: a host is another machine's terminal
// daemon attached to this catway, whereas a peer is a second catway that is a
// source of truth of its own — its own workspaces, its own plugin set, its own
// todo backlogs. Sync reconciles the two backends and reports what did not
// carry across; nothing is ever deleted on either side.
//
// URL is the peer catway's browser address (https://box.lan:8421). The peer's
// /peer/v1/* endpoints sit behind the same auth guard as everything else it
// serves, so the credential is that catway's shared secret (its CATS_PASSWORD),
// presented as a bearer token. Token/TokenFile hold it; TokenFile is the better
// of the pair for the reason Host gives — the settings modal rewrites this file
// wholesale, so a literal secret in it is one `git add` from being published.
//
// Fingerprint pins a self-signed certificate (the auto-generated one catway
// serves under --tls) by its SHA-256, exactly as a tls:// host pins cathost's.
// Without a pin the standard chain and hostname verification applies, which is
// right for a peer fronted by a real certificate; an http:// URL is accepted
// for a peer on the same machine or an ssh tunnel, and refused anywhere else
// (see validatePeers), because it would send the secret in the clear.
type Peer struct {
	ID          string `yaml:"id" json:"id"`
	Label       string `yaml:"label,omitempty" json:"label,omitempty"` // display name; "" ⇒ the id
	URL         string `yaml:"url" json:"url"`
	Token       string `yaml:"token,omitempty" json:"token,omitempty"`
	TokenFile   string `yaml:"token_file,omitempty" json:"token_file,omitempty"`
	Fingerprint string `yaml:"fingerprint,omitempty" json:"fingerprint,omitempty"` // pinned TLS cert SHA-256
}

// DisplayLabel is the peer's human name: its label, or its id when unlabelled.
func (p Peer) DisplayLabel() string {
	if p.Label != "" {
		return p.Label
	}
	return p.ID
}

// validatePeers checks the peers: block. The rules mirror validateHosts where
// the field is the same thing (id shape, one credential source), and add the
// one that is peer-specific: a cleartext URL may only name this machine.
func (c Config) validatePeers() error {
	seen := make(map[string]bool, len(c.Peers))
	for i, p := range c.Peers {
		if p.ID == "" {
			return fmt.Errorf("peers[%d]: id is required", i)
		}
		if !hostIDRe.MatchString(p.ID) {
			return fmt.Errorf("peers[%d]: id %q: want letters, digits, '.', '_' or '-'", i, p.ID)
		}
		if seen[p.ID] {
			return fmt.Errorf("peers: duplicate id %q", p.ID)
		}
		seen[p.ID] = true
		if err := ValidatePeerURL(p.URL); err != nil {
			return fmt.Errorf("peers.%s: %w", p.ID, err)
		}
		if p.Token != "" && p.TokenFile != "" {
			return fmt.Errorf("peers.%s: set token or token_file, not both", p.ID)
		}
	}
	return nil
}

// ValidatePeerURL is the shape check a peer URL must pass, exported so the
// live peer.attach command refuses the same strings the file would. Only the
// scheme and host are examined: http:// is confined to loopback, because the
// bearer token rides every request and a cleartext hop off this machine is a
// secret on the wire. A path is refused rather than ignored — the /peer/v1/*
// routes are appended to the URL, so a trailing path would silently 404.
func ValidatePeerURL(raw string) error {
	if raw == "" {
		return errors.New("url is required")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("url %q: %w", raw, err)
	}
	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("url %q: want http(s)://host[:port]", raw)
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("url %q: no path, query or fragment — just the scheme, host and port", raw)
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return fmt.Errorf("url %q: http is cleartext, so it may only reach this machine — use https:// for another machine", raw)
		}
	}
	return nil
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
		// Ten seconds for a clean exit is long enough to read "exited (0)",
		// notice the countdown and stop it if the pane still has something on
		// screen you wanted.
		Panes:       Panes{ReapExited: "4h", AutocloseExited: defaultAutocloseExitedStr, AgentRefresh: defaultAgentRefreshStr},
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
//
// The format follows the extension: .yaml/.yml is read as YAML (an explicit
// --config pointing at an old file keeps working), anything else as JSON. At
// the default location a pre-JSON config.yaml with no config.json beside it is
// migrated first (see migrateLegacy), so an upgrade keeps the user's settings
// without them doing anything.
func Load(override string) (Config, string, error) {
	path, explicit := resolvePath(override)
	if path == "" {
		return Default(), "", nil
	}
	if !explicit {
		if legacy, err := migrateLegacy(path); err != nil {
			// The JSON could not be written (read-only dir, disk full). Serving
			// the YAML is strictly better than silently reverting the user to
			// defaults, and Save follows the extension, so later saves land in
			// the YAML file rather than failing.
			log.Printf("config: could not migrate %s to JSON, reading it as-is: %v", legacy, err)
			path = legacy
		}
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
	cfg, err := parse(data, isYAMLPath(path))
	if err != nil {
		return Default(), path, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, path, nil
}

// parse decodes the document onto a defaults copy (so absent scalars keep their
// defaults) and merges the theme/keybinding maps key-wise (which unmarshal
// would otherwise replace wholesale — and for theme.colors the merge base is
// deliberately empty, normalizing an absent map to a non-nil one), then
// validates. asYAML picks the decoder; the struct tags are identical for both,
// so the two formats describe exactly the same schema.
//
// Unknown top-level keys are ignored rather than rejected: the JSON file is
// shared with the Mac app, which keeps its own "app" section in it (see
// ReadSection / WriteSection) that this struct deliberately does not model.
func parse(data []byte, asYAML bool) (Config, error) {
	cfg := Default()
	if len(bytes.TrimSpace(data)) == 0 {
		return cfg, nil // empty document ⇒ pure defaults (goccy would zero the struct)
	}
	defColors, defKeys := cfg.Theme.Colors, cfg.Keybindings.CopyMode
	cfg.Theme.Colors, cfg.Keybindings.CopyMode = nil, nil
	var err error
	if asYAML {
		err = yaml.Unmarshal(data, &cfg)
	} else {
		err = json.Unmarshal(data, &cfg)
	}
	if err != nil {
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

// Save writes cfg to path, creating parent directories. The Config struct is
// the whole schema, so marshalling it writes the complete file. Callers
// validate first.
//
// JSON (the default) goes through saveJSON, which keeps any top-level section
// this struct does not own — the Mac app's "app" block lives in the same file
// and is written by another process. A .yaml path (an explicit legacy --config)
// is still written as YAML, whole; comments in a hand-written YAML config are
// lost on the first save, as they always were.
func Save(path string, cfg Config) error {
	if path == "" {
		return errors.New("save config: empty path")
	}
	if !isYAMLPath(path) {
		return saveJSON(path, cfg)
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
	if err := c.validatePeers(); err != nil {
		return err
	}
	if c.Persistence.HistoryLines < 0 {
		return fmt.Errorf("persistence.history_lines %d: must be >= 0", c.Persistence.HistoryLines)
	}
	if _, err := c.Panes.ReapExitedAfter(); err != nil {
		return fmt.Errorf("panes.%w", err)
	}
	if _, err := c.Panes.AutocloseExitedAfter(); err != nil {
		return fmt.Errorf("panes.%w", err)
	}
	if _, err := c.Panes.AgentRefreshEvery(); err != nil {
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
	if err := c.UI.validate(); err != nil {
		return err
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

// ResolvePath is resolvePath for other processes that share the file (the Mac
// app): the path catway would load with no --config flag.
func ResolvePath() string {
	p, _ := resolvePath("")
	return p
}

// DefaultPath is $XDG_CONFIG_HOME/cats/config.json, falling back to
// ~/.config/cats/config.json (the conventional location for a dev CLI tool, on
// macOS too). Returns "" if neither the env var nor a home dir is available.
// Exported so config.set can create the file when no config was in use yet.
func DefaultPath() string {
	d := configDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, DefaultFile)
}

// LegacyPath is where the pre-JSON config.yaml lived: the file migrateLegacy
// reads once, and nothing reads after that.
func LegacyPath() string {
	d := configDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, legacyFile)
}

// configDir is $XDG_CONFIG_HOME/cats or ~/.config/cats.
func configDir() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "cats")
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
