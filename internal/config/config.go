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
	"net/url"
	"os"
	"path/filepath"
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
	Persistence Persistence `yaml:"persistence"`
	Theme       Theme       `yaml:"theme"`
	Keybindings Keybindings `yaml:"keybindings"`
	Worktrees   Worktrees   `yaml:"worktrees"`
	Push        Push        `yaml:"push"`
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
// copyModeKey in index.html.
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
		Theme:       Theme{Colors: map[string]string{}},
		Keybindings: Keybindings{CopyMode: cloneKeyMap(defaultCopyMode)},
		Worktrees:   Worktrees{Directory: "~/.cats/worktrees"},
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
	if c.Persistence.HistoryLines < 0 {
		return fmt.Errorf("persistence.history_lines %d: must be >= 0", c.Persistence.HistoryLines)
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
		if k != PushKindAttention && k != PushKindFinished {
			return fmt.Errorf("kinds: unknown notify kind %q", k)
		}
	}
	for k, v := range p.Priority {
		if k != PushKindAttention && k != PushKindFinished {
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
	return nil
}

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
