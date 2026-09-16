// Package peersync reconciles two catway backends: this one and a peer's.
//
// A cats instance's state of record is spread over three stores that have
// nothing in common but the user who owns them —
//
//	workspaces   the session model (session.json), keyed here by the folder a
//	             workspace identifies with (workspace.IdentityCwd)
//	todos        cats-todo's JSON backlogs: one per project folder
//	             (<project>/.cats-todo/todos.json) plus the global one
//	plugins      directories under the plugins root, each with a provenance
//	             record (.cats-plugin-source.json) saying where it came from
//
// — and a sync moves each across on its own terms. A workspace travels as its
// folder, and lands only where that folder exists on the other side (an
// "equivalent local folder", with the home directory translated between
// machines). A backlog travels as its rows and is MERGED, never replaced: rows
// the other side lacks are added, completion propagates, and everything that
// disagrees is kept as it was and reported. A plugin travels as its install
// source and is re-installed from git on the other side, because the built
// bytes are per-platform and the source record is the identity.
//
// # Shape of a sync
//
// Both sides speak the same envelope, a Bundle: "here is what I have". Sync
// is then symmetric by construction —
//
//	initiator                                   peer
//	   │  GET  /peer/v1/bundle?want=…             │
//	   │ ────────────────────────────────────────▶│
//	   │ ◀──────────────────── Bundle (theirs) ───│
//	   │  Apply(theirs) locally     ← "pull"      │
//	   │  POST /peer/v1/apply  Bundle (mine)      │
//	   │ ────────────────────────────────────────▶│
//	   │                 Apply(mine) there ← "push"
//	   │ ◀────────────────────── ApplyReport ─────│
//
// and the initiator's SyncReport is the two ApplyReports side by side: what
// changed here, what changed there, and — the part the user asked for by name
// — everything that did not sync, each with its reason.
//
// # What is never done
//
// Nothing is deleted or overwritten on either side. A sync can only add a
// workspace, add or complete a todo, or install a plugin. A backlog row that
// differs between the two machines stays as each machine had it and is listed
// as a conflict; a plugin installed on both is left alone even when the
// versions differ (plugin update is its own, deliberate, per-plugin act). That
// is what makes it safe to run a sync in either direction, repeatedly, without
// reading the report first — the report is for finding out what still needs a
// human, not for discovering what was lost.
package peersync

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

// Schema is the bundle envelope version. It changes only when the shape of
// the envelope changes in a way a reader has to know about; adding optional
// fields is not that.
const Schema = 1

// The peer HTTP surface a catway exposes. They sit behind the same auth guard
// as everything else catway serves, so the credential is that catway's shared
// secret as a bearer token — see config.Peer.
const (
	PathHello  = "/peer/v1/hello"  // GET  → Instance
	PathBundle = "/peer/v1/bundle" // GET ?want=workspaces,todos,plugins → Bundle
	PathApply  = "/peer/v1/apply"  // POST Bundle → ApplyReport
)

// Instance identifies one catway to another: enough for a report to say who
// it talked to, and for path translation to work (Home).
type Instance struct {
	Hostname string `json:"hostname"`
	User     string `json:"user,omitempty"`
	Home     string `json:"home"` // the user's home directory on that machine
	OS       string `json:"os"`   // runtime.GOOS
	Version  string `json:"version,omitempty"`
}

// Name is how a report refers to the instance: user@host, or just the host.
func (i Instance) Name() string {
	if i.User != "" {
		return i.User + "@" + i.Hostname
	}
	return i.Hostname
}

// LocalInstance describes the machine this process runs on.
func LocalInstance(version string) Instance {
	inst := Instance{OS: runtime.GOOS, Version: version}
	inst.Hostname, _ = os.Hostname()
	inst.Home, _ = os.UserHomeDir()
	if u, err := user.Current(); err == nil {
		inst.User = u.Username
	}
	return inst
}

// Want is the set of categories a sync covers. Every one is opt-in: the
// caller says which stores it means, and an empty Want is refused up front
// rather than treated as "everything".
type Want struct {
	Workspaces bool `json:"workspaces,omitempty"`
	Todos      bool `json:"todos,omitempty"`
	Plugins    bool `json:"plugins,omitempty"`
}

// Any reports whether at least one category is selected.
func (w Want) Any() bool { return w.Workspaces || w.Todos || w.Plugins }

// String is the comma-separated query form ("workspaces,todos").
func (w Want) String() string {
	var parts []string
	if w.Workspaces {
		parts = append(parts, "workspaces")
	}
	if w.Todos {
		parts = append(parts, "todos")
	}
	if w.Plugins {
		parts = append(parts, "plugins")
	}
	return strings.Join(parts, ",")
}

// ParseWant reads the query form. Unknown words are ignored rather than
// refused so an older peer asked for a category it does not know simply
// answers without it — the report then shows that category as absent.
func ParseWant(s string) Want {
	var w Want
	for p := range strings.SplitSeq(s, ",") {
		switch strings.TrimSpace(p) {
		case "workspaces":
			w.Workspaces = true
		case "todos":
			w.Todos = true
		case "plugins":
			w.Plugins = true
		}
	}
	return w
}

// WorkspaceEntry is one workspace as it travels: the folder it identifies
// with on the sender, and its pinned name if it has one. Tabs and panes do
// not travel — they are terminals on the sender's machine.
type WorkspaceEntry struct {
	Cwd  string `json:"cwd"`
	Name string `json:"name,omitempty"`
}

// Backlog is one todos.json as it travels. Cwd is the project folder on the
// sender ("" for the global backlog). Todos are the file's rows, each kept as
// the generic object it was read as: this package deliberately does not know
// cats-todo's Todo struct, so a field it has never heard of round-trips intact
// instead of being dropped by an older schema (the same discipline cats-todo's
// own bundle format keeps). Files are the attachments those rows reference,
// keyed by the same backlog-relative path the rows use (images/<id>/<file>).
type Backlog struct {
	Cwd   string            `json:"cwd,omitempty"`
	Todos []map[string]any  `json:"todos"`
	Files map[string][]byte `json:"files,omitempty"`
}

// PluginEntry is one installed plugin as it travels: its identity and where
// it was installed from. Linked (a developer's checkout registered in place)
// and Broken entries travel too, so the receiver can say WHY they did not
// install rather than silently not offering them.
type PluginEntry struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
	Source  string `json:"source,omitempty"` // what was typed at install (owner/repo or URL)
	Ref     string `json:"ref,omitempty"`
	Linked  bool   `json:"linked,omitempty"`
	Broken  string `json:"broken,omitempty"` // manifest error, when the entry is unusable
}

// Bundle is the envelope both sides exchange: one side's state, restricted to
// the categories in Want. A category outside Want is absent, not empty — the
// receiver checks Want before reading, so "no workspaces were asked for" and
// "there are no workspaces" never look alike.
type Bundle struct {
	Schema   int      `json:"schema"`
	Instance Instance `json:"instance"`
	Want     Want     `json:"want"`

	Workspaces []WorkspaceEntry `json:"workspaces,omitempty"`
	Global     *Backlog         `json:"global,omitempty"`
	Backlogs   []Backlog        `json:"backlogs,omitempty"`
	Plugins    []PluginEntry    `json:"plugins,omitempty"`
}

// TranslatePath maps a path from another machine onto this one. The only
// rewrite is the home directory: a path under the sender's home lands under
// ours, so /Users/me/projs/x on a Mac and /home/me/projs/x on a Linux box are
// the same project. Anything else (a path outside home, or a sender that did
// not say where its home is) is passed through as-is and left to the
// existence check that follows every translation.
func TranslatePath(p, fromHome, toHome string) string {
	if p == "" || fromHome == "" || toHome == "" {
		return p
	}
	fromHome = filepath.Clean(fromHome)
	p = filepath.Clean(p)
	if p == fromHome {
		return toHome
	}
	if rel, ok := strings.CutPrefix(p, fromHome+string(filepath.Separator)); ok {
		return filepath.Join(toHome, rel)
	}
	return p
}

// dirExists reports whether p names an existing directory (a symlink to one
// counts — Stat follows it, and a workspace on a linked folder is a folder).
func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
