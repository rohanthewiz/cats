package buildinfo

import (
	"encoding/base64"
	"testing"
)

// stamp swaps the link-time values (and the memoised result) for one test,
// restoring them afterwards, then re-runs the resolve step directly — Get's
// sync.Once has usually fired already by the time a test calls it.
func stamp(t *testing.T, hashVal, subject string) Info {
	t.Helper()
	oldHash, oldSubj, oldRes := hash, subjectB64, resolved
	t.Cleanup(func() { hash, subjectB64, resolved = oldHash, oldSubj, oldRes })

	hash, subjectB64, resolved = hashVal, subject, Info{}
	resolve()
	return resolved
}

// A stamped subject arrives base64-encoded and survives the characters that
// motivated the encoding in the first place: spaces, quotes and '$'.
func TestResolveDecodesSubject(t *testing.T) {
	const subject = `feat(ui): don't "break" on $vars & <angles>`
	got := stamp(t, "e251787", base64.StdEncoding.EncodeToString([]byte(subject)))

	if got.Hash != "e251787" {
		t.Errorf("hash = %q, want e251787", got.Hash)
	}
	if got.Subject != subject {
		t.Errorf("subject = %q, want %q", got.Subject, subject)
	}
}

// Only the first line shows: a multi-line body can't smuggle extra rows into
// the display.
func TestResolveKeepsFirstLine(t *testing.T) {
	got := stamp(t, "abc1234", base64.StdEncoding.EncodeToString([]byte("subject line\n\nbody line\n")))
	if got.Subject != "subject line" {
		t.Errorf("subject = %q, want %q", got.Subject, "subject line")
	}
}

// An undecodable stamp degrades to no subject rather than to garbage; the hash
// still stands on its own.
func TestResolveBadBase64(t *testing.T) {
	got := stamp(t, "abc1234", "not!valid!base64")
	if got.Subject != "" {
		t.Errorf("subject = %q, want empty", got.Subject)
	}
	if got.Hash != "abc1234" {
		t.Errorf("hash = %q, want abc1234", got.Hash)
	}
}

// Unstamped, the hash falls back to the toolchain's VCS stamping. That is only
// present when the test binary was built inside the checkout, so assert the
// shape rather than a value: a hash is either absent or a 7-char short hash.
func TestResolveUnstampedFallback(t *testing.T) {
	got := stamp(t, "", "")
	if got.Subject != "" {
		t.Errorf("subject = %q, want empty without a stamp", got.Subject)
	}
	if got.Hash != "" && len(got.Hash) != 7 {
		t.Errorf("fallback hash = %q, want empty or 7 chars", got.Hash)
	}
}
