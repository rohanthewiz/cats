//go:build ghostty

package main

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/ctlproto"
	"github.com/rohanthewiz/cats/internal/gwauth"
)

// pairResponder records what handlePair resolves with, standing in for the
// control connection's chanResponder.
type pairResponder struct {
	data any
	err  string
	done bool
}

func (r *pairResponder) WantsReply() bool { return true }
func (r *pairResponder) OK(data any)      { r.data, r.done = data, true }
func (r *pairResponder) Fail(msg string)  { r.err, r.done = msg, true }

func newPairOrch(t *testing.T, secret string) (*orch, *authGuard) {
	t.Helper()
	a, err := gwauth.New(secret, time.Hour)
	if err != nil {
		t.Fatalf("gwauth.New: %v", err)
	}
	guard := &authGuard{a: a}
	o := &orch{}
	o.pairing.Store(&pairing{auth: a, baseURL: "https://192.168.1.24:8421", fingerprint: "abc123"})
	return o, guard
}

// The end-to-end claim of the whole feature: a grant minted over the control
// socket buys exactly one session, and it is not the password.
func TestPairGrantRedeemsOnceAtLogin(t *testing.T) {
	const secret = "the-real-password"
	o, guard := newPairOrch(t, secret)

	r := &pairResponder{}
	o.handlePair(r)
	if !r.done || r.err != "" {
		t.Fatalf("handlePair failed: %q", r.err)
	}
	info, ok := r.data.(ctlproto.PairInfo)
	if !ok {
		t.Fatalf("handlePair resolved with %T, want ctlproto.PairInfo", r.data)
	}
	if info.Token == "" || info.Token == secret {
		t.Fatalf("token %q is empty or is the shared secret", info.Token)
	}
	if info.URL != "https://192.168.1.24:8421" || info.Fingerprint != "abc123" {
		t.Fatalf("unexpected pairing payload: %+v", info)
	}
	if d := time.Until(time.Unix(info.ExpiresAt, 0)); d <= 0 || d > gwauth.PairTTL {
		t.Fatalf("expiry %v is not within (0, %v]", d, gwauth.PairTTL)
	}

	// First redemption: a session, and it is not the shared secret.
	now := time.Now()
	out := guard.authorizeLogin(info.Token, now)
	if !out.ok {
		t.Fatal("redeeming the grant did not grant a session")
	}
	if !guard.a.ValidSession(out.session, now) {
		t.Fatal("the session handed to the device does not validate")
	}
	if out.session == secret {
		t.Fatal("pairing handed out the shared secret")
	}
	if !out.expires.After(now) {
		t.Fatalf("session expires at %v, which is not in the future", out.expires)
	}

	// The device presents that session in the Authorization header, having no
	// cookie jar — the path authed() gained for pairing.
	if !guard.a.ValidSession(mustBearer(t, "Bearer "+out.session), now) {
		t.Fatal("the session does not validate when presented as a bearer token")
	}

	// Second redemption: the replay this design exists to defeat.
	if guard.authorizeLogin(info.Token, now).ok {
		t.Fatal("replaying the grant granted a second session")
	}
}

// The password must still work, and using it must not burn somebody's
// outstanding grant — that is what the short circuit in handleLoginPost buys.
func TestPasswordLoginDoesNotConsumeAGrant(t *testing.T) {
	const secret = "the-real-password"
	o, guard := newPairOrch(t, secret)

	r := &pairResponder{}
	o.handlePair(r)
	info := r.data.(ctlproto.PairInfo)

	now := time.Now()
	if !guard.authorizeLogin(secret, now).ok {
		t.Fatal("the shared password stopped working")
	}
	if guard.a.PendingPairTokens(now) != 1 {
		t.Fatal("a password login consumed the outstanding pairing grant")
	}
	if !guard.authorizeLogin(info.Token, now).ok {
		t.Fatal("the grant no longer redeems after a password login")
	}
}

// Everything that is not a live credential is refused — including the empty
// string, which is what a login form submitted with no password decodes to.
func TestLoginRejectsNonCredentials(t *testing.T) {
	_, guard := newPairOrch(t, "pw")
	now := time.Now()
	for _, bad := range []string{"", "wrong", "PW", " pw"} {
		if out := guard.authorizeLogin(bad, now); out.ok || out.session != "" {
			t.Errorf("authorizeLogin(%q) granted a session", bad)
		}
	}
}

// An expired grant is refused, so a code left on screen over lunch is worth
// nothing by the time anyone walks past it.
func TestLoginRejectsExpiredGrant(t *testing.T) {
	o, guard := newPairOrch(t, "pw")
	r := &pairResponder{}
	o.handlePair(r)
	info := r.data.(ctlproto.PairInfo)

	late := time.Now().Add(gwauth.PairTTL + time.Second)
	if guard.authorizeLogin(info.Token, late).ok {
		t.Fatal("an expired pairing grant still redeemed")
	}
}

// Only a client that names application/json gets the session in the body; a
// browser's Accept header must keep taking the cookie-and-redirect path.
func TestWantsJSON(t *testing.T) {
	browser := "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,*/*;q=0.8"
	if wantsJSON(browser) {
		t.Error("a browser Accept header was read as a JSON request")
	}
	if wantsJSON("") || wantsJSON("*/*") {
		t.Error("an absent or wildcard Accept header was read as a JSON request")
	}
	if !wantsJSON("application/json") || !wantsJSON("application/json, text/plain, */*") {
		t.Error("an explicit JSON Accept header was not honoured")
	}
}

// The security boundary, asserted directly: a pair request is answered before
// app.Dispatcher, so it never becomes a §7 command the browser front end could
// also reach. If the intercept in controlDispatch were ever removed, the request
// would land on the mailbox instead — which is exactly what this measures.
func TestControlDispatchAnswersPairWithoutTheDispatcher(t *testing.T) {
	o, _ := newPairOrch(t, "pw")
	o.mailbox = make(chan func(), 4)

	r := &pairResponder{}
	o.controlDispatch(ctlproto.MethodPair, nil, r)
	if !r.done || r.err != "" {
		t.Fatalf("pair was not answered inline: done=%v err=%q", r.done, r.err)
	}
	if n := len(o.mailbox); n != 0 {
		t.Fatalf("pair posted %d closure(s) onto the orchestrator loop", n)
	}

	// A §7 command still takes the loop, so the assertion above is measuring the
	// intercept and not an inert mailbox.
	o.controlDispatch("pane.list", nil, &pairResponder{})
	if n := len(o.mailbox); n != 1 {
		t.Fatalf("a §7 command posted %d closures, want 1", n)
	}
}

// An unwired pairing context must fail loudly rather than panic: it is what
// --auth none looks like, and what a request arriving mid-startup looks like.
func TestPairUnavailable(t *testing.T) {
	o := &orch{}
	r := &pairResponder{}
	o.handlePair(r)
	if !r.done || r.err == "" {
		t.Fatalf("handlePair on an unwired orch: err=%q", r.err)
	}
	if r.data != nil {
		t.Fatal("handlePair resolved with data despite failing")
	}
}

// Under --auth none there is no authenticator to mint from, and buildPairing has
// to say so by returning nil rather than by constructing something half-formed.
func TestBuildPairingWithoutAuth(t *testing.T) {
	if p := buildPairing(nil, ":8421", ""); p != nil {
		t.Fatalf("buildPairing with no guard returned %+v", p)
	}
}

// buildPairing over plain HTTP: an http:// URL and no pin, because there is no
// certificate to pin.
func TestBuildPairingPlainHTTP(t *testing.T) {
	a, err := gwauth.New("pw", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	p := buildPairing(&authGuard{a: a}, ":8421", "")
	if p == nil {
		t.Fatal("buildPairing returned nil with a guard")
	}
	if !strings.HasPrefix(p.baseURL, "http://") || strings.HasPrefix(p.baseURL, "https://") {
		t.Errorf("baseURL = %q, want an http:// URL", p.baseURL)
	}
	if p.fingerprint != "" {
		t.Errorf("fingerprint = %q for a plain-HTTP server", p.fingerprint)
	}
}

// The advertised host is the single thing most likely to make pairing fail in
// the field: a phone handed "localhost" or "0.0.0.0" dials itself.
func TestAdvertiseURL(t *testing.T) {
	lan := lanAddress()

	for _, addr := range []string{":8421", "0.0.0.0:8421", "localhost:8421", "127.0.0.1:8421", "[::]:8421"} {
		got := advertiseURL("https", addr)
		host, port, err := net.SplitHostPort(strings.TrimPrefix(got, "https://"))
		if err != nil {
			t.Fatalf("advertiseURL(%q) = %q, which is not scheme://host:port", addr, got)
		}
		if port != "8421" {
			t.Errorf("advertiseURL(%q) lost the port: %q", addr, got)
		}
		if lan != "" && host != lan {
			t.Errorf("advertiseURL(%q) = %q, want the LAN address %q", addr, got, lan)
		}
		if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
			t.Errorf("advertiseURL(%q) advertises %q, which no other device can reach", addr, got)
		}
	}

	// A host the operator bound deliberately is kept, whatever this machine's
	// interfaces say — they chose it for a reason.
	if got := advertiseURL("https", "cats.lan:9000"); got != "https://cats.lan:9000" {
		t.Errorf("advertiseURL kept-host case = %q", got)
	}
	if got := advertiseURL("http", "192.168.5.5:80"); got != "http://192.168.5.5:80" {
		t.Errorf("advertiseURL kept-IP case = %q", got)
	}
}

func TestRoutableHost(t *testing.T) {
	for _, h := range []string{"", "0.0.0.0", "::", "[::]", "localhost", "127.0.0.1", "::1"} {
		if routableHost(h) {
			t.Errorf("routableHost(%q) = true", h)
		}
	}
	for _, h := range []string{"192.168.1.24", "10.0.0.2", "cats.lan", "studio.local", "2001:db8::1"} {
		if !routableHost(h) {
			t.Errorf("routableHost(%q) = false", h)
		}
	}
}

// --- helpers -----------------------------------------------------------------

// mustBearer extracts the credential from an Authorization header the way
// authed() does, failing the test if the header does not parse — the point being
// that a paired device's session survives the round trip through that header.
func mustBearer(t *testing.T, header string) string {
	t.Helper()
	token, ok := gwauth.BearerToken(header)
	if !ok {
		t.Fatalf("BearerToken(%q) did not parse", header)
	}
	return token
}
