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

// pluginRun launches an action in a fresh tab: one tab.create round trip
// carrying the resolved argv, the invoking directory as cwd (a plugin like
// cats-todo keys per-project state off it), the action title as the tab name,
// and the plugin identity env vars.
func pluginRun(args []string, socket string) int {
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprintln(os.Stderr, "usage: catctl plugin run <id> [action]")
		return 2
	}
	inst, err := plugin.Get(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	actionID := ""
	if len(args) == 2 {
		actionID = args[1]
	}
	action, ok := inst.FindAction(actionID)
	if !ok {
		fmt.Fprintf(os.Stderr, "plugin %s has no action %q (see `catctl plugin list`)\n", inst.ID, actionID)
		return 1
	}

	title := action.Title
	if title == "" {
		title = inst.Name
	}
	cwd, _ := os.Getwd()
	params, err := json.Marshal(app.TabCreateParams{
		Title:   title,
		Cwd:     cwd,
		Command: plugin.ActionArgv(inst, action),
		Env: map[string]string{
			plugin.IDEnvVar:      inst.ID,
			plugin.DirPathEnvVar: inst.Dir,
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	resp, err := ctlproto.Call(ctlproto.ResolveSocket(socket),
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

func printPluginHelp() {
	fmt.Fprint(os.Stderr, `catctl plugin commands:
  catctl plugin install <owner/repo|git-url> [--ref <branch|tag>]
  catctl plugin link <dir>          register a local checkout (dev mode)
  catctl plugin update <id>         fetch the recorded source and rebuild
  catctl plugin uninstall <id>
  catctl plugin list
  catctl plugin run <id> [action]   launch an action in a new tab (needs a running server)
`)
}
