package auth

import (
	"testing"
)

// mockUser implements Authenticatable for testing
type mockUser struct {
	id            interface{}
	password      string
	rememberToken string
	roles         []string
}

func (u *mockUser) GetAuthIdentifier() interface{} { return u.id }
func (u *mockUser) GetAuthPassword() string        { return u.password }
func (u *mockUser) GetRememberToken() string       { return u.rememberToken }
func (u *mockUser) SetRememberToken(token string)  { u.rememberToken = token }

// mockTeam for policy testing
type mockTeam struct {
	ID      int
	OwnerID interface{}
}

// mockPost for policy testing
type mockPost struct {
	ID       int
	AuthorID interface{}
}

func TestGate_Define(t *testing.T) {
	gate := NewGate()

	gate.Define("edit-post", func(user Authenticatable, args ...interface{}) bool {
		if len(args) == 0 {
			return false
		}
		post, ok := args[0].(*mockPost)
		if !ok {
			return false
		}
		return user.GetAuthIdentifier() == post.AuthorID
	})

	user := &mockUser{id: 1}
	ownPost := &mockPost{ID: 1, AuthorID: 1}
	otherPost := &mockPost{ID: 2, AuthorID: 2}

	if !gate.Allows(user, "edit-post", ownPost) {
		t.Error("expected user to be allowed to edit own post")
	}

	if gate.Allows(user, "edit-post", otherPost) {
		t.Error("expected user to be denied editing other's post")
	}
}

func TestGate_Denies(t *testing.T) {
	gate := NewGate()

	gate.Define("admin-action", func(user Authenticatable, args ...interface{}) bool {
		return false // always deny
	})

	user := &mockUser{id: 1}

	if !gate.Denies(user, "admin-action") {
		t.Error("expected Denies to return true for denied action")
	}
}

func TestGate_Check(t *testing.T) {
	gate := NewGate()

	gate.Define("read", func(user Authenticatable, args ...interface{}) bool {
		return true
	})
	gate.Define("write", func(user Authenticatable, args ...interface{}) bool {
		return true
	})
	gate.Define("delete", func(user Authenticatable, args ...interface{}) bool {
		return false
	})

	user := &mockUser{id: 1}

	if !gate.Check(user, []string{"read", "write"}) {
		t.Error("expected Check to pass for allowed abilities")
	}

	if gate.Check(user, []string{"read", "delete"}) {
		t.Error("expected Check to fail when one ability is denied")
	}
}

func TestGate_Any(t *testing.T) {
	gate := NewGate()

	gate.Define("read", func(user Authenticatable, args ...interface{}) bool {
		return false
	})
	gate.Define("write", func(user Authenticatable, args ...interface{}) bool {
		return true
	})

	user := &mockUser{id: 1}

	if !gate.Any(user, []string{"read", "write"}) {
		t.Error("expected Any to pass when at least one ability is allowed")
	}

	if gate.Any(user, []string{"read"}) {
		t.Error("expected Any to fail when no abilities are allowed")
	}
}

func TestGate_Before(t *testing.T) {
	gate := NewGate()

	// Admin bypass
	allowTrue := true
	gate.Before(func(user Authenticatable, ability string, args ...interface{}) *bool {
		if u, ok := user.(*mockUser); ok {
			for _, role := range u.roles {
				if role == "admin" {
					return &allowTrue
				}
			}
		}
		return nil
	})

	gate.Define("restricted", func(user Authenticatable, args ...interface{}) bool {
		return false
	})

	regularUser := &mockUser{id: 1, roles: []string{"user"}}
	adminUser := &mockUser{id: 2, roles: []string{"admin"}}

	if gate.Allows(regularUser, "restricted") {
		t.Error("expected regular user to be denied")
	}

	if !gate.Allows(adminUser, "restricted") {
		t.Error("expected admin user to bypass restriction")
	}
}

func TestGate_After(t *testing.T) {
	gate := NewGate()

	var afterCalled bool
	gate.After(func(user Authenticatable, ability string, result bool, args ...interface{}) bool {
		afterCalled = true
		return result
	})

	gate.Define("test", func(user Authenticatable, args ...interface{}) bool {
		return true
	})

	user := &mockUser{id: 1}
	gate.Allows(user, "test")

	if !afterCalled {
		t.Error("expected after callback to be called")
	}
}

func TestGate_RoleChecker(t *testing.T) {
	gate := NewGate()

	gate.SetRoleChecker(func(user Authenticatable, role string) bool {
		if u, ok := user.(*mockUser); ok {
			for _, r := range u.roles {
				if r == role {
					return true
				}
			}
		}
		return false
	})

	user := &mockUser{id: 1, roles: []string{"admin", "editor"}}

	if !gate.HasRole(user, "admin") {
		t.Error("expected user to have admin role")
	}

	if gate.HasRole(user, "superadmin") {
		t.Error("expected user to not have superadmin role")
	}
}

func TestGate_HasAnyRole(t *testing.T) {
	gate := NewGate()

	gate.SetRoleChecker(func(user Authenticatable, role string) bool {
		if u, ok := user.(*mockUser); ok {
			for _, r := range u.roles {
				if r == role {
					return true
				}
			}
		}
		return false
	})

	user := &mockUser{id: 1, roles: []string{"editor"}}

	if !gate.HasAnyRole(user, "admin", "editor") {
		t.Error("expected user to have at least one role")
	}

	if gate.HasAnyRole(user, "admin", "superadmin") {
		t.Error("expected user to not have any of the roles")
	}
}

func TestGate_HasAllRoles(t *testing.T) {
	gate := NewGate()

	gate.SetRoleChecker(func(user Authenticatable, role string) bool {
		if u, ok := user.(*mockUser); ok {
			for _, r := range u.roles {
				if r == role {
					return true
				}
			}
		}
		return false
	})

	user := &mockUser{id: 1, roles: []string{"admin", "editor"}}

	if !gate.HasAllRoles(user, "admin", "editor") {
		t.Error("expected user to have all roles")
	}

	if gate.HasAllRoles(user, "admin", "superadmin") {
		t.Error("expected user to not have all roles")
	}
}

func TestPolicy_Authorize(t *testing.T) {
	gate := NewGate()

	// Register a team policy
	teamPolicy := PolicyFunc(func(user Authenticatable, action string, resource interface{}) bool {
		team, ok := resource.(*mockTeam)
		if !ok {
			return false
		}

		switch action {
		case "view":
			return true // everyone can view
		case "update", "delete":
			return user.GetAuthIdentifier() == team.OwnerID
		default:
			return false
		}
	})

	gate.RegisterPolicy("team", teamPolicy)

	owner := &mockUser{id: 1}
	other := &mockUser{id: 2}
	team := &mockTeam{ID: 1, OwnerID: 1}

	if !gate.AuthorizePolicy(owner, "team", "view", team) {
		t.Error("expected owner to be able to view team")
	}

	if !gate.AuthorizePolicy(other, "team", "view", team) {
		t.Error("expected other user to be able to view team")
	}

	if !gate.AuthorizePolicy(owner, "team", "update", team) {
		t.Error("expected owner to be able to update team")
	}

	if gate.AuthorizePolicy(other, "team", "update", team) {
		t.Error("expected other user to not be able to update team")
	}
}

func TestUserGate(t *testing.T) {
	gate := NewGate()

	gate.Define("post.create", func(user Authenticatable, args ...interface{}) bool {
		return true
	})

	gate.Define("post.delete", func(user Authenticatable, args ...interface{}) bool {
		return false
	})

	user := &mockUser{id: 1}
	userGate := gate.ForUser(user)

	if !userGate.Can("post.create") {
		t.Error("expected Can to return true for allowed ability")
	}

	if userGate.Can("post.delete") {
		t.Error("expected Can to return false for denied ability")
	}

	if !userGate.Cannot("post.delete") {
		t.Error("expected Cannot to return true for denied ability")
	}

	if err := userGate.Authorize("post.create"); err != nil {
		t.Errorf("expected Authorize to return nil for allowed ability, got %v", err)
	}

	if err := userGate.Authorize("post.delete"); err != ErrUnauthorized {
		t.Errorf("expected Authorize to return ErrUnauthorized for denied ability, got %v", err)
	}
}

func TestGate_UndefinedAbility(t *testing.T) {
	gate := NewGate()
	user := &mockUser{id: 1}

	// Undefined ability should return false
	if gate.Allows(user, "undefined-ability") {
		t.Error("expected undefined ability to return false")
	}
}

func TestGate_Concurrent(t *testing.T) {
	gate := NewGate()

	gate.Define("concurrent-test", func(user Authenticatable, args ...interface{}) bool {
		return true
	})

	user := &mockUser{id: 1}

	done := make(chan bool)

	// Multiple goroutines checking authorization
	for i := 0; i < 100; i++ {
		go func() {
			gate.Allows(user, "concurrent-test")
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 100; i++ {
		<-done
	}
}
