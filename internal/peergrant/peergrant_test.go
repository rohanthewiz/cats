package peergrant

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "peer-grants.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

var t0 = time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)

func TestMintThenCheck(t *testing.T) {
	s, _ := openTemp(t)
	cred, g, err := s.Mint("home mini", "me@laptop", t0)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if !strings.HasPrefix(cred, Prefix) {
		t.Errorf("credential %q lacks prefix %q", cred, Prefix)
	}
	if strings.Contains(g.Hash, cred) || g.Hash == "" {
		t.Errorf("grant hash %q is empty or carries the credential", g.Hash)
	}
	got, ok := s.Check(cred, t0.Add(time.Minute))
	if !ok || got.ID != g.ID || got.Label != "home mini" || got.Peer != "me@laptop" {
		t.Fatalf("Check = %+v, %v; want grant %s", got, ok, g.ID)
	}
	if !got.LastUsed.Equal(t0.Add(time.Minute)) {
		t.Errorf("LastUsed = %v, want %v", got.LastUsed, t0.Add(time.Minute))
	}
}

func TestCheckRejects(t *testing.T) {
	s, _ := openTemp(t)
	cred, _, err := s.Mint("", "", t0)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"",
		"hunter2",                           // no prefix: the shared password path
		Prefix,                              // prefix alone
		Prefix + "AAAAAAAAAAAAAAAAAAAAAAAA", // well-formed, never issued
		cred[:len(cred)-1],                  // truncated
		cred + "x",                          // extended
	} {
		if _, ok := s.Check(bad, t0); ok {
			t.Errorf("Check(%q) accepted", bad)
		}
	}
	var nilStore *Store
	if _, ok := nilStore.Check(cred, t0); ok {
		t.Error("nil store accepted a credential")
	}
}

func TestRevoke(t *testing.T) {
	s, _ := openTemp(t)
	credA, a, _ := s.Mint("a", "", t0)
	credB, b, _ := s.Mint("b", "", t0.Add(time.Second))

	if _, err := s.Revoke(a.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, ok := s.Check(credA, t0); ok {
		t.Error("revoked credential still authenticates")
	}
	if _, ok := s.Check(credB, t0); !ok {
		t.Error("revoking one grant broke another")
	}
	if _, err := s.Revoke(a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Revoke err = %v, want ErrNotFound", err)
	}
	if l := s.List(); len(l) != 1 || l[0].ID != b.ID {
		t.Errorf("List after revoke = %+v, want only %s", l, b.ID)
	}
}

// The property the whole package exists for: a grant outlives the process
// that issued it, and so does a revocation.
func TestSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peer-grants.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	keep, _, _ := s.Mint("keep", "", t0)
	gone, goneG, _ := s.Mint("gone", "", t0.Add(time.Second))
	if _, err := s.Revoke(goneG.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if _, ok := s2.Check(keep, t0); !ok {
		t.Error("grant lost across reopen")
	}
	if _, ok := s2.Check(gone, t0); ok {
		t.Error("revoked grant resurrected by reopen")
	}
}

func TestListOrderAndLabels(t *testing.T) {
	s, _ := openTemp(t)
	_, second, _ := s.Mint("second", "", t0.Add(time.Hour))
	_, first, _ := s.Mint("first", "evil\x1b[2Jhost\n", t0)
	l := s.List()
	if len(l) != 2 || l[0].ID != first.ID || l[1].ID != second.ID {
		t.Fatalf("List order = %+v", l)
	}
	if l[0].Peer != "evil [2Jhost" {
		t.Errorf("control characters survived in peer label: %q", l[0].Peer)
	}
	long := strings.Repeat("é", maxLabelLen+10)
	_, g, _ := s.Mint(long, "", t0)
	if n := len([]rune(g.Label)); n != maxLabelLen {
		t.Errorf("label length %d runes, want capped at %d", n, maxLabelLen)
	}
}

// LastUsed is written through at most once per granularity window; the
// in-memory value is always current.
func TestLastUsedThrottled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peer-grants.db")
	s, _ := Open(path)
	cred, _, _ := s.Mint("", "", t0)
	s.Check(cred, t0.Add(2*time.Hour))               // persisted: first use
	s.Check(cred, t0.Add(2*time.Hour+5*time.Minute)) // in memory only
	if got := s.List()[0].LastUsed; !got.Equal(t0.Add(2*time.Hour + 5*time.Minute)) {
		t.Errorf("in-memory LastUsed = %v", got)
	}
	_ = s.Close()

	s2, _ := Open(path)
	defer s2.Close()
	if got := s2.List()[0].LastUsed; !got.Equal(t0.Add(2 * time.Hour)) {
		t.Errorf("persisted LastUsed = %v, want the throttled %v", got, t0.Add(2*time.Hour))
	}
}
