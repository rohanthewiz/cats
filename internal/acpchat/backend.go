// Package acpchat runs the chat side panel's engine: an external ACP agent
// subprocess and the loop-owned state machine that turns its stream into
// browserproto chat messages.
package acpchat

import "os/exec"

// BackendDef describes one ACP agent the chat panel can run. The registry is
// deliberately data, not code — ced's chat proved the whole per-agent
// difference fits in an argv plus auth strings, so adding an agent is one
// entry here, not a new implementation.
type BackendDef struct {
	ID   string // stable identifier ("copilot")
	Name string // display name for the status line ("Copilot")

	Binary string   // looked up on PATH; its absence is the "not installed" case
	Args   []string // argv tail that puts the binary in ACP server mode

	// DropEnv names environment variables stripped from the child. An agent's
	// credentials live in its own store, and a stray token in our environment
	// would silently authenticate it as a different identity — a failure that
	// reads as a broken agent rather than a wrong account.
	DropEnv []string

	// AuthHint and AuthArgv shape the panel's failure row when the agent dies
	// before serving: the hint is the transcript text, the argv is offered as
	// a button the client runs in a fresh tab (tab.create). Sign-in is the
	// overwhelmingly likely cause of a handshake death, so the remedy ships
	// with the failure instead of sending the user to the docs.
	AuthHint string
	AuthArgv []string
}

// Backends is the registry, first entry the default. Copilot only for now;
// the commented entries are the proven shapes for the next agents (ced runs
// all three through an identical seam).
func Backends() []BackendDef {
	return []BackendDef{
		{
			ID:      "copilot",
			Name:    "Copilot",
			Binary:  "copilot",
			Args:    []string{"--acp"},
			DropEnv: []string{"GH_TOKEN", "GITHUB_TOKEN"},
			AuthHint: "Copilot is not signed in — run `copilot login`, " +
				"then send your message again.",
			AuthArgv: []string{"copilot", "login"},
		},
		// {ID: "claude", Name: "Claude Code", Binary: "claude-code-acp"},
		// {ID: "gemini", Name: "Gemini", Binary: "gemini", Args: []string{"--experimental-acp"}},
	}
}

// LookPath resolves the backend binary; a package var so tests can pretend a
// binary exists (or doesn't) without touching PATH.
var LookPath = exec.LookPath
