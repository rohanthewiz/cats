package peersync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rohanthewiz/cats/internal/gwtls"
)

// Timeouts. A bundle fetch is a directory walk on the other side; an apply may
// run a plugin install (git clone + build) per missing plugin, so it gets the
// long one. Neither is unbounded: a peer that hangs must become a report line.
const (
	fetchTimeout = 60 * time.Second
	applyTimeout = 15 * time.Minute
	// MaxBodyBytes bounds what either side will read of the other: a bundle is
	// rows plus capped attachments, so a body past this is not a bundle.
	MaxBodyBytes = 96 << 20
)

// Client talks to one peer catway's /peer/v1 endpoints.
type Client struct {
	base  string // scheme://host[:port], no trailing slash
	token string
	http  *http.Client
}

// NewClient builds a client for a peer from its config: the URL, the shared
// secret to present as a bearer token, and an optional certificate pin. The
// pin turns off chain verification and requires the leaf to hash to exactly
// the configured SHA-256 — the same trust model catway uses toward a tls://
// cathost, and the only one that makes a self-signed peer safe. Without a pin
// the standard verification applies.
func NewClient(rawURL, token, fingerprint string) (*Client, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("peer url %q: want http(s)", rawURL)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if u.Scheme == "https" {
		cfg, err := gwtls.ClientConfig(u.Hostname(), fingerprint)
		if err != nil {
			return nil, err
		}
		transport.TLSClientConfig = cfg
	}
	return &Client{
		base:  strings.TrimRight(u.Scheme+"://"+u.Host, "/"),
		token: token,
		http: &http.Client{
			Transport: transport,
			// Never follow a redirect. catway's auth guard answers a request
			// it cannot authenticate with a 302 to its login page; followed,
			// that is a 200 full of HTML and a decode error that says nothing.
			// Seen as the 302 it is, it is a refused credential, said so.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}, nil
}

// Hello asks the peer who it is. It is the cheapest request there is, so it
// doubles as the reachability and credential check a "test connection"
// button wants.
func (c *Client) Hello(ctx context.Context) (Instance, error) {
	var inst Instance
	err := c.do(ctx, http.MethodGet, PathHello, nil, &inst, fetchTimeout)
	return inst, err
}

// Fetch asks the peer for its bundle over the wanted categories.
func (c *Client) Fetch(ctx context.Context, want Want) (Bundle, error) {
	var b Bundle
	err := c.do(ctx, http.MethodGet, PathBundle+"?want="+url.QueryEscape(want.String()), nil, &b, fetchTimeout)
	if err != nil {
		return b, err
	}
	if b.Schema != Schema {
		return b, fmt.Errorf("peer speaks bundle schema %d, this cats speaks %d", b.Schema, Schema)
	}
	return b, nil
}

// Apply sends our bundle to the peer and returns what it did with it.
func (c *Client) Apply(ctx context.Context, b Bundle) (ApplyReport, error) {
	var rep ApplyReport
	err := c.do(ctx, http.MethodPost, PathApply, b, &rep, applyTimeout)
	return rep, err
}

// do is one JSON round trip. Every failure is turned into a sentence naming
// the peer path, because the caller puts it straight into a report line.
func (c *Client) do(ctx context.Context, method, path string, in, out any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
	if err != nil {
		return fmt.Errorf("%s %s: read: %w", method, path, err)
	}
	if len(data) > MaxBodyBytes {
		return fmt.Errorf("%s %s: response larger than %d bytes", method, path, MaxBodyBytes)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect:
		// 401/403 are the guard's answers for /ws-shaped calls; a redirect is
		// its answer for everything else (to the login page). All one thing.
		if path == PathPair {
			// The pairing route's refusal is about the token in the body,
			// not a credential in a header — and the fix is on the other
			// machine, so say what to run there.
			return fmt.Errorf("%s %s: the peer refused the pairing token (%s) — it expired, was already used, or is not a peer token; run `catctl pair peer` there again", method, path, resp.Status)
		}
		return fmt.Errorf("%s %s: the peer refused the credential (%s) — token_file must hold a peer grant from `catctl pair peer` there, or that catway's CATS_PASSWORD; a revoked grant is re-paired with attach-peer", method, path, resp.Status)
	case http.StatusNotFound:
		return fmt.Errorf("%s %s: %s — the peer has no /peer/v1 endpoints; is it running a cats with peer sync?", method, path, resp.Status)
	default:
		msg := strings.TrimSpace(string(data))
		if len(msg) > 200 {
			msg = msg[:200] + "…"
		}
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, msg)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s %s: decode: %w", method, path, err)
	}
	return nil
}

// ErrNoToken is returned by ReadToken when a peer has no credential at all.
var ErrNoToken = errors.New("no token or token_file configured for this peer")
