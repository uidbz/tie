// Package auth provides the role-based access model shared by tie-triplestore and
// tie-filehost: read/write roles, per-request access classification, and HTTP
// Basic Auth resolution with constant-time password comparison.
//
// Passwords are stored in plaintext in each server's config (operator-managed
// secrecy); only the wire comparison is hardened. A Store is read-only after
// construction and safe for concurrent use.
package auth

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
)

// Role is an access tier. Roles are totally ordered (RoleNone < RoleRead <
// RoleWrite) and a higher role subsumes every lower one: write implies read.
type Role int

const (
	RoleNone Role = iota
	RoleRead
	RoleWrite
)

// Access classifies a request by the tier it requires.
type Access int

const (
	AccessRead Access = iota
	AccessWrite
)

// Allows reports whether a role is sufficient for an access level.
func (r Role) Allows(a Access) bool {
	switch a {
	case AccessRead:
		return r >= RoleRead
	case AccessWrite:
		return r >= RoleWrite
	default:
		return false
	}
}

// ParseUserRole parses a per-user Role config value. An empty string defaults to
// RoleWrite so accounts predating roles keep full read+write access.
func ParseUserRole(s string) (Role, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "write", "readwrite", "rw":
		return RoleWrite, nil
	case "read", "readonly", "ro":
		return RoleRead, nil
	default:
		return RoleNone, fmt.Errorf("auth: invalid user role %q (want \"read\" or \"write\")", s)
	}
}

// ParseAnonAccess parses the AnonymousAccess config value — the role granted to a
// request that presents no valid credentials. An empty string returns def, the
// per-server default.
func ParseAnonAccess(s string, def Role) (Role, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return def, nil
	case "none":
		return RoleNone, nil
	case "read", "readonly", "ro":
		return RoleRead, nil
	case "write", "readwrite", "rw":
		return RoleWrite, nil
	default:
		return RoleNone, fmt.Errorf("auth: invalid AnonymousAccess %q (want \"none\", \"read\" or \"write\")", s)
	}
}

// User is a resolved account: its plaintext password and granted role.
type User struct {
	Password string
	Role     Role
}

// Store resolves a request's role from HTTP Basic Auth credentials.
type Store struct {
	users map[string]User
	anon  Role
}

// NewStore builds a Store from a username->User map and the anonymous role
// granted to unauthenticated or invalid requests.
func NewStore(users map[string]User, anon Role) *Store {
	return &Store{users: users, anon: anon}
}

// resolve returns the role a request is granted and whether it authenticated as
// a known user. Valid credentials yield max(user role, anonymous role) — the
// anonymous role is a floor available to everyone, so a logged-in user is never
// worse off than an anonymous one. Anything else (no credentials, unknown user,
// wrong password) yields the anonymous role and authed=false. The password
// comparison is constant-time and runs even for unknown users, so response
// timing does not reveal which usernames exist.
func (s *Store) resolve(r *http.Request) (role Role, authed bool) {
	username, password, hasAuth := r.BasicAuth()
	if !hasAuth {
		return s.anon, false
	}
	u, ok := s.users[username] // zero User (empty password) for unknown usernames
	// Compare either way so timing does not depend on username existence.
	match := subtle.ConstantTimeCompare([]byte(password), []byte(u.Password)) == 1
	if ok && match {
		if u.Role > s.anon {
			return u.Role, true
		}
		return s.anon, true
	}
	return s.anon, false
}

// Resolve returns the role a request is authorized as (see resolve).
func (s *Store) Resolve(r *http.Request) Role {
	role, _ := s.resolve(r)
	return role
}

// Authorize reports whether a request may perform access a. When it may not,
// status is the HTTP status to return: 401 when authentication is missing or
// failed (no credentials, unknown user, wrong password), or 403 when a valid
// user authenticated but its role is too low.
func (s *Store) Authorize(r *http.Request, a Access) (ok bool, status int) {
	role, authed := s.resolve(r)
	if role.Allows(a) {
		return true, 0
	}
	if authed {
		return false, http.StatusForbidden
	}
	return false, http.StatusUnauthorized
}

// Require wraps h so it runs only when the request satisfies the access level.
// A 401 rejection carries a Basic-Auth challenge.
func (s *Store) Require(a Access, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ok, status := s.Authorize(r, a)
		if ok {
			h(w, r)
			return
		}
		if status == http.StatusUnauthorized {
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
		}
		http.Error(w, http.StatusText(status), status)
	}
}
