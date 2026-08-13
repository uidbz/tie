package auth

import (
	"net/http"
	"testing"
)

func TestRoleAllows(t *testing.T) {
	cases := []struct {
		role   Role
		access Access
		want   bool
	}{
		{RoleNone, AccessRead, false},
		{RoleNone, AccessWrite, false},
		{RoleRead, AccessRead, true},
		{RoleRead, AccessWrite, false},
		{RoleWrite, AccessRead, true},
		{RoleWrite, AccessWrite, true},
	}
	for _, c := range cases {
		if got := c.role.Allows(c.access); got != c.want {
			t.Errorf("Role(%d).Allows(%d) = %v, want %v", c.role, c.access, got, c.want)
		}
	}
}

func TestParseUserRole(t *testing.T) {
	cases := []struct {
		in      string
		want    Role
		wantErr bool
	}{
		{"", RoleWrite, false},
		{"write", RoleWrite, false},
		{"readwrite", RoleWrite, false},
		{"read", RoleRead, false},
		{"readonly", RoleRead, false},
		{" READ ", RoleRead, false},
		{"none", RoleNone, true},
		{"bogus", RoleNone, true},
	}
	for _, c := range cases {
		got, err := ParseUserRole(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseUserRole(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
		}
		if !c.wantErr && got != c.want {
			t.Errorf("ParseUserRole(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseAnonAccess(t *testing.T) {
	cases := []struct {
		in      string
		def     Role
		want    Role
		wantErr bool
	}{
		{"", RoleNone, RoleNone, false},
		{"", RoleWrite, RoleWrite, false},
		{"none", RoleWrite, RoleNone, false},
		{"read", RoleNone, RoleRead, false},
		{"write", RoleNone, RoleWrite, false},
		{"bogus", RoleWrite, RoleNone, true},
	}
	for _, c := range cases {
		got, err := ParseAnonAccess(c.in, c.def)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseAnonAccess(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
		}
		if !c.wantErr && got != c.want {
			t.Errorf("ParseAnonAccess(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func newReq(t *testing.T, user, pass string, withAuth bool) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if withAuth {
		r.SetBasicAuth(user, pass)
	}
	return r
}

func TestStoreResolve(t *testing.T) {
	store := NewStore(map[string]User{
		"writer": {Password: "wpass", Role: RoleWrite},
		"reader": {Password: "rpass", Role: RoleRead},
	}, RoleRead)

	cases := []struct {
		name       string
		user, pass string
		withAuth   bool
		want       Role
	}{
		{"valid writer", "writer", "wpass", true, RoleWrite},
		{"valid reader", "reader", "rpass", true, RoleRead},
		{"wrong password falls to anon", "writer", "nope", true, RoleRead},
		{"unknown user falls to anon", "ghost", "x", true, RoleRead},
		{"no credentials falls to anon", "", "", false, RoleRead},
	}
	for _, c := range cases {
		got := store.Resolve(newReq(t, c.user, c.pass, c.withAuth))
		if got != c.want {
			t.Errorf("%s: Resolve = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestStoreAuthorizeStatus(t *testing.T) {
	store := NewStore(map[string]User{
		"writer": {Password: "wpass", Role: RoleWrite},
		"reader": {Password: "rpass", Role: RoleRead},
	}, RoleRead) // anonymous may read

	cases := []struct {
		name       string
		user, pass string
		withAuth   bool
		access     Access
		wantOK     bool
		wantStatus int
	}{
		{"anon read allowed", "", "", false, AccessRead, true, 0},
		{"anon write -> 401", "", "", false, AccessWrite, false, http.StatusUnauthorized},
		{"wrong password write -> 401", "writer", "nope", true, AccessWrite, false, http.StatusUnauthorized},
		{"unknown user write -> 401", "ghost", "x", true, AccessWrite, false, http.StatusUnauthorized},
		{"valid reader write -> 403", "reader", "rpass", true, AccessWrite, false, http.StatusForbidden},
		{"valid writer write -> ok", "writer", "wpass", true, AccessWrite, true, 0},
	}
	for _, c := range cases {
		ok, status := store.Authorize(newReq(t, c.user, c.pass, c.withAuth), c.access)
		if ok != c.wantOK || status != c.wantStatus {
			t.Errorf("%s: Authorize = (%v, %d), want (%v, %d)", c.name, ok, status, c.wantOK, c.wantStatus)
		}
	}
}

// A logged-in user is never worse off than an anonymous one: the anonymous role
// is a floor.
func TestStoreAnonFloor(t *testing.T) {
	store := NewStore(map[string]User{
		"reader": {Password: "rpass", Role: RoleRead},
	}, RoleWrite) // anonymous may write
	if got := store.Resolve(newReq(t, "reader", "rpass", true)); got != RoleWrite {
		t.Errorf("authenticated reader with anon=write: Resolve = %d, want RoleWrite (floor)", got)
	}
}

func TestStoreResolveAnonNone(t *testing.T) {
	store := NewStore(map[string]User{"writer": {Password: "wpass", Role: RoleWrite}}, RoleNone)
	if got := store.Resolve(newReq(t, "", "", false)); got != RoleNone {
		t.Errorf("anonymous Resolve = %d, want RoleNone", got)
	}
	if got := store.Resolve(newReq(t, "writer", "wpass", true)); got != RoleWrite {
		t.Errorf("writer Resolve = %d, want RoleWrite", got)
	}
}
