package gwauth

import (
	"sync"
	"testing"
	"time"
)

// newPairAuth builds an Authenticator for the pairing tests. The secret and TTL
// are irrelevant to pairing itself but New refuses empty/zero values.
func newPairAuth(t *testing.T) *Authenticator {
	t.Helper()
	a, err := New("s3cret", time.Hour)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

// A minted token redeems exactly once. The second attempt is the replay this
// whole design exists to defeat: whoever else saw the QR, the terminal, or the
// scrollback file gets nothing.
func TestPairTokenSingleUse(t *testing.T) {
	a := newPairAuth(t)
	now := time.Unix(1_700_000_000, 0)

	token, expires, err := a.IssuePairToken(now)
	if err != nil {
		t.Fatalf("IssuePairToken: %v", err)
	}
	if token == "" {
		t.Fatal("issued an empty token")
	}
	if want := now.Add(PairTTL); !expires.Equal(want) {
		t.Fatalf("expires = %v, want %v", expires, want)
	}
	if !a.RedeemPairToken(token, now) {
		t.Fatal("first redemption failed")
	}
	if a.RedeemPairToken(token, now) {
		t.Fatal("second redemption succeeded — token is not single-use")
	}
	if n := a.PendingPairTokens(now); n != 0 {
		t.Fatalf("pending = %d after redemption, want 0", n)
	}
}

// A token is worthless once PairTTL has passed, which is what makes it safe to
// print into a terminal whose output is swept to disk.
func TestPairTokenExpires(t *testing.T) {
	a := newPairAuth(t)
	now := time.Unix(1_700_000_000, 0)

	token, _, err := a.IssuePairToken(now)
	if err != nil {
		t.Fatalf("IssuePairToken: %v", err)
	}
	if !a.RedeemPairToken(token, now.Add(PairTTL-time.Second)) {
		t.Fatal("token rejected one second before expiry")
	}

	token2, _, _ := a.IssuePairToken(now)
	if a.RedeemPairToken(token2, now.Add(PairTTL)) {
		t.Fatal("token accepted at its expiry instant")
	}
	if a.RedeemPairToken(token2, now.Add(PairTTL+time.Hour)) {
		t.Fatal("expired token accepted later")
	}
}

// Anything that was never issued is refused, including the empty string a
// missing form field decodes to — the case that would otherwise turn "no
// password submitted" into a successful login.
func TestPairTokenUnknownRejected(t *testing.T) {
	a := newPairAuth(t)
	now := time.Unix(1_700_000_000, 0)
	if _, _, err := a.IssuePairToken(now); err != nil {
		t.Fatalf("IssuePairToken: %v", err)
	}
	for _, bad := range []string{"", " ", "not-a-token", "s3cret"} {
		if a.RedeemPairToken(bad, now) {
			t.Fatalf("redeemed %q", bad)
		}
	}
}

// Tokens are independent: redeeming one must not disturb the others, so an
// operator who issued twice can still use the code they are looking at.
func TestPairTokensIndependent(t *testing.T) {
	a := newPairAuth(t)
	now := time.Unix(1_700_000_000, 0)

	t1, _, _ := a.IssuePairToken(now)
	t2, _, _ := a.IssuePairToken(now)
	if t1 == t2 {
		t.Fatal("two issues produced the same token")
	}
	if !a.RedeemPairToken(t2, now) {
		t.Fatal("second token did not redeem")
	}
	if !a.RedeemPairToken(t1, now) {
		t.Fatal("first token was invalidated by redeeming the second")
	}
}

// Issuing in a loop must not grow the store without bound, and the eviction has
// to drop the *oldest* grant: the newest is the one on screen.
func TestPairTokenCapacity(t *testing.T) {
	a := newPairAuth(t)
	base := time.Unix(1_700_000_000, 0)

	issued := make([]string, 0, maxPairTokens*3)
	for i := range maxPairTokens * 3 {
		// Stagger issue times so expiry ordering (and therefore eviction order)
		// is well defined rather than map-iteration order.
		tok, _, err := a.IssuePairToken(base.Add(time.Duration(i) * time.Second))
		if err != nil {
			t.Fatalf("IssuePairToken: %v", err)
		}
		issued = append(issued, tok)
	}
	now := base.Add(time.Duration(len(issued)) * time.Second)
	if n := a.PendingPairTokens(now); n > maxPairTokens {
		t.Fatalf("pending = %d, want <= %d", n, maxPairTokens)
	}
	if !a.RedeemPairToken(issued[len(issued)-1], now) {
		t.Fatal("the most recently issued token was evicted")
	}
	if a.RedeemPairToken(issued[0], now) {
		t.Fatal("the oldest token survived eviction")
	}
}

// The store is reached from the control-socket goroutine (issue) and every
// request goroutine (redeem) at once, so -race has to be clean and exactly one
// caller may win a contested token.
func TestPairTokenConcurrent(t *testing.T) {
	a := newPairAuth(t)
	now := time.Now()

	token, _, err := a.IssuePairToken(now)
	if err != nil {
		t.Fatalf("IssuePairToken: %v", err)
	}

	const racers = 16
	var wg sync.WaitGroup
	wins := make(chan bool, racers)
	for i := range racers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_, _, _ = a.IssuePairToken(now) // concurrent issue churns the map
			}
			wins <- a.RedeemPairToken(token, now)
		}(i)
	}
	wg.Wait()
	close(wins)

	won := 0
	for w := range wins {
		if w {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d goroutines redeemed the same token, want exactly 1", won)
	}
}

// A session value arriving in the Authorization header is what a paired device
// presents (it has no cookie jar), so the header parse has to hand back the raw
// credential for ValidSession as well as for the secret comparison.
func TestBearerToken(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"Bearer abc", "abc", true},
		{"bearer abc", "abc", true},
		{"BEARER  abc  ", "abc", true},
		{"Bearer ", "", false}, // no room for a token after the prefix
		{"Basic abc", "", false},
		{"abc", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := BearerToken(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("BearerToken(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
		}
	}
}
