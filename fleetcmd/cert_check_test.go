package fleetcmd

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"
)

func testCert(t *testing.T, names ...string) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: names[0]},
		DNSNames: names, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
}

// #166: a cert is pushed only to hosts it is valid for, checked for every
// target before anything is sent.
func TestCheckCertForHosts(t *testing.T) {
	crt, key := testCert(t, "runner1.example")
	hosts := []Host{{Name: "runner1", API: "https://runner1.example:1163"}, {Name: "runner2", API: "https://runner2.example"}}
	if err := checkCertForHosts(crt, key, hosts[:1], false); err != nil {
		t.Fatalf("matching host refused: %v", err)
	}
	err := checkCertForHosts(crt, key, hosts, false)
	if err == nil || !strings.Contains(err.Error(), "runner2") || strings.Contains(err.Error(), "runner1 (") {
		t.Fatalf("mismatch not reported precisely: %v", err)
	}
	if err := checkCertForHosts(crt, key, hosts, true); err != nil {
		t.Fatalf("--skip-name-check: %v", err)
	}
	wild, wkey := testCert(t, "*.example")
	if err := checkCertForHosts(wild, wkey, hosts, false); err != nil {
		t.Fatalf("wildcard cert refused: %v", err)
	}
	_, otherKey := testCert(t, "runner1.example")
	if err := checkCertForHosts(crt, otherKey, hosts[:1], true); err == nil {
		t.Fatal("mismatched key accepted")
	}
}

// #166: cert push without a target is a usage error, not "every host".
func TestCertPushNeedsTarget(t *testing.T) {
	crt, key := testCert(t, "runner1.example")
	dir := t.TempDir()
	writeFile := func(name string, b []byte) string {
		p := dir + "/" + name
		if err := writeTestFile(p, b); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cf := writeFile("c.pem", crt)
	kf := writeFile("k.pem", key)
	brand = TieBrand
	if err := runCert([]string{"push", "--crt", cf, "--key", kf}); err == nil || !strings.Contains(err.Error(), "--only HOST or --all") {
		t.Fatalf("push without target: %v", err)
	}
}

func writeTestFile(p string, b []byte) error { return os.WriteFile(p, b, 0o600) }
