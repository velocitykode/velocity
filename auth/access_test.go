package auth

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

func TestAccess_Define(t *testing.T) {
	access := NewAccess()

	access.Define("edit-post", func(user Authenticatable, args ...interface{}) bool {
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

	if !access.Allows(user, "edit-post", ownPost) {
		t.Error("expected user to be allowed to edit own post")
	}

	if access.Allows(user, "edit-post", otherPost) {
		t.Error("expected user to be denied editing other's post")
	}
}

func TestAccess_Denies(t *testing.T) {
	access := NewAccess()

	access.Define("admin-action", func(user Authenticatable, args ...interface{}) bool {
		return false // always deny
	})

	user := &mockUser{id: 1}

	if !access.Denies(user, "admin-action") {
		t.Error("expected Denies to return true for denied action")
	}
}

func TestAccess_Check(t *testing.T) {
	access := NewAccess()

	access.Define("read", func(user Authenticatable, args ...interface{}) bool {
		return true
	})
	access.Define("write", func(user Authenticatable, args ...interface{}) bool {
		return true
	})
	access.Define("delete", func(user Authenticatable, args ...interface{}) bool {
		return false
	})

	user := &mockUser{id: 1}

	if !access.Check(user, []string{"read", "write"}) {
		t.Error("expected Check to pass for allowed abilities")
	}

	if access.Check(user, []string{"read", "delete"}) {
		t.Error("expected Check to fail when one ability is denied")
	}
}

func TestAccess_Any(t *testing.T) {
	access := NewAccess()

	access.Define("read", func(user Authenticatable, args ...interface{}) bool {
		return false
	})
	access.Define("write", func(user Authenticatable, args ...interface{}) bool {
		return true
	})

	user := &mockUser{id: 1}

	if !access.Any(user, []string{"read", "write"}) {
		t.Error("expected Any to pass when at least one ability is allowed")
	}

	if access.Any(user, []string{"read"}) {
		t.Error("expected Any to fail when no abilities are allowed")
	}
}

func TestAccess_Before(t *testing.T) {
	access := NewAccess()

	// Admin bypass
	allowTrue := true
	access.Before(func(user Authenticatable, ability string, args ...interface{}) *bool {
		if u, ok := user.(*mockUser); ok {
			for _, role := range u.roles {
				if role == "admin" {
					return &allowTrue
				}
			}
		}
		return nil
	})

	access.Define("restricted", func(user Authenticatable, args ...interface{}) bool {
		return false
	})

	regularUser := &mockUser{id: 1, roles: []string{"user"}}
	adminUser := &mockUser{id: 2, roles: []string{"admin"}}

	if access.Allows(regularUser, "restricted") {
		t.Error("expected regular user to be denied")
	}

	if !access.Allows(adminUser, "restricted") {
		t.Error("expected admin user to bypass restriction")
	}
}

func TestAccess_After(t *testing.T) {
	access := NewAccess()

	var afterCalled bool
	access.After(func(user Authenticatable, ability string, result bool, args ...interface{}) bool {
		afterCalled = true
		return result
	})

	access.Define("test", func(user Authenticatable, args ...interface{}) bool {
		return true
	})

	user := &mockUser{id: 1}
	access.Allows(user, "test")

	if !afterCalled {
		t.Error("expected after callback to be called")
	}
}

func TestAccess_Allows_NoDeadlockUnderConcurrentDefine(t *testing.T) {
	access := NewAccess()
	access.Define("x", func(user Authenticatable, args ...interface{}) bool {
		return true
	})

	user := &mockUser{id: 1}
	const (
		allowGoroutines = 50
		allowIterations = 200
		writeGoroutines = 10
		writeIterations = 100
	)

	var wg sync.WaitGroup
	var denied atomic.Bool

	for i := 0; i < allowGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < allowIterations; j++ {
				if !access.Allows(user, "x") {
					denied.Store(true)
				}
			}
		}()
	}

	for i := 0; i < writeGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < writeIterations; j++ {
				access.Define("x", func(user Authenticatable, args ...interface{}) bool {
					return true
				})
				access.Before(func(user Authenticatable, ability string, args ...interface{}) *bool {
					return nil
				})
				access.After(func(user Authenticatable, ability string, result bool, args ...interface{}) bool {
					return result
				})
			}
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent Allows and access mutation did not complete")
	}

	if denied.Load() {
		t.Error("expected all Allows calls to succeed")
	}
}

func TestAccess_Allows_BeforeCallbackPanicDoesNotWedgeAccess(t *testing.T) {
	access := NewAccess()
	user := &mockUser{id: 1}
	var shouldPanic atomic.Bool
	shouldPanic.Store(true)

	access.Before(func(user Authenticatable, ability string, args ...interface{}) *bool {
		if shouldPanic.Swap(false) {
			panic("before callback panic")
		}
		return nil
	})

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("expected before callback panic")
			}
		}()
		access.Allows(user, "x")
	}()

	done := make(chan struct{})
	go func() {
		access.Define("x", func(user Authenticatable, args ...interface{}) bool {
			return true
		})
		if !access.Allows(user, "x") {
			t.Error("expected subsequent Allows call to succeed")
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("access remained wedged after before callback panic")
	}
}

func TestAccess_RoleChecker(t *testing.T) {
	access := NewAccess()

	access.SetRoleChecker(func(user Authenticatable, role string) bool {
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

	if !access.HasRole(user, "admin") {
		t.Error("expected user to have admin role")
	}

	if access.HasRole(user, "superadmin") {
		t.Error("expected user to not have superadmin role")
	}
}

func TestAccess_HasAnyRole(t *testing.T) {
	access := NewAccess()

	access.SetRoleChecker(func(user Authenticatable, role string) bool {
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

	if !access.HasAnyRole(user, "admin", "editor") {
		t.Error("expected user to have at least one role")
	}

	if access.HasAnyRole(user, "admin", "superadmin") {
		t.Error("expected user to not have any of the roles")
	}
}

func TestAccess_HasAllRoles(t *testing.T) {
	access := NewAccess()

	access.SetRoleChecker(func(user Authenticatable, role string) bool {
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

	if !access.HasAllRoles(user, "admin", "editor") {
		t.Error("expected user to have all roles")
	}

	if access.HasAllRoles(user, "admin", "superadmin") {
		t.Error("expected user to not have all roles")
	}
}

func TestPolicy_Authorize(t *testing.T) {
	access := NewAccess()

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

	access.RegisterPolicy("team", teamPolicy)

	owner := &mockUser{id: 1}
	other := &mockUser{id: 2}
	team := &mockTeam{ID: 1, OwnerID: 1}

	if !access.AuthorizePolicy(owner, "team", "view", team) {
		t.Error("expected owner to be able to view team")
	}

	if !access.AuthorizePolicy(other, "team", "view", team) {
		t.Error("expected other user to be able to view team")
	}

	if !access.AuthorizePolicy(owner, "team", "update", team) {
		t.Error("expected owner to be able to update team")
	}

	if access.AuthorizePolicy(other, "team", "update", team) {
		t.Error("expected other user to not be able to update team")
	}
}

func TestUserAccess(t *testing.T) {
	access := NewAccess()

	access.Define("post.create", func(user Authenticatable, args ...interface{}) bool {
		return true
	})

	access.Define("post.delete", func(user Authenticatable, args ...interface{}) bool {
		return false
	})

	user := &mockUser{id: 1}
	userAccess := access.ForUser(user)

	if !userAccess.Can("post.create") {
		t.Error("expected Can to return true for allowed ability")
	}

	if userAccess.Can("post.delete") {
		t.Error("expected Can to return false for denied ability")
	}

	if !userAccess.Cannot("post.delete") {
		t.Error("expected Cannot to return true for denied ability")
	}

	if err := userAccess.Authorize("post.create"); err != nil {
		t.Errorf("expected Authorize to return nil for allowed ability, got %v", err)
	}

	if err := userAccess.Authorize("post.delete"); err != ErrUnauthorized {
		t.Errorf("expected Authorize to return ErrUnauthorized for denied ability, got %v", err)
	}
}

func TestAccess_UndefinedAbility(t *testing.T) {
	access := NewAccess()
	user := &mockUser{id: 1}

	// Undefined ability should return false
	if access.Allows(user, "undefined-ability") {
		t.Error("expected undefined ability to return false")
	}
}

func TestAccess_Concurrent(t *testing.T) {
	access := NewAccess()

	access.Define("concurrent-test", func(user Authenticatable, args ...interface{}) bool {
		return true
	})

	user := &mockUser{id: 1}

	done := make(chan bool)

	// Multiple goroutines checking authorization
	for i := 0; i < 100; i++ {
		go func() {
			access.Allows(user, "concurrent-test")
			done <- true
		}()
	}

	// Wait for all goroutines
	for i := 0; i < 100; i++ {
		<-done
	}
}
