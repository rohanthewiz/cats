package peersync

import (
	"fmt"
	"strings"
)

// Status is what happened to one item on one side.
type Status string

const (
	// StatusSynced: the item was added / installed / merged with changes.
	StatusSynced Status = "synced"
	// StatusUnchanged: the item was already there and identical; nothing to do.
	StatusUnchanged Status = "unchanged"
	// StatusSkipped: the item could not be applied for a reason that is a fact
	// about this machine rather than a failure — no equivalent folder, a
	// linked plugin, a row conflict. The Detail says which. This is the list
	// the user asked to see at the end.
	StatusSkipped Status = "skipped"
	// StatusFailed: the item was attempted and the attempt errored.
	StatusFailed Status = "failed"
)

// Kinds of item a report holds.
const (
	KindWorkspace = "workspace"
	KindTodos     = "todos"
	KindPlugin    = "plugin"
)

// Item is one line of a report: what, on which side, what happened, and why.
type Item struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// ApplyReport is what one side says after applying a bundle: every item it
// considered, in the order it considered them. Error is set when a whole
// category could not even be attempted (the plugins root unreadable, say) —
// the per-item list still holds whatever came before it.
type ApplyReport struct {
	Instance Instance `json:"instance"`
	Items    []Item   `json:"items"`
	Error    string   `json:"error,omitempty"`
}

func (r *ApplyReport) add(kind, name string, st Status, detail string) {
	r.Items = append(r.Items, Item{Kind: kind, Name: name, Status: st, Detail: detail})
}

// Counts totals the items by status.
func (r ApplyReport) Counts() (synced, unchanged, skipped, failed int) {
	for _, it := range r.Items {
		switch it.Status {
		case StatusSynced:
			synced++
		case StatusUnchanged:
			unchanged++
		case StatusSkipped:
			skipped++
		case StatusFailed:
			failed++
		}
	}
	return
}

// Direction is which way a sync moves state.
type Direction string

const (
	DirectionBoth Direction = "both" // pull, then push
	DirectionPull Direction = "pull" // apply the peer's bundle here
	DirectionPush Direction = "push" // apply our bundle on the peer
)

// ParseDirection reads a direction word; "" means both.
func ParseDirection(s string) (Direction, error) {
	switch Direction(strings.ToLower(strings.TrimSpace(s))) {
	case "", DirectionBoth:
		return DirectionBoth, nil
	case DirectionPull:
		return DirectionPull, nil
	case DirectionPush:
		return DirectionPush, nil
	}
	return "", fmt.Errorf("direction %q: want both, pull or push", s)
}

// SyncReport is the whole story of one sync, as the initiator tells it.
// Pulled is what was applied here; Pushed is what the peer applied (its own
// ApplyReport, returned over the wire). Either is nil when that direction
// was not asked for, or could not start — Error then says why.
type SyncReport struct {
	Peer      string       `json:"peer"`
	Direction Direction    `json:"direction"`
	Want      Want         `json:"want"`
	Remote    Instance     `json:"remote"`
	Pulled    *ApplyReport `json:"pulled,omitempty"`
	Pushed    *ApplyReport `json:"pushed,omitempty"`
	Error     string       `json:"error,omitempty"`
}

// Render writes the report as lines a human reads top to bottom: a summary
// per side, then the items grouped by outcome with the ones that did NOT
// sync last and spelled out — those are what the reader is looking for, and
// putting a long "unchanged" list after them would bury the answer.
func (r SyncReport) Render() []string {
	var out []string
	title := "sync with " + r.Peer
	if r.Remote.Hostname != "" {
		title += " (" + r.Remote.Name() + ")"
	}
	out = append(out, title+" — "+string(r.Direction)+", "+r.Want.String())
	if r.Error != "" {
		out = append(out, "error: "+r.Error)
	}
	if r.Pulled != nil {
		out = append(out, renderSide("here", *r.Pulled)...)
	}
	if r.Pushed != nil {
		out = append(out, renderSide("on "+r.Peer, *r.Pushed)...)
	}
	return out
}

// renderSide renders one ApplyReport under a heading naming the side.
func renderSide(side string, a ApplyReport) []string {
	synced, unchanged, skipped, failed := a.Counts()
	out := []string{fmt.Sprintf("%s: %d synced, %d unchanged, %d skipped, %d failed",
		side, synced, unchanged, skipped, failed)}
	if a.Error != "" {
		out = append(out, "  error: "+a.Error)
	}
	// Synced first (what happened), then the two "did not sync" groups with
	// their reasons; unchanged is summarized by the count alone unless nothing
	// else happened, because a list of things that were already fine is noise.
	for _, st := range []Status{StatusSynced, StatusSkipped, StatusFailed} {
		for _, it := range a.Items {
			if it.Status != st {
				continue
			}
			line := fmt.Sprintf("  %-9s %-9s %s", st, it.Kind, it.Name)
			if it.Detail != "" {
				line += " — " + it.Detail
			}
			out = append(out, line)
		}
	}
	return out
}
