package main

// The `plugin` verb family is the cats plugin host's CLI (internal/plugin).
// install/link/update/uninstall/list never dial the control socket — they
// manage the plugins directory, exactly like `integration` (update does reach
// the plugin's git remote, but not the cats server).
// `run` is the one online verb: it resolves an action locally, then launches
// it in a fresh tab through the §7 tab.create command's spawn params
// (command/cwd/env) — the server stays plugin-agnostic; the manifest never
// crosses the socket.

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/ctlproto"
	"github.com/rohanthewiz/cats/internal/plugin"
)

// runPluginCmd dispatches `catctl plugin <subcommand> ...` and returns the
// process exit code (0 ok / 1 error / 2 usage — the integration convention).
// socket is the already-parsed global --socket value, forwarded because this
// family dispatches before main's post-verb flag re-parse.
func runPluginCmd(args []string, socket string) int {
	if len(args) == 0 {
		printPluginHelp()
		return 2
	}
	switch args[0] {
	case "install":
		return pluginInstall(args[1:])
	case "link":
		return pluginLink(args[1:])
	case "update":
		return pluginUpdate(args[1:])
	case "uninstall":
		return pluginUninstall(args[1:])
	case "list":
		return pluginList(args[1:])
	case "run":
		return pluginRun(args[1:], socket)
	case "help", "--help", "-h":
		printPluginHelp()
		return 0
	default:
		printPluginHelp()
		return 2
	}
}

func pluginInstall(args []string) int {
	var source, ref string
	// Manual scan instead of a FlagSet: the only flag is --ref, and keeping
	// the family FlagSet-free avoids the global-FlagSet re-parse pitfall that
	// shaped send/run (see subcommands.go).
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--ref":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "catctl: --ref requires a value")
				return 2
			}
			i++
			ref = args[i]
		case len(args[i]) > 6 && args[i][:6] == "--ref=":
			ref = args[i][6:]
		case source == "":
			source = args[i]
		default:
			fmt.Fprintln(os.Stderr, "usage: catctl plugin install <owner/repo|git-url> [--ref <branch|tag>]")
			return 2
		}
	}
	if source == "" {
		fmt.Fprintln(os.Stderr, "usage: catctl plugin install <owner/repo|git-url> [--ref <branch|tag>]")
		return 2
	}
	inst, err := plugin.Install(source, ref, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("installed %s v%s (%s)\n", inst.ID, inst.Version, inst.Dir)
	return 0
}

func pluginLink(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: catctl plugin link <dir>")
		return 2
	}
	inst, err := plugin.Link(args[0], os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("linked %s v%s → %s\n", inst.ID, inst.Version, inst.Dir)
	return 0
}

// pluginUpdate refreshes an installed plugin from its recorded source. Fully
// offline with respect to the cats server (it never dials the socket), though
// it does hit the plugin's git remote.
func pluginUpdate(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: catctl plugin update <id>")
		return 2
	}
	inst, updated, err := plugin.Update(args[0], os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !updated {
		fmt.Printf("%s is already up to date (v%s)\n", inst.ID, inst.Version)
		return 0
	}
	fmt.Printf("updated %s to v%s (%s)\n", inst.ID, inst.Version, inst.Dir)
	return 0
}

func pluginUninstall(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: catctl plugin uninstall <id>")
		return 2
	}
	msg, err := plugin.Uninstall(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(msg)
	return 0
}

func pluginList(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: catctl plugin list")
		return 2
	}
	plugins, err := plugin.List()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(plugins) == 0 {
		fmt.Println("no plugins installed (try `catctl plugin install <owner/repo>`)")
		return 0
	}
	for _, p := range plugins {
		if p.Err != nil {
			fmt.Printf("%s: broken (%v)\n", p.ID, p.Err)
			continue
		}
		kind := "installed"
		if p.Linked {
			kind = "linked"
		}
		fmt.Printf("%s v%s (%s, %s)\n", p.ID, p.Version, kind, p.Dir)
		for _, a := range p.Actions {
			fmt.Printf("  action %-12s %s\n", a.ID, a.Title)
		}
	}
	return 0
}

const pluginRunUsage = "usage: catctl plugin run <id> [action] [--all]"

// pluginRun launches an action in a fresh tab: one tab.create round trip
// carrying the resolved argv, the invoking directory as cwd (a plugin like
// cats-todo keys per-project state off it), and the plugin identity env vars.
// Deliberately no Title: a pinned title would block tab auto-naming forever,
// while the plugin's own OSC title (or its cwd) names the tab live — cats-todo
// reads "todo: <project>" instead of a stale manifest label.
//
// --all fans the same action out across every workspace instead (pluginRunAll),
// the CLI twin of the plugins dialog's "run all" button.
func pluginRun(args []string, socket string) int {
	// Manual scan instead of a FlagSet, matching pluginInstall: the family stays
	// FlagSet-free to avoid the global-FlagSet re-parse pitfall (subcommands.go).
	all := false
	pos := make([]string, 0, len(args))
	for _, a := range args {
		switch {
		case a == "--all":
			all = true
		case len(a) > 0 && a[0] == '-':
			// Caught here rather than left to fall through as an id: a mistyped
			// flag would otherwise come back as "no such plugin --foo", which
			// blames the plugin set for a typo in the command line.
			fmt.Fprintf(os.Stderr, "catctl: unknown flag %s\n%s\n", a, pluginRunUsage)
			return 2
		default:
			pos = append(pos, a)
		}
	}
	if len(pos) < 1 || len(pos) > 2 {
		fmt.Fprintln(os.Stderr, pluginRunUsage)
		return 2
	}
	inst, err := plugin.Get(pos[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	actionID := ""
	if len(pos) == 2 {
		actionID = pos[1]
	}
	action, ok := inst.FindAction(actionID)
	if !ok {
		fmt.Fprintf(os.Stderr, "plugin %s has no action %q (see `catctl plugin list`)\n", inst.ID, actionID)
		return 1
	}

	argv := plugin.ActionArgv(inst, action)
	env := map[string]string{
		plugin.IDEnvVar:      inst.ID,
		plugin.DirPathEnvVar: inst.Dir,
	}
	sock := ctlproto.ResolveSocket(socket)
	if all {
		return pluginRunAll(sock, argv, env)
	}

	cwd, _ := os.Getwd()
	params, err := json.Marshal(app.TabCreateParams{Cwd: cwd, Command: argv, Env: env})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	resp, err := ctlproto.Call(sock,
		ctlproto.Request{ID: "1", Method: app.CmdTabCreate, Params: params}, 10*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "catctl: %v\n", err)
		return 2
	}
	printResult(resp)
	if !resp.OK {
		return 1
	}
	return 0
}

// pluginRunAll starts one action in every workspace: a workspace.list to learn
// the roster, then one tab.create per workspace naming its target. Nothing is
// focused and nothing is switched to — the viewport stays where the user left
// it and the tabs are simply open when they arrive.
//
// Two things differ from the single-workspace launch, both deliberate:
//
//   - No cwd. The single launch sends the invoking directory because "here" is
//     what the user meant; a fan-out has no single "here", and sending this
//     shell's directory would start every copy against one project. Omitting it
//     lets each tab inherit its own workspace's directory, so a per-project
//     plugin scopes to the project it landed in.
//   - Locked workspaces are skipped rather than attempted. The server would
//     refuse them anyway, but a lock means "no automation lands here" and a
//     fan-out is the automation it was written for — so a skip is the expected
//     outcome and is reported as one, not as a failure.
//
// The report is one line per workspace on stdout, in session order, whatever
// happened to it; only the closing tally of genuine failures goes to stderr.
// Exit 1 when anything failed, and also when nothing started at all — a command
// that had no effect should not look like success to a script.
func pluginRunAll(sock string, argv []string, env map[string]string) int {
	resp, err := ctlproto.Call(sock,
		ctlproto.Request{ID: "1", Method: app.CmdWorkspaceList}, 10*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "catctl: %v\n", err)
		return 2
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "error: %s\n", resp.Error)
		return 1
	}
	var list app.WorkspaceListResult
	if err := json.Unmarshal(resp.Data, &list); err != nil {
		fmt.Fprintf(os.Stderr, "catctl: unreadable workspace.list: %v\n", err)
		return 1
	}
	if len(list.Workspaces) == 0 {
		fmt.Fprintln(os.Stderr, "catctl: no workspaces")
		return 1
	}

	started, failed := 0, 0
	for i, w := range list.Workspaces {
		if w.Locked {
			fmt.Printf("%-4s skipped (locked)\n", w.ID)
			continue
		}
		params, err := json.Marshal(app.TabCreateParams{Workspace: w.ID, Command: argv, Env: env})
		if err != nil {
			fmt.Printf("%-4s failed: %v\n", w.ID, err)
			failed++
			continue
		}
		// A fresh request id per workspace: ctlproto.Call is one connection per
		// call, so this only matters for a reader correlating the transcript,
		// but a transcript of six replies all labelled "1" is unreadable.
		resp, err := ctlproto.Call(sock,
			ctlproto.Request{ID: fmt.Sprint(i + 2), Method: app.CmdTabCreate, Params: params}, 10*time.Second)
		switch {
		case err != nil:
			fmt.Printf("%-4s failed: %v\n", w.ID, err)
			failed++
		case !resp.OK:
			fmt.Printf("%-4s failed: %s\n", w.ID, resp.Error)
			failed++
		default:
			var res app.TabCreateResult
			if err := json.Unmarshal(resp.Data, &res); err != nil {
				fmt.Printf("%-4s started\n", w.ID) // it ran; only the echo was unreadable
			} else {
				fmt.Printf("%-4s tab %d (pane %d)\n", w.ID, res.Num, res.Pane)
			}
			started++
		}
	}
	if failed > 0 {
		fmt.Fprintf(os.Stderr, "catctl: %d of %d workspaces failed\n", failed, len(list.Workspaces))
		return 1
	}
	if started == 0 {
		fmt.Fprintln(os.Stderr, "catctl: nothing started — every workspace is locked")
		return 1
	}
	return 0
}

func printPluginHelp() {
	fmt.Fprint(os.Stderr, `catctl plugin commands:
  catctl plugin install <owner/repo|git-url> [--ref <branch|tag>]
  catctl plugin link <dir>          register a local checkout (dev mode)
  catctl plugin update <id>         fetch the recorded source and rebuild
  catctl plugin uninstall <id>
  catctl plugin list
  catctl plugin run <id> [action]   launch an action in a new tab (needs a running server)
                     [--all]        ... in every workspace instead (locked ones are skipped)
`)
}
