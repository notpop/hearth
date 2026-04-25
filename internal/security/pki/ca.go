// Package pki manages the Hearth home-LAN certificate authority.
//
// The CA is rooted at a single Ed25519 keypair stored on the coordinator
// host. Worker enrollment produces a short-ish-lived (default 10 years —
// home network, low rotation cost) Ed25519 client certificate signed by
// the CA. The CA private key never leaves the coordinator filesystem.
//
// Why Ed25519: smaller keys, fast signing/verification, modern, no curve
// negotiation surprises. Crypto-agility-via-PEM is preserved (the on-disk
// format is PKCS#8) so the algorithm can be swapped without changing
// callers.
package pki

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const (
	caCertFile = "ca.crt"
	caKeyFile  = "ca.key"

	// defaultCAValidity bounds the CA cert. Long is fine: rotation in a home
	// LAN is voluntary, and the CA private key is the protected secret.
	defaultCAValidity = 10 * 365 * 24 * time.Hour
	// defaultClientValidity bounds an issued worker certificate. Re-enrol
	// every few years is no real burden but keeps blast radius reasonable
	// if a worker key leaks.
	defaultClientValidity = 5 * 365 * 24 * time.Hour
)

// CA is a loaded certificate authority.
type CA struct {
	Cert    *x509.Certificate
	Key     ed25519.PrivateKey
	CertPEM []byte
	dir     string
}

// InitCA creates a fresh CA at dir. It is an error if dir already contains
// a CA — callers must explicitly delete an old CA before re-initialising.
func InitCA(dir, commonName string) (*CA, error) {
	if commonName == "" {
		commonName = "hearth-home-ca"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(dir, caCertFile)); err == nil {
		return nil, fmt.Errorf("pki: CA already exists at %s", dir)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"Hearth"}},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(defaultCAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	if err := writeFile(filepath.Join(dir, caCertFile), certPEM, 0o644); err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(dir, caKeyFile), keyPEM, 0o600); err != nil {
		return nil, err
	}
	return &CA{Cert: cert, Key: priv, CertPEM: certPEM, dir: dir}, nil
}

// LoadCA reads an existing CA from dir.
func LoadCA(dir string) (*CA, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, caCertFile))
	if err != nil {
		return nil, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, caKeyFile))
	if err != nil {
		return nil, err
	}

	cBlock, _ := pem.Decode(certPEM)
	if cBlock == nil {
		return nil, errors.New("pki: ca.crt is not PEM")
	}
	cert, err := x509.ParseCertificate(cBlock.Bytes)
	if err != nil {
		return nil, err
	}

	kBlock, _ := pem.Decode(keyPEM)
	if kBlock == nil {
		return nil, errors.New("pki: ca.key is not PEM")
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(kBlock.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := keyAny.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("pki: unexpected CA key type %T", keyAny)
	}
	return &CA{Cert: cert, Key: key, CertPEM: certPEM, dir: dir}, nil
}

// IssuedClient is the result of issuing a worker certificate.
type IssuedClient struct {
	Name    string
	CertPEM []byte
	KeyPEM  []byte
}

// IssueClient signs a fresh client certificate for the named worker. The
// returned KeyPEM is the only copy of the private key — callers must hand
// it directly into the bundle and not write it elsewhere.
func (ca *CA) IssueClient(name string, validity time.Duration) (IssuedClient, error) {
	if name == "" {
		return IssuedClient{}, errors.New("pki: empty worker name")
	}
	if validity <= 0 {
		validity = defaultClientValidity
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return IssuedClient{}, err
	}
	serial, err := randomSerial()
	if err != nil {
		return IssuedClient{}, err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name, Organization: []string{"Hearth Worker"}},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, pub, ca.Key)
	if err != nil {
		return IssuedClient{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return IssuedClient{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return IssuedClient{Name: name, CertPEM: certPEM, KeyPEM: keyPEM}, nil
}

// IssueServer signs a server certificate for the coordinator. dnsNames
// should include "<host>.local" (mDNS) plus any IP-literal SANs needed.
func (ca *CA) IssueServer(commonName string, dnsNames []string, ips []string, validity time.Duration) (IssuedClient, error) {
	if validity <= 0 {
		validity = defaultClientValidity
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return IssuedClient{}, err
	}
	serial, err := randomSerial()
	if err != nil {
		return IssuedClient{}, err
	}
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName, Organization: []string{"Hearth Coordinator"}},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
	}
	for _, ip := range ips {
		if parsed := parseIP(ip); parsed != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, parsed)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, pub, ca.Key)
	if err != nil {
		return IssuedClient{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return IssuedClient{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return IssuedClient{Name: commonName, CertPEM: certPEM, KeyPEM: keyPEM}, nil
}

func randomSerial() (*big.Int, error) {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, max)
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	return os.WriteFile(path, data, mode)
}
