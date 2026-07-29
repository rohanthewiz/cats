package app

import (
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rohanthewiz/cats/internal/layout"
	"github.com/rohanthewiz/cats/internal/workspace"
)

// Tab auto-naming: an auto-named tab (no tab.rename) takes its display name
// from the "most interesting" of its panes instead of its bare number, so the
// tab bar says what each tab is *for* without the user naming anything. The
// derivation is shared by every surface that shows a tab name — the browser
// layout message, tab.list, and the notification context line — via
// Session.TabDisplayName; only the PaneMeta lookup differs per caller (it is
// runtime state the session cannot know, so it arrives as a func, the same
// seam as Backend.PaneMeta).

// TabDisplayName is the name surfaces should show for a tab: the custom name
// when the user renamed it (tab.rename stays authoritative, always), otherwise
// the best pane's derived label, otherwise the tab number — exactly what
// DisplayName already answers for the un-derivable cases.
//
// "Best" walks the ladder in paneAutoLabel over every pane; the highest rung
// wins, ties go to the tab's focused pane (the one the user considers "the"
// pane) and then to the first pane in layout order, so the winner is stable
// under meta churn elsewhere in the tab. A multi-pane tab carries a " +N"
// suffix — the name describes one pane and N more ride along.
func (s *Session) TabDisplayName(tab *workspace.Tab, meta func(uint32) PaneMeta) string {
	if !tab.IsAutoNamed() {
		return tab.DisplayName()
	}
	ids := tab.Layout.PaneIDs()
	focused := tab.Layout.Focused()
	best, bestScore := "", 0
	for _, id := range ids {
		label, score := s.paneAutoLabel(id, meta(uint32(id)))
		// Strict > keeps the earlier (layout-order) pane on a tie; the focused
		// pane alone may displace an equal-scoring winner.
		if score > bestScore || (score == bestScore && score > 0 && id == focused) {
			best, bestScore = label, score
		}
	}
	if best == "" {
		return tab.DisplayName()
	}
	if extra := len(ids) - 1; extra > 0 {
		best += " +" + strconv.Itoa(extra)
	}
	return best
}

// paneAutoLabel is one pane's contribution to its tab's derived name: the
// label it would give the tab, and how strong a claim it has (0 = none).
//
//	4  the pane's custom name — the user pinned intent on this pane
//	3  detected agent, as "claude · <cwd basename>" — the agent is usually why
//	   the tab exists, and this form is stable where the agent's own OSC title
//	   is not (Claude Code's title churns with its spinner/status, which is
//	   also why an agent pane never uses rung 2)
//	2  the pane's OSC title — TUIs that title themselves ("todo: cats", vim)
//	1  the cwd's basename — a plain shell is at least *somewhere*
func (s *Session) paneAutoLabel(id layout.PaneID, m PaneMeta) (string, int) {
	if name, ok := s.PaneCustomName(id); ok && name != "" {
		return name, 4
	}
	if m.Agent != "" {
		if dir := cwdBaseName(m.Cwd); dir != "" {
			return m.Agent + " · " + dir, 3
		}
		return m.Agent, 3
	}
	if t := strings.TrimSpace(m.Title); t != "" {
		return clipLabel(t), 2
	}
	if dir := cwdBaseName(m.Cwd); dir != "" {
		return dir, 1
	}
	return "", 0
}

// cwdBaseName is the last path element of a pane's cwd, or "" when the cwd is
// unknown or is the filesystem root — "/" names a place but not a project, and
// a tab called "/" reads as a glitch.
func cwdBaseName(cwd string) string {
	if cwd == "" {
		return ""
	}
	base := filepath.Base(cwd)
	if base == "/" || base == "." {
		return ""
	}
	return base
}

// autoLabelMaxRunes caps a derived label. Only the OSC-title rung needs it —
// programs write arbitrary junk there (the orchestrator accepts up to 4KB) —
// but a tab name also lands in tab.list and notification lines, so the cap is
// belt-and-braces for every rung's output surface.
const autoLabelMaxRunes = 40

func clipLabel(s string) string {
	if utf8.RuneCountInString(s) <= autoLabelMaxRunes {
		return s
	}
	runes := []rune(s)
	return strings.TrimSpace(string(runes[:autoLabelMaxRunes-1])) + "…"
}
