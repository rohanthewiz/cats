//go:build !darwin && !linux

package hostmeter

import (
	"fmt"
	"runtime"
)

// Everywhere without statfs(2). The build tag rather than a runtime.GOOS switch
// (which is how hostMemory chooses between its two hosts) because the syscall
// package does not even declare Statfs_t off these systems, so the choice has to
// be made before the compiler sees it. The caller treats the error the same way
// either way: no reading, so no row.
func diskBytes(path string) (total, used uint64, err error) {
	return 0, 0, fmt.Errorf("host disk: unsupported on %s", runtime.GOOS)
}
