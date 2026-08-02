package gwtls

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestEnsureSelfSignedGeneratesLoadablePair(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, err := EnsureSelfSigned(dir, nil)
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
	if !hasIP(cert.IPAddresses, net.IPv4(127, 0, 0, 1)) {
		t.Error("cert missing 127.0.0.1 SAN")
	}
}

func TestEnsureSelfSignedKeyPerms(t *testing.T) {
	dir := t.TempDir()
	_, keyPath, err := EnsureSelfSigned(dir, nil)
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
	certPath, _, err := EnsureSelfSigned(dir, nil)
	if err != nil {
		t.Fatalf("first EnsureSelfSigned: %v", err)
	}
	before := parseCert(t, certPath).SerialNumber

	// A second call with a still-valid cached cert must not regenerate.
	if _, _, err = EnsureSelfSigned(dir, nil); err != nil {
		t.Fatalf("second EnsureSelfSigned: %v", err)
	}
	after := parseCert(t, certPath).SerialNumber
	if before.Cmp(after) != 0 {
		t.Error("cached cert was regenerated despite being valid")
	}
}

func TestEnsureSelfSignedRegeneratesExpiring(t *testing.T) {
	dir := t.TempDir()
	certPath, _, err := EnsureSelfSigned(dir, nil)
	if err != nil {
		t.Fatalf("EnsureSelfSigned: %v", err)
	}
	before := parseCert(t, certPath).SerialNumber

	// Overwrite the cert file with a bogus PEM so usableCert() rejects it,
	// forcing regeneration on the next call.
	if err := os.WriteFile(certPath, []byte("-----BEGIN CERTIFICATE-----\nnope\n-----END CERTIFICATE-----\n"), 0o644); err != nil {
		t.Fatalf("clobber cert: %v", err)
	}
	if _, _, err = EnsureSelfSigned(dir, nil); err != nil {
		t.Fatalf("regen EnsureSelfSigned: %v", err)
	}
	after := parseCert(t, certPath).SerialNumber
	if before.Cmp(after) == 0 {
		t.Error("expected regeneration, got same serial")
	}
}

func TestEnsureSelfSignedEmptyDir(t *testing.T) {
	if _, _, err := EnsureSelfSigned("", nil); err == nil {
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

// --- --tls-san ----------------------------------------------------------------

// The point of the flag: a requested name lands in the certificate, as the
// right kind of SAN. An IP written into DNSNames is not an error any parser
// reports — it simply never matches — so the classification is the test.
func TestEnsureSelfSignedCoversRequestedSANs(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := EnsureSelfSigned(dir, []string{"cats.lan", "10.0.0.7", "[fd00::1]"}); err != nil {
		t.Fatalf("EnsureSelfSigned: %v", err)
	}
	cert := parseCert(t, filepath.Join(dir, certFile))

	if err := cert.VerifyHostname("cats.lan"); err != nil {
		t.Errorf("cert does not cover cats.lan: %v", err)
	}
	if !hasIP(cert.IPAddresses, net.ParseIP("10.0.0.7")) {
		t.Errorf("10.0.0.7 missing from IP SANs: %v", cert.IPAddresses)
	}
	if !hasIP(cert.IPAddresses, net.ParseIP("fd00::1")) {
		t.Errorf("bracketed IPv6 literal missing from IP SANs: %v", cert.IPAddresses)
	}
	for _, n := range cert.DNSNames {
		if net.ParseIP(n) != nil {
			t.Errorf("IP %q landed in DNSNames, where it can never match", n)
		}
	}
	// The auto-discovered names survive: extras extend the set, never replace it.
	if err := cert.VerifyHostname("localhost"); err != nil {
		t.Errorf("requested SANs displaced localhost: %v", err)
	}
}

// The cache is what makes this feature easy to get wrong. A cert is good for
// 825 days, so if a newly requested name did not invalidate it, --tls-san would
// appear to work, change nothing, and stay broken until the next expiry.
func TestEnsureSelfSignedRemintsForANewSAN(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := EnsureSelfSigned(dir, nil); err != nil {
		t.Fatalf("first EnsureSelfSigned: %v", err)
	}
	before := parseCert(t, filepath.Join(dir, certFile)).SerialNumber

	if _, _, err := EnsureSelfSigned(dir, []string{"cats.lan"}); err != nil {
		t.Fatalf("EnsureSelfSigned with a new SAN: %v", err)
	}
	cert := parseCert(t, filepath.Join(dir, certFile))
	if cert.SerialNumber.Cmp(before) == 0 {
		t.Fatal("a newly requested SAN did not re-mint the certificate")
	}
	if err := cert.VerifyHostname("cats.lan"); err != nil {
		t.Errorf("re-minted cert still does not cover cats.lan: %v", err)
	}
}

// ...and the converse, which matters just as much: re-minting changes the
// fingerprint a client may have pinned, so an unchanged request must not.
// Case and the root's trailing dot are not changes.
func TestEnsureSelfSignedKeepsCertForAnUnchangedSAN(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := EnsureSelfSigned(dir, []string{"cats.lan", "10.0.0.7"}); err != nil {
		t.Fatalf("first EnsureSelfSigned: %v", err)
	}
	before := parseCert(t, filepath.Join(dir, certFile)).SerialNumber

	for _, again := range [][]string{
		{"cats.lan", "10.0.0.7"},
		{"CATS.LAN", "10.0.0.7"},  // DNS is case-insensitive
		{"cats.lan.", "10.0.0.7"}, // the root dot is not part of the name
		{"10.0.0.7", "cats.lan"},  // order is not part of the request
	} {
		if _, _, err := EnsureSelfSigned(dir, again); err != nil {
			t.Fatalf("EnsureSelfSigned(%v): %v", again, err)
		}
		if parseCert(t, filepath.Join(dir, certFile)).SerialNumber.Cmp(before) != 0 {
			t.Fatalf("%v re-minted the certificate; a pinned fingerprint would break for nothing", again)
		}
	}
}

// A subset request must not re-mint either: the cert already covers it.
// (Dropping a name does not shrink the certificate — it only stops being a
// reason to regenerate, which is the conservative direction.)
func TestEnsureSelfSignedKeepsCertForASubsetOfSANs(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := EnsureSelfSigned(dir, []string{"cats.lan", "studio.lan"}); err != nil {
		t.Fatalf("first EnsureSelfSigned: %v", err)
	}
	before := parseCert(t, filepath.Join(dir, certFile)).SerialNumber

	if _, _, err := EnsureSelfSigned(dir, []string{"cats.lan"}); err != nil {
		t.Fatalf("second EnsureSelfSigned: %v", err)
	}
	if parseCert(t, filepath.Join(dir, certFile)).SerialNumber.Cmp(before) != 0 {
		t.Error("a narrower SAN request re-minted an already-sufficient certificate")
	}
}

func TestParseSANs(t *testing.T) {
	tests := []struct {
		name    string
		in      []string
		wantDNS []string
		wantIPs []string
		wantErr bool
	}{
		{name: "empty", in: nil},
		{name: "blanks are dropped", in: []string{"", "   "}},
		{name: "names normalise", in: []string{"Cats.LAN."}, wantDNS: []string{"cats.lan"}},
		{name: "ip literal", in: []string{"192.168.1.9"}, wantIPs: []string{"192.168.1.9"}},
		{name: "ipv6 in brackets", in: []string{"[fd00::1]"}, wantIPs: []string{"fd00::1"}},
		{name: "wildcard", in: []string{"*.cats.lan"}, wantDNS: []string{"*.cats.lan"}},
		{name: "underscore", in: []string{"my_host.lan"}, wantDNS: []string{"my_host.lan"}},
		{name: "dedup across case", in: []string{"cats.lan", "CATS.LAN"}, wantDNS: []string{"cats.lan"}},
		{name: "dedup ips", in: []string{"10.0.0.1", "10.0.0.1"}, wantIPs: []string{"10.0.0.1"}},

		// The mistakes an operator actually makes.
		{name: "a URL", in: []string{"https://cats.lan"}, wantErr: true},
		{name: "host:port", in: []string{"cats.lan:8421"}, wantErr: true},
		{name: "a path", in: []string{"cats.lan/ws"}, wantErr: true},
		{name: "empty label", in: []string{"cats..lan"}, wantErr: true},
		{name: "leading hyphen", in: []string{"-cats.lan"}, wantErr: true},
		{name: "ipv6 with a zone", in: []string{"fe80::1%en0"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dns, ips, err := ParseSANs(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want an error, got dns=%v ips=%v", dns, ips)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSANs: %v", err)
			}
			if !slices.Equal(dns, tc.wantDNS) {
				t.Errorf("dns = %v, want %v", dns, tc.wantDNS)
			}
			var gotIPs []string
			for _, ip := range ips {
				gotIPs = append(gotIPs, ip.String())
			}
			if !slices.Equal(gotIPs, tc.wantIPs) {
				t.Errorf("ips = %v, want %v", gotIPs, tc.wantIPs)
			}
		})
	}
}

// A bad SAN must stop startup rather than yielding a certificate quietly
// missing the name the operator asked for.
func TestEnsureSelfSignedRejectsBadSAN(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := EnsureSelfSigned(dir, []string{"https://cats.lan"}); err == nil {
		t.Fatal("want an error for a URL-shaped SAN, got nil")
	}
	if fileExists(filepath.Join(dir, certFile)) {
		t.Error("a rejected SAN still minted a certificate")
	}
}

// Fingerprint is what a paired device pins, so it has to be the value every
// other tool reports for the same certificate — openssl, a browser's cert
// viewer, `catctl pair`. Deriving it independently here from the parsed
// certificate's Raw DER is the check that it hashes the right bytes.
func TestFingerprintMatchesTheCertificateDER(t *testing.T) {
	dir := t.TempDir()
	certPath, _, err := EnsureSelfSigned(dir, nil)
	if err != nil {
		t.Fatalf("EnsureSelfSigned: %v", err)
	}
	got, err := Fingerprint(certPath)
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	want := sha256.Sum256(parseCert(t, certPath).Raw)
	if got != hex.EncodeToString(want[:]) {
		t.Fatalf("Fingerprint = %s, want %s", got, hex.EncodeToString(want[:]))
	}
	if len(got) != 64 {
		t.Fatalf("fingerprint is %d hex characters, want 64", len(got))
	}
	// Regenerating mints a fresh key and so a fresh certificate — which is
	// precisely why the pin is over the DER and not the public key.
	if err := generate(certPath, filepath.Join(dir, keyFile), nil, nil); err != nil {
		t.Fatalf("generate: %v", err)
	}
	again, err := Fingerprint(certPath)
	if err != nil {
		t.Fatalf("Fingerprint after regeneration: %v", err)
	}
	if again == got {
		t.Fatal("a regenerated certificate kept the same fingerprint")
	}
}

// A missing or non-certificate file must return an error, not a hash of
// whatever bytes happened to be there — buildPairing drops the pin on error, and
// a bogus pin would be worse than none.
func TestFingerprintRejectsNonCertificates(t *testing.T) {
	dir := t.TempDir()
	if _, err := Fingerprint(filepath.Join(dir, "absent.pem")); err == nil {
		t.Error("Fingerprint of a missing file returned no error")
	}
	junk := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(junk, []byte("not a pem file at all\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Fingerprint(junk); err == nil {
		t.Error("Fingerprint of a non-PEM file returned no error")
	}
}
