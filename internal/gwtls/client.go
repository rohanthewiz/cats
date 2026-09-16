package gwtls

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ClientConfig is the TLS configuration for dialing another cats process —
// a peer catway, or anything else that serves the self-signed certificate
// EnsureSelfSigned mints.
//
// With a fingerprint the certificate is PINNED: chain verification is turned
// off and the peer's leaf must hash to exactly the configured SHA-256 (the
// value Fingerprint computes and catway logs at startup). For a personal
// fleet with no CA that is stronger than the default, because the alternative
// to pinning is not a verified chain but a skipped check — and it is the only
// reason a self-signed certificate is safe here.
//
// With no fingerprint the standard chain-and-hostname verification applies,
// which is right for a peer fronted by a real certificate. There is no third
// option: an unpinned, unverified session authenticates nobody.
//
// This is the same rule cmd/catway's cathost dialer applies; it lives here so
// every dial site in cats pins the same way.
func ClientConfig(serverName, fingerprint string) (*tls.Config, error) {
	cfg := &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12}
	want := NormalizeFingerprint(fingerprint)
	if want == "" {
		return cfg, nil
	}
	if len(want) != sha256.Size*2 {
		return nil, fmt.Errorf("fingerprint %q: want a hex SHA-256 (%d characters)", fingerprint, sha256.Size*2)
	}
	cfg.InsecureSkipVerify = true // replaced, not waived, by the pin below
	cfg.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("peer presented no certificate")
		}
		sum := sha256.Sum256(rawCerts[0]) // the leaf; a self-signed certificate IS the chain
		if got := hex.EncodeToString(sum[:]); got != want {
			return fmt.Errorf("peer certificate fingerprint %s does not match the pinned %s", got, want)
		}
		return nil
	}
	return cfg, nil
}

// NormalizeFingerprint accepts a fingerprint in the shapes it gets pasted in:
// lower/upper hex, and the colon-separated form openssl and certificate
// viewers print. Anything else fails ClientConfig's length check.
func NormalizeFingerprint(fp string) string {
	return strings.ToLower(strings.NewReplacer(":", "", " ", "", "\n", "").Replace(strings.TrimSpace(fp)))
}
