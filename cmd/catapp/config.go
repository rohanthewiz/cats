//go:build darwin

package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// appConfig is the launcher's own persisted settings — deliberately separate
// from the catway's ~/.config/cats/config.yaml. It records which mode the
// window opens in and, for remote mode, where to point the webview, so a user's
// choice in the connect form survives relaunches. It lives in the platform
// app-data dir (see appDataDir) as app.json.
type appConfig struct {
	// Mode is "local" (supervise the in-bundle daemons) or "remote" (thin client
	// to a catway URL). An empty value falls back to the build-time defaultMode.
	Mode   string       `json:"mode"`
	Remote remoteTarget `json:"remote"`
	// Presets are the catways this client knows how to reach, in the order the
	// user added them. Remote is whichever one it is pointing at now, and is
	// kept as its own field rather than as an index because it is the only thing
	// a launch actually needs: the app must open on the last-used catway even if
	// the preset list is empty or was hand-edited into nonsense.
	//
	// A thin client that could only remember ONE address was the awkward part of
	// mode 2. People who use it have a laptop that follows them between a home
	// server, a work VPN and a relay, and switching meant deleting app.json.
	Presets []remoteTarget `json:"presets,omitempty"`
}

// remoteTarget is a catway a thin client can connect to: a relay host
// (https://<home-id>.relay.herdr.dev) or a direct LAN/VPN address. Only URL is
// load-bearing; Label is what the menu and the connect form show, defaulting to
// the URL's host.
type remoteTarget struct {
	URL   string `json:"url"`
	Label string `json:"label"`
}

// name is what to show for this target: the label the user gave it, else the
// host, else the raw URL. Never empty for a target with a URL, because an
// unnamed row in a menu is unclickable in practice.
func (t remoteTarget) name() string {
	if l := strings.TrimSpace(t.Label); l != "" {
		return l
	}
	if u, err := url.Parse(t.URL); err == nil && u.Host != "" {
		return u.Host
	}
	return t.URL
}

// upsertPreset records a target, replacing an existing entry with the same URL
// rather than adding a second one — reconnecting to a catway you already know
// is the common case, and it is how a label gets corrected.
//
// Order is insertion order and is preserved on update: the list becomes the
// menu, and a menu whose items move when you use them is a menu you cannot
// build muscle memory for.
func (c *appConfig) upsertPreset(t remoteTarget) {
	t.URL = strings.TrimSpace(t.URL)
	t.Label = strings.TrimSpace(t.Label)
	if t.URL == "" {
		return
	}
	for i, p := range c.Presets {
		if p.URL == t.URL {
			c.Presets[i] = t
			return
		}
	}
	c.Presets = append(c.Presets, t)
}

// removePreset forgets a target. The current connection is deliberately NOT
// cleared with it: the window is pointed at that catway right now, and yanking
// it out from under a live session to tidy a list is not what "forget" means.
func (c *appConfig) removePreset(rawURL string) {
	rawURL = strings.TrimSpace(rawURL)
	out := c.Presets[:0]
	for _, p := range c.Presets {
		if p.URL != rawURL {
			out = append(out, p)
		}
	}
	c.Presets = out
}

// currentPreset is the index of the preset the client is pointing at, -1 when
// the current URL is not a saved one (or there is none).
func (c appConfig) currentPreset() int {
	for i, p := range c.Presets {
		if p.URL == c.Remote.URL {
			return i
		}
	}
	return -1
}

// appConfigFile is the launcher settings filename inside appDataDir.
const appConfigFile = "app.json"

// appDataDir returns the per-user directory for the launcher's own state
// (app.json): ~/Library/Application Support/cats, the conventional home for a
// GUI app's support files, kept separate from the daemons' XDG config/state so
// packaging never disturbs existing sessions. (This launcher is macOS-only —
// see the darwin build constraint — so no other-platform branch is needed.)
func appDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	return filepath.Join(home, "Library", "Application Support", "cats"), nil
}

// loadAppConfig reads app.json, falling back to the build-time defaultMode on a
// first run or any read/parse problem — the launcher must always resolve to a
// usable mode, never fail to open. A malformed file is logged, not fatal.
func loadAppConfig() appConfig {
	cfg := appConfig{Mode: defaultMode}
	dir, err := appDataDir()
	if err != nil {
		log.Printf("app data dir unavailable, using build defaults: %v", err)
		return cfg
	}
	path := filepath.Join(dir, appConfigFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) { // a missing file is the normal first-run case
			log.Printf("read %s, using build defaults: %v", path, err)
		}
		return cfg
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("%s is malformed, using build defaults: %v", path, err)
		return appConfig{Mode: defaultMode}
	}
	if cfg.Mode == "" {
		cfg.Mode = defaultMode
	}
	return cfg
}

// saveAppConfig persists cfg to app.json (0600 in a 0700 dir — it can hold a
// remote URL that is nobody else's business). Parent dirs are created as needed.
func saveAppConfig(cfg appConfig) error {
	dir, err := appDataDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create app data dir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal app.json: %w", err)
	}
	path := filepath.Join(dir, appConfigFile)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
