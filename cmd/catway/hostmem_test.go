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
