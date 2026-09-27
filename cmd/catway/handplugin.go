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
// A plugin whose bin entry is a script is run by its interpreter, so the
// leader's executable is /bin/sh (or python, …) and no location test on it can
// work. The kernel still names the script: it exec's a `#!` file by running
// the interpreter with the script's path spliced into argv, and pane_job
// carries the head of that argv —
//
//	$ cats-notes list
//	  exe:  /bin/sh
//	  argv: [/bin/sh /Users/x/.cats/bin/cats-notes list]
//	                  └─ resolves to <plugins-root>/<id>/bin/cats-notes,
//	                     a declared bin entry of <id>
//
// — so when the exe is nobody's, each argv entry is resolved and compared with
// the installed plugins' declared bin entries (pluginForScript). That test is
// deliberately narrower than the exe one: any file inside a plugin dir, or
// even a bin entry, shows up in argv for jobs that merely *read* it
// (`vim bin/cats-notes`, `less README`). Two checks keep those out: only a
// declared bin entry counts, and its own `#!` line must name the program in
// argv[0] — which the kernel (or env) sets from that line for a real run, and
// which is `vim` or `less` for a reader.

import (
	"bufio"
	"os"
	"path/filepath"
	"slices"
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
	exe, argv := ev.Exe, ev.Argv
	if !ev.Busy {
		// defensive: an exe or argv with no job would describe nothing running
		exe, argv = "", nil
	}
	if exe == rt.jobExe && slices.Equal(argv, rt.jobArgv) {
		return
	}
	rt.jobExe, rt.jobArgv = exe, argv
	changed := rt.setHandPlugin("", "")
	if exe != "" || len(argv) > 0 {
		pane := ev.PaneID
		// The shell's cwd, read here on the loop goroutine: it is what a
		// relative script path in argv (`sh ./bin/cats-notes`) was typed
		// against, since the job inherited it at launch.
		cwd := rt.cwd
		go func() {
			id, typ := lookupPluginForJob(exe, argv, cwd)
			if id == "" {
				return
			}
			o.post(func() {
				// The pane may have closed, or moved on to another job, while
				// the lookup ran; an answer for a different job is not this
				// pane's answer any more.
				if o.panes[pane] != rt || rt.jobExe != exe || !slices.Equal(rt.jobArgv, argv) {
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

// lookupPluginForJob reads the installed set and matches the job against it:
// by its executable first (a binary plugin, the common case and the cheaper
// test), then by the script its argv names. Any failure to read the set is
// "no plugin" — the pane simply stays a plain shell, exactly as it was before
// this lookup existed.
func lookupPluginForJob(exe string, argv []string, cwd string) (id, typ string) {
	installed, err := plugin.List()
	if err != nil {
		return "", ""
	}
	if id, typ := pluginForExe(installed, exe); id != "" {
		return id, typ
	}
	return pluginForScript(installed, argv, cwd)
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

// pluginForScript finds the installed plugin one of whose declared bin entries
// is the script argv is running, and returns its id and declared type ("" for
// none). cwd resolves a relative argv entry; "" leaves relative entries
// unmatched.
//
// Every entry after argv[0] is a candidate, not just argv[1]: a `#!` line's
// optional argument (`#!/bin/bash -e`) or an `env -S` split (`#!/usr/bin/env
// -S deno run`) puts words before the script. The daemon sends only the head
// of argv (orchestration.JobArgvMax), so this is a handful of lstat walks, and
// it runs off the orchestrator loop.
//
// Paths are compared fully resolved on both sides: argv holds whatever path
// the shell exec'd (the ~/.cats/bin farm link, typically), and a bin entry may
// sit behind a dev-linked plugin dir or a symlinked parent, so only the real
// file is a stable identity.
func pluginForScript(installed []plugin.Installed, argv []string, cwd string) (id, typ string) {
	if len(argv) < 2 {
		return "", ""
	}
	// Resolved bin entry → owning plugin. Built per lookup rather than cached:
	// the lookup already re-reads every manifest (plugin.List), installs and
	// links change under a running catway, and lookups happen once per job.
	bins := map[string]plugin.Installed{}
	for _, inst := range installed {
		if inst.Err != nil || inst.Dir == "" {
			continue
		}
		for _, b := range inst.Bin {
			if real, err := filepath.EvalSymlinks(filepath.Join(inst.Dir, b)); err == nil {
				bins[real] = inst
			}
		}
	}
	if len(bins) == 0 {
		return "", ""
	}
	for _, arg := range argv[1:] {
		// A flag is never the script; skipping it saves the stat.
		if arg == "" || strings.HasPrefix(arg, "-") {
			continue
		}
		path := arg
		if !filepath.IsAbs(path) {
			if cwd == "" {
				continue
			}
			path = filepath.Join(cwd, path)
		}
		real, err := filepath.EvalSymlinks(path)
		if err != nil {
			continue
		}
		inst, ok := bins[real]
		if !ok {
			continue
		}
		// The bin entry is in argv; now make sure it is being *run*, not read.
		if interp := shebangInterpreter(real); interp != "" && filepath.Base(interp) == filepath.Base(argv[0]) {
			return inst.ID, inst.Type
		}
	}
	return "", ""
}

// shebangInterpreter returns the program a `#!` script's first line runs it
// with, "" for a file that is not a `#!` script (or cannot be read). Through
// env (`#!/usr/bin/env python3`, `#!/usr/bin/env -S deno run`) it is the
// program env goes on to exec, because that — not env — is what argv[0] holds
// once the job is running: env exec's in place, keeping the leader's pid.
//
// Only the base name matters to the caller: argv[0] is the shebang's path as
// written for a kernel-run script ("/bin/sh") but the bare word env was given
// ("python3") for an env-run one, and either way it is compared by name.
func shebangInterpreter(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	// A shebang line is bounded by the kernel (256 bytes on Linux before 5.1,
	// 512 on macOS); reading a little past that is enough, and a binary with
	// no newline in reach simply fails the prefix test.
	line, err := bufio.NewReaderSize(f, 1024).ReadSlice('\n')
	if err != nil && len(line) == 0 {
		return ""
	}
	rest, ok := strings.CutPrefix(string(line), "#!")
	if !ok {
		return ""
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	if filepath.Base(fields[0]) != "env" {
		return fields[0]
	}
	// env's own flags (-S, -i, -u NAME) and NAME=value assignments come before
	// the program. -u takes a separate argument, which is skipped with it.
	for i := 1; i < len(fields); i++ {
		w := fields[i]
		switch {
		case w == "-u" || w == "--unset":
			i++
		case strings.HasPrefix(w, "-"), strings.Contains(w, "="):
		default:
			return w
		}
	}
	return ""
}
