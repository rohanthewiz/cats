//go:build ghostty

package main

import (
	"encoding/json"
	"log"
	"net"
	"strconv"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/ctlproto"
	"github.com/rohanthewiz/cats/internal/dlog"
	"github.com/rohanthewiz/cats/internal/gwauth"
	"github.com/rohanthewiz/cats/internal/gwtls"
	"github.com/rohanthewiz/cats/internal/peergrant"
)

// Device pairing (`catctl pair`). The control socket mints a short-lived
// single-use grant; the device redeems it at /login for an ordinary session
// credential. gwauth's pair.go holds the reasoning for why this is not simply a
// method that hands back the password.
//
// The method is answered here rather than through app.Dispatcher, and that
// placement is the security boundary, not a convenience:
//
//	browser  ──ws cmd──▶ app.Dispatcher ──▶ §7 command table   ← no pairing here
//	catctl   ──unix───▶ controlDispatch ──┬─▶ app.Dispatcher
//	                                      └─▶ handlePair       ← owner-only socket
//
// A §7 command is reachable from both front ends by construction. Minting
// credentials from the browser would turn a stolen session cookie — which
// expires, and dies with the process — into a permanent second credential.

// pairing is everything handlePair needs, assembled by main once the auth guard
// and the TLS certificate are both resolved.
//
// It hangs off orch behind an atomic pointer because the control socket is
// already accepting connections by the time main can fill it in (the guard needs
// the resolved TLS state, which comes later in startup). That is the same shape
// as o.page: written once by main, read from a server goroutine. A pair request
// arriving in the gap — microseconds, and only from a process racing catway's
// own startup — gets a clear error rather than a torn read.
type pairing struct {
	auth *gwauth.Authenticator
	// baseURL is what a device on another machine should dial, e.g.
	// "https://192.168.1.24:8421". Never a loopback address: a phone dialling
	// localhost reaches itself.
	baseURL string
	// fingerprint is the hex SHA-256 of the served certificate's DER, "" when
	// serving plain HTTP.
	fingerprint string
	// grants is the durable peer-grant table (peergrants.go), nil when the
	// state directory could not hold it — device pairing still works then;
	// only `catctl pair peer` is refused.
	grants *peergrant.Store
}

// handlePair mints a pairing grant and answers the control request with it.
//
// It runs on the ctlproto connection goroutine and deliberately does not post
// onto the orchestrator loop: pairing touches no session state, and routing it
// through the loop would put credential minting behind whatever the loop is
// currently blocked on.
//
// params selects the grant's kind (ctlproto.PairParams): absent or zero is a
// device grant, as it always was; Peer mints a token that only another catway
// can redeem, for a durable peer-sync grant (peergrants.go).
func (o *orch) handlePair(params json.RawMessage, r app.Responder) {
	var pp ctlproto.PairParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &pp); err != nil {
			r.Fail("pair: " + err.Error())
			return
		}
	}
	p := o.pairing.Load()
	if p == nil {
		// Two very different causes, one message each would be better, but the
		// caller cannot act differently on them: either auth is off (nothing to
		// pair with) or catway has not finished starting.
		r.Fail("pairing unavailable: the server is starting, or auth is disabled (--auth none)")
		return
	}
	var (
		token   string
		expires time.Time
		err     error
		kind    string
	)
	if pp.Peer {
		// Refused up front rather than at redemption: a token that can only
		// fail when the other machine spends it would waste the operator's
		// trip there.
		if p.grants == nil {
			r.Fail(peerGrantsUnavailable)
			return
		}
		token, expires, err = p.auth.IssuePeerPairToken(pp.Label, time.Now())
		kind = ctlproto.PairKindPeer
	} else {
		token, expires, err = p.auth.IssuePairToken(time.Now())
	}
	if err != nil {
		r.Fail("mint pairing token: " + err.Error())
		return
	}
	what := "a pairing token"
	if pp.Peer {
		what = "a peer pairing token"
	}
	log.Printf("catway: issued %s for %s (valid %s, single use)", what, p.baseURL, gwauth.PairTTL)
	r.OK(ctlproto.PairInfo{
		URL:         p.baseURL,
		Token:       token,
		ExpiresAt:   expires.Unix(),
		Fingerprint: p.fingerprint,
		Kind:        kind,
	})
}

// buildPairing assembles the pairing context, or returns nil when there is
// nothing to pair with. certPath is "" when not serving TLS.
//
// A fingerprint that cannot be read is logged and dropped rather than failing:
// pairing without a pin still works (the device falls back to trust-on-first-use
// against the cert it is handed), and refusing to pair over an unreadable file
// would be a worse trade than pairing with one less assurance.
func buildPairing(guard *authGuard, addr, certPath string) *pairing {
	if guard == nil {
		return nil // --auth none: every client is already authorised
	}
	scheme := "http"
	fingerprint := ""
	if certPath != "" {
		scheme = "https"
		fp, err := gwtls.Fingerprint(certPath)
		if err != nil {
			dlog.Warnf("catway: pairing will not carry a certificate pin: %v", err)
		} else {
			fingerprint = fp
		}
	}
	return &pairing{auth: guard.a, baseURL: advertiseURL(scheme, addr), fingerprint: fingerprint, grants: guard.peers}
}

// advertiseURL turns the listen address into a URL another device can dial.
//
// The listen address is usually ":8421" — a wildcard bind with no host at all —
// and where it does name a host it is as likely to be "localhost" or "0.0.0.0"
// as anything routable. None of those help a phone, so an unusable host is
// replaced by this machine's LAN address. Getting this wrong is not subtle: the
// pairing simply never connects.
func advertiseURL(scheme, addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port = "", strconv.Itoa(defaultPort(scheme))
	}
	if !routableHost(host) {
		if lan := lanAddress(); lan != "" {
			host = lan
		} else if host == "" {
			// No interface address to advertise (offline, or every interface is
			// loopback). localhost at least produces a URL that works from this
			// machine, which is where `catctl pair --json` scripting runs.
			host = "localhost"
		}
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

func defaultPort(scheme string) int {
	if scheme == "https" {
		return 443
	}
	return 80
}

// routableHost reports whether a host from the listen address is worth putting
// in front of another device. The wildcard forms and loopback are not.
func routableHost(host string) bool {
	switch host {
	case "", "0.0.0.0", "::", "[::]", "localhost":
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !ip.IsLoopback() && !ip.IsUnspecified()
	}
	return true // a real DNS name the operator chose to bind
}

// lanAddress picks this machine's most likely reachable address: the first
// non-loopback, non-link-local IPv4 on an interface that is up.
//
// IPv4 is preferred over IPv6 not on principle but because a phone joining the
// same Wi-Fi reaches an RFC1918 address reliably, while IPv6 on a home LAN is
// frequently either absent or a temporary privacy address that rotates. When
// only IPv6 exists it is used — an address that might rotate beats none.
func lanAddress() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	var fallback string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP.IsLoopback() || ipNet.IP.IsLinkLocalUnicast() {
				continue
			}
			if v4 := ipNet.IP.To4(); v4 != nil {
				return v4.String()
			}
			if fallback == "" {
				fallback = ipNet.IP.String()
			}
		}
	}
	return fallback
}
