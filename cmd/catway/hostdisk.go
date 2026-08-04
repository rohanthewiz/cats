//go:build ghostty

package main

import (
	"fmt"
	"os"

	"github.com/rohanthewiz/cats/internal/browserproto"
)

// How much of the machine's disk is spoken for, as the second row of the
// sidebar's HOST subsection.
//
// It sits beside memory for the same reason memory sits beside the rate-limit
// windows: it answers "is anything about to stop?" and it is the other resource
// a long multi-agent session quietly eats. Transcripts, worktrees, module and
// build caches, container images and a language server per pane all land on the
// same volume, and a disk that fills does not degrade the way memory does — it
// fails outright, mid-write, in whichever pane happened to be writing. A row
// that has been creeping up all week is the only warning available before that.
//
// Like memory it rides the usage poll rather than a timer of its own: one
// statfs(2) every couple of minutes, on the goroutine already awake for it, in
// the message already being sent. It moves far slower than memory does, so the
// poll's cadence is generous rather than tight.
//
// Hosts that cannot be asked report nothing rather than a guess, and the sidebar
// draws no row — see hostDisk.

// hostDisk reads the volume behind the user's home directory as a usage window.
// A failed read is UsagePctUnknown, which the sidebar renders as no row at all,
// on the same reasoning as hostMemory: an empty disk row invites the reader to
// conclude something about a volume nobody measured.
func hostDisk() browserproto.UsageWindow {
	total, used, err := diskBytes(hostDiskPath())
	if err != nil || total == 0 {
		return browserproto.UsageWindow{Pct: browserproto.UsagePctUnknown}
	}
	return browserproto.UsageWindow{
		Pct: clampPct(100 * float64(used) / float64(total)),
		// The absolute pair, for the same reason memory carries one: 90% of a
		// 2 TB volume still has room for today's work, 90% of 128 GB does not.
		Detail: fmt.Sprintf("%s/%s", formatBytes(used), formatBytes(total)),
	}
}

// hostDiskPath picks the volume to measure: the one holding the user's home
// directory, falling back to the root.
//
// Home rather than "/" because home is where this program's own growth happens
// — sessions, transcripts, worktrees, ~/.cache, ~/go/pkg — and on a Linux host
// with a separate /home partition the root volume's figure would answer a
// question nobody asked. On macOS the two are the same APFS container, so the
// choice costs nothing there and pays on the host where it matters.
func hostDiskPath() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	return "/"
}
