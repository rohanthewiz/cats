//go:build ghostty && (darwin || linux)

package main

import (
	"fmt"
	"syscall"
)

// diskBytes asks the kernel about the filesystem that contains path.
//
// statfs(2) rather than a df(1) subprocess: it is the call df itself makes, it
// costs a syscall instead of a fork, and it hands back the numbers already
// parsed. The struct's field types differ between the two systems — Bsize is
// uint32 on darwin and int64 on linux — so the block size is widened before it
// is used and the rest of the arithmetic stays identical.
//
// Available space is Bavail (what an unprivileged process could actually write)
// rather than Bfree (which includes the blocks reserved for root). Used is then
// total - available, which counts the reserved blocks as spent. That is
// deliberate and matches df's own Capacity column: those blocks are not room
// this program's work can grow into, so promising them would be a lie of
// exactly the kind this row exists to prevent.
func diskBytes(path string) (total, used uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, fmt.Errorf("host disk: statfs %s: %w", path, err)
	}
	bsize := uint64(st.Bsize)
	if bsize == 0 || st.Blocks == 0 {
		// A filesystem that reports no block size or no blocks — a synthetic
		// mount, say. Nothing sane can be divided out of it.
		return 0, 0, fmt.Errorf("host disk: %s reports no size", path)
	}
	total = st.Blocks * bsize
	avail := uint64(st.Bavail) * bsize
	if avail > total {
		// Bavail is signed on some filesystems and can read past the total on
		// others; clamp rather than underflow the subtraction below.
		avail = total
	}
	return total, total - avail, nil
}
