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
	grants map[string]pairGrant // token → what it may be spent on, and until when
}

// pairKind says where a pairing token may be redeemed. The two kinds share a
// store (and its cap) because they share every other property — entropy, TTL,
// single use — but they buy different things, so neither may be spent at the
// other's door:
//
//	kindDevice ─▶ POST /login          ─▶ a session (full access, session TTL, dies on restart)
//	kindPeer   ─▶ POST /peer/v1/pair   ─▶ a peer grant (peer sync only, durable, revocable)
//
// A peer token accepted at /login would hand a remote catway full terminal
// access when its operator asked for sync; a device token accepted at the peer
// door would mint a durable credential from a flow the operator believes ends
// in something a restart revokes.
type pairKind uint8

const (
	kindDevice pairKind = iota
	kindPeer
)

// pairGrant is one outstanding token's terms. label rides along on a peer
// grant so the durable credential it becomes can be named by the operator who
// minted it, rather than only by the peer that redeemed it.
type pairGrant struct {
	expires time.Time
	kind    pairKind
	label   string
}

// IssuePairToken mints a single-use pairing token valid for PairTTL from now,
// returning it with its expiry. Callers reach this only over the local control
// socket — the browser front end has no path to it, deliberately.
func (a *Authenticator) IssuePairToken(now time.Time) (token string, expires time.Time, err error) {
	return a.issue(pairGrant{kind: kindDevice}, now)
}

// IssuePeerPairToken mints a single-use token that a peer catway redeems for a
// durable peer-sync grant (internal/peergrant) rather than a session. label is
// the operator's name for the grant; "" leaves the peer to be named by what it
// reports about itself.
func (a *Authenticator) IssuePeerPairToken(label string, now time.Time) (token string, expires time.Time, err error) {
	return a.issue(pairGrant{kind: kindPeer, label: label}, now)
}

// issue is the shared body of both issuers: fill in the token and expiry, then
// store the grant under the cap.
func (a *Authenticator) issue(g pairGrant, now time.Time) (token string, expires time.Time, err error) {
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
	g.expires = expires
	a.pairs.grants[token] = g
	return token, expires, nil
}

// RedeemPairToken consumes a pairing token, reporting whether it was live. A
// token redeems at most once: the successful call removes it, so a replay — by
// whoever else saw the QR, the scrollback, or the wire — finds nothing.
//
// The scan is linear and constant-time rather than a map lookup. With at most
// maxPairTokens entries the cost is irrelevant, and it removes any question
// about what a hash-table probe leaks about a secret an attacker supplied.
//
// Only a device token redeems here; a peer token is left untouched (see
// pairKind), so presenting one at /login neither succeeds nor burns it.
func (a *Authenticator) RedeemPairToken(token string, now time.Time) bool {
	_, ok := a.redeem(token, kindDevice, now)
	return ok
}

// RedeemPeerPairToken consumes a peer pairing token, reporting whether it was
// live and returning the label its issuer gave it. The rules are
// RedeemPairToken's, with the kinds swapped.
func (a *Authenticator) RedeemPeerPairToken(token string, now time.Time) (label string, ok bool) {
	g, ok := a.redeem(token, kindPeer, now)
	return g.label, ok
}

// redeem finds and consumes a live token of the given kind.
//
// The kind is checked after the constant-time scan, not folded into it: which
// kind a token is reveals nothing to someone who already holds it, and a
// mismatch must leave the grant in place rather than consume it — a phone
// pointed at the wrong door should not cost the operator their code.
func (a *Authenticator) redeem(token string, kind pairKind, now time.Time) (pairGrant, bool) {
	if token == "" {
		return pairGrant{}, false
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
		return pairGrant{}, false
	}
	g := a.pairs.grants[matched]
	if g.kind != kind {
		return pairGrant{}, false
	}
	delete(a.pairs.grants, matched)
	return g, true
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
	for token, g := range p.grants {
		if !now.Before(g.expires) {
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
	for token, g := range p.grants {
		if oldest == "" || g.expires.Before(oldestExp) {
			oldest, oldestExp = token, g.expires
		}
	}
	if oldest != "" {
		delete(p.grants, oldest)
	}
}
