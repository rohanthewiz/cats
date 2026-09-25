//go:build ghostty

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rohanthewiz/rweb"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/config"
	"github.com/rohanthewiz/cats/internal/ctlproto"
	"github.com/rohanthewiz/cats/internal/gwauth"
	"github.com/rohanthewiz/cats/internal/peergrant"
	"github.com/rohanthewiz/cats/internal/peersync"
)

// grantorFixture is a catway's grantor half wired the way main wires it: one
// authenticator shared by the guard and the pairing context, one grant store
// shared by the guard (checking) and the control handlers (minting, listing,
// revoking), and an rweb server with the real middleware in front of the real
// peer routes plus stand-ins for / and /ws.
type grantorFixture struct {
	o      *orch
	guard  *authGuard
	store  *peergrant.Store
	server *rweb.Server
}

func newGrantor(t *testing.T) *grantorFixture {
	t.Helper()
	a, err := gwauth.New("the-real-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	store, err := peergrant.Open(filepath.Join(t.TempDir(), peerGrantsFile))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	guard := &authGuard{a: a, peers: store}
	o := &orch{}
	o.pairing.Store(buildPairing(guard, "127.0.0.1:9431", ""))

	s := rweb.NewServer(rweb.ServerOptions{})
	s.Use(guard.middleware)
	o.registerPeerRoutes(s)
	s.Get("/", func(ctx rweb.Context) error { return ctx.WriteText("app") })
	// A stand-in for the WebSocket route: what matters here is whether the
	// guard lets the request reach a handler at all.
	s.Get("/ws", func(ctx rweb.Context) error { return ctx.WriteText("terminals") })
	return &grantorFixture{o: o, guard: guard, store: store, server: s}
}

// mint runs `catctl pair` (peer or device) against the fixture's control
// handler and returns the token.
func (f *grantorFixture) mint(t *testing.T, params string) ctlproto.PairInfo {
	t.Helper()
	r := &pairResponder{}
	var raw json.RawMessage
	if params != "" {
		raw = json.RawMessage(params)
	}
	f.o.handlePair(raw, r)
	info, ok := r.data.(ctlproto.PairInfo)
	if r.err != "" || !ok {
		t.Fatalf("handlePair(%s): err=%q data=%T", params, r.err, r.data)
	}
	return info
}

func (f *grantorFixture) redeem(token string) rweb.Response {
	body, _ := json.Marshal(peersync.PairRequest{Token: token, Name: "me@laptop"})
	return f.server.Request(http.MethodPost, peersync.PathPair,
		[]rweb.Header{{Key: "Content-Type", Value: "application/json"}}, bytes.NewReader(body))
}

func (f *grantorFixture) get(path, bearer string) rweb.Response {
	var h []rweb.Header
	if bearer != "" {
		h = append(h, rweb.Header{Key: "Authorization", Value: "Bearer " + bearer})
	}
	return f.server.Request(http.MethodGet, path, h, nil)
}

// The whole grantor flow, and the scoping that makes a grant less than the
// password: a peer token buys one grant; the grant opens /peer/v1/* and
// nothing else; revoking it closes that door at once.
func TestPeerGrantLifecycle(t *testing.T) {
	f := newGrantor(t)

	info := f.mint(t, `{"peer":true,"label":"laptop"}`)
	if info.Kind != ctlproto.PairKindPeer {
		t.Fatalf("peer pairing Kind = %q, want %q", info.Kind, ctlproto.PairKindPeer)
	}

	// A peer token is not a device token: /login will not take it, and trying
	// must not burn it.
	if f.guard.authorizeLogin(info.Token, time.Now()).ok {
		t.Fatal("a peer pairing token bought a full session at /login")
	}

	resp := f.redeem(info.Token)
	if resp.Status() != http.StatusOK {
		t.Fatalf("redeem: %d %s", resp.Status(), resp.Body())
	}
	var got peersync.PairResponse
	if err := json.Unmarshal(resp.Body(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Credential == "" || got.GrantID == "" {
		t.Fatalf("redeem answered %+v", got)
	}
	if replay := f.redeem(info.Token); replay.Status() != http.StatusUnauthorized {
		t.Fatalf("replayed pairing token: %d, want 401", replay.Status())
	}

	// Scope: the grant reaches the peer routes...
	if r := f.get(peersync.PathHello, got.Credential); r.Status() != http.StatusOK {
		t.Fatalf("hello with a grant: %d %s", r.Status(), r.Body())
	}
	// ...and nothing else. /ws is the one that matters: it is every shell.
	if r := f.get("/ws", got.Credential); r.Status() == http.StatusOK {
		t.Fatal("a peer grant opened /ws")
	}
	if r := f.get("/", got.Credential); r.Status() == http.StatusOK {
		t.Fatal("a peer grant opened the app page")
	}
	// The password still does everything, as before grants existed.
	if r := f.get("/ws", "the-real-password"); r.Status() != http.StatusOK {
		t.Fatalf("password on /ws: %d", r.Status())
	}

	// The listing names the grant by the operator's label and the peer's
	// self-description, and reports the use just made.
	lr := &pairResponder{}
	f.o.handlePeerGrants(lr)
	list, _ := lr.data.(ctlproto.PeerGrantList)
	if len(list.Grants) != 1 || list.Grants[0].ID != got.GrantID ||
		list.Grants[0].Label != "laptop" || list.Grants[0].Peer != "me@laptop" || list.Grants[0].LastUsed == 0 {
		t.Fatalf("peer.grants = %+v (err %q)", list, lr.err)
	}

	rr := &pairResponder{}
	f.o.handlePeerRevoke(json.RawMessage(`{"id":"`+got.GrantID+`"}`), rr)
	if rr.err != "" {
		t.Fatalf("peer.revoke: %s", rr.err)
	}
	if r := f.get(peersync.PathHello, got.Credential); r.Status() == http.StatusOK {
		t.Fatal("a revoked grant still reaches the peer routes")
	}
	rr = &pairResponder{}
	f.o.handlePeerRevoke(json.RawMessage(`{"id":"`+got.GrantID+`"}`), rr)
	if rr.err == "" {
		t.Fatal("revoking a revoked grant succeeded")
	}
}

// A device token presented at the peer door is refused and left live.
func TestPeerPairRefusesDeviceToken(t *testing.T) {
	f := newGrantor(t)
	info := f.mint(t, "")
	if info.Kind != "" {
		t.Fatalf("device pairing Kind = %q, want empty", info.Kind)
	}
	if r := f.redeem(info.Token); r.Status() != http.StatusUnauthorized {
		t.Fatalf("device token at /peer/v1/pair: %d, want 401", r.Status())
	}
	if !f.guard.authorizeLogin(info.Token, time.Now()).ok {
		t.Fatal("the wrong-door attempt burned the device token")
	}
}

// Without a grant table (no state directory) the peer kind is refused at
// mint time — before the operator carries a doomed token to the other
// machine — while device pairing is unaffected.
func TestPeerPairWithoutStore(t *testing.T) {
	a, _ := gwauth.New("pw", time.Hour)
	o := &orch{}
	o.pairing.Store(buildPairing(&authGuard{a: a}, "127.0.0.1:9431", ""))

	r := &pairResponder{}
	o.handlePair(json.RawMessage(`{"peer":true}`), r)
	if r.err == "" {
		t.Fatal("peer pairing succeeded with no grant table")
	}
	r = &pairResponder{}
	o.handlePair(nil, r)
	if r.err != "" {
		t.Fatalf("device pairing failed with no grant table: %s", r.err)
	}
	r = &pairResponder{}
	o.handlePeerGrants(r)
	if r.err == "" {
		t.Fatal("peer.grants succeeded with no grant table")
	}
}

// Re-pairing an attached peer replaces its credential in place: the literal
// token of a password-era entry is cleared (two credentials is a config
// error), the old label is kept unless a new one is given, and the entry keeps
// its position in the roster.
func TestWithPairedPeer(t *testing.T) {
	cfg := config.Config{Peers: []config.Peer{
		{ID: "a", URL: "https://a:8421"},
		{ID: "home", Label: "Home mini", URL: "https://old:8421", Token: "pw"},
		{ID: "z", URL: "https://z:8421"},
	}}
	got := withPairedPeer(cfg, app.PeerAttachParams{ID: "home", Fingerprint: "ff"}, "https://new:8421", "/s/peer-tokens/home.token")
	want := config.Peer{ID: "home", Label: "Home mini", URL: "https://new:8421", TokenFile: "/s/peer-tokens/home.token", Fingerprint: "ff"}
	if len(got.Peers) != 3 || got.Peers[1] != want {
		t.Fatalf("re-pair = %+v, want %+v at index 1", got.Peers, want)
	}
	if cfg.Peers[1].Token != "pw" {
		t.Fatal("withPairedPeer mutated its input roster")
	}

	got = withPairedPeer(cfg, app.PeerAttachParams{ID: "new", Label: "New"}, "https://n:8421", "/s/peer-tokens/new.token")
	if len(got.Peers) != 4 || got.Peers[3].ID != "new" || got.Peers[3].Label != "New" {
		t.Fatalf("attach = %+v", got.Peers)
	}
}

// Detach deletes the token file only when this catway wrote it.
func TestRemoveManagedTokenFile(t *testing.T) {
	state := t.TempDir()
	o := &orch{stateDir: state}

	managed := filepath.Join(state, peerTokensDir, "home.token")
	if err := writeTokenFile(managed, "catspeer_x"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(managed); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}
	mine := filepath.Join(t.TempDir(), "home.password")
	if err := os.WriteFile(mine, []byte("pw"), 0o600); err != nil {
		t.Fatal(err)
	}

	if !o.removeManagedTokenFile(config.Peer{ID: "home", TokenFile: managed}) {
		t.Error("a managed token file was not reported as managed")
	}
	if _, err := os.Stat(managed); !os.IsNotExist(err) {
		t.Error("the managed token file survived detach")
	}
	if o.removeManagedTokenFile(config.Peer{ID: "home", TokenFile: mine}) {
		t.Error("an operator-owned token file was reported as managed")
	}
	if _, err := os.Stat(mine); err != nil {
		t.Error("detach deleted a token file the operator owns")
	}
}
