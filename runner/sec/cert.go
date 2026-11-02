package sec

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// CertManager holds the live TLS keypair. Swaps are atomic, so pushing
// a renewed certificate needs no server restart (in-flight handshakes
// keep the old pair, new ones get the new pair) — long-running service
// containers never notice. Pass GetCertificate to tls.Config.
type CertManager struct {
	cur atomic.Value // *tls.Certificate
}

// Load reads a cert/key pair from disk and makes it live.
func (cm *CertManager) Load(certPath, keyPath string) error {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return err
	}
	cm.cur.Store(&cert)
	return nil
}

// GetCertificate is the tls.Config.GetCertificate hook.
func (cm *CertManager) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	v := cm.cur.Load()
	if v == nil {
		return nil, fmt.Errorf("no certificate loaded")
	}
	return v.(*tls.Certificate), nil
}

// CertResult reports what was installed.
type CertResult struct {
	Subject  string // leaf common name
	NotAfter string // UTC YYYY-MM-DD, for the operator's log line
}

// Install validates a PEM cert/key pair BEFORE touching disk (a
// mismatched or unparsable pair must leave the live cert untouched),
// backs up any existing files, writes both, then hot-swaps.
func (cm *CertManager) Install(certPEM, keyPEM, certPath, keyPath string) (*CertResult, error) {
	if certPath == "" || keyPath == "" {
		return nil, fmt.Errorf("no TLS cert/key paths configured")
	}
	pair, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("invalid cert/key pair: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return nil, fmt.Errorf("empty certificate chain")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("unparsable leaf: %w", err)
	}
	if b, err := os.ReadFile(certPath); err == nil {
		_ = os.WriteFile(certPath+".bak", b, 0o644)
	}
	if b, err := os.ReadFile(keyPath); err == nil {
		_ = os.WriteFile(keyPath+".bak", b, 0o600)
	}
	if err := os.WriteFile(certPath, []byte(certPEM), 0o644); err != nil {
		return nil, fmt.Errorf("writing %s: %w", certPath, err)
	}
	if err := os.WriteFile(keyPath, []byte(keyPEM), 0o600); err != nil {
		return nil, fmt.Errorf("writing %s: %w", keyPath, err)
	}
	// Hot swap only after both files are on disk.
	cm.cur.Store(&pair)
	return &CertResult{
		Subject:  leaf.Subject.CommonName,
		NotAfter: leaf.NotAfter.UTC().Format("2006-01-02"),
	}, nil
}

// CertNotAfter returns the PEM file's expiry in RFC3339, or "" when
// unset/unreadable/unparsable — the fleet's cert-expiry monitor input.
func CertNotAfter(certPath string) string {
	if certPath == "" {
		return ""
	}
	pemBytes, err := os.ReadFile(certPath)
	if err != nil {
		return ""
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return ""
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return ""
	}
	return cert.NotAfter.UTC().Format(time.RFC3339)
}
