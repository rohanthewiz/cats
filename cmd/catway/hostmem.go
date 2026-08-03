//go:build ghostty

package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/rohanthewiz/cats/internal/browserproto"
)

// How much of the machine's RAM is spoken for, as one more row in the sidebar's
// USAGE section.
//
// It belongs there for the same reason the rate-limit windows do: it is a
// number a long session keeps wanting and cannot get without leaving the pane
// it is working in, and it answers the same question they answer — is anything
// about to stop? A handful of agents, each with a language server and a test
// run behind it, will exhaust a laptop's memory long before they exhaust the
// week's tokens, and the failure looks nothing like a rate limit: the machine
// starts swapping and every pane goes treacle. A row that has been climbing for
// an hour is the warning that arrives before that.
//
// It rides the usage poll rather than a timer of its own. The reading is two
// cheap syscalls' worth of work every couple of minutes, taken on the goroutine
// that is already awake for it, and pushed in the message that is already being
// sent. Memory does move faster than a five-hour window, so this is a trend
// line, not a monitor — the refresh control takes a fresh one on demand.
//
// Two hosts are supported, and the answer means the same thing on both: the
// bytes a program could take right now without pushing something else out of
// RAM, subtracted from the total. Everywhere else reports nothing rather than a
// guess, and the sidebar draws no row.

// hostMemory reads the host's memory as a usage window: a percentage in use and
// the absolute figures behind it. A failed read is UsagePctUnknown, which the
// sidebar renders as no row at all — an empty memory row would be a worse
// answer than the absence of one, because it invites the reader to conclude
// something about a machine nobody measured.
func hostMemory() browserproto.UsageWindow {
	total, used, err := hostMemoryBytes()
	if err != nil || total == 0 {
		return browserproto.UsageWindow{Pct: browserproto.UsagePctUnknown}
	}
	return browserproto.UsageWindow{
		Pct: clampPct(100 * float64(used) / float64(total)),
		// The absolute pair, because a percentage alone cannot be acted on:
		// 70% of 8 GB and 70% of 128 GB call for different decisions.
		Detail: fmt.Sprintf("%s/%s", formatBytes(used), formatBytes(total)),
	}
}

func hostMemoryBytes() (total, used uint64, err error) {
	switch runtime.GOOS {
	case "darwin":
		return darwinMemory()
	case "linux":
		return linuxMemory()
	}
	return 0, 0, fmt.Errorf("host memory: unsupported on %s", runtime.GOOS)
}

// --- macOS ---------------------------------------------------------------------

// darwinMemTotal caches the machine's physical RAM. It cannot change under a
// running kernel, and the read is a subprocess, so it is taken once. Only
// runUsage's goroutine reaches this, so the bare variable needs no guard.
var darwinMemTotal uint64

func darwinMemory() (total, used uint64, err error) {
	total, err = darwinTotalMemory()
	if err != nil {
		return 0, 0, err
	}
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return 0, 0, fmt.Errorf("host memory: vm_stat unavailable")
	}
	avail, err := parseVMStatAvailable(out)
	if err != nil {
		return 0, 0, err
	}
	if avail > total {
		// The two numbers come from different sources and are sampled a moment
		// apart; clamp rather than underflow the subtraction below.
		avail = total
	}
	return total, total - avail, nil
}

func darwinTotalMemory() (uint64, error) {
	if darwinMemTotal > 0 {
		return darwinMemTotal, nil
	}
	// sysctl(8) rather than the syscall: hw.memsize is a 64-bit value, and
	// syscall.Sysctl returns it as a raw byte string with the terminating NUL
	// trimmed — which silently eats the high byte of any size that ends in one.
	// A subprocess taken once at startup is the cheaper mistake.
	out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0, fmt.Errorf("host memory: sysctl unavailable")
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("host memory: unreadable hw.memsize")
	}
	darwinMemTotal = n
	return n, nil
}

// vmStatAvailable names the page buckets that can be handed to a new allocation
// without evicting anything a program is still using.
//
// macOS reports no "free memory" figure that means what a reader expects, so
// the buckets are picked deliberately:
//
//	free         unused pages
//	inactive     not touched recently — the first thing the pager takes back
//	speculative  read-ahead file pages nobody has asked for yet
//
// Everything else counts as used, including "occupied by compressor": a
// compressed page still occupies physical RAM, it merely holds more of a
// process's address space per byte. Purgeable pages are deliberately absent —
// they are already counted inside active and inactive, so adding them would
// credit the same pages twice and report a machine as roomier than it is.
//
// This lands between the two figures macOS itself will quote, and deliberately:
//
//	top(1) "PhysMem … unused"       counts inactive as used, so a healthy Mac
//	                                reads as 95%+ full and the row would be red
//	                                from boot.
//	kern.memorystatus_level         counts clean file-backed pages as free too,
//	                                which is the kernel's question (when do I
//	                                start killing processes?) rather than ours.
//
// The question here is the one a user is actually asking of the row — how much
// room is left before this machine starts fighting for it — and a page in the
// compressor is already evidence of that fight.
var vmStatAvailable = map[string]bool{
	"Pages free":        true,
	"Pages inactive":    true,
	"Pages speculative": true,
}

// parseVMStatAvailable turns vm_stat's report into available bytes. Its output
// is a page size in the header line and one "Pages <bucket>: <count>." per line
// after it:
//
//	Mach Virtual Memory Statistics: (page size of 16384 bytes)
//	Pages free:                               19455.
//	Pages active:                            425314.
func parseVMStatAvailable(out []byte) (uint64, error) {
	const sizeMark = "page size of "
	var pageSize, pages uint64
	var found int

	for _, line := range strings.Split(string(out), "\n") {
		if i := strings.Index(line, sizeMark); i >= 0 {
			rest := line[i+len(sizeMark):]
			if j := strings.IndexByte(rest, ' '); j > 0 {
				pageSize, _ = strconv.ParseUint(rest[:j], 10, 64)
			}
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok || !vmStatAvailable[strings.TrimSpace(key)] {
			continue
		}
		// Counts carry a trailing period, which ParseUint will not take.
		n, err := strconv.ParseUint(strings.Trim(strings.TrimSpace(val), "."), 10, 64)
		if err != nil {
			continue
		}
		pages += n
		found++
	}
	// A missing page size or not one recognised bucket means the output is not
	// the shape this reads — report nothing rather than scale a partial count.
	if pageSize == 0 || found == 0 {
		return 0, fmt.Errorf("host memory: unreadable vm_stat")
	}
	return pages * pageSize, nil
}

// --- Linux ---------------------------------------------------------------------

func linuxMemory() (total, used uint64, err error) {
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, fmt.Errorf("host memory: /proc/meminfo unreadable")
	}
	return parseMeminfo(raw)
}

// parseMeminfo reads the two figures out of /proc/meminfo, whose lines are
// "MemTotal:       32752180 kB".
//
// MemAvailable is the kernel's own estimate of what a new allocation could take
// without swapping, and it is the right number: it credits the reclaimable part
// of the page cache without crediting the part that is pinned. MemFree alone
// would read catastrophically low on any host that has been up long enough to
// fill its cache — a machine that is fine would show 95% used. Kernels before
// 3.14 predate the field, so the classic approximation stands in for them.
func parseMeminfo(raw []byte) (total, used uint64, err error) {
	vals := map[string]uint64{}
	for _, line := range strings.Split(string(raw), "\n") {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		if len(fields) > 1 && fields[1] == "kB" { // every line but a few counters
			n *= 1024
		}
		vals[key] = n
	}
	total = vals["MemTotal"]
	if total == 0 {
		return 0, 0, fmt.Errorf("host memory: no MemTotal")
	}
	avail, ok := vals["MemAvailable"]
	if !ok {
		avail = vals["MemFree"] + vals["Buffers"] + vals["Cached"]
	}
	if avail > total {
		avail = total
	}
	return total, total - avail, nil
}

// formatBytes renders a byte count for the sidebar's small print. Powers of two,
// because RAM is sold, specified and reported that way: 25769803776 bytes is the
// "24 GB" printed on the machine, and rendering it as 25.8 GB beside a
// percentage of itself would read as an error in the percentage.
func formatBytes(n uint64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0fM", float64(n)/(1<<20))
	default:
		return fmt.Sprintf("%dK", n>>10)
	}
}
