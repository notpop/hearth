package pki

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
)

// ServerTLSConfig builds a server-side mTLS config: presents serverCert,
// requires + verifies client certs against this CA.
func (ca *CA) ServerTLSConfig(serverCert IssuedClient) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(serverCert.CertPEM, serverCert.KeyPEM)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca.CertPEM) {
		return nil, errors.New("pki: invalid CA PEM")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// ClientTLSConfig builds a client-side mTLS config from raw PEM bytes.
// caCertPEM verifies the server. clientCert/Key authenticate this client.
// serverName is the SNI / hostname expected from the server certificate.
func ClientTLSConfig(caCertPEM, clientCertPEM, clientKeyPEM []byte, serverName string) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCertPEM) {
		return nil, errors.New("pki: invalid CA PEM")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		ServerName:   serverName,
		MinVersion:   tls.VersionTLS13,
	}, nil
}
