//go:build ghostty

package main

// Plugins typed at a prompt.
//
// A plugin pane is normally known by its launch: the plugin host puts
// CATS_PLUGIN_ID in the spawn environment and createPane records it on the
// pane's durable state. A plugin the user types into a shell (`cats-todo` at a
// prompt) never passes through a launch, so nothing is recorded and, before
// this, the pane got no row in PLUGINS.
//
// The daemon closes that gap without learning what a plugin is: its pane_job
// report now names the executable the foreground job is running (the kernel's
// own path, symlinks resolved). Everything a typed plugin name resolves
// through ends inside the plugin's directory —
//
//	$ cats-todo
//	  └─ ~/.cats/bin/cats-todo                      (bin farm symlink)
//	       └─ <plugins-root>/<id>/bin/cats-todo      (stable link target)
//	            └─ <checkout>/bin/cats-todo          (dev-linked: <id> is a symlink)
//
// — so "which installed plugin's resolved dir contains this path" is the
// question, and a path prefix answers it. Matching on location rather than on
// the command's name is deliberate: a same-named binary built somewhere else,
// or a plugin's bin name reused by an unrelated tool, is not claimed.
//
// The answer is runtime-only (paneRuntime.handPlugin), never written to the
// pane's durable PluginID: it describes the job running now, it goes away the
// moment the job ends and the shell prompt returns, and a restarted catway
// re-learns it from the daemon's pane_job replay.
//
// Known gap: a plugin whose bin entry is a script is run by its interpreter,
// so the leader's executable is /bin/sh (or python, …), not a file in the
// plugin dir, and it still gets no row. Every plugin shipped today is a Go
// binary.

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/rohanthewiz/cats/internal/orchestration"
	"github.com/rohanthewiz/cats/internal/plugin"
)

// applyPaneJob mirrors a pane_job report onto the pane's runtime and, when the
// job's executable changed, re-resolves which plugin (if any) it is. Loop
// goroutine.
//
// The stale answer is dropped synchronously and the new one looked up off the
// loop: matching reads the plugins root and every manifest (plugin.List),
// which is disk work the orchestrator loop must never wait on — and pane_job
// arrives for every command typed in any pane, `ls` included.
func (o *orch) applyPaneJob(ev orchestration.PaneJob) {
	rt := o.panes[ev.PaneID]
	if rt == nil {
		return
	}
	rt.job = ev.Busy
	exe := ev.Exe
	if !ev.Busy {
		exe = "" // defensive: an exe with no job would describe nothing running
	}
	if exe == rt.jobExe {
		return
	}
	rt.jobExe = exe
	changed := rt.setHandPlugin("", "")
	if exe != "" {
		pane := ev.PaneID
		go func() {
			id, typ := lookupPluginForExe(exe)
			if id == "" {
				return
			}
			o.post(func() {
				// The pane may have closed, or moved on to another job, while
				// the lookup ran; an answer for a different exe is not this
				// pane's answer any more.
				if o.panes[pane] != rt || rt.jobExe != exe {
					return
				}
				if rt.setHandPlugin(id, typ) {
					o.broadcast(o.agentsMsg())
				}
			})
		}()
	}
	if changed {
		o.broadcast(o.agentsMsg())
	}
}

// setHandPlugin records the plugin the pane's foreground job was matched to
// and reports whether that changed anything the rollup draws.
func (rt *paneRuntime) setHandPlugin(id, typ string) bool {
	if rt.handPlugin == id && rt.handPluginType == typ {
		return false
	}
	rt.handPlugin, rt.handPluginType = id, typ
	return true
}

// plugin layers the hand-started match under a pane's recorded launch
// identity: the launch record wins when there is one (it is what the user
// actually asked the plugin host to run), and the foreground job's match fills
// in only for a pane nothing launched. Both rollup readers — agentsMsg and
// pane.list's paneMeta — go through here so they cannot disagree.
func (rt *paneRuntime) plugin(recorded, declared string) (id, typ string) {
	if recorded == "" && rt.handPlugin != "" {
		return rt.handPlugin, rt.handPluginType
	}
	return recorded, declared
}

// lookupPluginForExe reads the installed set and matches exe against it. Any
// failure to read the set is "no plugin" — the pane simply stays a plain shell,
// exactly as it was before this lookup existed.
func lookupPluginForExe(exe string) (id, typ string) {
	installed, err := plugin.List()
	if err != nil {
		return "", ""
	}
	return pluginForExe(installed, exe)
}

// pluginForExe finds the installed plugin whose directory contains exe and
// returns its id and declared type ("" for none).
//
// Each plugin's Dir is resolved through its symlinks before comparing, because
// exe arrives fully resolved from the kernel: an installed plugin's Dir is the
// entry under the plugins root, which may itself sit behind a symlinked parent
// (macOS's /var → /private/var, a symlinked ~/.config), and a linked plugin's
// Dir is already its checkout. Broken entries (Err set) are skipped — they
// have no manifest to report a type from, and nothing launchable in them.
func pluginForExe(installed []plugin.Installed, exe string) (id, typ string) {
	if exe == "" {
		return "", ""
	}
	for _, inst := range installed {
		if inst.Err != nil || inst.Dir == "" {
			continue
		}
		dir := inst.Dir
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			dir = real
		}
		if strings.HasPrefix(exe, dir+string(os.PathSeparator)) {
			return inst.ID, inst.Type
		}
	}
	return "", ""
}
