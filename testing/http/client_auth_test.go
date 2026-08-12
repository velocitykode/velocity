package http_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/schemes"
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

// memStore is a minimal in-memory auth.UserStore keyed by identifier.
type memStore struct {
	users map[interface{}]*memUser
}

func (p *memStore) FindByID(id interface{}) (auth.Authenticatable, error) {
	u, ok := p.users[id]
	if !ok {
		return nil, nil
	}
	return u, nil
}

func (p *memStore) FindByIDCtx(_ context.Context, id interface{}) (auth.Authenticatable, error) {
	return p.FindByID(id)
}

func (p *memStore) FindByCredentials(map[string]interface{}) (auth.Authenticatable, error) {
	return nil, nil
}

func (p *memStore) FindByCredentialsCtx(_ context.Context, c map[string]interface{}) (auth.Authenticatable, error) {
	return p.FindByCredentials(c)
}

func (p *memStore) ValidateCredentials(auth.Authenticatable, map[string]interface{}) bool {
	return true
}

func (p *memStore) UpdateRememberToken(auth.Authenticatable, string) error { return nil }

func (p *memStore) UpdateRememberTokenCtx(_ context.Context, u auth.Authenticatable, t string) error {
	return p.UpdateRememberToken(u, t)
}

// newActingAsScheme builds a SessionScheme backed by the production CookieStore
// and a real AES-256-GCM encryptor, with an in-memory user store holding user.
func newActingAsScheme(t *testing.T, user *memUser) *schemes.SessionScheme {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	userStore := &memStore{users: map[interface{}]*memUser{user.id: user}}
	scheme, err := schemes.NewSessionScheme(userStore, auth.SessionConfig{
		Name:     "vel_session",
		Lifetime: 60,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}, enc)
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}
	return scheme
}

// newAuthGatedRouter returns a router whose GET /me route is gated by the
// session scheme: authenticated requests get 200, guests get 401.
func newAuthGatedRouter(scheme *schemes.SessionScheme) *router.VelocityRouterV2 {
	r := router.New()
	r.Use(scheme.SessionMiddleware())
	r.Get("/me", func(c *router.Context) error {
		if !scheme.Check(c.Request) {
			return c.String(http.StatusUnauthorized, "guest")
		}
		return c.String(http.StatusOK, "authenticated")
	})
	return r
}

func TestActingAs_AuthenticatesGatedRoute(t *testing.T) {
	user := &memUser{id: "user-1", password: "x"}
	scheme := newActingAsScheme(t, user)
	r := newAuthGatedRouter(scheme)

	client := velhttp.NewTestClient(t, r).ActingAs(scheme, user)

	client.Get("/me").AssertOk().AssertBodyContains("authenticated")
	client.AssertAuthenticated(scheme)
}

func TestActingAsID_AuthenticatesGatedRoute(t *testing.T) {
	user := &memUser{id: "user-2", password: "x"}
	scheme := newActingAsScheme(t, user)
	r := newAuthGatedRouter(scheme)

	client := velhttp.NewTestClient(t, r).ActingAsID(scheme, "user-2")

	client.Get("/me").AssertOk().AssertBodyContains("authenticated")
	client.AssertAuthenticated(scheme)
}

func TestActingAs_GuestWithoutLogin(t *testing.T) {
	user := &memUser{id: "user-3", password: "x"}
	scheme := newActingAsScheme(t, user)
	r := newAuthGatedRouter(scheme)

	client := velhttp.NewTestClient(t, r)

	client.Get("/me").AssertUnauthorized()
	client.AssertGuest(scheme)
}
