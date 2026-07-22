package gwauth

import (
	"testing"
	"time"
)

func newTestAuth(t *testing.T, secret string, ttl time.Duration) *Authenticator {
	t.Helper()
	a, err := New(secret, ttl)
	if err != nil {
		t.Fatalf("New(%q): %v", secret, err)
	}
	return a
}

func TestNewRejectsBadInput(t *testing.T) {
	if _, err := New("", time.Hour); err == nil {
		t.Error("empty secret: want error, got nil")
	}
	if _, err := New("s", 0); err == nil {
		t.Error("zero ttl: want error, got nil")
	}
	if _, err := New("s", -time.Second); err == nil {
		t.Error("negative ttl: want error, got nil")
	}
}

func TestCheckSecret(t *testing.T) {
	a := newTestAuth(t, "hunter2", time.Hour)
	if !a.CheckSecret("hunter2") {
		t.Error("correct secret rejected")
	}
	if a.CheckSecret("hunter3") {
		t.Error("wrong secret accepted")
	}
	if a.CheckSecret("") {
		t.Error("empty secret accepted")
	}
	if a.CheckSecret("hunter2 ") {
		t.Error("secret with trailing space accepted")
	}
}

func TestCheckBearer(t *testing.T) {
	a := newTestAuth(t, "s3cr3t", time.Hour)
	cases := []struct {
		hdr  string
		want bool
	}{
		{"Bearer s3cr3t", true},
		{"bearer s3cr3t", true},   // scheme is case-insensitive
		{"Bearer  s3cr3t ", true}, // surrounding whitespace trimmed
		{"Bearer wrong", false},
		{"Bearer ", false},
		{"Bearer", false},
		{"", false},
		{"Basic s3cr3t", false},
		{"s3cr3t", false},
	}
	for _, c := range cases {
		if got := a.CheckBearer(c.hdr); got != c.want {
			t.Errorf("CheckBearer(%q) = %v, want %v", c.hdr, got, c.want)
		}
	}
}

func TestSessionRoundTrip(t *testing.T) {
	a := newTestAuth(t, "s", time.Hour)
	now := time.Unix(1_700_000_000, 0)
	cookie := a.IssueSession(now)
	if !a.ValidSession(cookie, now) {
		t.Fatal("freshly issued session invalid")
	}
	if !a.ValidSession(cookie, now.Add(59*time.Minute)) {
		t.Error("session invalid before expiry")
	}
}

func TestSessionExpires(t *testing.T) {
	a := newTestAuth(t, "s", time.Hour)
	now := time.Unix(1_700_000_000, 0)
	cookie := a.IssueSession(now)
	if a.ValidSession(cookie, now.Add(time.Hour+time.Second)) {
		t.Error("expired session accepted")
	}
	if a.ValidSession(cookie, now.Add(2*time.Hour)) {
		t.Error("long-expired session accepted")
	}
}

func TestSessionRejectsTampering(t *testing.T) {
	a := newTestAuth(t, "s", time.Hour)
	now := time.Unix(1_700_000_000, 0)

	bad := []string{
		"",
		".",
		"abc",                 // no dot
		"9999999999",          // no signature
		"9999999999.deadbeef", // wrong signature
		"9999999999.nothex",   // non-hex signature
		"notanumber.deadbeef", // non-numeric payload
	}
	for _, c := range bad {
		if a.ValidSession(c, now) {
			t.Errorf("ValidSession(%q) accepted a malformed/forged cookie", c)
		}
	}

	// Extending the expiry without re-signing must fail: reuse a real
	// signature under a later payload.
	cookie := a.IssueSession(now)
	forged := "9999999999." + cookie[indexDot(cookie)+1:]
	if a.ValidSession(forged, now) {
		t.Error("payload-swapped cookie accepted")
	}
}

func TestSessionsUnforgeableAcrossKeys(t *testing.T) {
	// A cookie from one Authenticator (its own random signing key) must not
	// validate against another — the signing key is per process.
	now := time.Unix(1_700_000_000, 0)
	a1 := newTestAuth(t, "s", time.Hour)
	a2 := newTestAuth(t, "s", time.Hour)
	cookie := a1.IssueSession(now)
	if a2.ValidSession(cookie, now) {
		t.Error("session validated under a different signing key")
	}
}

func TestGenerateSecretUnique(t *testing.T) {
	s1, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	s2, _ := GenerateSecret()
	if s1 == "" || s1 == s2 {
		t.Errorf("weak/duplicate generated secrets: %q, %q", s1, s2)
	}
}

func TestOriginOK(t *testing.T) {
	cases := []struct {
		origin, host string
		allowed      []string
		want         bool
	}{
		{"", "example.com:8421", nil, true},                          // non-browser: no Origin
		{"https://example.com:8421", "example.com:8421", nil, true},  // same origin (https)
		{"http://example.com:8421", "example.com:8421", nil, true},   // same authority, http
		{"https://evil.com", "example.com:8421", nil, false},         // cross-site
		{"https://example.com:9999", "example.com:8421", nil, false}, // port mismatch
		{"null", "example.com:8421", nil, false},                     // opaque/file origin
		{"not a url ::::", "example.com:8421", nil, false},           // unparseable
		// allow-list: a proxy/relay host that differs from the request Host.
		{"https://app.relay.dev", "example.com:8421", []string{"https://app.relay.dev"}, true}, // full-origin entry
		{"https://app.relay.dev", "example.com:8421", []string{"app.relay.dev"}, true},         // bare-authority entry
		{"https://app.relay.dev:8443", "example.com:8421", []string{"app.relay.dev:8443"}, true},
		{"https://other.relay.dev", "example.com:8421", []string{"app.relay.dev"}, false}, // not on the list
		{"https://app.relay.dev", "example.com:8421", []string{""}, false},                // empty entry never matches
	}
	for _, c := range cases {
		if got := OriginOK(c.origin, c.host, c.allowed); got != c.want {
			t.Errorf("OriginOK(%q, %q, %v) = %v, want %v", c.origin, c.host, c.allowed, got, c.want)
		}
	}
}

// indexDot returns the position of the first '.' in s (session cookies always
// contain one). Small helper to keep the tamper test readable.
func indexDot(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}
