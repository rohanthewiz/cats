package app

import "github.com/rohanthewiz/cats/internal/layout"

// This file is the cats-level navigation history: the editor-style "go back /
// go forward" over focus locations that nav.back / nav.forward walk. It is the
// temporal complement to directional focus — pane.focus_direction answers
// "what is left of me", this answers "where was I before".
//
// The stack is per WINDOW, not per session. A window's trail of "places I have
// been" is exactly as window-private as its workspace pin (view.ws) — two
// windows on the same session must not walk each other's history — so the
// runtime owns one NavHistory per view and hands the dispatcher the issuing
// window's stack through the optional navHistoryBackend seam below. Like
// everything else about a view, it is not persisted (the same rule as
// layout.TileLayout's FocusLast slot).
//
// Entries are recorded OUTSIDE this file, by the runtime's post-dispatch hook
// (catway's noteNav): after any command, the issuing view's focus location is
// offered to Note, which dedups against the current entry. That is what makes
// recording generic — focus moves as a side effect of many commands (a split
// focuses the new pane, pane.close refocuses a survivor, ledger.jump reveals)
// and a snapshot-compare catches all of them without per-command bookkeeping.

// NavLocation is one entry: a place the user's focus has been.
//
// Only Pane is load-bearing for restore — pane ids are session-global, and
// Session.RevealPaneView reconstitutes workspace + tab + focus from the pane
// alone (the agent.focus recipe). Workspace and Tab exist so Note can tell
// "another pane of the same tab" from a genuine jump, which is the coalescing
// decision; they are never trusted at restore time, where the pane's CURRENT
// owner wins (a pane moved to another tab since the visit is revisited where
// it lives now, not where it was).
type NavLocation struct {
	Workspace string        // public workspace id ("w2"), for coalescing only
	Tab       int           // stable public tab number, for coalescing only
	Pane      layout.PaneID // the restore target
}

// navHistoryCap bounds the stack. 100 locations is far past what back-back-back
// usefully reaches; the cap exists so a week-long window cannot grow an
// unbounded slice, not to shape behaviour.
const navHistoryCap = 100

// NavHistory is one window's focus-location trail: a slice of visited
// locations and a cursor at "where we are now".
//
//	entries: [ A, B, C, D ]
//	                  ^cur          back → cur=B (focus B)
//	                                forward → cur=D (focus D)
//	                                Note(E) at cur=C → [ A, B, C, E ] (D truncated)
//
// The invariant Note maintains is that entries[cur] is the current focus
// location, which is what lets Step move the cursor first and focus what it
// lands on — and what makes nav.back itself recording-free: after the walk the
// cursor already matches the new focus, so the hook's snapshot dedups to a
// no-op even if it ran (it doesn't; the hook skips nav.* by name).
type NavHistory struct {
	entries []NavLocation
	cur     int // index of the current location; -1 when entries is empty
}

// NewNavHistory builds an empty history.
func NewNavHistory() *NavHistory { return &NavHistory{cur: -1} }

// Note offers the current focus location to the history.
//
// coalesce is the anti-pollution rule, decided by the CALLER from the command
// that just ran: a small directional move (pane.focus_direction, pane.cycle)
// within the same workspace+tab REPLACES the current entry instead of pushing,
// so a burst of hjkl hops reads back as one location — the vim-jumplist feel —
// while clicks, tab switches and workspace jumps each earn an entry.
func (h *NavHistory) Note(loc NavLocation, coalesce bool) {
	if h.cur >= 0 && h.entries[h.cur] == loc {
		return // nothing moved; the common case for every non-focus command
	}
	// A new location while mid-stack abandons the forward tail, exactly as an
	// editor or browser does: the past is a line, not a tree.
	h.entries = h.entries[:h.cur+1]
	if coalesce && h.cur >= 0 &&
		h.entries[h.cur].Workspace == loc.Workspace && h.entries[h.cur].Tab == loc.Tab {
		h.entries[h.cur] = loc
		return
	}
	h.entries = append(h.entries, loc)
	h.cur++
	if len(h.entries) > navHistoryCap {
		h.entries = h.entries[1:]
		h.cur--
	}
}

// Step walks the cursor one live entry backwards (back=true) or forwards and
// reports the location it landed on. valid says whether an entry can still be
// restored (its pane still exists); a stale entry is DROPPED — removed, not
// skipped in place — and the walk continues in the same direction, so a
// history full of closed panes drains itself rather than accumulating dead
// stops the user has to click through. ok=false means there was nowhere left
// to go, which callers treat as a silent no-op (the pane.last convention).
func (h *NavHistory) Step(back bool, valid func(NavLocation) bool) (NavLocation, bool) {
	for {
		if back {
			if h.cur <= 0 {
				return NavLocation{}, false
			}
			if !valid(h.entries[h.cur-1]) {
				h.entries = append(h.entries[:h.cur-1], h.entries[h.cur:]...)
				h.cur--
				continue
			}
			h.cur--
		} else {
			if h.cur+1 >= len(h.entries) {
				return NavLocation{}, false
			}
			if !valid(h.entries[h.cur+1]) {
				h.entries = append(h.entries[:h.cur+1], h.entries[h.cur+2:]...)
				continue
			}
			h.cur++
		}
		return h.entries[h.cur], true
	}
}

// navHistoryBackend is the optional half of the Backend seam for navigation,
// on the recorderBackend pattern: a backend that owns per-window histories
// implements it, and every test fake that doesn't is unaffected. It may return
// nil (a backend with no view to walk), which reads the same as not
// implementing it — nav.back/forward become silent no-ops.
type navHistoryBackend interface {
	NavHistory() *NavHistory
}
