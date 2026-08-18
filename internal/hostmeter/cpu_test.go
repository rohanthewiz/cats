package hostmeter

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Both parsers are tested against captured output rather than the live host, on
// the same reasoning as hostmem_test.go: a macOS report parsed on a Linux runner
// and the other way round is the one arrangement that catches a parser that only
// works where it was written.

// Two headers and three samples, from a Mac with two disks attached — the layout
// that makes reading from the END of the line load-bearing, since the number of
// disk column groups is a property of the machine.
const iostatSample = `              disk0               disk5       cpu    load average
    KB/t  tps  MB/s     KB/t  tps  MB/s  us sy id   1m   5m   15m
    6.60 1408  9.08    15.39    0  0.00  14 12 74  12.39 9.45 10.61
    5.92  170  0.98     0.00    0  0.00  19 14 68  12.41 9.50 10.60
    8.80    5  0.04     0.00    0  0.00   1  1 98   9.42 8.96 10.40
`

func TestParseIostatCPU(t *testing.T) {
	lines := splitLines(iostatSample)
	// The two header rows must not parse: "us" is not a float, and that is the
	// whole guard against reading a label as a reading.
	for _, h := range lines[:2] {
		if _, _, ok := parseIostatCPU(h); ok {
			t.Fatalf("header line parsed as a sample: %q", h)
		}
	}
	// 100 - idle, so a machine 74% idle is 26% busy however iostat splits the
	// rest between user and system.
	for i, want := range []struct{ busy, load float64 }{{26, 12.39}, {32, 12.41}, {2, 9.42}} {
		busy, load, ok := parseIostatCPU(lines[2+i])
		if !ok {
			t.Fatalf("sample %d did not parse: %q", i, lines[2+i])
		}
		if busy != want.busy || load != want.load {
			t.Errorf("sample %d = %v busy / %v load, want %v / %v", i, busy, load, want.busy, want.load)
		}
	}
}

// A line whose last six fields are numeric but are not a CPU reading — the
// shares must come to about a hundred, which is what keeps a stray numeric line
// out of the history.
func TestParseIostatCPURejectsNonCPULines(t *testing.T) {
	for _, line := range []string{
		"    6.60 1408  9.08    15.39    0  0.00  14 12 20  12.39 9.45 10.61", // us+sy+id = 46
		"1 2 3",           // too few fields
		"disk0 disk5 cpu", // words
		"",                // blank line between reports
	} {
		if _, _, ok := parseIostatCPU(line); ok {
			t.Errorf("parsed as a sample: %q", line)
		}
	}
}

const procStatSample = `cpu  100 10 40 800 50 0 0 0 0 0
cpu0 50 5 20 400 25 0 0 0 0 0
intr 12345
ctxt 987654
`

func TestParseProcStatCPU(t *testing.T) {
	busy, total, ok := parseProcStatCPU([]byte(procStatSample))
	if !ok {
		t.Fatal("aggregate line did not parse")
	}
	if total != 1000 {
		t.Errorf("total = %v, want every field summed (1000)", total)
	}
	// idle (800) and iowait (50) are both idle: a core waiting on a disk is a
	// core anything else could have.
	if busy != 150 {
		t.Errorf("busy = %v, want total minus idle and iowait (150)", busy)
	}
}

func TestParseProcStatCPUWithoutAggregateLine(t *testing.T) {
	if _, _, ok := parseProcStatCPU([]byte("cpu0 1 2 3 4\nintr 5\n")); ok {
		t.Error("parsed a /proc/stat with no aggregate line")
	}
}

// The ring is bounded, keeps the newest samples, and keeps them in order — the
// chart is drawn straight from it, so a wrong order is a wrong shape.
func TestCPUSamplerRingKeepsTheNewest(t *testing.T) {
	c := NewSampler()
	for i := 0; i < cpuHistoryLen+10; i++ {
		c.add(float64(i%101), 1)
	}
	w := c.Row()
	if len(w.Spark) != cpuHistoryLen {
		t.Fatalf("spark = %d samples, want the ring's cap (%d)", len(w.Spark), cpuHistoryLen)
	}
	last := float64((cpuHistoryLen + 9) % 101)
	if w.Spark[len(w.Spark)-1] != last || w.Pct != last {
		t.Errorf("newest = %v (pct %v), want %v — the ring dropped from the wrong end",
			w.Spark[len(w.Spark)-1], w.Pct, last)
	}
	if w.Spark[0] != float64(10%101) {
		t.Errorf("oldest = %v, want the 11th sample — the ring kept too much", w.Spark[0])
	}
}

// A sampler with nothing in it, or one whose source has gone quiet, reports no
// reading rather than a stale one: hostUsageGroup drops the row, which is the
// same contract hostMemory and hostDisk keep on a host they cannot ask.
func TestCPUSamplerSilentWhenEmptyOrStale(t *testing.T) {
	var nilSampler *Sampler
	if w := nilSampler.Row(); w.Pct != PctUnknown {
		t.Errorf("nil sampler pct = %v, want unknown", w.Pct)
	}
	if w := NewSampler().Row(); w.Pct != PctUnknown {
		t.Errorf("empty sampler pct = %v, want unknown", w.Pct)
	}

	c := NewSampler()
	c.add(42, 1)
	c.at = time.Now().Add(-cpuStaleAfter - time.Second)
	if w := c.Row(); w.Pct != PctUnknown {
		t.Errorf("stale sampler pct = %v, want unknown — a pinned old reading invites action", w.Pct)
	}
}

// A single sample is a reading but not a chart: two points are the minimum a
// line can be drawn through, and the front-end skips the strip below that. The
// row itself still reports its percentage.
func TestCPUSamplerSingleSampleHasNoSpark(t *testing.T) {
	c := NewSampler()
	c.add(42, -1)
	w := c.Row()
	if w.Pct != 42 {
		t.Errorf("pct = %v, want 42", w.Pct)
	}
	if len(w.Spark) != 0 {
		t.Errorf("spark = %v, want none from one sample", w.Spark)
	}
	if w.Detail != "" {
		t.Errorf("detail = %q, want none when the load average could not be read", w.Detail)
	}
}

// The live host, against the source the sampler actually reads. It does not
// start the sampler — that owns a long-lived process and a goroutine that only
// the server should own — but it does run the same command with a bounded count,
// so a wrong flag or a changed column layout fails here rather than showing up
// as a silently missing sidebar row.
func TestCPUSourceLive(t *testing.T) {
	switch runtime.GOOS {
	case "linux":
		raw, err := os.ReadFile("/proc/stat")
		if err != nil {
			t.Skipf("/proc/stat unreadable: %v", err)
		}
		if _, _, ok := parseProcStatCPU(raw); !ok {
			t.Fatal("this host's /proc/stat did not parse")
		}
	case "darwin":
		// Two samples one second apart: the same output the streamed form
		// produces, over a window short enough for a test.
		out, err := exec.Command("iostat", "-c", "2", "-w", "1").Output()
		if err != nil {
			t.Skipf("iostat unavailable: %v", err)
		}
		var got int
		for _, line := range strings.Split(string(out), "\n") {
			if _, _, ok := parseIostatCPU(line); ok {
				got++
			}
		}
		if got != 2 {
			t.Fatalf("parsed %d of this host's iostat samples, want 2:\n%s", got, out)
		}
	default:
		t.Skipf("no CPU source on %s", runtime.GOOS)
	}
}

func splitLines(s string) []string { return strings.Split(strings.TrimSuffix(s, "\n"), "\n") }
