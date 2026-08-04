//go:build ghostty

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rohanthewiz/cats/internal/browserproto"
)

// What copilot has spent on this machine, for the sidebar's COPILOT group.
//
// Everything here is read off the local session tree — no network call and no
// second credential. That is deliberate: cats already reads this tree for the
// per-pane model (agentmodel.go), so the numbers cost a file sweep, and asking
// GitHub would mean discovering, storing and refreshing a token that belongs to
// copilot rather than to us.
//
// # Why output tokens, and not premium requests
//
// The obvious meter is session.usage_checkpoint.totalPremiumRequests — copilot's
// own count of what it billed. It does not survive contact with a real tree.
// Measured across the sessions on the machine this was written on:
//
//	signal                         coverage
//	session.usage_checkpoint       11 of 23 sessions — absent on every 1.0.60
//	                               session, on 1.0.78, and on 6 of 16 1.0.77
//	                               sessions that completed turns
//	totalPremiumRequests           0.33 machine-wide — mini models bill at 0x,
//	                               so it rounds to zero and separates nothing
//	assistant.message.outputTokens 36 of 36 assistant messages, every version
//
// So the count that is always there is the one we use. It has the same shape as
// a claude transcript record — an id, a timestamp and a number — which is why
// this file is a close sibling of usageEstimator rather than a new idea.
//
// totalNanoAiu (AI units, copilot's newer meter) survives as an extra row drawn
// only when a checkpoint actually landed inside the week, because it is the
// number that moves when the premium count does not.
//
// # No denominator
//
// Nothing on disk records the plan's allowance, so every row here carries
// Pct = UsagePctUnknown: a figure, no bar. The same honest rendering the claude
// fallback already uses — an estimate dressed as a limit would be worse than no
// answer.

const (
	// copilotUsageMaxFile bounds a session's events file. Over this it is
	// SKIPPED rather than tail-read; see foldFile for why a tail is not an
	// option here. The whole tree is a couple of megabytes in practice, so this
	// is a runaway guard, not a working limit.
	copilotUsageMaxFile = 32 << 20
	// copilotUsageNote is the group's caption. It says which numbers these are,
	// because "tok out" alone invites comparison with the claude group's
	// input+output+cache figure — a comparison wrong by an order of magnitude.
	copilotUsageNote = "output tokens counted on this machine"
)

// copilotEstimator sums copilot's spend out of its session tree, incrementally.
//
// The bookkeeping mirrors usageEstimator's — read each file once from where the
// last sweep stopped, fold into per-minute buckets, prune to the week — with one
// addition the claude side does not need:
//
//	base holds the last cumulative nano-AIU seen in each file. The checkpoint
//	counter is cumulative WITHIN A SESSION, not a per-event amount, so what a
//	window wants is the delta between consecutive checkpoints. A baseline that
//	is wrong by one checkpoint is wrong by a whole session's spend.
//
// seen dedups on the event's own id (a checkpoint) or data.messageId (an
// assistant message), and does double duty for forks: `copilot --resume` can
// start a new session directory that replays the earlier events verbatim, and
// offsets alone cannot tell a replayed checkpoint from a new one.
//
// Loop-goroutine-free: only runUsage touches this.
type copilotEstimator struct {
	root    string           // copilotStateDir(), captured once
	offsets map[string]int64 // events.jsonl → bytes already folded
	base    map[string]int64 // events.jsonl → last cumulative nano-AIU seen
	tokens  map[int64]int64  // unix minute → output tokens
	aiu     map[int64]int64  // unix minute → nano-AIU spent in it
	seen    map[string]int64 // event/message id → unix minute
}

func newCopilotEstimator(root string) *copilotEstimator {
	return &copilotEstimator{
		root:    root,
		offsets: map[string]int64{},
		base:    map[string]int64{},
		tokens:  map[int64]int64{},
		aiu:     map[int64]int64{},
		seen:    map[string]int64{},
	}
}

// group sweeps and returns the COPILOT subsection, or false when this machine
// has no copilot state tree at all.
//
// The group is returned even when every number is zero, as long as the tree is
// there. A subsection that appears and vanishes between polls — because the
// week happened to empty out — would read as a bug; a stable zero reads as an
// answer.
func (e *copilotEstimator) group(now time.Time) (browserproto.UsageGroup, bool) {
	if e.root == "" {
		return browserproto.UsageGroup{}, false
	}
	if fi, err := os.Stat(e.root); err != nil || !fi.IsDir() {
		return browserproto.UsageGroup{}, false
	}
	e.sweep(now)

	g := browserproto.UsageGroup{
		ID:   "copilot",
		Name: "Copilot",
		Note: copilotUsageNote,
		Windows: []browserproto.UsageWindow{
			e.tokenWindow("5 hr", now, usageFiveHour),
			e.tokenWindow("Week", now, usageWeek),
		},
	}
	// The AIU row is drawn only when a checkpoint landed inside the week: on a
	// copilot too old to emit them there is nothing to say, and a row reading
	// "0.0 AIU" would say the wrong thing — that nothing was spent, rather than
	// that nothing was reported.
	if nano, ok := e.weekAIU(now); ok {
		g.Windows = append(g.Windows, browserproto.UsageWindow{
			Name:   "Week · AIU",
			Pct:    browserproto.UsagePctUnknown,
			Detail: fmt.Sprintf("%.1f AIU", float64(nano)/1e9),
		})
	}
	return g, true
}

// tokenWindow sums the output tokens of the last d. No percentage: there is no
// allowance on disk to measure them against (see this file's header).
func (e *copilotEstimator) tokenWindow(name string, now time.Time, d time.Duration) browserproto.UsageWindow {
	cutoff := now.Add(-d).Unix() / 60
	var total int64
	for min, n := range e.tokens {
		if min >= cutoff {
			total += n
		}
	}
	return browserproto.UsageWindow{
		Name: name,
		Pct:  browserproto.UsagePctUnknown,
		// " out" rather than formatTokens' bare "tok": this counts only what
		// copilot wrote, while the claude group's figure counts what it read as
		// well. The suffix is the only thing stopping the two being compared.
		Detail: formatTokens(total) + " out",
	}
}

// weekAIU totals the week's nano-AIU and reports whether any checkpoint fell in
// it at all. The second return is derived from the buckets rather than kept as a
// sticky flag so the row disappears on its own once the last checkpoint ages
// out — which is also why a zero delta still writes its bucket key.
func (e *copilotEstimator) weekAIU(now time.Time) (int64, bool) {
	cutoff := now.Add(-usageWeek).Unix() / 60
	var total int64
	var any bool
	for min, nano := range e.aiu {
		if min >= cutoff {
			total += nano
			any = true
		}
	}
	return total, any
}

// sweep folds every session's new bytes into the buckets, then prunes.
func (e *copilotEstimator) sweep(now time.Time) {
	cutoff := now.Add(-usageWeek)
	paths, err := filepath.Glob(filepath.Join(e.root, "*", copilotEventsFile))
	if err != nil {
		return
	}
	live := make(map[string]bool, len(paths))
	for _, path := range paths {
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		live[path] = true
		off, known := e.offsets[path]
		if known && off == fi.Size() {
			continue // nothing appended
		}
		// A file untouched since before the window holds nothing that counts,
		// and its offset is left alone so a later append still reads only the
		// new bytes. Unlike the claude sweep this skip is also safe for the
		// baseline: a file first read after this point is read from byte 0, so
		// its pre-window checkpoints still walk past and set base on the way.
		if !known && fi.ModTime().Before(cutoff) {
			continue
		}
		if !known && fi.Size() > copilotUsageMaxFile {
			// Skipped whole, and deliberately WITHOUT recording an offset:
			// see foldFile. Leaving the file unknown means a later sweep tries
			// it again from the start rather than folding appended checkpoints
			// against a baseline that was never established.
			continue
		}
		e.foldFile(path, fi.Size(), cutoff)
	}
	for path := range e.offsets {
		if !live[path] {
			delete(e.offsets, path) // session directory removed
			delete(e.base, path)
		}
	}
	e.prune(now)
}

// foldFile reads path from where the last sweep stopped and adds what it finds.
//
// The first read of a file starts at byte 0, always. usageEstimator caps its
// first read and takes the tail instead, which is right for per-event amounts
// and WRONG here: a tail lands at an unknown baseline, so the first checkpoint
// it meets is credited its entire session-cumulative value as a delta — a whole
// session's AIU dumped into the five-hour window on every cats restart. A file
// too large to read whole is skipped by sweep rather than tailed here.
func (e *copilotEstimator) foldFile(path string, size int64, cutoff time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	// A recorded offset is always a record boundary, so reading resumes at it
	// exactly.
	off := e.offsets[path]
	if off > size {
		// Truncated or replaced under us. A fresh file's counter starts over,
		// so the baseline has to as well or the first checkpoint reads as a
		// negative delta and folds nothing.
		off, e.base[path] = 0, 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return
	}
	sc := bufio.NewScanner(bufio.NewReaderSize(f, 64<<10))
	sc.Buffer(make([]byte, 0, 64<<10), usageMaxLine)
	for sc.Scan() {
		e.foldLine(path, sc.Bytes(), cutoff)
	}
	// Record the size we stat'ed, not where the scanner stopped: a record still
	// being written is re-read next sweep from the last full line, and dedup by
	// id keeps a re-read from counting twice. A scanner error (an over-long
	// line) is not fatal for the same reason.
	e.offsets[path] = size
}

// copilotUsageEvent is the slice of an events.jsonl line this reads. One struct
// covers both event types: their payloads are disjoint, so the fields that do
// not apply stay zero. (agentmodel.go's copilotEvent reads a different slice of
// the same lines — the model, not the spend.)
type copilotUsageEvent struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Data      struct {
		MessageID    string `json:"messageId"`    // assistant.message
		OutputTokens int64  `json:"outputTokens"` // assistant.message
		// session.usage_checkpoint. Cumulative within the session, so what
		// counts is the delta from the previous checkpoint in the same file.
		TotalNanoAiu int64 `json:"totalNanoAiu"`
	} `json:"data"`
}

const (
	copilotEvAssistantMsg = "assistant.message"
	copilotEvCheckpoint   = "session.usage_checkpoint"
)

func (e *copilotEstimator) foldLine(path string, line []byte, cutoff time.Time) {
	// Cheap gate: tool traffic, hooks and user turns are most of the file and
	// carry no spend. Matched on the raw bytes rather than string(line) —
	// copilot's assistant records carry content, encryptedContent and
	// reasoningOpaque, so a string copy per line is real allocation.
	if !bytes.Contains(line, []byte(`"outputTokens"`)) &&
		!bytes.Contains(line, []byte(`"`+copilotEvCheckpoint+`"`)) {
		return
	}
	var ev copilotUsageEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return
	}
	switch ev.Type {
	case copilotEvAssistantMsg:
		e.foldMessage(ev, cutoff)
	case copilotEvCheckpoint:
		e.foldCheckpoint(path, ev, cutoff)
	}
}

func (e *copilotEstimator) foldMessage(ev copilotUsageEvent, cutoff time.Time) {
	if ev.Data.MessageID == "" || ev.Data.OutputTokens <= 0 {
		return
	}
	at, err := time.Parse(time.RFC3339, ev.Timestamp)
	if err != nil || at.Before(cutoff) {
		return
	}
	if _, dup := e.seen[ev.Data.MessageID]; dup {
		return
	}
	min := at.Unix() / 60
	e.seen[ev.Data.MessageID] = min
	e.tokens[min] += ev.Data.OutputTokens
}

// foldCheckpoint advances the file's baseline and folds the difference.
//
// Order matters here. The baseline is advanced on EVERY checkpoint the sweep
// meets, including ones too old to count and ones already seen, because it
// describes where the session's counter had reached — not what was spent in the
// window. Only the fold is conditional.
func (e *copilotEstimator) foldCheckpoint(path string, ev copilotUsageEvent, cutoff time.Time) {
	if ev.ID == "" {
		return // nothing to dedup on; a double-count is worse than a miss
	}
	total := ev.Data.TotalNanoAiu
	if _, dup := e.seen[ev.ID]; dup {
		// A replayed prefix: `copilot --resume` can fork a session into a new
		// directory carrying the earlier events verbatim. Take the baseline so
		// this file's later checkpoints measure from the right place, and fold
		// nothing — the spend was already counted under the original session.
		e.base[path] = total
		return
	}
	// Floored at zero: a counter that went backwards (a reset, a rewritten
	// file) is a baseline problem, not negative spend.
	delta := total - e.base[path]
	if delta < 0 {
		delta = 0
	}
	// The baseline advances before any reason to stop does, including an
	// unreadable timestamp. It records where the session's counter had reached,
	// which is true whether or not this checkpoint can be dated; leaving it
	// behind would make the NEXT checkpoint fold this one's spend a second time.
	e.base[path] = total
	at, err := time.Parse(time.RFC3339, ev.Timestamp)
	if err != nil || at.Before(cutoff) {
		return // baseline advanced, nothing folded
	}
	min := at.Unix() / 60
	e.seen[ev.ID] = min
	// Written even when the delta is zero: the presence of a bucket in the week
	// is what tells group() this copilot reports AIU at all (see weekAIU).
	e.aiu[min] += delta
}

// prune drops everything that has fallen out of the week. The first checkpoint
// of a session covers everything since session.start, so a long session's spend
// lands in the single minute the checkpoint was written rather than spread over
// the turns that earned it. For a figure with no bar that is close enough; the
// alternative is reconstructing per-turn cost from a counter that does not
// record it.
func (e *copilotEstimator) prune(now time.Time) {
	cutoff := now.Add(-usageWeek).Unix() / 60
	for min := range e.tokens {
		if min < cutoff {
			delete(e.tokens, min)
		}
	}
	for min := range e.aiu {
		if min < cutoff {
			delete(e.aiu, min)
		}
	}
	for id, min := range e.seen {
		if min < cutoff {
			delete(e.seen, id)
		}
	}
}
