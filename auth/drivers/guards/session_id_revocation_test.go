package guards

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
)

func TestSessionScheme_IDMatchesUserAndCheck(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(t *testing.T) (*SessionScheme, *http.Cookie)
		wantID interface{}
		wantOK bool
	}{
		{
			name: "authenticated valid session returns user identifier",
			setup: func(t *testing.T) (*SessionScheme, *http.Cookie) {
				t.Helper()
				store := session.NewMemoryStore()
				t.Cleanup(func() { _ = store.Close(context.Background()) })

				scheme, _ := newRevokeScheme(t, store)
				return scheme, loginAndCookie(t, scheme, "u1")
			},
			wantID: "u1",
			wantOK: true,
		},
		{
			name: "revoked server-store record returns nil",
			setup: func(t *testing.T) (*SessionScheme, *http.Cookie) {
				t.Helper()
				store := session.NewMemoryStore()
				t.Cleanup(func() { _ = store.Close(context.Background()) })

				scheme, _ := newRevokeScheme(t, store)
				cookie := loginAndCookie(t, scheme, "u1")

				list, err := store.ListForUser(context.Background(), "u1")
				if err != nil || len(list) != 1 {
					t.Fatalf("expected 1 stored session, got %d err=%v", len(list), err)
				}

				mgr := auth.NewManager()
				mgr.SetServerSessionStore(store)
				if err := mgr.RevokeSession(context.Background(), list[0].ID); err != nil {
					t.Fatalf("RevokeSession: %v", err)
				}

				return scheme, cookie
			},
			wantOK: false,
		},
		{
			name: "deleted user returns nil",
			setup: func(t *testing.T) (*SessionScheme, *http.Cookie) {
				t.Helper()
				store := session.NewMemoryStore()
				t.Cleanup(func() { _ = store.Close(context.Background()) })

				scheme, userStore := newRevokeScheme(t, store)
				cookie := loginAndCookie(t, scheme, "u1")
				delete(userStore.users, "u1")
				return scheme, cookie
			},
			wantOK: false,
		},
		{
			name: "no session returns nil",
			setup: func(t *testing.T) (*SessionScheme, *http.Cookie) {
				t.Helper()
				store := session.NewMemoryStore()
				t.Cleanup(func() { _ = store.Close(context.Background()) })

				scheme, _ := newRevokeScheme(t, store)
				return scheme, nil
			},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			scheme, cookie := tt.setup(t)

			if got := scheme.ID(idRevocationRequest(cookie)); got != tt.wantID {
				t.Fatalf("ID() = %v, want %v", got, tt.wantID)
			}

			if got := scheme.Check(idRevocationRequest(cookie)); got != tt.wantOK {
				t.Fatalf("Check() = %v, want %v", got, tt.wantOK)
			}

			user := scheme.User(idRevocationRequest(cookie))
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
