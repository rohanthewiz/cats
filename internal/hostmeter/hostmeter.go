// Package hostmeter reads the machine's own memory, CPU and disk pressure as
// display-ready rows.
//
// It exists as a package, rather than as three files inside catway, because two
// different processes now need the same reading of two different machines.
// catway measures the box it runs on for the sidebar's HOST section; a cathost
// measures the box IT runs on and reports it back over the orchestration seam,
// which is the only way a pane on another machine can say anything about the
// machine it is on. Both build the same rows from the same code — if the
// presentation lived in catway and only the numbers travelled, the local and
// remote sections would drift apart one bug fix at a time.
//
// Nothing here imports browserproto. The rows are a neutral shape; catway turns
// them into UsageWindows and cathost puts them on the wire, and neither the
// daemon nor this package needs to know what a sidebar looks like.
package hostmeter

// PctUnknown marks a reading that could not be taken. It is the same sentinel
// browserproto.UsagePctUnknown carries, deliberately kept in step: a row with
// this percentage is dropped rather than drawn, because an empty meter invites
// the reader to conclude something about a machine nobody measured.
const PctUnknown = -1

// Row is one meter: a name, a percentage, an optional caption, and — for a
// reading that only means something as a shape — a short history.
//
// The three rows differ in what their caption carries and that is not
// incidental. Memory and disk carry a used/total pair, because a percentage
// alone cannot be acted on (70% of 8 GB and 70% of 128 GB call for different
// decisions). CPU has no pair to give — the percentage already is the whole
// machine — so it carries the load average, which answers the question the
// percentage cannot: 100% at a load of 2 on ten cores is one busy process, 100%
// at a load of 40 is a queue.
type Row struct {
	Name   string    `json:"name"`
	Pct    float64   `json:"pct"`
	Detail string    `json:"detail,omitempty"`
	Spark  []float64 `json:"spark,omitempty"`
}

// Known reports whether this row has a reading worth drawing.
func (r Row) Known() bool { return r.Pct >= 0 }

// Rows is the whole meter set for this machine, in reading order, with the
// unreadable ones dropped.
//
// The order is by how fast each one moves against how badly it ends: memory
// first (minutes, and it stops the session), CPU second (seconds, and it is
// usually the work rather than a problem), disk last (weeks). CPU sits in the
// middle rather than first for that reason — it is the row most often high for a
// good reason, and leading with it would train the eye to skip the group.
//
// Each reader reports nothing rather than a guess on a host it cannot ask, and a
// reader that came up empty drops its own row instead of taking the set down
// with it: a permission, a synthetic mount or a killed iostat can silence any
// one of them, and the surviving numbers are still worth showing.
//
// cpu may be nil — a caller that never started a sampler gets memory and disk,
// which is exactly what a one-shot reading can honestly produce.
func Rows(cpu *Sampler) []Row {
	var out []Row
	for _, r := range []Row{MemoryRow(), cpu.Row(), DiskRow()} {
		if r.Known() {
			out = append(out, r)
		}
	}
	return out
}

// ClampPct keeps a computed percentage inside 0..100. Every source here divides
// two numbers sampled a moment apart, so a reading a hair outside the range is a
// rounding artefact rather than news.
func ClampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
