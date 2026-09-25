//go:build ghostty

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rohanthewiz/rweb"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/config"
	"github.com/rohanthewiz/cats/internal/ctlproto"
	"github.com/rohanthewiz/cats/internal/dlog"
	"github.com/rohanthewiz/cats/internal/peergrant"
	"github.com/rohanthewiz/cats/internal/peersync"
)

// Peer grants: pairing for peer sync (N-011). internal/peergrant carries the
// full argument; this file is the catway's two halves of it.
//
//	GRANTOR (B)                                    REDEEMER (A)
//	catctl pair peer ─socket─▶ handlePair
//	    │ IssuePeerPairToken (5 min, single use)
//	    ▼
//	cats://peer?u=…&t=…&f=…  ── pasted by a human ──▶ catctl attach-peer <id> <link>
//	                                                   │ peer.attach {pair_token}
//	                                                   ▼
//	handlePeerPair ◀──── POST /peer/v1/pair ──────── attachPeerByPairing (goroutine)
//	    │ RedeemPeerPairToken                          │
//	    │ grants.Mint ─▶ peer-grants.db (hash only)    │
//	    └──── {credential, grant_id} ────────────────▶ │ write <state>/peer-tokens/<id>.token (0600)
//	                                                   │ post ▶ loop: peers: entry, token_file, save
//	auth guard: catspeer_… accepted on /peer/v1/* only
//	catctl peer-grants / revoke-peer-grant <id> ─socket─▶ handlePeerGrants / handlePeerRevoke

// peerGrantsFile and peerTokensDir are where the two halves keep their state,
// both under the state directory rather than beside the config file. The
// config directory is the one people keep in a dotfiles repo; a credential
// placed next to config.yaml is one `git add .` from being published, which is
// the exact leak config.Peer's comment warns about for a literal token.
const (
	peerGrantsFile = "peer-grants.db"
	peerTokensDir  = "peer-tokens"
)

// openPeerGrants opens the grantor's store, or returns nil (peer pairing
// unavailable, logged) when there is no state directory or the file will not
// open. Never fatal: a catway that could not open its grant table still serves
// terminals, still syncs with password-holding peers, and says why `catctl
// pair peer` is refused.
func openPeerGrants(stateDir string) *peergrant.Store {
	if stateDir == "" {
		dlog.Warnf("catway: peer pairing disabled — no resolvable state directory for %s", peerGrantsFile)
		return nil
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		dlog.Warnf("catway: peer pairing disabled (%v)", err)
		return nil
	}
	path := filepath.Join(stateDir, peerGrantsFile)
	s, err := peergrant.Open(path)
	if err != nil {
		dlog.Warnf("catway: peer pairing disabled (%v)", err)
		return nil
	}
	if n := len(s.List()); n > 0 {
		log.Printf("catway: %d peer grant(s) in %s", n, path)
	}
	return s
}

// --- grantor: control socket ----------------------------------------------------

// peerGrantsUnavailable is the one refusal every grant operation shares. The
// causes are listed rather than guessed at because the control socket cannot
// tell them apart after the fact, and each has a different fix.
const peerGrantsUnavailable = "peer grants unavailable: auth is disabled (--auth none), the server is starting, or the state directory could not hold " + peerGrantsFile

// grants returns the live grant store, or nil. Any goroutine.
func (o *orch) grants() *peergrant.Store {
	if p := o.pairing.Load(); p != nil {
		return p.grants
	}
	return nil
}

// handlePeerGrants answers peer.grants. Like handlePair it runs on the
// ctlproto connection goroutine: the store is safe for concurrent use and has
// nothing to do with the session the loop owns.
func (o *orch) handlePeerGrants(r app.Responder) {
	s := o.grants()
	if s == nil {
		r.Fail(peerGrantsUnavailable)
		return
	}
	list := ctlproto.PeerGrantList{Grants: []ctlproto.PeerGrant{}}
	for _, g := range s.List() {
		list.Grants = append(list.Grants, toWireGrant(g))
	}
	r.OK(list)
}

// handlePeerRevoke answers peer.revoke. The revocation is effective before
// this returns — the next request carrying that credential is refused — and
// is logged, because "who cut my laptop off?" is a question the log should be
// able to answer.
func (o *orch) handlePeerRevoke(params json.RawMessage, r app.Responder) {
	s := o.grants()
	if s == nil {
		r.Fail(peerGrantsUnavailable)
		return
	}
	var p ctlproto.PeerRevokeParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			r.Fail("peer.revoke: " + err.Error())
			return
		}
	}
	if p.ID == "" {
		r.Fail("peer.revoke: id is required (catctl peer-grants lists them)")
		return
	}
	g, err := s.Revoke(p.ID)
	if err != nil {
		r.Fail(err.Error())
		return
	}
	log.Printf("catway: revoked peer grant %s (%s)", g.ID, grantWho(g))
	r.OK(toWireGrant(g))
}

func toWireGrant(g peergrant.Grant) ctlproto.PeerGrant {
	w := ctlproto.PeerGrant{ID: g.ID, Label: g.Label, Peer: g.Peer, Created: g.Created.Unix()}
	if !g.LastUsed.IsZero() {
		w.LastUsed = g.LastUsed.Unix()
	}
	return w
}

// grantWho names a grant for a log line: the operator's label first, since
// that is the name they will recognise.
func grantWho(g peergrant.Grant) string {
	switch {
	case g.Label != "" && g.Peer != "":
		return g.Label + ", held by " + g.Peer
	case g.Label != "":
		return g.Label
	case g.Peer != "":
		return g.Peer
	}
	return "unlabelled"
}

// --- grantor: HTTP ---------------------------------------------------------------

// handlePeerPair redeems a peer pairing token for a peer grant (POST
// /peer/v1/pair). It is the one public route under /peer/v1 — the auth guard
// lets it through — and the pairing token in the body is its credential.
//
// Order of operations: the token is spent before the grant is minted. The
// reverse would leave a window in which two concurrent requests with the same
// token both mint; spending first makes the single-use property the store's
// problem (one mutex) rather than this handler's. A mint that then fails costs
// the operator a re-run of `catctl pair peer`, which is the cheaper failure.
func (o *orch) handlePeerPair(ctx rweb.Context) error {
	p := o.pairing.Load()
	if p == nil {
		// Under --auth none there is no guard, so no pairing — and nothing to
		// pair for: the peer routes are open. Say so, since the fix is on the
		// other end (attach with no credential).
		return ctx.Status(http.StatusServiceUnavailable).
			WriteText("peer pairing unavailable: this catway runs without auth, or is still starting — attach it with no token")
	}
	if p.grants == nil {
		return ctx.Status(http.StatusServiceUnavailable).WriteText(peerGrantsUnavailable)
	}
	body := ctx.Request().Body()
	if len(body) > peersync.MaxPairBodyBytes {
		return ctx.Status(http.StatusRequestEntityTooLarge).WriteText("pair request too large")
	}
	var req peersync.PairRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return ctx.Status(http.StatusBadRequest).WriteText("pair request: " + err.Error())
	}
	now := time.Now()
	label, ok := p.auth.RedeemPeerPairToken(req.Token, now)
	if !ok {
		// Deliberately one answer for unknown, expired, used and wrong-kind:
		// distinguishing them would tell a guesser which tokens exist.
		return ctx.Status(http.StatusUnauthorized).WriteText("invalid pairing token")
	}
	cred, g, err := p.grants.Mint(label, req.Name, now)
	if err != nil {
		dlog.Errorf("catway: peer pairing: %v", err)
		return ctx.Status(http.StatusInternalServerError).WriteText("could not mint a peer grant; see the catway log")
	}
	log.Printf("catway: issued peer grant %s (%s)", g.ID, grantWho(g))
	return ctx.WriteJSON(peersync.PairResponse{Credential: cred, GrantID: g.ID, Instance: peerLocal{o}.Instance()})
}

// --- redeemer ----------------------------------------------------------------------

// attachPeerByPairing is peer.attach with a pair_token: redeem it at the peer,
// store the grant in a token file, then add (or re-pair) the peers: entry.
// Loop goroutine on entry; the network round trip runs on its own goroutine
// and the config write hops back onto the loop, the StartPeerSync shape.
//
// Everything that can be checked locally is checked BEFORE the round trip,
// because the round trip spends the token: an id with a bad shape discovered
// afterwards would cost the operator a trip back to the other machine.
func (o *orch) attachPeerByPairing(r app.Responder, p app.PeerAttachParams) {
	if p.Token != "" || p.TokenFile != "" {
		r.Fail("peer.attach: pair_token replaces token and token_file — pass only one credential")
		return
	}
	if o.stateDir == "" {
		r.Fail("peer.attach: no state directory to keep the peer credential in")
		return
	}
	peerURL := strings.TrimRight(p.URL, "/")
	tokenPath := filepath.Join(o.stateDir, peerTokensDir, p.ID+".token")
	// Dry-run the config change with the final values. saveConfig will run
	// the same Validate again afterwards; this copy exists only to fail early.
	if err := withPairedPeer(o.cfg, p, peerURL, tokenPath).Validate(); err != nil {
		r.Fail(err.Error())
		return
	}
	client, err := peersync.NewClient(peerURL, "", p.Fingerprint)
	if err != nil {
		r.Fail("peer.attach: " + err.Error())
		return
	}
	self := peerLocal{o}.Instance().Name()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		res, err := client.Redeem(ctx, p.PairToken, self)
		if err != nil {
			o.post(func() { r.Fail("peer.attach: " + err.Error()) })
			return
		}
		// The credential exists nowhere else — the peer kept only its hash —
		// so it goes to disk before anything else can fail.
		if err := writeTokenFile(tokenPath, res.Credential); err != nil {
			o.post(func() {
				r.Fail(fmt.Sprintf("peer.attach: paired, but could not store the credential (%v); revoke grant %s on the peer and pair again", err, res.GrantID))
			})
			return
		}
		o.post(func() {
			_, existed := o.findPeer(p.ID)
			if msg := o.saveConfig(withPairedPeer(o.cfg, p, peerURL, tokenPath)); msg != "" {
				if !existed {
					_ = os.Remove(tokenPath) // nothing refers to it; do not leave a live credential lying about
				}
				r.Fail(fmt.Sprintf("%s (grant %s on the peer is now unused; revoke it there)", msg, res.GrantID))
				return
			}
			verb := "attached"
			if existed {
				verb = "re-paired"
			}
			log.Printf("catway: peer.attach %s %s via pairing with %s (%s), grant %s",
				verb, p.ID, res.Instance.Name(), peerURL, res.GrantID)
			r.OK(app.PeerListResult{Peers: o.peerInfos()})
		})
	}()
}

// withPairedPeer returns cfg with the peers: entry for p added, or — when the
// id is already attached — replaced in place, keeping its position (and its
// label, unless a new one was given). Re-pairing is how a peer recovers from a
// revoked grant, so it must not demand a detach first.
//
// The credential is always the token file: a literal token left over from a
// password-era entry is cleared, since two credentials is a config error.
func withPairedPeer(cfg config.Config, p app.PeerAttachParams, peerURL, tokenPath string) config.Config {
	entry := config.Peer{ID: p.ID, Label: p.Label, URL: peerURL, TokenFile: tokenPath, Fingerprint: p.Fingerprint}
	peers := slices.Clone(cfg.Peers)
	if i := slices.IndexFunc(peers, func(q config.Peer) bool { return q.ID == p.ID }); i >= 0 {
		if entry.Label == "" {
			entry.Label = peers[i].Label
		}
		peers[i] = entry
	} else {
		peers = append(peers, entry)
	}
	cfg.Peers = peers
	return cfg
}

// writeTokenFile stores a credential owner-only, atomically: written beside
// the target and renamed over it, so a crash never leaves a half-written file
// that the next sync would present as a (wrong) credential. The directory is
// 0700 because the file name itself says which peers this machine can reach.
func writeTokenFile(path, credential string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(credential + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// removeManagedTokenFile deletes a peer's token file when — and only when —
// this catway wrote it (it lives in <state>/peer-tokens). A token_file the
// operator pointed at by hand is theirs; deleting it on detach would destroy
// a password file some other tool may still read. Reports whether the peer
// was a paired one (its token file was managed here), whatever became of the
// file.
func (o *orch) removeManagedTokenFile(p config.Peer) bool {
	if o.stateDir == "" || p.TokenFile == "" {
		return false
	}
	managed := filepath.Join(o.stateDir, peerTokensDir) + string(filepath.Separator)
	if !strings.HasPrefix(filepath.Clean(p.TokenFile), managed) {
		return false
	}
	if err := os.Remove(p.TokenFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		dlog.Warnf("catway: peer.detach %s: remove %s: %v", p.ID, p.TokenFile, err)
	}
	return true
}
