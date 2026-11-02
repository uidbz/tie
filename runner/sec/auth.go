// Package sec holds the runner's access-control primitives: the Basic
// Auth gates (a configurable auth user, per-image deploy keys, an admin
// token) and the hot-swappable TLS certificate manager.
package sec

import (
	"fmt"
	"net/http"

	"github.com/uidbz/tie/runner/images"
)

// Auth configures the Basic Auth vocabulary. User is resolved per
// request so a hot-reloaded change applies immediately; the default
// matches new deployments, while existing fleets keep whatever user
// name their clients already send.
type Auth struct {
	User func() string
}

func (a *Auth) user() string {
	if a != nil && a.User != nil {
		if u := a.User(); u != "" {
			return u
		}
	}
	return "tie"
}

// ValidateDeployKey reports whether the given Basic Auth credentials
// match the DeployKey of the named image. It gates service management
// and job cancellation (a job is authorized by the DeployKey of its
// handler image).
func (a *Auth) ValidateDeployKey(reg *images.Registry, imageName, user, pass string, ok bool) bool {
	return a.deployKeyFailure(reg, imageName, user, pass, ok) == ""
}

// DeployKeyGate is the Basic Auth middleware for routes that name an
// image ({service} for service lifecycle or {image} for image deletion):
// the password must be that image's DeployKey. 401 distinguishes "wrong
// key" from "not this runner's image" so the reply is actionable; the
// status stays 401 for older-CLI compat.
func (a *Auth) DeployKeyGate(reg *images.Registry) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			user, pass, ok := r.BasicAuth()
			name := r.PathValue("service")
			if name == "" {
				name = r.PathValue("image")
			}
			msg := a.deployKeyFailure(reg, name, user, pass, ok)
			if msg != "" {
				w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
				http.Error(w, msg, http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}
}

// deployKeyFailure returns "" when authorized, else the 401 message.
func (a *Auth) deployKeyFailure(reg *images.Registry, name, user, pass string, ok bool) string {
	if !ok || user != a.user() {
		return "Unauthorized: wrong DeployKey"
	}
	img, found := reg.Get(name)
	if !found {
		return "Unauthorized: image not deployed on this runner"
	}
	if pass != img.DeployKey {
		return "Unauthorized: wrong DeployKey"
	}
	return ""
}

// AdminGate guards the admin surface (reload, fleet registry, cert push):
// Basic Auth with the admin user and the token. An unset token disables
// the endpoints entirely (403), never open access.
func (a *Auth) AdminGate(token func() string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			tok := ""
			if token != nil {
				tok = token()
			}
			if tok == "" {
				http.Error(w, "admin access disabled: no admin token configured", http.StatusForbidden)
				return
			}
			user, pass, ok := r.BasicAuth()
			if !ok || user != a.user() || pass != tok {
				w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}
}

// GetBaseURL derives a runner's own base URL from a request: the
// DeployManager stamp on jobs and the self entry of fleet listings.
// Faithful to the wire rule: scheme comes from the connection, host from
// the request — deployments that terminate TLS in front stamp what the
// runner itself sees, and status forwarding compares like against like.
func GetBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s", scheme, r.Host)
}
