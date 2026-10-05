package db

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// TLS builds a *tls.Config for mode; nil means TLS is off. host is the
// name verified in verify-full mode (ServerName overrides it).
func (t TLSConfig) TLS(host string) (*tls.Config, error) {
	if !t.Enabled() {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if t.ServerName != "" {
		host = t.ServerName
	}
	if t.CAFile != "" {
		pem, err := os.ReadFile(t.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("CA file holds no PEM certificate")
		}
		cfg.RootCAs = pool
	}
	if t.CertFile != "" || t.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	mode := t.Mode
	if mode == "require" && t.CAFile != "" {
		mode = "verify-ca" // libpq semantics: a root CA turns require into verify-ca
	}
	switch mode {
	case "require":
		cfg.InsecureSkipVerify = true
	case "verify-ca":
		// verify the chain but not the host name
		cfg.InsecureSkipVerify = true
		roots := cfg.RootCAs
		cfg.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
			if len(raw) == 0 {
				return errors.New("server sent no certificate")
			}
			certs := make([]*x509.Certificate, len(raw))
			for i, r := range raw {
				c, err := x509.ParseCertificate(r)
				if err != nil {
					return err
				}
				certs[i] = c
			}
			inter := x509.NewCertPool()
			for _, c := range certs[1:] {
				inter.AddCert(c)
			}
			_, err := certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter})
			return err
		}
	case "verify-full":
		cfg.ServerName = host
	default:
		return nil, fmt.Errorf("unknown TLS mode %q", t.Mode)
	}
	return cfg, nil
}
