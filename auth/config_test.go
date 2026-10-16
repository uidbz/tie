package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestBuildStoreRejectsEmptyPassword is the §5.1 L18 guard: an account with no
// password authenticates anyone who names it.
func TestBuildStoreRejectsEmptyPassword(t *testing.T) {
	_, err := BuildStore([]ConfigUser{{Username: "ops", Password: "", Role: "write"}}, RoleNone)
	if err == nil {
		t.Fatal("an empty password was accepted")
	}

	// A whitespace-only password is the same hole.
	if _, err := BuildStore([]ConfigUser{{Username: "ops", Password: "  ", Role: "read"}}, RoleNone); err == nil {
		t.Error("a whitespace-only password was accepted")
	}
	// An empty username is not an account.
	if _, err := BuildStore([]ConfigUser{{Username: "", Password: "secret", Role: "read"}}, RoleNone); err == nil {
		t.Error("an empty username was accepted")
	}
}

// TestBuildStoreRejectsDuplicateUsername: the config used to be folded into a
// map, so the last entry silently won.
func TestBuildStoreRejectsDuplicateUsername(t *testing.T) {
	_, err := BuildStore([]ConfigUser{
		{Username: "ops", Password: "one", Role: "read"},
		{Username: "ops", Password: "two", Role: "write"},
	}, RoleNone)
	if err == nil {
		t.Fatal("a duplicate username was accepted silently")
	}

	// Case matters: two accounts differing only in case are still distinct, and
	// that is worth a warning-free pass.
	if _, err := BuildStore([]ConfigUser{
		{Username: "ops", Password: "one", Role: "read"},
		{Username: "Ops", Password: "two", Role: "write"},
	}, RoleNone); err != nil {
		t.Errorf("distinct usernames rejected: %v", err)
	}
}

// TestBuildStoreRejectsBadRole keeps role parsing in one place.
func TestBuildStoreRejectsBadRole(t *testing.T) {
	if _, err := BuildStore([]ConfigUser{{Username: "ops", Password: "x", Role: "admin"}}, RoleNone); err == nil {
		t.Error("an invalid role string was accepted")
	}
	s, err := BuildStore([]ConfigUser{{Username: "ops", Password: "x", Role: "read"}}, RoleWrite)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Resolve(basicRequest("ops", "x")); got != RoleWrite {
		t.Errorf("role = %v, want the anonymous floor to apply", got)
	}
}

// TestEmptyPasswordNeverAuthenticates is defense in depth: even a Store built
// directly (not through BuildStore) must not let a blank configured password
// match a request.
func TestEmptyPasswordNeverAuthenticates(t *testing.T) {
	s := NewStore(map[string]User{"guest": {Password: "", Role: RoleWrite}}, RoleNone)
	if got := s.Resolve(basicRequest("guest", "")); got != RoleNone {
		t.Errorf("an empty configured password authenticated as %v", got)
	}
	if ok, _ := s.Authorize(basicRequest("guest", ""), AccessWrite); ok {
		t.Error("Authorize accepted an empty-password account")
	}
}

func basicRequest(user, pass string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if user != "" || pass != "" {
		r.SetBasicAuth(user, pass)
	}
	return r
}
