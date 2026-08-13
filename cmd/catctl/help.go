package main

// help.go is `catctl help [topic]`. The bare form is main's usage(); with a
// topic it prints one verb's page — synopsis, what it does, the §7 method it
// builds, and, for the verbs whose shape is not self-evident from a one-liner,
// the notes a user would otherwise have to read the source for.
//
// Naming a family (integration, plugin, probe, completion) forwards to that
// family's own help, so `catctl help <TAB>` and `catctl help <anything it
// offered>` always lead somewhere.

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/ctlproto"
)

// verbDetail holds the paragraphs a one-line summary cannot carry. Only the
// verbs that genuinely need one appear here; the rest are fully described by
// their synopsis, summary and method. Kept out of the subcommands table so that
// table stays a scannable vocabulary rather than a document.
var verbDetail = map[string]string{
	"split": `Direction is how the new border runs: h puts the panes side by side, v
stacks them. Without a pane, the focused one is split.`,

	"cycle": `Moves focus in creation order within the tab, wrapping at the ends.
For spatial movement use focus-dir.`,

	"resize": `Border ids come from the pane tree, not from ` + "`catctl panes`" + `; ratio is the
fraction of the split given to the first child (0.5 is even).`,

	"capture": `With no line count, captures the entire scrollback buffer; with one, only
the last N rows. ANSI colour and unwrapping options are reachable only
through the raw form:

    catctl capture --params '{"pane":1,"scope":1,"lines":100,"ansi":true}'`,

	"read": `Points are [row,col], both zero-based, in the pane's current viewport.
Rectangular (rather than line-flowed) selection needs the raw form:

    catctl read --params '{"pane":1,"anchor":[0,0],"cursor":[0,5],"rect":true}'`,

	"wait": `The pattern is a plain substring, and the command blocks until it appears
in the pane's output or the timeout elapses (fractional seconds are fine).
The round-trip deadline is widened to cover the wait, so --timeout rarely
needs touching. Regex and line-scoped matching need the raw form.`,

	"send": `Types the text without submitting it, leaving it staged in the pane for
review — the tmux send-keys habit. Follow with a bare ` + "`run <pane>`" + ` to press
Enter. Words are joined with single spaces; quote the text to keep exact
whitespace.`,

	"run": `Types the text and presses Enter. With no text it sends only the Enter,
submitting whatever an earlier ` + "`send`" + ` staged.`,

	"events": `Streams pane events — exit, agent, title, cwd, and the structural
added/removed/focus_changed — one JSON object per line, until Ctrl-C.
With a pane, only that pane's events. Filtering by event type needs the
raw form:

    catctl events.subscribe --params '{"events":["agent","exit"]}'`,

	"theme": `Switching themes replaces any per-key colour overrides with the theme's
own palette — stale overrides from the previous theme are exactly what a
switch has to shed. ` + "`catctl themes`" + ` lists what is available.`,

	"new-ws": `With no name the workspace is auto-named and rooted in the session's own
directory. A start directory needs the raw form, since an ergonomic verb
only ever sees positional args:

    catctl workspace.create --params '{"name":"api","path":"~/src/api"}'`,

	"stop": `Stops the cats server. The terminals themselves survive — they belong to
cathost — so a restarted server reattaches to them.`,
}

// runHelp implements `catctl help [topic]`.
func runHelp(args []string) int {
	if len(args) == 0 {
		usage()
		return 0
	}
	topic := args[0]
	switch topic {
	case "integration":
		printIntegrationHelp()
		return 0
	case "plugin":
		printPluginHelp()
		return 0
	case "completion":
		printCompletionHelp()
		return 0
	case "probe":
		fmt.Fprintln(os.Stderr, "catctl probe [--url ws://...] [--script '...'] [--cols N] [--rows N]")
		fmt.Fprintln(os.Stderr, "  headless browser-protocol WebSocket probe; op reference: cmd/catctl/probe.go")
		return 0
	case "commands":
		fmt.Fprintln(os.Stderr, "catctl commands — print the raw §7 method names, one per line")
		return 0
	case ctlproto.MethodPair:
		printPairHelp()
		return 0
	case "help":
		fmt.Fprintln(os.Stderr, "catctl help [verb] — the verb table, or one verb's page")
		return 0
	}

	if sc, ok := lookupSubcommand(topic); ok {
		printVerbHelp(sc)
		return 0
	}
	if ctlproto.IsTransportMethod(topic) || slices.Contains(app.CommandNames(), topic) {
		kind := "a raw §7 command"
		if ctlproto.IsTransportMethod(topic) {
			// Worth saying: a transport method is answered by the control server
			// itself and is absent from `catctl commands`, so a reader who went
			// looking for it there and found nothing is not misreading the list.
			kind = "a control-transport method (not on the §7 table)"
		}
		fmt.Fprintf(os.Stderr, "%s — %s\n\n  catctl %s [--params '<json>']\n\n", topic, kind, topic)
		if verb, ok := verbForMethod(topic); ok {
			fmt.Fprintf(os.Stderr, "The ergonomic verb `catctl %s` builds its params for you (catctl help %s).\n", verb, verb)
		}
		return 0
	}

	fmt.Fprintf(os.Stderr, "catctl: no help for %q (try `catctl help`)\n", topic)
	return 2
}

// printPairHelp documents the pairing verb. It gets a page of its own rather
// than a one-liner because the thing a reader most needs to know is what the
// code is *not*: it is not the password, and it stops working almost at once.
func printPairHelp() {
	fmt.Fprint(os.Stderr, `catctl pair

  Mint a one-time device-pairing code and print it as a scannable QR.

Scan it with the cats mobile app (or open the printed cats:// link on the
device) to pair a phone with this server. The code carries the server's URL,
a single-use token, and — when serving HTTPS — the certificate fingerprint the
app pins.

The token is not the access password. It expires in a few minutes, works once,
and what it buys is an ordinary session credential: bounded by the session TTL
and revoked by restarting the server. That is the whole reason pairing exists
rather than a command that prints the password.

Requires auth to be enabled; under --auth none there is nothing to pair with.

  catctl pair            print the code
  catctl --json pair     the raw response, for scripting

`)
}

// printVerbHelp renders one ergonomic verb's page.
func printVerbHelp(sc subcommand) {
	fmt.Fprintf(os.Stderr, "catctl %s\n\n  %s\n\n", sc.synopsis, sc.summary)
	if d, ok := verbDetail[sc.verb]; ok {
		fmt.Fprintf(os.Stderr, "%s\n\n", d)
	}
	fmt.Fprintf(os.Stderr, "Builds: %s\n", sc.method)
	if hints := argHints(sc); hints != "" {
		fmt.Fprintf(os.Stderr, "Completes: %s\n", hints)
	}
}

// argHints describes what shell completion offers for this verb's arguments —
// the quickest way to learn that <pane> means a live id you can Tab through
// rather than something to look up first.
func argHints(sc subcommand) string {
	var parts []string
	for i, k := range sc.args {
		var what string
		switch k {
		case argPane:
			what = "live pane ids"
		case argTab:
			what = "live tab numbers"
		case argWorkspace:
			what = "live workspace ids"
		case argTheme:
			what = "theme names"
		case argDirection:
			what = "left|right|up|down"
		case argSplitDir:
			what = "h|v"
		case argCycleDir:
			what = "next|prev"
		default:
			continue
		}
		parts = append(parts, fmt.Sprintf("arg %d: %s", i+1, what))
	}
	return strings.Join(parts, "; ")
}

// verbForMethod finds the ergonomic verb that builds a raw method, so the raw
// page can point at the friendlier form.
func verbForMethod(method string) (string, bool) {
	for _, sc := range subcommands {
		if sc.method == method {
			return sc.verb, true
		}
	}
	return "", false
}
