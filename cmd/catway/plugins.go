//go:build ghostty

package main

import (
	"os"
	"os/exec"
	"path/filepath"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/plugin"
)

// The plugin commands (app.Backend seam, plugins dialog). Same shape as the
// worktree commands: each Start* runs on the loop goroutine, then does its
// filesystem work on its own goroutine and posts the Responder resolution back
// onto the loop, so a slow disk (or a large plugin dir removal) can never stall
// the orchestrator.
//
// Only list and uninstall live here. install/update are deliberately NOT server
// commands: they shell out to git and a build whose live output the user wants
// to watch, so the dialog spawns them as `catctl plugin …` in a fresh tab via
// tab.create — a pane is the streaming surface the app already has, and the
// server stays out of the subprocess business.

// StartPluginList scans the plugins root and answers with wire-ready entries:
// action argv already anchored to each plugin's dir, the identity env a launch
// must carry, and the resolved catctl path for the dialog's install/update tabs.
func (o *orch) StartPluginList(r app.Responder) {
	go func() {
		plugins, err := plugin.List()
		if err != nil {
			o.post(func() { r.Fail(err.Error()) })
			return
		}
		res := app.PluginListResult{Catctl: catctlPath(), Plugins: make([]app.PluginInfo, 0, len(plugins))}
		for _, p := range plugins {
			info := app.PluginInfo{
				ID:     p.ID,
				Dir:    p.Dir,
				Linked: p.Linked,
				Source: p.Source,
				Ref:    p.Ref,
			}
			if p.Err != nil {
				// A broken entry still lists (so it can be uninstalled) but
				// carries no launchable surface.
				info.Broken = p.Err.Error()
				res.Plugins = append(res.Plugins, info)
				continue
			}
			info.Name = p.Name
			info.Version = p.Version
			info.Description = p.Description
			info.Env = map[string]string{
				plugin.IDEnvVar:      p.ID,
				plugin.DirPathEnvVar: p.Dir,
			}
			for _, a := range p.Actions {
				info.Actions = append(info.Actions, app.PluginActionInfo{
					ID:    a.ID,
					Title: a.Title,
					Argv:  plugin.ActionArgv(p, a),
				})
			}
			res.Plugins = append(res.Plugins, info)
		}
		o.post(func() { r.OK(res) })
	}()
}

// StartPluginUninstall removes an installed plugin's directory (or unlinks a
// linked one) and answers with the same human outcome line the CLI prints.
func (o *orch) StartPluginUninstall(r app.Responder, p app.PluginUninstallParams) {
	go func() {
		msg, err := plugin.Uninstall(p.ID)
		if err != nil {
			o.post(func() { r.Fail(err.Error()) })
			return
		}
		o.post(func() { r.OK(app.PluginUninstallResult{Message: msg}) })
	}()
}

// catctlPath resolves the catctl binary the plugins dialog spawns for
// install/update tabs. Order: an explicit CATS_CATCTL override, the canonical
// name on PATH, then a sibling of the running server binary (make binaries and
// make local both drop catway and catctl into the same directory). The
// bare-name fallback makes a failed spawn at least say what was missing.
func catctlPath() string {
	if p := os.Getenv("CATS_CATCTL"); p != "" {
		return p
	}
	if p, err := exec.LookPath("catctl"); err == nil {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "catctl")
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand
		}
	}
	return "catctl"
}
