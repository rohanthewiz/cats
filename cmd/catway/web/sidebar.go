package web

import "github.com/rohanthewiz/element"

// Sidebar is the left column: the wordmark, then the seven sections the
// front-end fills in.
//
// Section order is the session's coordinate system, outermost first. Usage
// leads because it is the one reading that is about the machine rather than
// about the session. Hosts sits above Workspaces because a machine holds
// workspaces, which hold tabs, which hold panes, and the sidebar reads down
// that chain. History is last because it is the only section that looks
// BACKWARD — every other one describes the session as it is now.
//
// Runbooks breaks that chain, which is why it sits at the bottom of it rather
// than inside it. Every section above it is a reading OF the session; a runbook
// is a file on disk that does something TO one. So the column reads down
// through the session's own structure (machine → workspace → tab → pane →
// agent) and then leaves it for the two sections that are about acting: what
// can be run, and then what already was.
//
// Every section carries an id so its heading's fold arrow has something to hang
// the .folded class on (initSectionFold in js/), and every heading carries an
// .hctl even when there is nothing in it but the arrow — Agents has no groups
// to fold, yet its arrow still rides the same right-edge berth as the others'.
type Sidebar struct{}

func (Sidebar) Render(b *element.Builder) (x any) {
	nl(b, 1)
	b.Aside("id", "sidebar").R(
		nl(b, 2),
		Brand{}.Render(b),
		nl(b, 2),
		section(b, "sec-usage", "Usage", "usage-hctl", "usage-list", false, "…"),
		nl(b, 2),
		// Hosts starts hidden and stays hidden for as long as the session has
		// one host (renderHosts decides) — which, with no hosts: block in the
		// config, is forever.
		section(b, "sec-hosts", "Hosts", "host-hctl", "host-list", true),
		nl(b, 2),
		// Workspaces is the one heading with a second control: #ws-global-todo
		// is the roll-up of every workspace's open todos, and #ws-count inside
		// the .hctl is the "n of m shown" the fold arrow acts on.
		b.Section("id", "sec-workspaces").R(
			b.H2().R(
				b.T("Workspaces"),
				b.Span("id", "ws-global-todo").R(),
				b.SpanClass("hctl", "id", "ws-hctl").R(
					b.SpanClass("hcount", "id", "ws-count").R(),
				),
			),
			b.Ul("id", "ws-list").R(),
		),
		nl(b, 2),
		section(b, "sec-panes", "Panes", "pane-hctl", "pane-list", false),
		nl(b, 2),
		section(b, "sec-agents", "Agents", "agent-hctl", "agent-list", false, "none"),
		nl(b, 2),
		// Runbooks is hidden until the directory has something in it, like Hosts
		// and History: an install that has never recorded a macro sees exactly
		// the sidebar it always had, and the section appears by itself the first
		// time a recording is saved.
		section(b, "sec-runbooks", "Runbooks", "rb-hctl", "rb-list", true),
		nl(b, 2),
		// History is hidden until the command ledger has something in it, so a
		// session whose shells have no OSC 133 integration installed sees
		// exactly the sidebar it always had.
		section(b, "sec-history", "History", "hist-hctl", "hist-list", true),
		nl(b, 1),
	)
	return
}

// section builds the shape five of the six sidebar sections share: a heading
// with its fold-control berth, and the <ul> the front-end renders rows into.
//
// emptyRow, when given, is the placeholder the section shows before its first
// message lands. Only Usage and Agents have one: they are the two sections that
// are always present, so "nothing here yet" is a real state for them, whereas
// Hosts and History hide themselves outright and Panes/Workspaces are filled by
// the first layout, which arrives with the connection.
func section(b *element.Builder, secID, title, hctlID, listID string, hidden bool, emptyRow ...string) any {
	attrs := []string{"id", secID}
	if hidden {
		attrs = append(attrs, "hidden", "")
	}
	b.Section(attrs...).R(
		b.H2().R(
			b.T(title),
			b.SpanClass("hctl", "id", hctlID).R(),
		),
		b.Ul("id", listID).R(
			b.Wrap(func() {
				for _, row := range emptyRow {
					b.LiClass("empty").T(row)
				}
			}),
		),
	)
	return nil
}
