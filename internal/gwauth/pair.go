package gwauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"sync"
	"time"
)

// Pairing hands a new device (the mobile client) a credential without ever
// showing it the shared secret.
//
// The obvious design — a control-socket method that returns the password — is
// an escalation, not a convenience. Any local process can reach the control
// socket: a plugin, a `curl | sh` postinstall, one of the coding agents this
// machine exists to run. A password read that way reaches the catway from
// anywhere on the network, forever, with no revocation short of a restart. And
// `catctl pair` run inside a cats pane has its output swept into the scrollback
// history file, writing to disk the one secret this package promises never
// touches it.
//
// So the control socket mints a *pairing token* instead:
//
//	catctl pair ──socket──▶ IssuePairToken ──▶ token (5 min, single use)
//	                                            │
//	              QR / URI ──▶ phone ───────────┘
//	                             │
//	                             ▼
//	          POST /login password=<token> ──▶ RedeemPairToken ──▶ session
//
// What the phone ends up holding is an ordinary session credential: signed with
// the per-process key, expiring at the configured TTL, and invalidated by a
// restart. That is the point — it is exactly the revocability the password
// lacks. A token that leaks is worth minutes, not years, and only until the
// first redemption.
const (
	// PairTTL is how long a minted pairing token stays redeemable. Long enough
	// to put the phone down, unlock it and open the camera; short enough that a
	// token swept into a scrollback file or a shell history is worthless by the
	// time anyone reads it back.
	PairTTL = 5 * time.Minute

	// maxPairTokens caps outstanding grants. Issuing is unauthenticated beyond
	// "can open the owner-only control socket", so `while true; do catctl pair;
	// done` must not grow memory without bound. Evicting the token nearest to
	// expiry keeps the most recent request — the one the operator is actually
	// looking at — working.
	maxPairTokens = 8

	// pairTokenBytes is the entropy in a pairing token. 18 bytes → 24 base64url
	// characters, matching GenerateSecret. Brute force is not the threat model
	// (five minutes, one use, and the redemption endpoint is the login form),
	// but there is no reason to be stingy.
	pairTokenBytes = 18
)

// pairStore holds the outstanding pairing grants. It is small, in-memory and
// per-process by design: nothing about pairing survives a restart, which is the
// same statement as "a restart revokes every paired device".
type pairStore struct {
	mu     sync.Mutex
	grants map[string]time.Time // token → expiry
}

// IssuePairToken mints a single-use pairing token valid for PairTTL from now,
// returning it with its expiry. Callers reach this only over the local control
// socket — the browser front end has no path to it, deliberately.
func (a *Authenticator) IssuePairToken(now time.Time) (token string, expires time.Time, err error) {
	b := make([]byte, pairTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", time.Time{}, fmt.Errorf("gwauth: generate pair token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	expires = now.Add(PairTTL)

	a.pairs.mu.Lock()
	defer a.pairs.mu.Unlock()
	a.pairs.sweepLocked(now)
	if len(a.pairs.grants) >= maxPairTokens {
		a.pairs.evictSoonestLocked()
	}
	a.pairs.grants[token] = expires
	return token, expires, nil
}

// RedeemPairToken consumes a pairing token, reporting whether it was live. A
// token redeems at most once: the successful call removes it, so a replay — by
// whoever else saw the QR, the scrollback, or the wire — finds nothing.
//
// The scan is linear and constant-time rather than a map lookup. With at most
// maxPairTokens entries the cost is irrelevant, and it removes any question
// about what a hash-table probe leaks about a secret an attacker supplied.
func (a *Authenticator) RedeemPairToken(token string, now time.Time) bool {
	if token == "" {
		return false
	}
	a.pairs.mu.Lock()
	defer a.pairs.mu.Unlock()
	a.pairs.sweepLocked(now)

	var matched string
	found := false
	for candidate := range a.pairs.grants {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1 {
			matched, found = candidate, true
		}
	}
	if !found {
		return false
	}
	delete(a.pairs.grants, matched)
	return true
}

// PendingPairTokens reports how many grants are still redeemable at now. It
// exists for tests and diagnostics; nothing on a request path consults it.
func (a *Authenticator) PendingPairTokens(now time.Time) int {
	a.pairs.mu.Lock()
	defer a.pairs.mu.Unlock()
	a.pairs.sweepLocked(now)
	return len(a.pairs.grants)
}

// sweepLocked drops expired grants. Called from every entry point, so the map
// is bounded by issue rate over PairTTL rather than by process lifetime — there
// is no separate reaper goroutine to own or stop.
func (p *pairStore) sweepLocked(now time.Time) {
	for token, exp := range p.grants {
		if !now.Before(exp) {
			delete(p.grants, token)
		}
	}
}

// evictSoonestLocked removes the grant closest to expiring. At the cap the
// operator is issuing faster than they are redeeming, and the oldest outstanding
// token is the one least likely to be the one they are about to scan.
func (p *pairStore) evictSoonestLocked() {
	var oldest string
	var oldestExp time.Time
	for token, exp := range p.grants {
		if oldest == "" || exp.Before(oldestExp) {
			oldest, oldestExp = token, exp
		}
	}
	if oldest != "" {
		delete(p.grants, oldest)
	}
}
