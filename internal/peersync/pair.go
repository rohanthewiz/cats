package peersync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Peer pairing: how one catway obtains a durable peer-sync credential from
// another without anyone copying the other's password (internal/peergrant has
// the full rationale and the sequence diagram).
//
// The grantor's operator mints a short-lived, single-use pairing token and
// hands it over as a cats://peer link; the other catway redeems the token at
// PathPair and keeps what it gets back in a token_file. That path is the one
// route under /peer/v1 that is public — the pairing token in the body is its
// credential, exactly as the password is /login's.

// PathPair redeems a peer pairing token for a peer grant.
const PathPair = "/peer/v1/pair" // POST PairRequest → PairResponse

// PathPrefix is the common prefix of every peer route; the grantor's auth guard
// accepts a peer grant on paths under it and nowhere else.
const PathPrefix = "/peer/v1/"

// MaxPairBodyBytes bounds a PairRequest. It is two short strings; the general
// MaxBodyBytes is sized for bundles, and an unauthenticated route has no
// business reading 96 MiB before deciding a token is bad.
const MaxPairBodyBytes = 4 << 10

// PairRequest is what the redeeming catway sends: the token, and what it calls
// itself (Instance.Name) so the grantor's listing can say who holds the grant.
type PairRequest struct {
	Token string `json:"token"`
	Name  string `json:"name,omitempty"`
}

// PairResponse is the grant. Credential is shown exactly once — the grantor
// keeps only a hash — so the redeemer must store it before doing anything else
// that could fail. GrantID is the handle the grantor's operator revokes it by;
// it is returned so the redeemer can log it and a human can match the two ends.
type PairResponse struct {
	Credential string   `json:"credential"`
	GrantID    string   `json:"grant_id"`
	Instance   Instance `json:"instance"`
}

// Redeem trades a pairing token for a peer grant. The client must have been
// built with no token: the route is public, and sending a stale credential
// alongside a pairing token would only muddy what the grantor logs.
func (c *Client) Redeem(ctx context.Context, token, name string) (PairResponse, error) {
	var out PairResponse
	if err := c.do(ctx, http.MethodPost, PathPair, PairRequest{Token: token, Name: name}, &out, fetchTimeout); err != nil {
		return out, err
	}
	if out.Credential == "" {
		return out, errors.New("the peer answered the pairing with no credential")
	}
	return out, nil
}

// PeerLinkScheme is the link `catctl pair peer` prints and `catctl attach-peer`
// accepts in place of a URL. It is a sibling of the device link (cats://pair)
// rather than a query flag on it: the mobile app registers cats://pair, and a
// peer link opened on a phone should not look to that app like a pairing it
// ought to try — the token would be refused at /login anyway, and the error
// would be a mystery.
const PeerLinkScheme = "cats://peer"

// PeerLink composes the link carrying everything the redeeming side needs: the
// grantor's URL, the token, and the certificate pin when there is one.
func PeerLink(baseURL, token, fingerprint string) string {
	q := url.Values{}
	q.Set("u", baseURL)
	q.Set("t", token)
	if fingerprint != "" {
		q.Set("f", fingerprint)
	}
	return PeerLinkScheme + "?" + q.Encode()
}

// IsPeerLink reports whether s is (meant to be) a peer link, so a caller can
// route it to ParsePeerLink and surface that function's error rather than
// treat a mangled link as a URL.
func IsPeerLink(s string) bool {
	return strings.HasPrefix(s, PeerLinkScheme)
}

// ParsePeerLink is PeerLink's inverse. The URL inside is not validated here —
// the caller applies config.ValidatePeerURL, the one rule every peer URL
// passes, so a link cannot smuggle in an address the config would refuse.
func ParsePeerLink(s string) (baseURL, token, fingerprint string, err error) {
	rest, ok := strings.CutPrefix(s, PeerLinkScheme)
	if !ok || (rest != "" && rest[0] != '?') {
		return "", "", "", fmt.Errorf("not a %s link", PeerLinkScheme)
	}
	q, err := url.ParseQuery(strings.TrimPrefix(rest, "?"))
	if err != nil {
		return "", "", "", fmt.Errorf("%s link: %w", PeerLinkScheme, err)
	}
	baseURL, token, fingerprint = q.Get("u"), q.Get("t"), q.Get("f")
	if baseURL == "" || token == "" {
		// The usual cause is a shell: an unquoted link is cut at its first '&'
		// and the rest run as a background job.
		return "", "", "", fmt.Errorf("%s link is missing its url or token — was it quoted?", PeerLinkScheme)
	}
	return baseURL, token, fingerprint, nil
}
