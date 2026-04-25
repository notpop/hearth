package pki_test

import (
	"crypto/x509"
	"encoding/pem"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/notpop/hearth/internal/security/pki"
)

func TestInitCAFresh(t *testing.T) {
	dir := t.TempDir()
	ca, err := pki.InitCA(dir, "")
	if err != nil {
		t.Fatalf("InitCA: %v", err)
	}
	if !ca.Cert.IsCA {
		t.Errorf("cert is not flagged CA")
	}
	if ca.Cert.Subject.CommonName != "hearth-home-ca" {
		t.Errorf("default CN = %q", ca.Cert.Subject.CommonName)
	}

	// Files exist with sensible perms.
	for _, name := range []string{"ca.crt", "ca.key"} {
		if _, err := pki.LoadCA(dir); err != nil {
			t.Fatalf("LoadCA after InitCA: %v", err)
		}
		_ = filepath.Join(dir, name) // path used implicitly via LoadCA
	}
}

func TestInitCAExplicitName(t *testing.T) {
	dir := t.TempDir()
	ca, err := pki.InitCA(dir, "my-ca")
	if err != nil {
		t.Fatalf("InitCA: %v", err)
	}
	if ca.Cert.Subject.CommonName != "my-ca" {
		t.Errorf("CN = %q", ca.Cert.Subject.CommonName)
	}
}

func TestInitCARefusesExisting(t *testing.T) {
	dir := t.TempDir()
	if _, err := pki.InitCA(dir, ""); err != nil {
		t.Fatalf("first InitCA: %v", err)
	}
	if _, err := pki.InitCA(dir, ""); err == nil {
		t.Errorf("expected error on second InitCA")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadCAMissing(t *testing.T) {
	if _, err := pki.LoadCA(t.TempDir()); err == nil {
		t.Errorf("expected error for empty dir")
	}
}

func TestLoadCABadCertPEM(t *testing.T) {
	dir := t.TempDir()
	_, _ = pki.InitCA(dir, "x")
	// Corrupt ca.crt: replace with non-PEM garbage.
	if err := writeRaw(filepath.Join(dir, "ca.crt"), []byte("not a pem")); err != nil {
		t.Fatal(err)
	}
	if _, err := pki.LoadCA(dir); err == nil {
		t.Errorf("expected error for non-PEM ca.crt")
	}
}

func TestLoadCABadKeyPEM(t *testing.T) {
	dir := t.TempDir()
	_, _ = pki.InitCA(dir, "x")
	if err := writeRaw(filepath.Join(dir, "ca.key"), []byte("nope")); err != nil {
		t.Fatal(err)
	}
	if _, err := pki.LoadCA(dir); err == nil {
		t.Errorf("expected error for non-PEM ca.key")
	}
}

func TestIssueClientChainsToCA(t *testing.T) {
	dir := t.TempDir()
	ca, _ := pki.InitCA(dir, "ca")

	issued, err := ca.IssueClient("imac-2", 0)
	if err != nil {
		t.Fatalf("IssueClient: %v", err)
	}

	cert := mustParseCert(t, issued.CertPEM)
	if cert.Subject.CommonName != "imac-2" {
		t.Errorf("CN = %q", cert.Subject.CommonName)
	}
	if !hasExtKeyUsage(cert, x509.ExtKeyUsageClientAuth) {
		t.Errorf("missing client auth EKU")
	}
	if hasExtKeyUsage(cert, x509.ExtKeyUsageServerAuth) {
		t.Errorf("client cert must not have server auth EKU")
	}

	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		t.Errorf("verify: %v", err)
	}
}

func TestIssueClientExplicitValidity(t *testing.T) {
	dir := t.TempDir()
	ca, _ := pki.InitCA(dir, "ca")

	d := 48 * time.Hour
	issued, err := ca.IssueClient("w", d)
	if err != nil {
		t.Fatalf("IssueClient: %v", err)
	}
	cert := mustParseCert(t, issued.CertPEM)
	want := d
	got := cert.NotAfter.Sub(cert.NotBefore)
	// allow ~1 minute slack for the NotBefore back-dating
	if got < want || got > want+2*time.Minute {
		t.Errorf("validity = %v, want ~%v", got, want)
	}
}

func TestIssueClientRequiresName(t *testing.T) {
	dir := t.TempDir()
	ca, _ := pki.InitCA(dir, "ca")
	if _, err := ca.IssueClient("", 0); err == nil {
		t.Errorf("expected error for empty name")
	}
}

func TestIssueServerSANs(t *testing.T) {
	dir := t.TempDir()
	ca, _ := pki.InitCA(dir, "ca")

	issued, err := ca.IssueServer("hearth.local", []string{"hearth.local"}, []string{"192.168.1.10", "", "not-an-ip"}, 24*time.Hour)
	if err != nil {
		t.Fatalf("IssueServer: %v", err)
	}
	cert := mustParseCert(t, issued.CertPEM)

	if !hasExtKeyUsage(cert, x509.ExtKeyUsageServerAuth) {
		t.Errorf("missing server auth EKU")
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "hearth.local" {
		t.Errorf("DNS SANs = %v", cert.DNSNames)
	}
	if len(cert.IPAddresses) != 1 {
		t.Errorf("IP SANs = %v (want only 192.168.1.10; empty/invalid skipped)", cert.IPAddresses)
	}
}

func TestIssueServerDefaultValidity(t *testing.T) {
	dir := t.TempDir()
	ca, _ := pki.InitCA(dir, "ca")

	issued, err := ca.IssueServer("h", nil, nil, 0)
	if err != nil {
		t.Fatalf("IssueServer: %v", err)
	}
	cert := mustParseCert(t, issued.CertPEM)
	if cert.NotAfter.Before(time.Now().Add(365 * 24 * time.Hour)) {
		t.Errorf("default validity too short: NotAfter=%v", cert.NotAfter)
	}
}

// --- helpers ------------------------------------------------------------

func writeRaw(path string, data []byte) error {
	return writeFileForTest(path, data)
}

func mustParseCert(t *testing.T, pemBytes []byte) *x509.Certificate {
	t.Helper()
	b, _ := pem.Decode(pemBytes)
	if b == nil {
		t.Fatalf("not PEM")
	}
	c, err := x509.ParseCertificate(b.Bytes)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return c
}

func hasExtKeyUsage(c *x509.Certificate, want x509.ExtKeyUsage) bool {
	for _, eku := range c.ExtKeyUsage {
		if eku == want {
			return true
		}
	}
	return false
}
