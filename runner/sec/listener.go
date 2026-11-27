package sec

import (
	"crypto/tls"
	"net"
	"sync/atomic"
)

// UpgradableListener serves plain TCP until EnableTLS is called, then
// wraps every newly accepted connection in TLS. A runner that starts
// without a certificate can therefore switch to HTTPS when one is pushed
// (POST /api/cert) on the same address and the same http.Server: no
// second listener competing for the port, and no restart. Connections
// accepted before the switch stay plain until they close.
type UpgradableListener struct {
	net.Listener
	cfg *tls.Config
	on  atomic.Bool
}

// NewUpgradableListener wraps ln; cfg is used for connections accepted
// after EnableTLS (typically with GetCertificate from a CertManager).
func NewUpgradableListener(ln net.Listener, cfg *tls.Config) *UpgradableListener {
	return &UpgradableListener{Listener: ln, cfg: cfg}
}

// EnableTLS switches new connections to TLS. It reports whether this
// call did the switch (false when TLS was already on).
func (l *UpgradableListener) EnableTLS() bool { return !l.on.Swap(true) }

// TLS reports whether new connections are wrapped in TLS.
func (l *UpgradableListener) TLS() bool { return l.on.Load() }

// Accept implements net.Listener.
func (l *UpgradableListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil || !l.on.Load() {
		return c, err
	}
	return tls.Server(c, l.cfg), nil
}
