package main

import "testing"

// sampleItems is two groups of selectable rows separated by non-selectable
// headings — the shape the todo list and target picker both produce.
func sampleItems() []listItem {
	return []listItem{
		{name: "Project", selectable: false},
		{name: "alpha task", desc: "do alpha", selectable: true, ref: 10},
		{name: "beta task", desc: "do beta", selectable: true, ref: 11},
		{name: "Global", selectable: false},
		{name: "gamma task", desc: "do gamma", selectable: true, ref: 12},
	}
}

// setQuery types a query into the list and re-filters, mirroring editQuery
// without needing a tea.Msg.
func setQuery(l *fuzzyList, q string) {
	l.input.SetValue(q)
	l.filter()
}

func TestFilterEmptyQueryShowsEverything(t *testing.T) {
	l := newFuzzyList("", sampleItems())
	// An empty query keeps every row, separators included, in natural order.
	if len(l.filtered) != 5 {
		t.Fatalf("empty-query filtered = %d rows, want 5 (all incl. separators)", len(l.filtered))
	}
	// The cursor must land on the first selectable row, skipping the "Project"
	// heading at index 0.
	if l.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (first selectable, past the heading)", l.cursor)
	}
	if got := l.selectedIndex(); got != 10 {
		t.Errorf("selectedIndex = %d, want ref 10", got)
	}
}

func TestFilterExcludesSeparatorsAndNonMatches(t *testing.T) {
	l := newFuzzyList("", sampleItems())
	setQuery(&l, "alpha")
	if len(l.filtered) != 1 {
		t.Fatalf("query 'alpha' filtered = %d rows, want 1", len(l.filtered))
	}
	if l.filtered[0].item.ref != 10 {
		t.Errorf("matched ref = %d, want 10", l.filtered[0].item.ref)
	}
	if got := l.selectedIndex(); got != 10 {
		t.Errorf("selectedIndex after filter = %d, want 10", got)
	}
}

func TestFilterMatchesDescriptionToo(t *testing.T) {
	// "do gamma" lives in the description; filter searches name+desc together.
	l := newFuzzyList("", sampleItems())
	setQuery(&l, "gamma")
	if len(l.filtered) != 1 || l.filtered[0].item.ref != 12 {
		t.Fatalf("query 'gamma' filtered = %+v, want only ref 12", l.filtered)
	}
}

func TestFilterNoMatchesGivesNoSelection(t *testing.T) {
	l := newFuzzyList("", sampleItems())
	setQuery(&l, "zzzznotpresent")
	if len(l.filtered) != 0 {
		t.Fatalf("no-match query filtered = %d rows, want 0", len(l.filtered))
	}
	if got := l.selectedIndex(); got != -1 {
		t.Errorf("selectedIndex with no matches = %d, want -1", got)
	}
}

// TestMatchedIndexesStayWithinName guards highlighting: only name characters
// should be reported as matched, never characters that matched in the
// description (which is appended to the haystack but not highlighted).
func TestMatchedIndexesStayWithinName(t *testing.T) {
	// name "abc" (3 runes) + "  " + desc "xyz"; query "ax" matches 'a' at name
	// index 0 and 'x' at haystack index 5 (inside the desc). Only index 0 should
	// survive as an in-name match.
	l := newFuzzyList("", []listItem{{name: "abc", desc: "xyz", selectable: true, ref: 0}})
	setQuery(&l, "ax")
	if len(l.filtered) != 1 {
		t.Fatalf("filtered = %d rows, want 1", len(l.filtered))
	}
	got := l.filtered[0].matched
	if len(got) != 1 || got[0] != 0 {
		t.Errorf("matched = %v, want [0] (desc matches must be dropped)", got)
	}
}

func TestMoveDownAndUpSkipSeparators(t *testing.T) {
	l := newFuzzyList("", sampleItems())
	// Start parked on the first selectable (index 1, ref 10).
	if l.cursor != 1 {
		t.Fatalf("setup: cursor = %d, want 1", l.cursor)
	}

	l.moveDown() // -> index 2, ref 11
	if l.selectedIndex() != 11 {
		t.Errorf("after one moveDown selectedIndex = %d, want 11", l.selectedIndex())
	}
	l.moveDown() // skips the "Global" heading at index 3 -> index 4, ref 12
	if l.selectedIndex() != 12 {
		t.Errorf("after two moveDowns selectedIndex = %d, want 12", l.selectedIndex())
	}
	l.moveDown() // already on the last selectable: stays put
	if l.selectedIndex() != 12 {
		t.Errorf("moveDown past the end moved off the last row: %d", l.selectedIndex())
	}

	l.moveUp() // skips the heading going back up -> index 2, ref 11
	if l.selectedIndex() != 11 {
		t.Errorf("after moveUp selectedIndex = %d, want 11", l.selectedIndex())
	}
	l.moveUp() // -> index 1, ref 10
	l.moveUp() // already first selectable: stays put
	if l.selectedIndex() != 10 {
		t.Errorf("moveUp past the start selectedIndex = %d, want 10", l.selectedIndex())
	}
}

func TestSetItemsKeepsQueryAndClampsCursor(t *testing.T) {
	l := newFuzzyList("", sampleItems())
	setQuery(&l, "task") // matches all three selectable rows
	l.moveDown()
	l.moveDown() // cursor now on the third match

	// Replace with a shorter list; the query persists and the cursor must clamp
	// back into range and onto a selectable row.
	l.setItems([]listItem{{name: "alpha task", desc: "", selectable: true, ref: 99}})
	if l.input.Value() != "task" {
		t.Errorf("setItems dropped the query: %q", l.input.Value())
	}
	if got := l.selectedIndex(); got != 99 {
		t.Errorf("after setItems selectedIndex = %d, want 99", got)
	}
}

func TestSelectedIndexAllSeparators(t *testing.T) {
	// A list with no selectable rows (only headings) has nothing to select.
	l := newFuzzyList("", []listItem{{name: "Heading", selectable: false}})
	if got := l.selectedIndex(); got != -1 {
		t.Errorf("selectedIndex with no selectable rows = %d, want -1", got)
	}
}
