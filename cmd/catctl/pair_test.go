package main

import (
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/rohanthewiz/cats/internal/app"
	"github.com/rohanthewiz/cats/internal/ctlproto"
	"github.com/rohanthewiz/cats/internal/qr"
)

func sampleInfo() ctlproto.PairInfo {
	return ctlproto.PairInfo{
		URL:         "https://192.168.1.24:8421",
		Token:       "Xk3dQm9pR2sV7wYbN4tL8cJz",
		ExpiresAt:   time.Unix(1_700_000_300, 0).Unix(),
		Fingerprint: "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08",
	}
}

// The deep link is what the app parses, so every field has to survive the round
// trip through query encoding intact — the URL in particular, which carries the
// "://" and ":" that a naive concatenation would leave ambiguous.
func TestPairURIRoundTrip(t *testing.T) {
	info := sampleInfo()
	uri := pairURI(info)

	if !strings.HasPrefix(uri, pairScheme+"?") {
		t.Fatalf("uri %q does not start with %q", uri, pairScheme)
	}
	q, err := url.ParseQuery(strings.TrimPrefix(uri, pairScheme+"?"))
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if got := q.Get("u"); got != info.URL {
		t.Errorf("u = %q, want %q", got, info.URL)
	}
	if got := q.Get("t"); got != info.Token {
		t.Errorf("t = %q, want %q", got, info.Token)
	}
	if got := q.Get("f"); got != info.Fingerprint {
		t.Errorf("f = %q, want %q", got, info.Fingerprint)
	}
	// The raw URI must not contain a bare "://" past the scheme — that would
	// mean the server URL went in unencoded and an app splitting on it would
	// mis-parse.
	if strings.Count(uri, "://") != 1 {
		t.Errorf("server URL is not query-encoded: %s", uri)
	}
}

// Plain HTTP has no certificate to pin, and an empty f= parameter would make an
// app store the empty string as a pin.
func TestPairURIOmitsEmptyFingerprint(t *testing.T) {
	info := sampleInfo()
	info.Fingerprint = ""
	if strings.Contains(pairURI(info), "f=") {
		t.Fatalf("uri carries an empty fingerprint: %s", pairURI(info))
	}
}

// The whole point of choosing an in-repo encoder over a printed URL is that the
// real payload fits in a scannable symbol, so this is the drift gate on
// PairInfo's size: a field added there eats the budget the server URL needs, and
// the failure mode in a terminal is silent — the QR is simply replaced by a
// fallback message nobody reads.
//
// Two claims, because "does it fit" alone would pass right up to the cliff:
// a typical LAN pairing stays well inside the range, and a pathological
// hostname still encodes at all.
func TestPairURIFitsAQRCode(t *testing.T) {
	info := sampleInfo() // https://192.168.1.24:8421 — what a LAN pairing looks like
	code, err := qr.Encode(pairURI(info))
	if err != nil {
		t.Fatalf("typical pairing URI does not encode: %v", err)
	}
	if code.Version > 8 {
		t.Errorf("a typical pairing URI (%d bytes) needs version %d; it fitted in 8 before, "+
			"so PairInfo has grown and the URL budget shrank", len(pairURI(info)), code.Version)
	}

	// The URL a pairing has left to spend, measured rather than assumed: the
	// encoder's ceiling less everything else the URI carries.
	info.URL = "https://cats-workstation.internal.example-company.net:8421" // 57 chars
	long := pairURI(info)
	if _, err := qr.Encode(long); err != nil {
		t.Errorf("a 57-character server URL (%d-byte URI) no longer encodes: %v", len(long), err)
	}
}

// The rendered block has to carry the link and the fields a human checks by eye,
// and must never carry a colour escape when colour is off (a piped `catctl pair`
// feeds scripts and log files).
func TestRenderPairing(t *testing.T) {
	info := sampleInfo()
	now := time.Unix(1_700_000_000, 0)

	plain := renderPairing(info, now, false)
	for _, want := range []string{
		pairURI(info),
		info.URL,
		info.Token,
		info.Fingerprint,
		"in 5m0s",
		"single use",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("render is missing %q", want)
		}
	}
	if strings.Contains(plain, "\x1b[") {
		t.Error("uncoloured render emitted ANSI escapes")
	}

	colored := renderPairing(info, now, true)
	if !strings.Contains(colored, "\x1b[") {
		t.Error("coloured render emitted no ANSI escapes")
	}
}

// A payload past the encoder's ceiling must still produce a usable block: the
// URI is the actual credential and the code is a convenience, so the fallback
// has to print the link and say why there is no code — not print nothing and
// leave the operator wondering whether pairing worked.
func TestRenderPairingFallsBackWhenTheCodeWillNotEncode(t *testing.T) {
	info := sampleInfo()
	info.URL = "https://" + strings.Repeat("x", 300) + ":8421"
	out := renderPairing(info, time.Unix(1_700_000_000, 0), false)

	if !strings.Contains(out, pairURI(info)) {
		t.Error("the fallback dropped the pairing link")
	}
	if !strings.Contains(out, "too long to render as a code") {
		t.Errorf("the fallback does not explain the missing code:\n%s", out)
	}
	if !strings.Contains(out, info.Token) {
		t.Error("the fallback dropped the token")
	}
}

// Plain HTTP: no pin to show, and no empty label pretending there is one.
func TestRenderPairingWithoutFingerprint(t *testing.T) {
	info := sampleInfo()
	info.Fingerprint = ""
	out := renderPairing(info, time.Unix(1_700_000_000, 0), false)
	if strings.Contains(out, "SHA-256") {
		t.Errorf("render shows a certificate pin for a plain-HTTP server:\n%s", out)
	}
}

func TestExpiresIn(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	cases := []struct {
		at   int64
		want string
	}{
		{now.Add(5 * time.Minute).Unix(), "in 5m0s"},
		{now.Add(90 * time.Second).Unix(), "in 1m30s"},
		{now.Unix(), "already expired — run `catctl pair` again"},
		{now.Add(-time.Hour).Unix(), "already expired — run `catctl pair` again"},
	}
	for _, c := range cases {
		if got := expiresIn(c.at, now); got != c.want {
			t.Errorf("expiresIn(%d) = %q, want %q", c.at, got, c.want)
		}
	}
}

// The three transport-level methods are answered before app.Dispatcher ever
// sees the name, so a §7 command that took one of them would be unreachable from
// the control socket — silently, with no error anywhere. Assert the collision
// does not exist rather than trusting that nobody picks the name.
//
// pair is the one that would matter most: it is off the §7 table on purpose, so
// that the browser front end cannot mint credentials.
func TestTransportMethodsDoNotShadowCommands(t *testing.T) {
	transport := []string{ctlproto.MethodPing, ctlproto.MethodEventsSubscribe, ctlproto.MethodPair}
	for _, n := range app.CommandNames() {
		if slices.Contains(transport, n) {
			t.Fatalf("§7 command %q collides with a transport-level method", n)
		}
	}
}
