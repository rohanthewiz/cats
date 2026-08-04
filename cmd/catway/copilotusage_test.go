//go:build ghostty

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/browserproto"
)

// --- Fixtures -----------------------------------------------------------------

// copilotMsgLine is an assistant.message as copilot writes it, trimmed to the
// fields the estimator reads plus one it must ignore.
func copilotMsgLine(id string, at time.Time, tokens int64) string {
	return fmt.Sprintf(
		`{"type":"assistant.message","id":%q,"timestamp":%q,`+
			`"data":{"messageId":%q,"model":"gpt-5-mini","outputTokens":%d,"toolRequests":[]}}`,
		"ev-"+id, at.UTC().Format(time.RFC3339), id, tokens)
}

// copilotCkptLine is a session.usage_checkpoint. totalNanoAiu is CUMULATIVE
// within the session, which is the whole reason these tests exist.
func copilotCkptLine(id string, at time.Time, nano int64) string {
	return fmt.Sprintf(
		`{"type":"session.usage_checkpoint","id":%q,"timestamp":%q,`+
			`"data":{"totalNanoAiu":%d,"totalPremiumRequests":0,"modelCacheState":[]}}`,
		id, at.UTC().Format(time.RFC3339), nano)
}

// writeCopilotEvents creates root/<name>/events.jsonl holding lines. Unlike
// agentmodel_test's writeCopilotSession it lays down no workspace.yaml and
// stamps no mtime: the usage sweep finds sessions by glob and dates them by the
// events' own timestamps, never by the directory the session started in.
func writeCopilotEvents(t *testing.T, root, name string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, copilotEventsFile)
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func appendCopilotLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
}

// detailOf is the Detail of the named row, "" when the group has no such row.
// It also asserts the row carries no percentage, which is true of every copilot
// row: nothing on disk records the plan's allowance.
func detailOf(t *testing.T, g browserproto.UsageGroup, name string) string {
	t.Helper()
	for _, w := range g.Windows {
		if w.Name != name {
			continue
		}
		if w.Pct != browserproto.UsagePctUnknown {
			t.Errorf("row %q has pct %v; copilot has no denominator", name, w.Pct)
		}
		return w.Detail
	}
	return ""
}

const oneAIU = 1_000_000_000 // nano-AIU in one AI unit

// --- Output tokens ------------------------------------------------------------

// The primary signal: outputTokens summed over both windows, with the five-hour
// window a strict subset of the week.
func TestCopilotEstimatorSumsOutputTokens(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	writeCopilotEvents(t, root, "s1",
		copilotMsgLine("m1", now.Add(-10*time.Minute), 400),
		copilotMsgLine("m2", now.Add(-2*time.Hour), 100),
		copilotMsgLine("m3", now.Add(-3*24*time.Hour), 500), // week only
		copilotMsgLine("m4", now.Add(-9*24*time.Hour), 999), // outside both
	)

	g, ok := newCopilotEstimator(root).group(now)
	if !ok {
		t.Fatal("no group for a populated state dir")
	}
	if got := detailOf(t, g, "5 hr"); got != "500 tok out" {
		t.Errorf("5 hr = %q, want 500 tok out", got)
	}
	if got := detailOf(t, g, "Week"); got != "1K tok out" {
		t.Errorf("Week = %q, want 1K tok out", got)
	}
}

// A resumed session is a new directory replaying the earlier turns, so the same
// messageId shows up in two files. Counting it twice would inflate both windows
// by however often the user has resumed.
func TestCopilotEstimatorDedupsMessagesAcrossSessions(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	shared := copilotMsgLine("m1", now.Add(-30*time.Minute), 300)
	writeCopilotEvents(t, root, "a", shared)
	writeCopilotEvents(t, root, "b", shared,
		copilotMsgLine("m2", now.Add(-20*time.Minute), 200))

	g, _ := newCopilotEstimator(root).group(now)
	if got := detailOf(t, g, "Week"); got != "500 tok out" {
		t.Errorf("Week = %q, want 500 tok out (the shared turn counted once)", got)
	}
}

// --- The cumulative AIU counter -----------------------------------------------

// The regression test this file exists for. totalNanoAiu is cumulative within a
// session, so a sweep that resumes at a byte offset must remember where the
// counter had reached. Read the file, then append: the second sweep must fold
// only the difference.
//
// A tail-read first pass, or a baseline reset between sweeps, fails here by
// reporting 10.0 instead of 7.0.
func TestCopilotEstimatorFoldsCumulativeDeltas(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	path := writeCopilotEvents(t, root, "s1",
		copilotCkptLine("c1", now.Add(-40*time.Minute), 1*oneAIU),
		copilotCkptLine("c2", now.Add(-30*time.Minute), 3*oneAIU),
	)
	e := newCopilotEstimator(root)

	g, _ := e.group(now)
	if got := detailOf(t, g, "Week · AIU"); got != "3.0 AIU" {
		t.Fatalf("first sweep = %q, want 3.0 AIU", got)
	}

	appendCopilotLines(t, path, copilotCkptLine("c3", now.Add(-5*time.Minute), 7*oneAIU))
	g, _ = e.group(now)
	if got := detailOf(t, g, "Week · AIU"); got != "7.0 AIU" {
		t.Errorf("second sweep = %q, want 7.0 AIU (the counter's total, not 3+7)", got)
	}
}

// A fork replays the original session's checkpoints verbatim into a new
// directory. Their ids are already known, so they set the fork's baseline and
// fold nothing — only what the fork itself spent counts.
func TestCopilotEstimatorIgnoresReplayedCheckpoints(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	c1 := copilotCkptLine("c1", now.Add(-40*time.Minute), 2*oneAIU)
	c2 := copilotCkptLine("c2", now.Add(-35*time.Minute), 5*oneAIU)
	writeCopilotEvents(t, root, "a", c1, c2)
	// The fork carries a's history, then adds one checkpoint of its own: the
	// counter reaches 6, so the fork itself spent 1.
	writeCopilotEvents(t, root, "b", c1, c2,
		copilotCkptLine("c3", now.Add(-10*time.Minute), 6*oneAIU))

	g, _ := newCopilotEstimator(root).group(now)
	if got := detailOf(t, g, "Week · AIU"); got != "6.0 AIU" {
		t.Errorf("Week · AIU = %q, want 6.0 AIU (5 from a, 1 from the fork)", got)
	}
}

// A counter that goes backwards — a reset, a file rewritten under us — is a
// baseline problem, not negative spend. The delta floors at zero and the
// baseline still advances, so the next checkpoint measures from the right place.
func TestCopilotEstimatorFloorsBackwardsCounter(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	writeCopilotEvents(t, root, "s1",
		copilotCkptLine("c1", now.Add(-40*time.Minute), 9*oneAIU),
		copilotCkptLine("c2", now.Add(-30*time.Minute), 2*oneAIU), // backwards
		copilotCkptLine("c3", now.Add(-20*time.Minute), 4*oneAIU), // +2 from 2
	)

	g, _ := newCopilotEstimator(root).group(now)
	if got := detailOf(t, g, "Week · AIU"); got != "11.0 AIU" {
		t.Errorf("Week · AIU = %q, want 11.0 AIU (9 + 0 + 2)", got)
	}
}

// A checkpoint whose timestamp will not parse still advances the baseline. It
// cannot be dated, so it folds nothing — but leaving the baseline behind would
// make the next checkpoint fold this one's spend a second time.
func TestCopilotEstimatorAdvancesBaselinePastUndatableCheckpoint(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	undated := fmt.Sprintf(
		`{"type":"session.usage_checkpoint","id":"c2","timestamp":"not-a-time",`+
			`"data":{"totalNanoAiu":%d}}`, 5*oneAIU)
	writeCopilotEvents(t, root, "s1",
		copilotCkptLine("c1", now.Add(-40*time.Minute), 2*oneAIU),
		undated,
		copilotCkptLine("c3", now.Add(-10*time.Minute), 6*oneAIU), // +1 from 5
	)

	g, _ := newCopilotEstimator(root).group(now)
	if got := detailOf(t, g, "Week · AIU"); got != "3.0 AIU" {
		t.Errorf("Week · AIU = %q, want 3.0 AIU (2 dated + 1 after the undatable one)", got)
	}
}

// A file too large to read whole is skipped rather than tail-read, and skipping
// must leave no offset behind: a baseline that was never established would make
// every later append fold the session's whole running total.
func TestCopilotEstimatorSkipsOversizeFileWithoutPoisoningBaseline(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	path := writeCopilotEvents(t, root, "s1",
		copilotCkptLine("c1", now.Add(-40*time.Minute), 3*oneAIU))
	// Sparse: no disk written, but the stat the sweep reads says 32 MB + 1.
	f, err := os.OpenFile(path, os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := f.Truncate(copilotUsageMaxFile + 1); err != nil {
		f.Close()
		t.Fatalf("truncate: %v", err)
	}
	f.Close()

	e := newCopilotEstimator(root)
	if g, _ := e.group(now); detailOf(t, g, "Week · AIU") != "" {
		t.Error("an oversize file was read")
	}
	if _, ok := e.offsets[path]; ok {
		t.Fatal("a skipped file recorded an offset; a later append would fold against a zero baseline")
	}

	// Back to a readable size, with the first checkpoint intact and a second
	// appended. Both are new to the estimator, so the total is the counter's.
	if err := os.Truncate(path, 0); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := os.WriteFile(path, []byte(
		copilotCkptLine("c1", now.Add(-40*time.Minute), 3*oneAIU)+"\n"+
			copilotCkptLine("c2", now.Add(-10*time.Minute), 8*oneAIU)+"\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	g, _ := e.group(now)
	if got := detailOf(t, g, "Week · AIU"); got != "8.0 AIU" {
		t.Errorf("Week · AIU = %q, want 8.0 AIU", got)
	}
}

// --- The group ----------------------------------------------------------------

// A machine with no copilot contributes no subsection at all — not an empty one.
func TestCopilotGroupAbsentWithoutStateDir(t *testing.T) {
	if _, ok := newCopilotEstimator("").group(time.Now()); ok {
		t.Error("a group was returned for an empty root")
	}
	missing := filepath.Join(t.TempDir(), "nope")
	if _, ok := newCopilotEstimator(missing).group(time.Now()); ok {
		t.Error("a group was returned for a missing state dir")
	}
}

// A copilot too old to write checkpoints (measured: every 1.0.60 session) still
// reports output tokens. It gets the two token rows and no AIU row — a row
// reading "0.0 AIU" would claim nothing was spent rather than that nothing was
// reported.
func TestCopilotGroupOmitsAIURowWithoutCheckpoints(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	writeCopilotEvents(t, root, "s1", copilotMsgLine("m1", now.Add(-time.Hour), 250))

	g, ok := newCopilotEstimator(root).group(now)
	if !ok {
		t.Fatal("no group")
	}
	if len(g.Windows) != 2 {
		t.Fatalf("rows = %d, want 2 (no AIU row): %+v", len(g.Windows), g.Windows)
	}
	if g.ID != "copilot" || g.Name != "Copilot" || g.Note == "" {
		t.Errorf("group header = %+v", g)
	}
}

// The state dir exists but holds nothing yet. The subsection still appears: one
// that came and went between polls would read as a bug, a stable zero reads as
// an answer.
func TestCopilotGroupPresentAtZero(t *testing.T) {
	root := t.TempDir()
	g, ok := newCopilotEstimator(root).group(time.Now())
	if !ok {
		t.Fatal("no group for an empty but present state dir")
	}
	if got := detailOf(t, g, "Week"); got != "0 tok out" {
		t.Errorf("Week = %q, want 0 tok out", got)
	}
}
