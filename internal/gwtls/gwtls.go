// Package gwtls provides the catway's TLS certificate for WS10: it reuses a
// cached self-signed certificate or mints a fresh one on demand. It exists
// because rweb loads certificates from files (tls.LoadX509KeyPair), so an
// auto-generated cert must be written to disk before the server can use it.
//
// The generated certificate is self-signed (there is no CA for a self-hosted
// dev tool), so browsers show a trust warning on first connect — expected. An
// operator who wants a trusted cert supplies their own PEMs and bypasses this
// package entirely.
//
// The certificate covers what this host can work out about itself plus any
// names the operator supplies (--tls-san): a LAN DNS name, or the hostname a
// relay will front. Since a cached certificate is good for 825 days, adding a
// name has to invalidate the cache — see usableCert.
package gwtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	certFile = "catway-cert.pem"
	keyFile  = "catway-key.pem"

	// validity is the lifetime of a freshly minted certificate.
	validity = 825 * 24 * time.Hour
	// renewBefore triggers regeneration when a cached cert is within this
	// window of expiring, so a long-running server never serves an expired one.
	renewBefore = 30 * 24 * time.Hour
)

// EnsureSelfSigned returns paths to a usable cert/key PEM pair under dir,
// minting a new self-signed certificate if none is cached, the cached one is
// near expiry, or it does not cover every name in extraSANs. dir is created if
// absent; the key file is written 0600.
//
// extraSANs are operator-supplied subject alternative names (--tls-san) on top
// of the ones this host discovers about itself: a LAN DNS name, or the hostname
// a relay will present. Each entry is either an IP literal or a DNS name;
// ParseSANs decides which and rejects the rest.
func EnsureSelfSigned(dir string, extraSANs []string) (certPath, keyPath string, err error) {
	if dir == "" {
		return "", "", fmt.Errorf("gwtls: empty cert directory")
	}
	dns, ips, err := ParseSANs(extraSANs)
	if err != nil {
		return "", "", err
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("gwtls: create cert dir: %w", err)
	}
	certPath = filepath.Join(dir, certFile)
	keyPath = filepath.Join(dir, keyFile)

	if usableCert(certPath, dns, ips) && fileExists(keyPath) {
		return certPath, keyPath, nil
	}
	if err = generate(certPath, keyPath, dns, ips); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

// Fingerprint returns the hex SHA-256 of the leaf certificate's DER encoding at
// path — the value a client pins to trust a self-signed catway on first use.
//
// It hashes the certificate, not the public key. SPKI pinning is the usual
// advice because it survives a re-issue with the same key, but generate mints a
// fresh ECDSA key on every regeneration, so here it would survive nothing DER
// pinning does not — and the DER hash is the value openssl and every browser's
// certificate viewer show, which makes it checkable by eye.
func Fingerprint(path string) (string, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("gwtls: read certificate: %w", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", fmt.Errorf("gwtls: %s holds no PEM certificate", path)
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:]), nil
}

// usableCert reports whether path holds a PEM certificate that parses, is not
// within renewBefore of expiring, and already covers every requested SAN.
//
// The coverage half is what makes --tls-san take effect at all: a cached cert
// is valid for 825 days, so without it a newly added name would sit in the
// config doing nothing until the next presidential term. Adding a name has to
// be what invalidates the cache.
//
// Only the *requested* names are checked, never the ones hostSANs discovers.
// Those follow the machine: a laptop changing Wi-Fi networks gets new interface
// addresses, and checking them would re-mint the certificate — and change the
// fingerprint a client pinned — on every network hop. The auto-discovered set
// is a best effort at mint time; the requested set is a promise.
func usableCert(path string, dns []string, ips []net.IP) bool {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return false
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false
	}
	if !time.Now().Add(renewBefore).Before(cert.NotAfter) {
		return false
	}
	return covers(cert, dns, ips)
}

// covers reports whether cert already carries every requested SAN.
//
// Matching is exact against the SAN lists rather than via VerifyHostname,
// because the question here is "did we mint what was asked for", not "would a
// TLS client accept this". A wildcard cert would satisfy the second and still
// mean the operator's literal name never made it in.
func covers(cert *x509.Certificate, dns []string, ips []net.IP) bool {
	if len(dns) > 0 {
		have := make(map[string]bool, len(cert.DNSNames))
		for _, n := range cert.DNSNames {
			have[normalizeDNS(n)] = true
		}
		for _, want := range dns { // ParseSANs already normalised these
			if !have[want] {
				return false
			}
		}
	}
	for _, want := range ips {
		if !hasIP(cert.IPAddresses, want) {
			return false
		}
	}
	return true
}

func hasIP(ips []net.IP, want net.IP) bool {
	for _, ip := range ips {
		if ip.Equal(want) {
			return true
		}
	}
	return false
}

// ParseSANs classifies operator-supplied SANs into DNS names and IPs, dropping
// blanks and duplicates. It is exported so config validation can reject a bad
// entry at load time — a typo that only surfaced as a browser trust warning
// months later would be a poor trade.
//
// An entry that parses as an IP literal becomes an IP SAN; anything else must
// be a plausible DNS name. That order matters: "127.0.0.1" in a DNS SAN is not
// an error any parser would report, it just silently never matches.
func ParseSANs(sans []string) (dns []string, ips []net.IP, err error) {
	seenDNS, seenIP := map[string]bool{}, map[string]bool{}
	for _, raw := range sans {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		// A bracketed literal is how IPv6 appears inside a URL, so it is what
		// gets pasted. Accept it rather than failing on the brackets.
		if len(s) > 2 && s[0] == '[' && s[len(s)-1] == ']' {
			s = s[1 : len(s)-1]
		}
		if ip := net.ParseIP(s); ip != nil {
			if key := ip.String(); !seenIP[key] {
				seenIP[key] = true
				ips = append(ips, ip)
			}
			continue
		}
		name := normalizeDNS(s)
		if err := validDNSName(name); err != nil {
			return nil, nil, fmt.Errorf("gwtls: SAN %q: %w", raw, err)
		}
		if !seenDNS[name] {
			seenDNS[name] = true
			dns = append(dns, name)
		}
	}
	return dns, ips, nil
}

// normalizeDNS lowercases and drops the root's trailing dot, so "Cats.Local."
// and "cats.local" are one name. DNS is case-insensitive and the trailing dot
// is not part of the name a TLS client compares against.
func normalizeDNS(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

// validDNSName rejects the mistakes an operator actually makes — a URL, a
// host:port, a stray path — rather than enforcing the full grammar. A leading
// "*" label is allowed: a wildcard is a legitimate thing to want in a cert,
// even though nothing in cats mints one for itself.
func validDNSName(name string) error {
	switch {
	case name == "":
		return errors.New("empty")
	case len(name) > 253:
		return errors.New("longer than 253 characters")
	case strings.ContainsAny(name, `:/\ `):
		return errors.New("looks like a URL or host:port; want a bare name or IP")
	}
	for i, label := range strings.Split(name, ".") {
		if i == 0 && label == "*" {
			continue
		}
		if label == "" {
			return errors.New("empty label")
		}
		if len(label) > 63 {
			return fmt.Errorf("label %q is longer than 63 characters", label)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("label %q starts or ends with a hyphen", label)
		}
		for _, r := range label {
			// Already lowercased; underscore is not strictly legal in a hostname
			// but is common in practice and x509 carries it happily.
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
				return fmt.Errorf("invalid character %q", r)
			}
		}
	}
	return nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// generate mints a self-signed ECDSA P-256 certificate covering localhost, the
// loopback addresses, this host's names/IPs, and any requested extras, then
// writes the PEM pair.
func generate(certPath, keyPath string, extraDNS []string, extraIPs []net.IP) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("gwtls: generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("gwtls: serial: %w", err)
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "cats catway (self-signed)"},
		NotBefore:             now.Add(-time.Hour), // tolerate small clock skew
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	dns, ips := hostSANs()
	tmpl.DNSNames, tmpl.IPAddresses = appendSANs(dns, ips, extraDNS, extraIPs)

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("gwtls: create certificate: %w", err)
	}

	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("gwtls: marshal key: %w", err)
	}
	if err := writePEM(keyPath, "EC PRIVATE KEY", keyDER, 0o600); err != nil {
		return err
	}
	return nil
}

// appendSANs merges the requested SANs into the auto-discovered set, dropping
// anything already covered. A hostname the operator names explicitly is very
// often one hostSANs found too — listing it twice is harmless but makes the
// certificate's own output confusing to read when diagnosing a trust warning.
func appendSANs(dns []string, ips []net.IP, extraDNS []string, extraIPs []net.IP) ([]string, []net.IP) {
	have := make(map[string]bool, len(dns))
	for _, n := range dns {
		have[n] = true
	}
	for _, n := range extraDNS {
		if !have[n] {
			have[n] = true
			dns = append(dns, n)
		}
	}
	for _, ip := range extraIPs {
		if !hasIP(ips, ip) {
			ips = append(ips, ip)
		}
	}
	return dns, ips
}

// hostSANs collects the DNS names and IPs the cert should cover: always
// localhost + the loopback addresses, plus this host's name and its
// non-loopback interface addresses so LAN access to the catway validates.
//
// Names come back normalised (see normalizeDNS) so they compare equal to a
// requested SAN naming the same host — macOS reports a hostname like
// "Studio.local" while an operator would type "studio.local", and DNS does not
// consider those different.
func hostSANs() (dns []string, ips []net.IP) {
	dns = []string{"localhost"}
	ips = []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}

	if host, err := os.Hostname(); err == nil {
		if h := normalizeDNS(host); h != "" && h != "localhost" {
			dns = append(dns, h)
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok || ipNet.IP.IsLoopback() || ipNet.IP.IsLinkLocalUnicast() {
				continue
			}
			ips = append(ips, ipNet.IP)
		}
	}
	return dns, ips
}

func writePEM(path, blockType string, der []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("gwtls: open %s: %w", path, err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		return fmt.Errorf("gwtls: encode %s: %w", path, err)
	}
	return nil
}
