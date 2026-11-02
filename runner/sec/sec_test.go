package sec

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/uidbz/tie/runner/images"
)

func authReg() *images.Registry {
	return images.New(nil, images.Image{Name: "tool:v2", DeployKey: "s3cret"})
}

func TestDeployKeyGate(t *testing.T) {
	mux := http.NewServeMux()
	a := &Auth{}
	mux.HandleFunc("POST /api/services/{service}", a.DeployKeyGate(authReg())(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	mux.HandleFunc("DELETE /api/images/{image}", a.DeployKeyGate(authReg())(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	call := func(method, path, user, pass string, auth bool) (int, string) {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		if auth {
			req.SetBasicAuth(user, pass)
		}
		rec := httptest.NewRecorder()
		srv.Config.Handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	if code, body := call(http.MethodPost, "/api/services/tool:v2", "tie", "s3cret", true); code != 200 || body != "ok" {
		t.Errorf("valid key: %d %q", code, body)
	}
	if code, body := call(http.MethodPost, "/api/services/tool:v2", "tie", "wrong", true); code != 401 || body != "Unauthorized: wrong DeployKey\n" {
		t.Errorf("wrong key: %d %q", code, body)
	}
	if code, body := call(http.MethodPost, "/api/services/ghost:v1", "tie", "s3cret", true); code != 401 || body != "Unauthorized: image not deployed on this runner\n" {
		t.Errorf("unknown image: %d %q", code, body)
	}
	if code, _ := call(http.MethodPost, "/api/services/tool:v2", "", "", false); code != 401 {
		t.Errorf("no auth header: %d", code)
	}
	if code, _ := call(http.MethodPost, "/api/services/tool:v2", "someone-else", "s3cret", true); code != 401 {
		t.Errorf("wrong user: %d", code)
	}
	if code, body := call(http.MethodDelete, "/api/images/tool:v2", "tie", "s3cret", true); code != 200 || body != "ok" {
		t.Errorf("image path: %d %q", code, body)
	}

	// A deployment can run under a different auth user; the gate follows.
	custom := &Auth{User: func() string { return "legacy" }}
	mux2 := http.NewServeMux()
	mux2.HandleFunc("POST /api/services/{service}", custom.DeployKeyGate(authReg())(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	req, _ := http.NewRequest(http.MethodPost, "http://x/api/services/tool:v2", nil)
	req.SetBasicAuth("legacy", "s3cret")
	rec := httptest.NewRecorder()
	mux2.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("custom auth user: %d %q", rec.Code, rec.Body.String())
	}

	// ValidateDeployKey matches the gate (cancel path uses it directly).
	if !custom.ValidateDeployKey(authReg(), "tool:v2", "legacy", "s3cret", true) {
		t.Error("ValidateDeployKey must agree with the gate")
	}
	if custom.ValidateDeployKey(authReg(), "tool:v2", "tie", "s3cret", true) {
		t.Error("default user must not pass under a custom auth user")
	}
}

func TestAdminGate(t *testing.T) {
	token := "admintok"
	gate := (&Auth{}).AdminGate(func() string { return token })(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	badTokenGate := (&Auth{}).AdminGate(func() string { return "" })(func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not run") })

	do := func(h http.HandlerFunc, user, pass string, auth bool) int {
		req, _ := http.NewRequest(http.MethodPost, "http://x/api/reload", nil)
		if auth {
			req.SetBasicAuth(user, pass)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := do(gate, "tie", token, true); code != 200 {
		t.Errorf("valid admin: %d", code)
	}
	if code := do(gate, "tie", "nope", true); code != 401 {
		t.Errorf("wrong token: %d", code)
	}
	if code := do(gate, "", "", false); code != 401 {
		t.Errorf("no auth: %d", code)
	}
	if code := do(badTokenGate, "tie", "whatever", true); code != 403 {
		t.Errorf("unset token must disable with 403, got %d", code)
	}
}

// selfSignedPEM mints a throwaway cert/key pair for the cert tests.
func selfSignedPEM(t *testing.T, cn string, notAfter time.Time) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, key.Public(), key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

func TestCertInstallAndSwap(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "tls.crt")
	keyPath := filepath.Join(dir, "tls.key")

	cm := &CertManager{}
	first, firstKey := selfSignedPEM(t, "first", time.Now().Add(90*24*time.Hour))
	res, err := cm.Install(string(first), string(firstKey), certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if res.Subject != "first" {
		t.Errorf("subject = %q", res.Subject)
	}
	if b, _ := os.ReadFile(certPath); string(b) != string(first) {
		t.Error("cert file not written")
	}
	if fi, err := os.Stat(keyPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("key file mode: %v %v", fi, err)
	}
	cert, err := cm.GetCertificate(nil)
	if err != nil || cert == nil {
		t.Fatalf("GetCertificate: %v", err)
	}

	// A bad pair must be refused BEFORE touching anything: the live cert
	// and the files on disk stay as they were.
	_, err = cm.Install("garbage", string(firstKey), certPath, keyPath)
	if err == nil {
		t.Fatal("bad pair accepted")
	}
	if b, _ := os.ReadFile(certPath); string(b) != string(first) {
		t.Error("bad install touched the cert file")
	}
	if cert2, _ := cm.GetCertificate(nil); cert2 != cert {
		t.Error("bad install swapped the live cert")
	}

	// A good renewal hot-swaps and backs up the old files.
	second, secondKey := selfSignedPEM(t, "second", time.Now().Add(180*24*time.Hour))
	res, err = cm.Install(string(second), string(secondKey), certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if res.Subject != "second" {
		t.Errorf("renewal subject = %q", res.Subject)
	}
	if b, _ := os.ReadFile(certPath + ".bak"); string(b) != string(first) {
		t.Error("old cert not backed up")
	}
	if b, _ := os.ReadFile(keyPath + ".bak"); string(b) != string(firstKey) {
		t.Error("old key not backed up")
	}
	cert2, _ := cm.GetCertificate(nil)
	if cert2 == cert {
		t.Error("live cert not swapped")
	}

	if notAfter := CertNotAfter(certPath); notAfter == "" {
		t.Error("CertNotAfter empty on a real cert")
	} else if _, err := time.Parse(time.RFC3339, notAfter); err != nil {
		t.Errorf("CertNotAfter not RFC3339: %q", notAfter)
	}
	if CertNotAfter(filepath.Join(dir, "missing.crt")) != "" {
		t.Error("missing cert must read as empty")
	}
	if CertNotAfter("") != "" {
		t.Error("unset path must read as empty")
	}
}

func TestLoadAndBaseURL(t *testing.T) {
	cm := &CertManager{}
	if _, err := cm.GetCertificate(nil); err == nil {
		t.Error("no cert loaded must error")
	}
	certPEM, keyPEM := selfSignedPEM(t, "disk", time.Now().Add(24*time.Hour))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "c.pem"), certPEM, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "k.pem"), keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cm.Load(filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")); err != nil {
		t.Fatal(err)
	}
	if _, err := cm.GetCertificate(nil); err != nil {
		t.Fatalf("after Load: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://runner:1163/api/run", nil)
	if got := GetBaseURL(req); got != "http://runner:1163" {
		t.Errorf("GetBaseURL = %q", got)
	}
}
