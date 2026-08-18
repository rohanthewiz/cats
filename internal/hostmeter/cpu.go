package hostmeter

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// How hard the machine is working, as the third row of the sidebar's HOST
// subsection — plus the one thing no other row in the section has: a history.
//
// CPU belongs beside memory and disk because it answers the same question they
// do ("is anything about to stop?"), but it answers it on a different clock. A
// week of tokens moves in hours and a disk in days; CPU moves in seconds, and a
// single number sampled every couple of minutes says almost nothing about it —
// 8% could be a machine at rest or the trough between two test runs. So this row
// carries a short history (UsageWindow.Spark) and the sidebar draws it: the
// shape is the reading, and the current percentage is only its right-hand edge.
//
// # Why a sampler rather than a reading on the poll
//
// The other two host rows are read on the usage poll, which costs two syscalls
// every couple of minutes. CPU cannot be read that way and mean anything:
//
//   - Utilization is a RATE, so every source of it is a difference between two
//     cumulative readings, or an interval sample somebody else took. There is
//     nothing to read "right now".
//   - Differencing across the two-minute poll would yield one two-minute average
//     per point — a chart whose every feature has already been smoothed away.
//
// So this keeps its own goroutine on a ten-second cadence, and the poll simply
// takes whatever the ring currently holds. Ten seconds is short enough that a
// build or a test run is a visible hump rather than a bump, and long enough that
// an hour of history costs a few hundred float64s.
//
// # The two hosts
//
//	linux   /proc/stat's cumulative jiffies, differenced across the interval.
//	        Exact, allocation-free, no subprocess.
//	darwin  one long-lived `iostat -w 10`, whose every line after the first is a
//	        true ten-second average. macOS exports no cumulative tick counter to
//	        userspace without cgo (there is no kern.cp_time as on the BSDs), and
//	        the alternatives are worse: `top -l 1` reports since-boot percentages
//	        that cannot be differenced, and re-running a short `iostat -c 2 -w 1`
//	        every ten seconds would spawn a process per sample for a strictly
//	        worse figure — a one-second spot check instead of the full interval.
//	        iostat writes a line per interval and flushes it, which is what makes
//	        the streaming read work at all (verified: lines arrive through a pipe
//	        at the interval, not in 4 KB blocks).
//
// Everywhere else samples nothing, the ring stays empty, and hostUsageGroup
// draws no CPU row — the same contract hostMemory and hostDisk keep. A sampler
// that has stopped reporting goes quiet the same way rather than leaving a
// frozen number on screen (see cpuStaleAfter).

const (
	// cpuSampleInterval is both the sampling cadence and, on darwin, the width
	// of the interval iostat averages over — the two are the same number by
	// construction, because each sample IS the interval it covers.
	cpuSampleInterval = 10 * time.Second
	// cpuHistoryLen caps the ring: ten minutes at the cadence above. Long enough
	// to show a test run start and finish, short enough that the strip stays a
	// shape rather than a smear at sidebar width.
	cpuHistoryLen = 60
	// cpuStaleAfter is how long the newest sample stays believable. A sampler
	// whose source has died (iostat killed, /proc unmounted) would otherwise keep
	// publishing the last reading forever, and a pinned "97%" from ten minutes
	// ago is worse than no row: it is a number that invites action.
	cpuStaleAfter = 3 * cpuSampleInterval
	// cpuRestartDelay paces a retry after the source falls over, and
	// cpuMaxRestarts bounds them: a host where the command simply is not there
	// should cost one log line and nothing else, not a subprocess every half
	// minute for the life of the server.
	cpuRestartDelay = 30 * time.Second
	cpuMaxRestarts  = 3
)

// Sampler holds the recent history and hands it to the poll.
//
// It is the one piece of usage state touched by two goroutines — its own
// sampling loop writes, runUsage's poll reads — so it carries a mutex where the
// rest of this file's readers are plain functions. The critical sections are a
// slice append and a slice copy; nothing that can block is done under the lock.
type Sampler struct {
	mu      sync.Mutex
	samples []float64 // busy percent, oldest first, at most cpuHistoryLen
	load    float64   // 1-minute load average as of the newest sample; -1 unknown
	at      time.Time // when the newest sample landed

	// quit ends the sampling loop. It exists because a cathost samples only
	// while somebody is subscribed: the whole cost of this row on darwin is a
	// long-lived iostat, and a daemon that kept one running for a section
	// nobody is looking at would be a monitoring agent nobody asked for. A
	// catway's own sampler is never stopped — it runs for the life of the
	// process, as it always did.
	quit     chan struct{}
	quitOnce sync.Once
}

func NewSampler() *Sampler { return &Sampler{load: -1, quit: make(chan struct{})} }

// Stop ends the sampling loop and, on darwin, kills the iostat behind it. The
// history is left intact — a caller may still read the last rows — but it goes
// stale on the ordinary cpuStaleAfter schedule and Row then reports nothing.
// Idempotent, and safe to call on a sampler that was never started.
func (c *Sampler) Stop() {
	if c == nil {
		return
	}
	c.quitOnce.Do(func() { close(c.quit) })
}

// Stopped reports whether Stop has been called — the observable half of the
// contract for a caller that needs to know its subprocess is gone.
func (c *Sampler) Stopped() bool { return c != nil && c.stopping() }

// stopping reports whether Stop has been called.
func (c *Sampler) stopping() bool {
	select {
	case <-c.quit:
		return true
	default:
		return false
	}
}

// add records one interval's reading, dropping the oldest when the ring is full.
func (c *Sampler) add(busy, load float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.samples = append(c.samples, ClampPct(busy))
	if len(c.samples) > cpuHistoryLen {
		// Copy down rather than reslice: a bare c.samples[1:] keeps the whole
		// backing array alive and walks its head off the front forever, so the
		// allocation would grow without bound on a server that runs for weeks.
		copy(c.samples, c.samples[len(c.samples)-cpuHistoryLen:])
		c.samples = c.samples[:cpuHistoryLen]
	}
	c.load, c.at = load, time.Now()
}

// Row is the CPU row as of now, or PctUnknown when there is nothing trustworthy
// to report — no samples yet (the first arrives one interval after launch), an
// unsupported host, or a source that has gone quiet. A nil sampler answers the
// same way, which is what lets a caller build a HOST group without starting a
// goroutine.
func (c *Sampler) Row() Row {
	unknown := Row{Name: "CPU", Pct: PctUnknown}
	if c == nil {
		return unknown
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.samples) == 0 || time.Since(c.at) > cpuStaleAfter {
		return unknown
	}
	r := Row{Name: "CPU", Pct: c.samples[len(c.samples)-1]}
	if c.load >= 0 {
		// Load average rather than a used/total pair, because CPU has no pair to
		// give: the percentage already IS the whole machine. Load answers the
		// question the percentage cannot — 100% with a load of 2 on ten cores is
		// one busy process, 100% with a load of 40 is a queue.
		r.Detail = fmt.Sprintf("load %.2f", c.load)
	}
	if len(c.samples) > 1 {
		// A copy: the caller marshals this on another goroutine while the
		// sampling loop keeps appending to the original.
		r.Spark = append([]float64(nil), c.samples...)
	}
	return r
}

// run is the sampling loop (own goroutine, started by runUsage). It returns on
// a host that cannot be sampled, and only then.
func (c *Sampler) Run() {
	switch runtime.GOOS {
	case "darwin":
		c.runDarwin()
	case "linux":
		c.runLinux()
	}
}

// --- macOS ---------------------------------------------------------------------

// runDarwin keeps one iostat alive. A stream that ends is restarted, because the
// process can be killed out from under us for reasons that have nothing to do
// with this server; a stream that ends having produced nothing is counted as a
// failure, and enough of those in a row means the host cannot answer at all.
func (c *Sampler) runDarwin() {
	fails := 0
	for {
		if c.stopping() {
			return
		}
		n, err := c.streamIostat()
		if c.stopping() {
			return // the stream ended because we killed it, not because it failed
		}
		if n > 0 {
			fails = 0 // it worked at least once; whatever ended it was not fatal
		} else {
			fails++
			if fails >= cpuMaxRestarts {
				reason := "iostat unavailable"
				if err != nil {
					reason = "iostat: " + err.Error()
				}
				log.Printf("hostmeter: host CPU unavailable (%s) — the sidebar drops the row", reason)
				return
			}
		}
		select {
		case <-time.After(cpuRestartDelay):
		case <-c.quit:
			return
		}
	}
}

// streamIostat runs one iostat to exhaustion, folding in every line it prints.
// It returns how many CPU lines it saw, so the supervisor can tell "the process
// died" from "the process is not there".
func (c *Sampler) streamIostat() (int, error) {
	cmd := exec.Command("iostat", "-w", strconv.Itoa(int(cpuSampleInterval/time.Second)))
	out, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	// Kill on the way out. A reader that returns on a scan error without this
	// would leave iostat running for the life of the server, printing into a
	// pipe nobody drains.
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	// And kill it on Stop, which is the only thing that can interrupt the scan
	// below: it blocks on a pipe that only produces a line every ten seconds, so
	// waiting for it to notice would make an unsubscribe take that long.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-c.quit:
			_ = cmd.Process.Kill()
		case <-done:
		}
	}()

	sc := bufio.NewScanner(out)
	seen := 0
	for sc.Scan() {
		busy, load, ok := parseIostatCPU(sc.Text())
		if !ok {
			continue // one of the two header lines
		}
		seen++
		if seen == 1 {
			// iostat's first data line averages since boot, which is a different
			// measurement wearing the same shape — plotting it would put a
			// week-long average at the left edge of a ten-minute chart.
			continue
		}
		c.add(busy, load)
	}
	return seen, sc.Err()
}

// parseIostatCPU pulls the busy share and the 1-minute load out of one iostat
// line. The layout carries three columns per disk and the machine may have any
// number of them, so the six fields that matter are taken from the END, where
// they always sit:
//
//	          disk0               disk5       cpu    load average
//	KB/t  tps  MB/s     KB/t  tps  MB/s  us sy id   1m   5m   15m
//	6.60 1408  9.08    15.39    0  0.00  14 12 74  12.39 9.45 10.61
//	                                     └── us sy id 1m 5m 15m ──┘
//
// Header lines are rejected by the same parse that reads the data ones: "us" is
// not a float. The us+sy+id sanity check is what keeps a coincidentally numeric
// line from being read as a sample — the three are shares of one CPU and must
// come to about a hundred.
func parseIostatCPU(line string) (busy, load float64, ok bool) {
	f := strings.Fields(line)
	if len(f) < 6 {
		return 0, 0, false
	}
	var v [6]float64
	for i, s := range f[len(f)-6:] {
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, 0, false
		}
		v[i] = n
	}
	us, sy, id, load1m := v[0], v[1], v[2], v[3]
	if sum := us + sy + id; sum < 95 || sum > 105 {
		return 0, 0, false
	}
	// 100-idle rather than us+sy: the two agree here, and idle is the one column
	// whose meaning cannot drift if a future iostat splits the busy time further.
	return 100 - id, load1m, true
}

// --- Linux ---------------------------------------------------------------------

// runLinux differences /proc/stat across each interval. The first pass only
// establishes the baseline — a rate needs two readings — so the first sample
// lands one interval in, exactly as darwin's does.
func (c *Sampler) runLinux() {
	var prevBusy, prevTotal float64
	have, fails := false, 0
	t := time.NewTicker(cpuSampleInterval)
	defer t.Stop()
	for {
		raw, err := os.ReadFile("/proc/stat")
		switch {
		case err != nil:
			fails++
		default:
			busy, total, ok := parseProcStatCPU(raw)
			if !ok {
				fails++
				break
			}
			fails = 0
			// A total that did not advance means the two readings landed inside
			// one jiffy; dividing by it would be a divide-by-zero dressed as a
			// reading. Skip the sample and keep the baseline.
			if have && total > prevTotal {
				c.add(100*(busy-prevBusy)/(total-prevTotal), linuxLoadAvg())
			}
			prevBusy, prevTotal, have = busy, total, true
		}
		if fails >= cpuMaxRestarts {
			log.Printf("hostmeter: host CPU unavailable (/proc/stat unreadable) — the sidebar drops the row")
			return
		}
		select {
		case <-t.C:
		case <-c.quit:
			return
		}
	}
}

// parseProcStatCPU reads the aggregate line of /proc/stat — cumulative jiffies
// since boot, per state:
//
//	cpu  1247 0 830 91238 122 0 61 0 0 0
//	     user nice system idle iowait irq softirq steal guest guest_nice
//
// Busy is everything but idle and iowait. iowait counts as idle deliberately: a
// core waiting on a disk is a core available to any process that wants it, and
// counting it as busy would paint a machine that is merely reading files as one
// that is out of CPU. Fields past the ones named are summed into the total
// without being enumerated, so a kernel that adds a state keeps the denominator
// right.
func parseProcStatCPU(raw []byte) (busy, total float64, ok bool) {
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue // per-core lines ("cpu0") and every other counter
		}
		var idle float64
		for i, s := range strings.Fields(line)[1:] {
			n, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return 0, 0, false
			}
			total += n
			if i == 3 || i == 4 { // idle, iowait
				idle += n
			}
		}
		if total == 0 {
			return 0, 0, false
		}
		return total - idle, total, true
	}
	return 0, 0, false
}

// linuxLoadAvg reads the 1-minute load average, or -1 when it cannot: the load
// is the row's caption and a missing caption costs the row nothing, so it is
// never worth failing the sample over.
func linuxLoadAvg() float64 {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return -1
	}
	f := strings.Fields(string(raw))
	if len(f) == 0 {
		return -1
	}
	n, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return -1
	}
	return n
}
