package peersync

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/rohanthewiz/cats/internal/plugin"
)

// collectPlugins lists this machine's plugins as they travel. A broken entry
// (directory present, manifest missing or invalid) travels with its error so
// the other side can say why nothing was installed for it, and a linked one
// travels marked, for the same reason — both are facts about this machine
// that the report on the other side should be able to state.
func collectPlugins() ([]PluginEntry, error) {
	installed, err := plugin.List()
	if err != nil {
		return nil, err
	}
	out := make([]PluginEntry, 0, len(installed))
	for _, p := range installed {
		e := PluginEntry{ID: p.ID, Linked: p.Linked, Source: p.Source, Ref: p.Ref}
		if p.Err != nil {
			e.Broken = p.Err.Error()
		} else {
			e.Name, e.Version = p.Name, p.Version
		}
		out = append(out, e)
	}
	return out, nil
}

// applyPlugins installs, on this machine, every plugin the bundle names that
// is not here yet, from the source it was installed from over there. That is
// an install — git clone plus the manifest's build steps — and its output is
// captured rather than streamed, because a sync answers once, with a report.
// The tail of a failed build goes into the item's detail so the report says
// what went wrong without the user having to re-run the install by hand.
//
// What is deliberately NOT done:
//
//   - A plugin present on both sides is left alone, whatever the versions.
//     Updating tracks upstream, not the peer, and is its own command.
//   - A linked plugin is not installable: it is a developer's checkout on
//     the other machine, and its source record is a path there.
//   - A broken entry has no manifest to name a source with.
//   - A plugin whose manifest does not support this platform is refused by
//     plugin.Install itself, and reported as skipped, not failed.
func applyPlugins(incoming []PluginEntry, rep *ApplyReport) {
	installed, err := plugin.List()
	if err != nil {
		rep.Error = "plugins: " + err.Error()
		return
	}
	have := make(map[string]plugin.Installed, len(installed))
	for _, p := range installed {
		have[p.ID] = p
	}
	for _, in := range incoming {
		if in.Broken != "" {
			rep.add(KindPlugin, in.ID, StatusSkipped, "broken on the peer: "+in.Broken)
			continue
		}
		if in.Linked {
			rep.add(KindPlugin, in.ID, StatusSkipped, "linked to a local checkout on the peer; install it from its source here")
			continue
		}
		if p, ok := have[in.ID]; ok {
			detail := "already installed"
			if p.Err == nil && in.Version != "" && p.Version != in.Version {
				detail += fmt.Sprintf(" (here v%s, peer v%s — update is a separate step)", p.Version, in.Version)
			}
			rep.add(KindPlugin, in.ID, StatusUnchanged, detail)
			continue
		}
		if in.Source == "" {
			rep.add(KindPlugin, in.ID, StatusSkipped, "the peer's copy records no install source")
			continue
		}
		var out bytes.Buffer
		inst, err := plugin.Install(in.Source, in.Ref, &out)
		if err != nil {
			st := StatusFailed
			if strings.Contains(err.Error(), "does not support this platform") {
				st = StatusSkipped
			}
			rep.add(KindPlugin, in.ID, st, err.Error()+tail(out.String()))
			continue
		}
		rep.add(KindPlugin, inst.ID, StatusSynced, "installed from "+in.Source+refNote(in.Ref)+" v"+inst.Version)
	}
}

func refNote(ref string) string {
	if ref == "" {
		return ""
	}
	return "@" + ref
}

// tail keeps the last few lines of a build log for a report line: enough to
// show the error, not enough to be the log.
func tail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	return " | " + strings.Join(lines, " | ")
}
