package guards

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
)

func TestSessionGuard_IDMatchesUserAndCheck(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T) (*SessionGuard, *http.Cookie)
		wantID interface{}
		wantOK bool
	}{
		{
			name: "authenticated valid session returns user identifier",
			setup: func(t *testing.T) (*SessionGuard, *http.Cookie) {
				t.Helper()
				store := session.NewMemoryStore()
				t.Cleanup(func() { _ = store.Close(context.Background()) })

				guard, _ := newRevokeGuard(t, store)
				return guard, loginAndCookie(t, guard, "u1")
			},
			wantID: "u1",
			wantOK: true,
		},
		{
			name: "revoked server-store record returns nil",
			setup: func(t *testing.T) (*SessionGuard, *http.Cookie) {
				t.Helper()
				store := session.NewMemoryStore()
				t.Cleanup(func() { _ = store.Close(context.Background()) })

				guard, _ := newRevokeGuard(t, store)
				cookie := loginAndCookie(t, guard, "u1")

				list, err := store.ListForUser(context.Background(), "u1")
				if err != nil || len(list) != 1 {
					t.Fatalf("expected 1 stored session, got %d err=%v", len(list), err)
				}

				mgr := auth.NewManager()
				mgr.SetServerSessionStore(store)
				if err := mgr.RevokeSession(context.Background(), list[0].ID); err != nil {
					t.Fatalf("RevokeSession: %v", err)
				}

				return guard, cookie
			},
			wantOK: false,
		},
		{
			name: "deleted user returns nil",
			setup: func(t *testing.T) (*SessionGuard, *http.Cookie) {
				t.Helper()
				store := session.NewMemoryStore()
				t.Cleanup(func() { _ = store.Close(context.Background()) })

				guard, provider := newRevokeGuard(t, store)
				cookie := loginAndCookie(t, guard, "u1")
				delete(provider.users, "u1")
				return guard, cookie
			},
			wantOK: false,
		},
		{
			name: "no session returns nil",
			setup: func(t *testing.T) (*SessionGuard, *http.Cookie) {
				t.Helper()
				store := session.NewMemoryStore()
				t.Cleanup(func() { _ = store.Close(context.Background()) })

				guard, _ := newRevokeGuard(t, store)
				return guard, nil
			},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			guard, cookie := tt.setup(t)

			if got := guard.ID(idRevocationRequest(cookie)); got != tt.wantID {
				t.Fatalf("ID() = %v, want %v", got, tt.wantID)
			}

			if got := guard.Check(idRevocationRequest(cookie)); got != tt.wantOK {
				t.Fatalf("Check() = %v, want %v", got, tt.wantOK)
			}

			user := guard.User(idRevocationRequest(cookie))
			if !tt.wantOK {
				if user != nil {
					t.Fatalf("User() = %v, want nil", user)
				}
				return
			}
			if user == nil {
				t.Fatal("User() = nil, want authenticated user")
			}
			if got := user.GetAuthIdentifier(); got != tt.wantID {
				t.Fatalf("User().GetAuthIdentifier() = %v, want %v", got, tt.wantID)
			}
		})
	}
}

func idRevocationRequest(cookie *http.Cookie) *http.Request {
	if cookie == nil {
		return WithSessionContext(httptest.NewRequest("GET", "/", nil))
	}
	return requestWith(cookie)
}
