//go:build ghostty

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/browserproto"
	"github.com/rohanthewiz/cats/internal/config"
	"github.com/rohanthewiz/cats/internal/theme"
)

// The config + theme commands (app.Backend seam, settings modal). All run
// synchronously on the loop goroutine — the config and theme files are small
// and local, like the session save. o.cfg holds the config-file state
// (defaults + file); server settings shown here are the file's values, not the
// flag-overridden effective ones, so config.set can marshal o.cfg back to disk
// without baking flags into the file.

// ConfigGet resolves the live configuration snapshot (config.get).
func (o *orch) ConfigGet(r app.Responder) {
	r.OK(o.configSnapshot())
}

// ConfigSet merges the live-appliable sections onto the current config,
// validates, writes the YAML (creating the default file when none was in use
// yet), re-renders the served page, and broadcasts the new theme so every
// connected client restyles immediately.
//
// Theme semantics (see app.ConfigSetParams): a request carrying a theme NAME
// is a switch — colors/font REPLACE the stored overrides (empty means "the
// theme, clean"); a request without a name is the legacy poke — colors merge
// key-wise, a non-empty font sticks.
func (o *orch) ConfigSet(r app.Responder, p app.ConfigSetParams) {
	cfg := o.cfg
	cfg.Theme.Colors = maps.Clone(cfg.Theme.Colors)
	cfg.Keybindings.CopyMode = maps.Clone(cfg.Keybindings.CopyMode)
	if p.Theme != nil {
		if p.Theme.Name != "" {
			cfg.Theme.Name = p.Theme.Name
			cfg.Theme.Colors = maps.Clone(p.Theme.Colors)
			cfg.Theme.Font = p.Theme.Font
		} else {
			if len(p.Theme.Colors) > 0 && cfg.Theme.Colors == nil {
				cfg.Theme.Colors = map[string]string{}
			}
			maps.Copy(cfg.Theme.Colors, p.Theme.Colors)
			if p.Theme.Font != "" {
				cfg.Theme.Font = p.Theme.Font
			}
		}
	}
	if len(p.CopyMode) > 0 {
		if cfg.Keybindings.CopyMode == nil {
			cfg.Keybindings.CopyMode = map[string][]string{}
		}
		for action, keys := range p.CopyMode {
			cfg.Keybindings.CopyMode[action] = slices.Clone(keys)
		}
	}
	if len(p.Options) > 0 {
		if err := applyOptions(&cfg, p.Options); err != nil {
			r.Fail(err.Error())
			return
		}
	}
	if err := o.saveConfig(cfg); err != "" {
		r.Fail(err)
		return
	}
	o.applyLiveOptions()
	// Reply before broadcasting: the issuer acts on its reply, and the theme
	// push it also receives is idempotent (same values it just applied).
	r.OK(o.configSnapshot())
	o.broadcastTheme()
}

// ThemeList enumerates the available themes (theme.list).
func (o *orch) ThemeList(r app.Responder) {
	res := resolveTheme(o.cfg)
	r.OK(app.ThemeListResult{Active: res.Name, Themes: themeInfos()})
}

// ThemeSave writes a user theme file (theme.save) and, with Activate, switches
// the config to it — clearing the color overrides, which are presumed baked
// into the palette just saved. This is how "save current look as a custom
// theme" and how a plugin's one-shot theme install both land.
func (o *orch) ThemeSave(r app.Responder, p app.ThemeSaveParams) {
	t := theme.Theme{Name: p.Name, Label: p.Label, Colors: p.Colors, Font: p.Font}
	if p.Dark != nil {
		t.Dark = *p.Dark
	} else {
		t.Dark = theme.DarkBG(p.Colors["bg"])
	}
	if _, err := theme.Save(t); err != nil {
		r.Fail(err.Error())
		return
	}
	if p.Activate {
		cfg := o.cfg
		cfg.Theme.Name = p.Name
		cfg.Theme.Colors = map[string]string{}
		cfg.Theme.Font = ""
		if err := o.saveConfig(cfg); err != "" {
			r.Fail(err)
			return
		}
	} else if o.baseHTML != nil {
		// Not activated, but a re-save of the *active* theme's file still
		// changes the effective palette — re-render so new loads see it.
		page := renderPage(o.baseHTML, o.cfg)
		o.page.Store(&page)
	}
	r.OK(o.configSnapshot())
	o.broadcastTheme()
}

// ThemeDelete removes a user theme file (theme.delete). If the deleted theme
// was active the config falls back to the default theme explicitly, so the
// file never keeps naming a theme that no longer exists.
func (o *orch) ThemeDelete(r app.Responder, p app.ThemeDeleteParams) {
	if err := theme.Delete(p.Name); err != nil {
		r.Fail(err.Error())
		return
	}
	if o.cfg.Theme.Name == p.Name {
		cfg := o.cfg
		cfg.Theme.Name = ""
		cfg.Theme.Colors = maps.Clone(cfg.Theme.Colors)
		if err := o.saveConfig(cfg); err != "" {
			r.Fail(err)
			return
		}
	} else if o.baseHTML != nil {
		// A user theme can shadow a builtin/plugin theme; deleting it may
		// change the effective palette even when it wasn't the named theme.
		page := renderPage(o.baseHTML, o.cfg)
		o.page.Store(&page)
	}
	r.OK(o.configSnapshot())
	o.broadcastTheme()
}

// --- generic option sections -------------------------------------------------
//
// The settings screen edits most of the file through one generic channel:
// config.get hands out each section as the JSON it is on disk, config.set
// hands edited sections back. The alternative — a typed wire struct and a
// bespoke config.set field per section — would put every new knob through a
// protocol release (wire is pinned by other repos), for sections whose schema
// already lives, validated, in internal/config.

// optionSections are the sections config.set may write through Options, in the
// order the screen shows them. Deliberately absent:
//   - server, hosts, peers: restart-bound or owned by their own dialogs and
//     commands (host.attach, peer.attach), which do the live roster work a raw
//     write would skip.
//   - theme, keybindings: already have their own typed params, with
//     switch-vs-merge semantics a raw section write would bypass.
//   - runbooks: a runbook can issue config.set, so making triggers settable
//     here would let a runbook re-enable its own triggers (see config.Runbooks).
var optionSections = []string{"panes", "persistence", "worktrees", "push", "editor", "ledger", "ui"}

// restartSections are the optionSections catway only reads at startup (main.go
// builds the persister, the push bridge and the ledger once). Saving them is
// still useful — it is the next launch's config — but the screen must say so.
var restartSections = []string{"persistence", "push", "ledger"}

// optionTarget maps a section name to the field it decodes onto.
func optionTarget(c *config.Config, section string) any {
	switch section {
	case "panes":
		return &c.Panes
	case "persistence":
		return &c.Persistence
	case "worktrees":
		return &c.Worktrees
	case "push":
		return &c.Push
	case "editor":
		return &c.Editor
	case "ledger":
		return &c.Ledger
	case "ui":
		return &c.UI
	}
	return nil
}

// applyOptions decodes each section ONTO cfg's current value, so a partial
// object changes only the keys it carries. Unknown sections and unknown keys
// are errors: the screen only sends what config.get gave it, so either one is a
// client bug or a typo in a hand-made catctl call, and a silent no-op there
// would read as a successful save.
//
// cfg is a copy of the live config, but its maps and slices still alias it —
// Push.Priority is cloned before decoding, because encoding/json writes INTO an
// existing map, and a request that then fails validation must leave the live
// config untouched. (Slices are replaced wholesale by the decoder, never
// written through.)
func applyOptions(cfg *config.Config, opts map[string]json.RawMessage) error {
	cfg.Push.Priority = maps.Clone(cfg.Push.Priority)
	// Sorted so which error a multi-section mistake reports is deterministic.
	for _, section := range slices.Sorted(maps.Keys(opts)) {
		target := optionTarget(cfg, section)
		if target == nil {
			return fmt.Errorf("config.set: %q is not an editable section (want one of %v)", section, optionSections)
		}
		dec := json.NewDecoder(bytes.NewReader(opts[section]))
		dec.DisallowUnknownFields()
		if err := dec.Decode(target); err != nil {
			return fmt.Errorf("config.set: %s: %w", section, err)
		}
	}
	return nil
}

// configOptions is the Options half of configSnapshot: each editable section
// marshalled exactly as config.json holds it.
func configOptions(c config.Config) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(optionSections))
	for _, section := range optionSections {
		raw, err := json.Marshal(optionTarget(&c, section))
		if err != nil {
			continue // plain structs; cannot fail
		}
		out[section] = raw
	}
	return out
}

// applyLiveOptions re-derives the runtime values that are cached off o.cfg at
// startup but are safe to change while running — the same set ReloadConfig
// re-applies, plus the worktree directory. Everything else either reads o.cfg
// on use (editor, ui via the re-rendered page) or is in restartSections.
func (o *orch) applyLiveOptions() {
	o.reapAfter = reapAfterFromConfig(o.cfg.Panes)
	o.autocloseAfter = autocloseAfterFromConfig(o.cfg.Panes)
	o.worktreeDir = o.cfg.Worktrees.Directory
}

// saveConfig validates and persists cfg, adopts it as the live config, and
// re-renders the served page. Returns a non-empty message on failure (the
// Responder-facing error), keeping the three callers above uniform.
func (o *orch) saveConfig(cfg config.Config) string {
	if err := cfg.Validate(); err != nil {
		return err.Error()
	}
	path := o.cfgPath
	if path == "" {
		path = config.DefaultPath()
	}
	if path == "" {
		return "no resolvable config path"
	}
	if err := config.Save(path, cfg); err != nil {
		return err.Error()
	}
	o.cfg = cfg
	o.cfgPath = path // a first save adopts the default path for future reloads
	if o.baseHTML != nil {
		page := renderPage(o.baseHTML, cfg)
		o.page.Store(&page)
	}
	return ""
}

// broadcastTheme pushes the effective theme to every connected browser and, when
// the appearance actually changed, emits theme_changed to the control-API
// subscribers.
//
// It is the single funnel for "the look just changed" — config.set, theme.save
// and theme.delete all end here — which is why the event is emitted from this
// one place rather than from each of the three commands. The dedupe is against
// the RESOLVED theme, not the config, because that is what a subscriber renders:
// a config edit that leaves the effective palette identical (re-saving the same
// override, deleting a shadowed theme whose colors matched) is not a change to
// anyone downstream.
func (o *orch) broadcastTheme() {
	res := resolveTheme(o.cfg)
	o.broadcast(browserproto.NewTheme(res.Name, res.Colors, res.Font))

	next := app.ConfigTheme{Name: res.Name, Colors: res.Colors, Font: res.Font}
	if sameTheme(o.lastTheme, next) {
		return
	}
	o.lastTheme = next
	// Pane 0: theme_changed is session-scoped and names no pane (see
	// app.EventThemeChanged).
	o.emitEvent(app.EventThemeChanged, 0, app.ThemeChangedEvent(next))
}

// seedTheme records the starting appearance without emitting, so a subscriber
// that connects later never receives a retroactive theme_changed for the theme
// it already sees. The structural-event seed's twin (seedStructure).
func (o *orch) seedTheme() {
	res := resolveTheme(o.cfg)
	o.lastTheme = app.ConfigTheme{Name: res.Name, Colors: res.Colors, Font: res.Font}
}

// sameTheme reports whether two resolved appearances are identical. Colors is a
// map, so this cannot be ==; resolveTheme builds a fresh map every call, and
// comparing the pointers would make every save look like a change.
func sameTheme(a, b app.ConfigTheme) bool {
	return a.Name == b.Name && a.Font == b.Font && maps.Equal(a.Colors, b.Colors)
}

// themeInfos builds the wire view of the theme registry: normalized palettes
// and concrete fonts, so front-ends can preview without resolving anything.
func themeInfos() []app.ThemeInfo {
	themes, _ := theme.Registry() // warns are logged when the page renders
	out := make([]app.ThemeInfo, 0, len(themes))
	for _, t := range themes {
		n := theme.Normalize(t)
		font := n.Font
		if font == "" {
			font = theme.DefaultFont
		}
		out = append(out, app.ThemeInfo{
			Name: n.Name, Label: n.Label, Dark: n.Dark, Source: n.Source,
			Colors: n.Colors, Font: font,
		})
	}
	return out
}

// configSnapshot builds the wire view of the current config. Maps are cloned so
// a marshalled reply can never alias the live config state.
func (o *orch) configSnapshot() app.ConfigGetResult {
	path := o.cfgPath
	if path == "" {
		path = config.DefaultPath()
	}
	c := o.cfg
	res := resolveTheme(c)
	return app.ConfigGetResult{
		Path: path,
		Theme: app.ConfigTheme{
			Name:   res.Name,
			Colors: res.Colors, // resolveTheme built this map; no aliasing
			Font:   res.Font,
		},
		ThemeOverrides: maps.Clone(c.Theme.Colors),
		Themes:         themeInfos(),
		CopyMode:       maps.Clone(c.Keybindings.CopyMode),
		Server: app.ConfigServerInfo{
			Addr:          c.Server.Addr,
			Auth:          c.Server.Auth,
			TLS:           c.Server.TLS.Enabled || c.Server.TLS.Cert != "",
			CathostSocket: c.Server.CathostSocket,
			ControlSocket: c.Server.ControlSocket,
			HookSocket:    c.Server.HookSocket,
			SessionTTL:    c.Server.SessionTTL,
			// The live roster, not c.Hosts: the synthesized local host is not in
			// the file, and connectivity is not a config fact at all.
			Hosts: o.Hosts(),
		},
		Options:         configOptions(c),
		RestartSections: slices.Clone(restartSections),
	}
}
