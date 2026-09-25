// Package peergrant holds the durable, revocable credentials one catway issues
// to another for peer sync.
//
// # Why a second kind of credential
//
// Peer sync used to authenticate with the peer's shared secret (its
// CATS_PASSWORD), copied by hand into a token_file on the other machine. That
// has three faults a pairing flow can fix:
//
//   - the password travels, and is written to disk somewhere it was never
//     meant to live;
//   - it grants everything — /ws, and with it every shell — when peer sync
//     needs three endpoints;
//   - there is no revoking it short of changing the password, which logs out
//     every browser and every other peer at once.
//
// A device-pairing session (gwauth's pair.go) fixes the first two for a phone,
// but not for a peer: a session dies with the process and after the session
// TTL, and a peer that silently loses its credential every day is worse than
// one holding the password. So a peer gets a grant of its own:
//
//	B: catctl pair peer ──▶ pairing token (5 min, single use, kind=peer)
//	                                   │  pasted into A as a cats://peer link
//	A: catctl attach-peer home cats://peer?u=…&t=…
//	      │
//	      └─ A's catway ── POST /peer/v1/pair {token} ──▶ B
//	                                                     │ RedeemPeerPairToken
//	                                                     │ Store.Mint
//	          token_file (0600) ◀── {credential} ────────┘
//
// The grant is long-lived (it survives restarts of either side), scoped (B
// accepts it on /peer/v1/* and nowhere else) and individually revocable
// (`catctl revoke-peer-grant <id>` on B). B stores only a SHA-256 of the
// credential, so its state directory holds nothing that authenticates anyone.
//
// # Storage
//
// btypedb, keyed by grant id. The dataset is a handful of rows, so all of it is
// also mirrored in an in-memory hash → id map: the auth check runs on every
// peer request, and it should be a map lookup, not a scan.
package peergrant

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/rohanthewiz/btypedb"
)

const (
	// Prefix marks a peer credential. It lets the auth guard skip the store
	// entirely for the password and session tokens that make up nearly all
	// traffic, and it makes a leaked credential recognisable to a secret
	// scanner or a human reading a file — "what is this string?" has an answer.
	Prefix = "catspeer_"

	// credentialBytes is the entropy behind the prefix: 32 bytes, 43 base64url
	// characters. More than a session or pairing token carries, because this
	// one is long-lived — there is no expiry to cap how long a guess has.
	credentialBytes = 32

	// idBytes sizes the public grant id — 4 bytes, 8 hex characters. It is a
	// handle for `revoke-peer-grant`, not a secret, so it only has to be short
	// enough to type and unique among a handful of rows (Mint retries on the
	// astronomically unlikely collision).
	idBytes = 4

	// maxLabelLen caps a label. Labels come from the redeeming peer (its
	// hostname) as well as from the operator, so they are untrusted text bound
	// for a terminal table.
	maxLabelLen = 64

	// lastUsedGranularity is how stale the persisted LastUsed may get. A sync
	// is three or four requests in a burst; writing the log on each would
	// grow it for no reader's benefit. The in-memory value is always current,
	// and listing reads that.
	lastUsedGranularity = time.Hour
)

// ErrNotFound reports a revoke of an id with no grant.
var ErrNotFound = errors.New("no such peer grant")

// Grant is one issued credential, minus the credential. Hash is the hex
// SHA-256 of it; nothing that could authenticate is ever stored.
//
// Label is what the grantor's operator typed (`catctl pair peer <label>`), and
// Peer is what the redeeming catway called itself. They are kept apart
// because they are trusted differently: the first came from the owner of this
// machine, the second from whoever held the pairing token.
type Grant struct {
	ID       string    `json:"id"`
	Label    string    `json:"label,omitempty"`
	Peer     string    `json:"peer,omitempty"`
	Hash     string    `json:"hash"`
	Created  time.Time `json:"created"`
	LastUsed time.Time `json:"last_used,omitzero"`
}

// Store is the grant table. Safe for concurrent use: the auth guard reads it
// from rweb goroutines while the control socket mints and revokes.
type Store struct {
	db *btypedb.DB[string, Grant]

	// mu guards the in-memory mirror. btypedb is itself safe for concurrent
	// use; the mirror is not, and the two are updated together so a revoke
	// cannot leave a hash that still authenticates.
	mu     sync.Mutex
	byHash map[string]*Grant // hash → the live row (mirror of db)
	byID   map[string]*Grant // id → the same row
}

// Open opens (or creates) the store at path and loads it into memory.
//
// SyncAlways (the btypedb default) rather than the ledger's SyncEverySecond:
// writes here are rare — a mint, a revoke, an hourly last-used — and each is a
// security decision. A revoke lost to a power cut would resurrect a credential
// the operator believes is dead.
func Open(path string) (*Store, error) {
	db, err := btypedb.Open(path, btypedb.StringCodec, btypedb.JSONCodec[Grant]())
	if err != nil {
		return nil, fmt.Errorf("peer grants %s: %w", path, err)
	}
	s := &Store{db: db, byHash: map[string]*Grant{}, byID: map[string]*Grant{}}
	for id, g := range db.All() {
		s.byID[id] = &g
		s.byHash[g.Hash] = &g
	}
	return s, nil
}

// Close flushes and closes the store. Safe on nil and idempotent.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Mint issues a new credential, persisting its hash under a fresh id. The
// credential is returned exactly once — here — and is unrecoverable after.
func (s *Store) Mint(label, peer string, now time.Time) (credential string, g Grant, err error) {
	raw := make([]byte, credentialBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", Grant{}, fmt.Errorf("peer grant: generate credential: %w", err)
	}
	credential = Prefix + base64.RawURLEncoding.EncodeToString(raw)

	s.mu.Lock()
	defer s.mu.Unlock()
	id, err := s.freshIDLocked()
	if err != nil {
		return "", Grant{}, err
	}
	g = Grant{
		ID:      id,
		Label:   cleanLabel(label),
		Peer:    cleanLabel(peer),
		Hash:    hashOf(credential),
		Created: now.UTC(),
	}
	// Persist first, mirror second: a grant that is in memory but not on disk
	// would work until the next restart and then vanish, which reads as a
	// flaky peer rather than a failed mint.
	if err := s.db.Set(id, g); err != nil {
		return "", Grant{}, fmt.Errorf("peer grant: store: %w", err)
	}
	row := g
	s.byID[id] = &row
	s.byHash[row.Hash] = &row
	return credential, g, nil
}

// Check reports whether credential is a live grant, returning it. A string
// without the Prefix is rejected before any hashing, so the guard can call
// this unconditionally.
//
// The lookup is a map probe keyed by SHA-256 of caller input. That is safe
// where a probe keyed by the input itself would not be: timing can at most
// reveal something about the hash of a string the attacker chose, and a
// preimage-resistant hash turns that into nothing about any stored secret.
func (s *Store) Check(credential string, now time.Time) (Grant, bool) {
	if s == nil || !strings.HasPrefix(credential, Prefix) {
		return Grant{}, false
	}
	h := hashOf(credential)

	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.byHash[h]
	if !ok {
		return Grant{}, false
	}
	persist := now.Sub(row.LastUsed) >= lastUsedGranularity
	row.LastUsed = now.UTC()
	if persist {
		// Best effort: a failed write costs an out-of-date "last used" column,
		// and failing the request over it would turn a disk hiccup into an
		// outage of peer sync.
		_ = s.db.Set(row.ID, *row)
	}
	return *row, true
}

// List returns every grant, oldest first.
func (s *Store) List() []Grant {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	out := make([]Grant, 0, len(s.byID))
	for _, g := range s.byID {
		out = append(out, *g)
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Created.Equal(out[j].Created) {
			return out[i].Created.Before(out[j].Created)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Revoke deletes the grant with the given id. It takes effect on the next
// request: the mirror entry goes in the same critical section as the disk
// delete, and Check consults nothing else.
//
// Disk first, as in Mint, but for the opposite reason: a revoke that reached
// memory and not the disk would quietly come back on the next restart — the
// failure an operator would be least likely to notice.
func (s *Store) Revoke(id string) (Grant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.byID[id]
	if !ok {
		return Grant{}, fmt.Errorf("%w %q", ErrNotFound, id)
	}
	if _, err := s.db.Delete(id); err != nil {
		return Grant{}, fmt.Errorf("peer grant: revoke %s: %w", id, err)
	}
	delete(s.byID, id)
	delete(s.byHash, row.Hash)
	return *row, nil
}

// freshIDLocked draws a random id not already in use.
func (s *Store) freshIDLocked() (string, error) {
	b := make([]byte, idBytes)
	for range 8 {
		if _, err := rand.Read(b); err != nil {
			return "", fmt.Errorf("peer grant: generate id: %w", err)
		}
		id := hex.EncodeToString(b)
		if _, taken := s.byID[id]; !taken {
			return id, nil
		}
	}
	// Eight collisions in a 2^32 space with a handful of rows means the
	// random source is broken, not that the table is full.
	return "", errors.New("peer grant: could not draw an unused id")
}

func hashOf(credential string) string {
	sum := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(sum[:])
}

// cleanLabel makes untrusted text safe for a one-line table cell: control
// characters (an escape sequence in a hostname would repaint the operator's
// terminal) become spaces, runs of space collapse, and the length is capped
// on a rune boundary.
func cleanLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxLabelLen {
		s = string(r[:maxLabelLen])
	}
	return s
}
