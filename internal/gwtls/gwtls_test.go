package gwtls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureSelfSignedGeneratesLoadablePair(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, err := EnsureSelfSigned(dir)
	if err != nil {
		t.Fatalf("EnsureSelfSigned: %v", err)
	}
	if certPath != filepath.Join(dir, certFile) || keyPath != filepath.Join(dir, keyFile) {
		t.Fatalf("unexpected paths: %q, %q", certPath, keyPath)
	}

	// rweb loads the pair exactly this way — prove it round-trips.
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		t.Fatalf("LoadX509KeyPair: %v", err)
	}

	cert := parseCert(t, certPath)
	if !cert.NotAfter.After(time.Now().Add(validity - 24*time.Hour)) {
		t.Errorf("validity too short: NotAfter=%v", cert.NotAfter)
	}
	if err := cert.VerifyHostname("localhost"); err != nil {
		t.Errorf("cert does not cover localhost: %v", err)
	}
	if err := cert.VerifyHostname("127.0.0.1"); err != nil {
		t.Errorf("cert does not cover 127.0.0.1: %v", err)
	}
	if !containsIP(cert.IPAddresses, net.IPv4(127, 0, 0, 1)) {
		t.Error("cert missing 127.0.0.1 SAN")
	}
}

func TestEnsureSelfSignedKeyPerms(t *testing.T) {
	dir := t.TempDir()
	_, keyPath, err := EnsureSelfSigned(dir)
	if err != nil {
		t.Fatalf("EnsureSelfSigned: %v", err)
	}
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key perms = %o, want 600", perm)
	}
}

func TestEnsureSelfSignedReusesCachedCert(t *testing.T) {
	dir := t.TempDir()
	certPath, _, err := EnsureSelfSigned(dir)
	if err != nil {
		t.Fatalf("first EnsureSelfSigned: %v", err)
	}
	before := parseCert(t, certPath).SerialNumber

	// A second call with a still-valid cached cert must not regenerate.
	if _, _, err = EnsureSelfSigned(dir); err != nil {
		t.Fatalf("second EnsureSelfSigned: %v", err)
	}
	after := parseCert(t, certPath).SerialNumber
	if before.Cmp(after) != 0 {
		t.Error("cached cert was regenerated despite being valid")
	}
}

func TestEnsureSelfSignedRegeneratesExpiring(t *testing.T) {
	dir := t.TempDir()
	certPath, _, err := EnsureSelfSigned(dir)
	if err != nil {
		t.Fatalf("EnsureSelfSigned: %v", err)
	}
	before := parseCert(t, certPath).SerialNumber

	// Overwrite the cert file with a bogus PEM so usableCert() rejects it,
	// forcing regeneration on the next call.
	if err := os.WriteFile(certPath, []byte("-----BEGIN CERTIFICATE-----\nnope\n-----END CERTIFICATE-----\n"), 0o644); err != nil {
		t.Fatalf("clobber cert: %v", err)
	}
	if _, _, err = EnsureSelfSigned(dir); err != nil {
		t.Fatalf("regen EnsureSelfSigned: %v", err)
	}
	after := parseCert(t, certPath).SerialNumber
	if before.Cmp(after) == 0 {
		t.Error("expected regeneration, got same serial")
	}
}

func TestEnsureSelfSignedEmptyDir(t *testing.T) {
	if _, _, err := EnsureSelfSigned(""); err == nil {
		t.Error("empty dir: want error, got nil")
	}
}

func parseCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("cert is not valid PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}

func containsIP(ips []net.IP, want net.IP) bool {
	for _, ip := range ips {
		if ip.Equal(want) {
			return true
		}
	}
	return false
}
