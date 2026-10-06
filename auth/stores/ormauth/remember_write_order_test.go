package ormauth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/orm"
)

// A remember-token write that fails leaves the caller's user as it was: the
// row is written first and the in-memory value follows only a write that
// succeeded, so a user value never names a token the store does not hold.
func TestStore_UpdateRememberToken_FailedWriteLeavesTheUserUnchanged(t *testing.T) {
	stores := map[string]func(t *testing.T) (auth.UserStore, contract.Authenticatable, *orm.Manager, string){
		"default model": func(t *testing.T) (auth.UserStore, contract.Authenticatable, *orm.Manager, string) {
			m := newManager(t)
			seedUser(t, m, testEmail, testPassword)
			p := newStore(t)
			user, err := p.FindByIDCtx(context.Background(), 1)
			if err != nil {
				t.Fatalf("FindByIDCtx: %v", err)
			}
			return p, user, m, "users"
		},
		"mapped model": func(t *testing.T) (auth.UserStore, contract.Authenticatable, *orm.Manager, string) {
			m := newManager(t)
			seedAdmin(t, m, "root", testPassword)
			p := newAdminStore(t)
			user, err := p.FindByIDCtx(context.Background(), 1)
			if err != nil {
				t.Fatalf("FindByIDCtx: %v", err)
			}
			return p, user, m, "admins"
		},
	}
	failures := map[string]func(t *testing.T, m *orm.Manager, table string) context.Context{
		"cancelled context": func(t *testing.T, m *orm.Manager, table string) context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		},
		"table gone": func(t *testing.T, m *orm.Manager, table string) context.Context {
			if _, err := m.DB().Exec(`DROP TABLE ` + table); err != nil {
				t.Fatalf("drop: %v", err)
			}
			return context.Background()
		},
	}
	for storeName, build := range stores {
		for failName, fail := range failures {
			t.Run(storeName+"/"+failName, func(t *testing.T) {
				p, user, m, table := build(t)
				if err := p.UpdateRememberTokenCtx(context.Background(), user, "held"); err != nil {
					t.Fatalf("the first write: %v", err)
				}
				ctx := fail(t, m, table)
				if err := p.UpdateRememberTokenCtx(ctx, user, "not-written"); err == nil {
					t.Fatal("the write succeeded; the fixture did not make it fail")
				}
				if got := user.GetRememberToken(); got != "held" {
					t.Fatalf("after a failed write the user's token = %q, want the token the store holds (%q)", got, "held")
				}
			})
		}
	}
}

// A nil user is refused before any write, as before.
func TestStore_UpdateRememberToken_NilUser(t *testing.T) {
	m := newManager(t)
	seedUser(t, m, testEmail, testPassword)
	if err := newStore(t).UpdateRememberTokenCtx(context.Background(), nil, "t"); !errors.Is(err, auth.ErrUserNotFound) {
		t.Fatalf("UpdateRememberTokenCtx(nil user) = %v, want auth.ErrUserNotFound", err)
	}
}
