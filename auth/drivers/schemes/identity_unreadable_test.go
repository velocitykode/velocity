package schemes

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/auth/drivers/session"
	"github.com/velocitykode/velocity/contract"
)

// unreadableMode is how a user's identifier fails to read.
type unreadableMode string

const (
	getterPanics unreadableMode = "GetAuthIdentifier panics"
	stringPanics unreadableMode = "the identifier's String panics"
)

var unreadableModes = []unreadableMode{getterPanics, stringPanics}

// panickingID is an identifier whose String panics.
type panickingID struct{}

func (panickingID) String() string { panic("String called") }

// unreadableUser is a user whose identifier cannot be read.
type unreadableUser struct {
	mode  unreadableMode
	token string
}

func (u *unreadableUser) GetAuthIdentifier() interface{} {
	if u.mode == getterPanics {
		panic("GetAuthIdentifier called")
	}
	return panickingID{}
}
func (u *unreadableUser) GetAuthPassword() string   { return "" }
func (u *unreadableUser) GetRememberToken() string  { return u.token }
func (u *unreadableUser) SetRememberToken(t string) { u.token = t }

// countingRecords counts the server session records written.
type countingRecords struct {
	auth.ServerSessionStore
	puts atomic.Int32
}

func (c *countingRecords) Put(ctx context.Context, s *auth.StoredSession) error {
	c.puts.Add(1)
	return c.ServerSessionStore.Put(ctx, s)
}

// catching runs fn and returns its error, or the panic that escaped it as
// a test failure.
func catching(t *testing.T, fn func() error) (err error) {
	t.Helper()
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("a panic escaped: %v", p)
		}
	}()
	return fn()
}

// A Login of a user whose identifier cannot be read signs nobody in: it
// returns auth.ErrIdentifierUnreadable, writes no server session record,
// sets no cookie and leaves the request's session unauthenticated.
func TestSessionScheme_LoginWithAnUnreadableIdentifierWritesNothing(t *testing.T) {
	for _, mode := range unreadableModes {
		t.Run(string(mode), func(t *testing.T) {
			records := &countingRecords{ServerSessionStore: session.NewMemoryStore()}
			scheme, _ := newRevokeScheme(t, records)
			w := httptest.NewRecorder()
			r := WithSessionContext(httptest.NewRequest(http.MethodPost, "/login", nil))

			err := catching(t, func() error { return scheme.Login(w, r, &unreadableUser{mode: mode}, true) })
			if !errors.Is(err, auth.ErrIdentifierUnreadable) {
				t.Fatalf("Login = %v, want auth.ErrIdentifierUnreadable", err)
			}
			if n := records.puts.Load(); n != 0 {
				t.Errorf("Login wrote %d server session records, want 0", n)
			}
			if cs := w.Result().Cookies(); len(cs) != 0 {
				t.Errorf("Login set %d cookies, want none", len(cs))
			}
			if s := scheme.getSession(r); s != nil && s.Get(auth.UserIDSessionKey) != nil {
				t.Errorf("the session holds user id %v after the refused Login", s.Get(auth.UserIDSessionKey))
			}
		})
	}
}

// unreadableRecallStore finds, for any id, a user whose identifier cannot
// be read but whose remember token is the one the recalled cookie carries.
type unreadableRecallStore struct {
	*rememberRevivalStore
	mode unreadableMode
}

func (s *unreadableRecallStore) FindByID(interface{}) (contract.Authenticatable, error) {
	return &unreadableUser{mode: s.mode, token: s.user.rememberToken}, nil
}

func (s *unreadableRecallStore) FindByIDCtx(context.Context, interface{}) (contract.Authenticatable, error) {
	return s.FindByID(nil)
}

// A remember-me recall that finds a user whose identifier cannot be read
// revives nothing: no user, no server session record, no session cookie.
func TestSessionScheme_RecallOfAnUnreadableIdentifierRevivesNothing(t *testing.T) {
	for _, mode := range unreadableModes {
		t.Run(string(mode), func(t *testing.T) {
			records := &countingRecords{ServerSessionStore: session.NewMemoryStore()}
			scheme, _ := newRevokeScheme(t, records)
			known := &rememberRevivalStore{user: &revokeTestUser{id: "u1"}}
			scheme.SetUserStore(known)
			remember := mintRememberCookie(t, scheme)
			scheme.SetUserStore(&unreadableRecallStore{rememberRevivalStore: known, mode: mode})
			before := records.puts.Load()

			r := WithSessionContext(httptest.NewRequest(http.MethodGet, "/dashboard", nil))
			r.AddCookie(remember)
			var user contract.Authenticatable
			_ = catching(t, func() error { user = scheme.User(r); return nil })
			if user != nil {
				t.Errorf("the recall revived a user whose identifier cannot be read")
			}
			if n := records.puts.Load() - before; n != 0 {
				t.Errorf("the recall wrote %d server session records, want 0", n)
			}
			if s := scheme.getSession(r); s != nil && s.Get(auth.UserIDSessionKey) != nil {
				t.Errorf("the session holds user id %v after the refused recall", s.Get(auth.UserIDSessionKey))
			}
		})
	}
}

// Minting a remember credential for a user whose identifier cannot be read
// fails with auth.ErrIdentifierUnreadable and persists nothing.
func TestSessionScheme_RememberCredentialOfAnUnreadableIdentifier(t *testing.T) {
	for _, mode := range unreadableModes {
		t.Run(string(mode), func(t *testing.T) {
			scheme, _ := newRevokeScheme(t, nil)
			persisted := false
			var c *http.Cookie
			err := catching(t, func() error {
				var err error
				c, err = scheme.mintRememberCookie(&unreadableUser{mode: mode}, func(string) error { persisted = true; return nil })
				return err
			})
			if !errors.Is(err, auth.ErrIdentifierUnreadable) || c != nil {
				t.Fatalf("mintRememberCookie = %v, %v; want nil, auth.ErrIdentifierUnreadable", c, err)
			}
			if persisted {
				t.Error("the remember token was persisted for an unreadable identifier")
			}
		})
	}
}
