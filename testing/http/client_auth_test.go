package http_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/guards"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/router"
	velhttp "github.com/velocitykode/velocity/testing/http"
)

// memUser is a minimal auth.Authenticatable for the acting-as tests.
type memUser struct {
	id            interface{}
	password      string
	rememberToken string
}

func (u *memUser) GetAuthIdentifier() interface{} { return u.id }
func (u *memUser) GetAuthPassword() string        { return u.password }
func (u *memUser) GetRememberToken() string       { return u.rememberToken }
func (u *memUser) SetRememberToken(token string)  { u.rememberToken = token }

// memProvider is a minimal in-memory auth.UserProvider keyed by identifier.
type memProvider struct {
	users map[interface{}]*memUser
}

func (p *memProvider) FindByID(id interface{}) (auth.Authenticatable, error) {
	u, ok := p.users[id]
	if !ok {
		return nil, nil
	}
	return u, nil
}

func (p *memProvider) FindByIDCtx(_ context.Context, id interface{}) (auth.Authenticatable, error) {
	return p.FindByID(id)
}

func (p *memProvider) FindByCredentials(map[string]interface{}) (auth.Authenticatable, error) {
	return nil, nil
}

func (p *memProvider) FindByCredentialsCtx(_ context.Context, c map[string]interface{}) (auth.Authenticatable, error) {
	return p.FindByCredentials(c)
}

func (p *memProvider) ValidateCredentials(auth.Authenticatable, map[string]interface{}) bool {
	return true
}

func (p *memProvider) UpdateRememberToken(auth.Authenticatable, string) error { return nil }

func (p *memProvider) UpdateRememberTokenCtx(_ context.Context, u auth.Authenticatable, t string) error {
	return p.UpdateRememberToken(u, t)
}

// newActingAsGuard builds a SessionGuard backed by the production CookieStore
// and a real AES-256-GCM encryptor, with an in-memory provider holding user.
func newActingAsGuard(t *testing.T, user *memUser) *guards.SessionGuard {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	provider := &memProvider{users: map[interface{}]*memUser{user.id: user}}
	guard, err := guards.NewSessionGuard(provider, auth.SessionConfig{
		Name:     "vel_session",
		Lifetime: 60,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}, enc)
	if err != nil {
		t.Fatalf("NewSessionGuard: %v", err)
	}
	return guard
}

// newAuthGatedRouter returns a router whose GET /me route is gated by the
// session guard: authenticated requests get 200, guests get 401.
func newAuthGatedRouter(guard *guards.SessionGuard) *router.VelocityRouterV2 {
	r := router.New()
	r.Use(guard.SessionMiddleware())
	r.Get("/me", func(c *router.Context) error {
		if !guard.Check(c.Request) {
			return c.String(http.StatusUnauthorized, "guest")
		}
		return c.String(http.StatusOK, "authenticated")
	})
	return r
}

func TestActingAs_AuthenticatesGatedRoute(t *testing.T) {
	user := &memUser{id: "user-1", password: "x"}
	guard := newActingAsGuard(t, user)
	r := newAuthGatedRouter(guard)

	client := velhttp.NewTestClient(t, r).ActingAs(guard, user)

	client.Get("/me").AssertOk().AssertBodyContains("authenticated")
	client.AssertAuthenticated(guard)
}

func TestActingAsID_AuthenticatesGatedRoute(t *testing.T) {
	user := &memUser{id: "user-2", password: "x"}
	guard := newActingAsGuard(t, user)
	r := newAuthGatedRouter(guard)

	client := velhttp.NewTestClient(t, r).ActingAsID(guard, "user-2")

	client.Get("/me").AssertOk().AssertBodyContains("authenticated")
	client.AssertAuthenticated(guard)
}

func TestActingAs_GuestWithoutLogin(t *testing.T) {
	user := &memUser{id: "user-3", password: "x"}
	guard := newActingAsGuard(t, user)
	r := newAuthGatedRouter(guard)

	client := velhttp.NewTestClient(t, r)

	client.Get("/me").AssertUnauthorized()
	client.AssertGuest(guard)
}
