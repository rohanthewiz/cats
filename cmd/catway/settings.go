//go:build ghostty

package main

import (
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
	if err := o.saveConfig(cfg); err != "" {
		r.Fail(err)
		return
	}
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

// broadcastTheme pushes the effective theme to every connected browser.
func (o *orch) broadcastTheme() {
	res := resolveTheme(o.cfg)
	o.broadcast(browserproto.NewTheme(res.Name, res.Colors, res.Font))
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
		},
	}
}
