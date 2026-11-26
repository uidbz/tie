package sec

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestUpgradableListenerSwitchesInPlace pins the cert-push flip: the same
// listener and http.Server serve plain HTTP, then HTTPS after EnableTLS.
// Starting a second (TLS) server on the same address used to fail with
// "address already in use" and exit the process.
func TestUpgradableListenerSwitchesInPlace(t *testing.T) {
	// httptest's self-signed cert stands in for a pushed one.
	ts := httptest.NewUnstartedServer(nil)
	cert := ts.TLS
	if cert == nil {
		ts.StartTLS()
		cert = ts.TLS
		ts.Close()
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ul := NewUpgradableListener(ln, &tls.Config{Certificates: cert.Certificates})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			io.WriteString(w, "tls")
			return
		}
		io.WriteString(w, "plain")
	})}
	go srv.Serve(ul)
	t.Cleanup(func() { srv.Close() })
	addr := ln.Addr().String()

	get := func(c *http.Client, url string) (string, error) {
		resp, err := c.Get(url)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return string(b), err
	}
	plain := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	if got, err := get(plain, "http://"+addr); err != nil || got != "plain" {
		t.Fatalf("before switch: %q, %v", got, err)
	}
	if !ul.EnableTLS() || ul.EnableTLS() || !ul.TLS() {
		t.Fatal("EnableTLS should switch exactly once")
	}
	secure := &http.Client{Transport: &http.Transport{DisableKeepAlives: true,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	if got, err := get(secure, "https://"+addr); err != nil || got != "tls" {
		t.Fatalf("after switch: %q, %v", got, err)
	}
}
