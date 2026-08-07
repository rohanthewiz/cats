//go:build ghostty

package main

import (
	"runtime"
	"strings"
	"testing"

	"github.com/rohanthewiz/cats/internal/browserproto"
)

// The parsers are tested against captured output rather than the live host, so
// the numbers are checked on a Linux runner reading a macOS report and the other
// way round — the one arrangement that catches a parser that only works on the
// machine it was written on.

const vmStatSample = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                               19455.
Pages active:                            425314.
Pages inactive:                          425521.
Pages speculative:                          946.
Pages throttled:                              0.
Pages wired down:                        118425.
Pages purgeable:                          23960.
"Translation faults":                3191714332.
Pages stored in compressor:             1189972.
Pages occupied by compressor:            545078.
Swapouts:                                125820.
`

func TestParseVMStatAvailable(t *testing.T) {
	got, err := parseVMStatAvailable([]byte(vmStatSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// free + inactive + speculative, in 16 KiB pages. Nothing else counts:
	// active, wired and the compressor's pages are all occupying RAM, and
	// purgeable is a subset of active/inactive that must not be added again.
	want := uint64(19455+425521+946) * 16384
	if got != want {
		t.Fatalf("available = %d, want %d", got, want)
	}
}

func TestParseVMStatAvailableRejectsJunk(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		// No header line, so no page size: the counts cannot be scaled and a
		// bare page count would be reported as bytes.
		{"no page size", "Pages free: 19455.\nPages inactive: 425521.\n"},
		// A header but no bucket this recognises — a shape change, which must
		// not read as "zero pages available", i.e. a machine 100% full.
		{"no buckets", "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages wired down: 118425.\n"},
		{"empty", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseVMStatAvailable([]byte(tc.in)); err == nil {
				t.Fatal("want an error, got a reading")
			}
		})
	}
}

const meminfoSample = `MemTotal:       32752180 kB
MemFree:         1234560 kB
MemAvailable:   16376090 kB
Buffers:          204800 kB
Cached:         12345678 kB
HugePages_Total:       0
`

func TestParseMeminfo(t *testing.T) {
	total, used, err := parseMeminfo([]byte(meminfoSample))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if want := uint64(32752180) * 1024; total != want {
		t.Fatalf("total = %d, want %d", total, want)
	}
	// MemAvailable is exactly half of MemTotal here, so used must be too —
	// which also proves Buffers/Cached were not added on top of it.
	if want := uint64(32752180-16376090) * 1024; used != want {
		t.Fatalf("used = %d, want %d", used, want)
	}
}

// A kernel too old for MemAvailable must still produce a sane answer rather
// than counting the whole page cache as used.
func TestParseMeminfoWithoutMemAvailable(t *testing.T) {
	in := "MemTotal:       32752180 kB\nMemFree:         1234560 kB\nBuffers:          204800 kB\nCached:         12345678 kB\n"
	total, used, err := parseMeminfo([]byte(in))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if want := uint64(32752180-1234560-204800-12345678) * 1024; used != want {
		t.Fatalf("used = %d, want %d", used, want)
	}
	if used >= total {
		t.Fatalf("used %d >= total %d", used, total)
	}
}

func TestParseMeminfoNoTotal(t *testing.T) {
	if _, _, err := parseMeminfo([]byte("MemFree: 1234560 kB\n")); err == nil {
		t.Fatal("want an error without MemTotal")
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		in   uint64
		want string
	}{
		{25769803776, "24.0G"}, // the "24 GB" printed on the machine
		{17_930_000_000, "16.7G"},
		{494_384_795_648, "460G"}, // disk scale: no tenths above 100 G
		{100 << 30, "100G"},       // exactly at the boundary, which takes the wider form
		{(100 << 30) - 1, "100.0G"},
		{512 << 20, "512M"},
		{64 << 10, "64K"},
		{0, "0K"},
	}
	for _, tc := range tests {
		if got := formatBytes(tc.in); got != tc.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The live read, on the two hosts that support it. It is a smoke test rather
// than an assertion about the machine: any figure in range proves the plumbing
// from subprocess to window works, and there is no independent number to check
// it against from inside the process.
func TestHostMemoryLive(t *testing.T) {
	w := hostMemory()
	switch runtime.GOOS {
	case "darwin", "linux":
		if w.Pct < 0 {
			t.Fatalf("no reading on %s", runtime.GOOS)
		}
		if w.Pct == 0 || w.Pct > 100 {
			t.Fatalf("pct = %v, want a plausible share", w.Pct)
		}
		if !strings.Contains(w.Detail, "/") {
			t.Fatalf("detail = %q, want used/total", w.Detail)
		}
	default:
		if w.Pct != browserproto.UsagePctUnknown {
			t.Fatalf("pct = %v on %s, want unknown", w.Pct, runtime.GOOS)
		}
	}
}

// The window's other half: an unreadable host yields no HOST subsection at all,
// rather than a heading with nothing under it. The ID is checked because the
// sidebar branches on it to pick the memory and disk warning scales — a machine
// 70% into its RAM is in more trouble than a week 70% spent. The row names are
// checked for the same reason: within the HOST group the sidebar picks a scale
// per row by name, so a renamed row would silently take the wrong thresholds.
//
// A nil sampler stands in for "CPU has nothing yet", which is the state for the
// first ten seconds of every run — the group must be the memory/disk pair then,
// not a group waiting on a third row.
func TestHostUsageGroup(t *testing.T) {
	g, ok := hostUsageGroup(nil)
	switch runtime.GOOS {
	case "darwin", "linux":
		if !ok {
			t.Fatalf("no group on %s", runtime.GOOS)
		}
		if g.ID != "host" {
			t.Errorf("id = %q, want host — the sidebar's host scales key on it", g.ID)
		}
		var names []string
		for _, w := range g.Windows {
			names = append(names, w.Name)
		}
		if len(names) != 2 || names[0] != "Memory" || names[1] != "Disk" {
			t.Fatalf("rows = %v, want [Memory Disk] — the sidebar keys its scales on these names", names)
		}
		// Memory is what a folded HOST shows in place of its rows, so the flag is
		// on exactly one window and on that one.
		var heads []string
		for _, w := range g.Windows {
			if w.Headline {
				heads = append(heads, w.Name)
			}
		}
		if len(heads) != 1 || heads[0] != "Memory" {
			t.Fatalf("headline rows = %v, want [Memory] — the folded HOST heading quotes it", heads)
		}
	default:
		if ok {
			t.Fatalf("a group was returned on %s, where neither resource is readable", runtime.GOOS)
		}
	}
}

// The CPU row takes its place between memory and disk once the sampler has
// something, and carries its history with it. The sampler is fed by hand rather
// than started: this is about the group's shape, and a real sample is one
// interval away.
func TestHostUsageGroupWithCPU(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("no host readings on %s", runtime.GOOS)
	}
	cpu := newCPUSampler()
	cpu.add(12, 2.5)
	cpu.add(34, 3.0)

	g, ok := hostUsageGroup(cpu)
	if !ok {
		t.Fatalf("no group on %s", runtime.GOOS)
	}
	var names []string
	var cpuWin browserproto.UsageWindow
	for _, w := range g.Windows {
		names = append(names, w.Name)
		if w.Name == "CPU" {
			cpuWin = w
		}
	}
	// Between the two, so the row most often high for a good reason is not the
	// first thing the group says.
	if len(names) != 3 || names[0] != "Memory" || names[1] != "CPU" || names[2] != "Disk" {
		t.Fatalf("rows = %v, want [Memory CPU Disk]", names)
	}
	if cpuWin.Pct != 34 {
		t.Errorf("cpu pct = %v, want the newest sample (34)", cpuWin.Pct)
	}
	if len(cpuWin.Spark) != 2 {
		t.Errorf("spark = %v, want both samples — the sidebar draws the history, not the point", cpuWin.Spark)
	}
	if cpuWin.Detail != "load 3.00" {
		t.Errorf("detail = %q, want the newest load average beside the percentage", cpuWin.Detail)
	}
	if cpuWin.Headline {
		t.Error("cpu is flagged as the headline; memory is the row a folded HOST quotes")
	}
}
