//go:build darwin

package main

import "C"

import (
	"encoding/json"
	"log"
	"strings"
)

// The native half of the settings screen's "app" tab (cmd/catway/web/js/
// 33-settings.js). The page reaches it through window.catsAppSettingsGet/Set,
// a reply-style WebKit bridge (window_darwin.m), and never through catway: the
// launcher's section lives in THIS machine's config.json, and in remote mode
// the catway serving the page is on another one.

// appSettingsView is what the screen edits: the launch mode and the saved
// catways. The current connection and the window layout are deliberately not
// in it — they are state the app keeps as it is used, and a settings save
// carrying a stale copy of either would undo whatever happened since the
// screen was opened.
type appSettingsView struct {
	Mode    string         `json:"mode"`
	Presets []remoteTarget `json:"presets"`
}

// catappSettingsGet returns the app section as JSON. Mode is the EFFECTIVE mode
// (build default applied), so the screen never shows "local" for a thin-client
// build that simply never wrote one down. The caller frees the result.
//
//export catappSettingsGet
func catappSettingsGet() *C.char {
	cfg := currentAppConfig()
	view := appSettingsView{Mode: cfg.Mode, Presets: cfg.Presets}
	if view.Presets == nil {
		view.Presets = []remoteTarget{} // [] not null: the page iterates it
	}
	data, err := json.Marshal(view)
	if err != nil { // plain structs; cannot fail
		return C.CString("null")
	}
	return C.CString(string(data))
}

// catappSettingsSet applies an edited app section and returns "" or an error
// message for the screen to show. The caller frees the result.
//
//export catappSettingsSet
func catappSettingsSet(cJSON *C.char) *C.char {
	var in appSettingsView
	if err := json.Unmarshal([]byte(C.GoString(cJSON)), &in); err != nil {
		return C.CString("malformed settings: " + err.Error())
	}
	if msg := applyAppSettings(in); msg != "" {
		return C.CString(msg)
	}
	return C.CString("")
}

// currentAppConfig is the freshest view of the app section: the thin client's
// in-memory copy when there is one (it may hold a connect that has not been
// re-read since), else the file.
func currentAppConfig() appConfig {
	if r := activeRemote; r != nil {
		return r.cfg
	}
	return loadAppConfig()
}

// applyAppSettings validates in, writes it, and — in remote mode — adopts it in
// the running thin client so the Connect menu redraws now and the client's next
// own save (a connect, a forget) does not write its old preset list back over
// this one. Runs on the main thread (WebKit delivers script messages there),
// which refreshMenu requires.
func applyAppSettings(in appSettingsView) string {
	switch in.Mode {
	case "local", "remote":
	default:
		return `mode: want "local" or "remote"`
	}
	// The same identity rules as the connect form: trimmed, de-duplicated by
	// URL, insertion order kept. upsertPreset is that rule, so it is reused
	// rather than restated.
	var clean appConfig
	for _, p := range in.Presets {
		if strings.TrimSpace(p.URL) == "" {
			return "saved catways: an entry has no address"
		}
		clean.upsertPreset(p)
	}

	// Re-read, then lay the two edited fields over it: the file's Remote and
	// Windows are newer than anything the screen saw.
	cfg := loadAppConfig()
	cfg.Mode = in.Mode
	cfg.Presets = clean.Presets
	if err := saveAppConfig(cfg); err != nil {
		log.Printf("could not save app settings: %v", err)
		return err.Error()
	}
	if r := activeRemote; r != nil {
		r.cfg.Mode = cfg.Mode
		r.cfg.Presets = cfg.Presets
		r.refreshMenu()
	}
	return ""
}
