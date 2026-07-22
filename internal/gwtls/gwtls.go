// Package gwtls provides the catway's TLS certificate for WS10: it reuses a
// cached self-signed certificate or mints a fresh one on demand. It exists
// because rweb loads certificates from files (tls.LoadX509KeyPair), so an
// auto-generated cert must be written to disk before the server can use it.
//
// The generated certificate is self-signed (there is no CA for a self-hosted
// dev tool), so browsers show a trust warning on first connect — expected. An
// operator who wants a trusted cert supplies their own PEMs and bypasses this
// package entirely.
package gwtls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
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
// minting a new self-signed certificate if none is cached or the cached one is
// near expiry. dir is created if absent; the key file is written 0600.
func EnsureSelfSigned(dir string) (certPath, keyPath string, err error) {
	if dir == "" {
		return "", "", fmt.Errorf("gwtls: empty cert directory")
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", "", fmt.Errorf("gwtls: create cert dir: %w", err)
	}
	certPath = filepath.Join(dir, certFile)
	keyPath = filepath.Join(dir, keyFile)

	if usableCert(certPath) && fileExists(keyPath) {
		return certPath, keyPath, nil
	}
	if err = generate(certPath, keyPath); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

// usableCert reports whether path holds a PEM certificate that parses and is
// not within renewBefore of expiring.
func usableCert(path string) bool {
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
	return time.Now().Add(renewBefore).Before(cert.NotAfter)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// generate mints a self-signed ECDSA P-256 certificate covering localhost, the
// loopback addresses, and this host's names/IPs, and writes the PEM pair.
func generate(certPath, keyPath string) error {
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
	tmpl.DNSNames = dns
	tmpl.IPAddresses = ips

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

// hostSANs collects the DNS names and IPs the cert should cover: always
// localhost + the loopback addresses, plus this host's name and its
// non-loopback interface addresses so LAN access to the catway validates.
func hostSANs() (dns []string, ips []net.IP) {
	dns = []string{"localhost"}
	ips = []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}

	if host, err := os.Hostname(); err == nil && host != "" && host != "localhost" {
		dns = append(dns, host)
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
