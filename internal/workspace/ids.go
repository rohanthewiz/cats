// Package workspace ports cats's workspace/tab bookkeeping (src/workspace.rs)
// — pane identity, tabs, and stable public numbering — with no terminal-backend
// coupling (pane creation goes through a spawner seam).
//
// This file ports the public id/number scheme: short human-readable handles in
// a bijective base-32 alphabet (no I/L/O/U; "0" is digit 32), so workspace ids
// look like "w1".."w9", "wA".., and survive restore without reuse.
package workspace

import (
	"math"
	"strings"
	"sync/atomic"
)

// publicIDAlphabet is cats's 32-character handle alphabet
// (cf. PUBLIC_ID_ALPHABET, workspace.rs:73). Digit values 1..32 in order.
const publicIDAlphabet = "123456789ABCDEFGHJKMNPQRSTVWXYZ0"

// nextWorkspaceID is the process-global workspace id counter
// (cf. NEXT_WORKSPACE_ID). Add(1) hands out 1, 2, ...
var nextWorkspaceID atomic.Uint64

// GenerateWorkspaceID allocates the next stable public workspace id ("w1",
// "w2", ... "wA", ...).
func GenerateWorkspaceID() string {
	return "w" + EncodePublicNumber(int(nextWorkspaceID.Add(1)))
}

// EncodePublicNumber renders value as a bijective base-32 handle. Zero
// encodes as "0" (which does not round-trip; real numbering starts at 1).
func EncodePublicNumber(value int) string {
	if value == 0 {
		return "0"
	}

	var encoded []byte
	for value > 0 {
		digit := (value - 1) % len(publicIDAlphabet)
		encoded = append(encoded, publicIDAlphabet[digit])
		value = (value - 1) / len(publicIDAlphabet)
	}
	// Digits were emitted least-significant first; reverse.
	for i, j := 0, len(encoded)-1; i < j; i, j = i+1, j-1 {
		encoded[i], encoded[j] = encoded[j], encoded[i]
	}
	return string(encoded)
}

// DecodePublicNumber parses a bijective base-32 handle. Returns false for
// characters outside the alphabet or on overflow.
func DecodePublicNumber(value string) (int, bool) {
	decoded := 0
	for _, ch := range value {
		digit := strings.IndexRune(publicIDAlphabet, ch)
		if digit < 0 {
			return 0, false
		}
		// Mirror Rust's checked_mul/checked_add overflow guards.
		if decoded > (math.MaxInt-(digit+1))/len(publicIDAlphabet) {
			return 0, false
		}
		decoded = decoded*len(publicIDAlphabet) + digit + 1
	}
	return decoded, true
}

// PublicWorkspaceNumber extracts the numeric handle from a workspace id like
// "wZ". Returns false when the id has no "w" prefix or a malformed number.
func PublicWorkspaceNumber(id string) (int, bool) {
	rest, ok := strings.CutPrefix(id, "w")
	if !ok {
		return 0, false
	}
	return DecodePublicNumber(rest)
}

// ReserveWorkspaceIDs raises the workspace id counter past every id in the
// given list, so restored workspaces never collide with newly generated ids.
// (Rust takes &[Workspace]; the Go port takes the id strings to stay
// decoupled from the struct.)
func ReserveWorkspaceIDs(ids []string) {
	maxSeen := 0
	found := false
	for _, id := range ids {
		if n, ok := PublicWorkspaceNumber(id); ok {
			found = true
			maxSeen = max(maxSeen, n)
		}
	}
	if !found || maxSeen == math.MaxInt {
		return
	}
	// The counter stores the last handed-out number; raise it to maxSeen so
	// the next GenerateWorkspaceID returns maxSeen+1 (cf. the Rust CAS loop,
	// where NEXT_WORKSPACE_ID stores the next number instead).
	target := uint64(maxSeen)
	for {
		current := nextWorkspaceID.Load()
		if current >= target {
			return
		}
		if nextWorkspaceID.CompareAndSwap(current, target) {
			return
		}
	}
}
