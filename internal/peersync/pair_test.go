package peersync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPeerLinkRoundTrip(t *testing.T) {
	for _, fp := range []string{"", "ab12cd"} {
		link := PeerLink("https://10.0.0.5:8421", "tok-en_", fp)
		if !IsPeerLink(link) {
			t.Fatalf("IsPeerLink(%q) = false", link)
		}
		u, tok, gotFP, err := ParsePeerLink(link)
		if err != nil || u != "https://10.0.0.5:8421" || tok != "tok-en_" || gotFP != fp {
			t.Errorf("ParsePeerLink(%q) = %q, %q, %q, %v", link, u, tok, gotFP, err)
		}
	}
}

func TestParsePeerLinkRejects(t *testing.T) {
	for _, bad := range []string{
		"https://10.0.0.5:8421",
		"cats://pair?u=https://x&t=y",   // the device link
		"cats://peerx?u=https://x&t=y",  // a different scheme sharing the prefix
		"cats://peer?u=https%3A%2F%2Fx", // what an unquoted link leaves behind
		"cats://peer?t=y",
	} {
		if _, _, _, err := ParsePeerLink(bad); err == nil {
			t.Errorf("ParsePeerLink(%q) accepted", bad)
		}
	}
}

// Redeem posts the token with no Authorization header and decodes the grant;
// a refusal comes back as the pairing-specific sentence, not the "your
// token_file is wrong" one.
func TestRedeem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PathPair || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("redeem sent an Authorization header")
		}
		var req PairRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Token != "good" {
			http.Error(w, "invalid pairing token", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(PairResponse{Credential: "catspeer_x", GrantID: "0a0b0c0d",
			Instance: Instance{Hostname: "mini"}})
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Redeem(context.Background(), "good", "me@laptop")
	if err != nil || got.Credential != "catspeer_x" || got.GrantID != "0a0b0c0d" || got.Instance.Hostname != "mini" {
		t.Fatalf("Redeem = %+v, %v", got, err)
	}
	_, err = c.Redeem(context.Background(), "stale", "me@laptop")
	if err == nil || !strings.Contains(err.Error(), "pairing token") {
		t.Fatalf("refused Redeem err = %v, want the pairing-token sentence", err)
	}
}

// Unpair posts to PathUnpair with the grant as the bearer and decodes which
// grant went; a peer from before the route answers 404, which surfaces as an
// error the detaching side logs.
func TestUnpair(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PathUnpair || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer catspeer_x" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(UnpairResponse{GrantID: "0a0b0c0d"})
	}))
	defer srv.Close()

	c, err := NewClient(srv.URL, "catspeer_x", "")
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Unpair(context.Background())
	if err != nil || got.GrantID != "0a0b0c0d" {
		t.Fatalf("Unpair = %+v, %v", got, err)
	}
	stale, _ := NewClient(srv.URL, "catspeer_gone", "")
	if _, err := stale.Unpair(context.Background()); err == nil {
		t.Fatal("Unpair with a refused credential succeeded")
	}
	old := httptest.NewServer(http.NotFoundHandler())
	defer old.Close()
	oc, _ := NewClient(old.URL, "catspeer_x", "")
	if _, err := oc.Unpair(context.Background()); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("Unpair against a peer without the route: %v, want a 404 error", err)
	}
}
