package schemes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// sharedUser is one user value handed to every request, guarded by its
// own mutex: the shape of a store that caches its users.
type sharedUser struct {
	mu    sync.Mutex
	id    string
	token string
}

func (u *sharedUser) GetAuthIdentifier() interface{} { return u.id }
func (u *sharedUser) GetAuthPassword() string        { return "" }
func (u *sharedUser) GetRememberToken() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.token
}
func (u *sharedUser) SetRememberToken(t string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.token = t
}

// sharedUserStore keeps the remember token on the shared user value and
// swaps it under the value's own mutex. afterSwap, when set, runs once
// right after a swap that landed, before the swap returns to its caller:
// the change another request makes in that window.
type sharedUserStore struct {
	user      *sharedUser
	mu        sync.Mutex
	afterSwap func()
}

func (p *sharedUserStore) FindByID(interface{}) (contract.Authenticatable, error) {
	return p.user, nil
}
func (p *sharedUserStore) FindByIDCtx(_ context.Context, id interface{}) (contract.Authenticatable, error) {
	return p.FindByID(id)
}
func (p *sharedUserStore) FindByCredentials(map[string]interface{}) (contract.Authenticatable, error) {
	return nil, errors.New("unused")
}
func (p *sharedUserStore) FindByCredentialsCtx(_ context.Context, c map[string]interface{}) (contract.Authenticatable, error) {
	return p.FindByCredentials(c)
}
func (p *sharedUserStore) ValidateCredentials(contract.Authenticatable, map[string]interface{}) bool {
	return true
}
func (p *sharedUserStore) UpdateRememberToken(_ contract.Authenticatable, token string) error {
	p.user.SetRememberToken(token)
	return nil
}
func (p *sharedUserStore) UpdateRememberTokenCtx(_ context.Context, u contract.Authenticatable, token string) error {
	return p.UpdateRememberToken(u, token)
}
func (p *sharedUserStore) CompareAndSwapRememberToken(_ context.Context, _ contract.Authenticatable, oldToken, newToken string) (bool, error) {
	p.user.mu.Lock()
	if p.user.token != oldToken {
		p.user.mu.Unlock()
		return false, nil
	}
	p.user.token = newToken
	p.user.mu.Unlock()

	p.mu.Lock()
	hook := p.afterSwap
	p.afterSwap = nil
	p.mu.Unlock()
	if hook != nil {
		hook()
	}
	return true, nil
}

func (p *sharedUserStore) onNextSwap(fn func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.afterSwap = fn
}

// With a store that shares one synchronized user value, a change another
// request makes to the credential right after this request's swap landed
// survives: the scheme writes nothing to the user value after the swap, in
// the rotation and in the rotation's undo.
func TestRememberRotation_AnotherRequestsChangeSurvivesOnASharedUserValue(t *testing.T) {
	scheme, _ := newRevokeScheme(t, nil)
	users := &sharedUserStore{user: &sharedUser{id: "u1"}}
	scheme.SetUserStore(users)

	w := httptest.NewRecorder()
	r := WithSessionContext(httptest.NewRequest(http.MethodPost, "/login", nil))
	if err := scheme.Login(w, r, users.user, true); err != nil {
		t.Fatalf("Login: %v", err)
	}
	cookie := findRememberCookie(w)
	if cookie == nil {
		t.Fatal("no remember cookie issued")
	}

	// A's rotation: its swap lands, then B (a logout elsewhere) clears
	// the credential before the swap returns to A.
	a := rememberRecallRequest(t, cookie, httptest.NewRecorder())
	match, ok := scheme.matchRememberCookie(a)
	if !ok {
		t.Fatal("the issued cookie did not validate")
	}
	issued := match.hash
	users.onNextSwap(func() { users.user.SetRememberToken("") })
	var op gateOp
	if _, _, err := reserveOperation(a, &op); err != nil {
		t.Fatalf("reserveOperation: %v", err)
	}
	if err := scheme.rotateRememberToken(a, match, &op); err != nil {
		t.Fatalf("rotateRememberToken: %v", err)
	}
	if got := users.user.GetRememberToken(); got != "" {
		t.Fatalf("B cleared the credential after A's swap, and A wrote %q over it", got)
	}
	// A's undo finds B's change and restores nothing.
	op.abort()
	if got := users.user.GetRememberToken(); got != "" {
		t.Fatalf("A's undo wrote %q over B's cleared credential", got)
	}

	// A's undo: its restoring swap lands, then B (a sign-in elsewhere)
	// issues a new credential before that swap returns to A.
	users.user.SetRememberToken(issued)
	a = rememberRecallRequest(t, cookie, httptest.NewRecorder())
	match, ok = scheme.matchRememberCookie(a)
	if !ok {
		t.Fatal("the restored cookie did not validate")
	}
	var op2 gateOp
	if _, _, err := reserveOperation(a, &op2); err != nil {
		t.Fatalf("reserveOperation: %v", err)
	}
	if err := scheme.rotateRememberToken(a, match, &op2); err != nil {
		t.Fatalf("rotateRememberToken: %v", err)
	}
	users.onNextSwap(func() { users.user.SetRememberToken("issued by B") })
	op2.abort()
	if got := users.user.GetRememberToken(); got != "issued by B" {
		t.Fatalf("B issued a credential after A's undo swap, and A wrote %q over it", got)
	}
}
