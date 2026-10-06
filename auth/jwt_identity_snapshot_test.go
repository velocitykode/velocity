package auth

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// mutableID is an identifier object whose value application code can
// change, with its own text and JSON forms.
type mutableID struct {
	mu sync.Mutex
	v  string
}

func (m *mutableID) get() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.v
}

func (m *mutableID) set(v string) {
	m.mu.Lock()
	m.v = v
	m.mu.Unlock()
}

func (m *mutableID) String() string               { return m.get() }
func (m *mutableID) MarshalJSON() ([]byte, error) { return []byte(`"` + m.get() + `"`), nil }

// mutableIDUser hands out the same identifier object on every read.
type mutableIDUser struct{ id *mutableID }

func (u *mutableIDUser) GetAuthIdentifier() interface{} { return u.id }
func (u *mutableIDUser) GetAuthPassword() string        { return "" }
func (u *mutableIDUser) GetRememberToken() string       { return "" }
func (u *mutableIDUser) SetRememberToken(string)        {}

// hookedGenerationStore runs a hook on the nth Current call.
type hookedGenerationStore struct {
	*InMemoryRefreshGenerationStore
	calls atomic.Int32
	on    int32
	hook  func()
}

func (s *hookedGenerationStore) Current(userID string) (int64, error) {
	if s.calls.Add(1) == s.on && s.hook != nil {
		s.hook()
	}
	return s.InMemoryRefreshGenerationStore.Current(userID)
}

// assertOneIdentity checks a token names one user in both its subject and
// its user id claim.
func assertOneIdentity(t *testing.T, mgr *JWTManager, token, want string) {
	t.Helper()
	claims, err := mgr.ValidateToken(token)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if claims.Subject != want || claims.UserID != want {
		t.Fatalf("token names sub=%q uid=%v, want both %q: the two claims were read from the user value at different times", claims.Subject, claims.UserID, want)
	}
}

// The identity a refresh mints for is read once, into a form later code
// cannot change: an identifier object that changes during the final
// generation read still yields a token whose subject and user id agree.
func TestJWT_RefreshToken_IdentifierChangesDuringFinalCheck_OneIdentitySigned(t *testing.T) {
	for _, mode := range blacklistModes {
		t.Run(mode.name, func(t *testing.T) {
			mgr := newConsumeManager(t, NewInMemoryBlacklistStore(), mode.enabled)
			id := &mutableID{v: "user-a"}
			user := &mutableIDUser{id: id}
			refresh, err := mgr.GenerateRefreshToken(user)
			if err != nil {
				t.Fatalf("GenerateRefreshToken: %v", err)
			}
			// Current call 1 is the early check, call 2 the final one.
			gens := &hookedGenerationStore{InMemoryRefreshGenerationStore: NewInMemoryRefreshGenerationStore(), on: 2, hook: func() { id.set("user-b") }}
			mgr.SetRefreshGenerationStore(gens)

			token, err := mgr.RefreshToken(refresh, &hookedUserStore{user: user})
			if err != nil {
				t.Fatalf("RefreshToken: %v", err)
			}
			if id.get() != "user-b" {
				t.Fatal("test setup error: the identifier did not change during the final check")
			}
			assertOneIdentity(t, mgr, token, "user-a")
		})
	}
}

// The same holds when a refresh token is minted: the generation read sits
// between the identity read and the signature.
func TestJWT_GenerateRefreshToken_IdentifierChangesDuringGenerationRead_OneIdentitySigned(t *testing.T) {
	mgr := newConsumeManager(t, NewInMemoryBlacklistStore(), true)
	id := &mutableID{v: "user-a"}
	gens := &hookedGenerationStore{InMemoryRefreshGenerationStore: NewInMemoryRefreshGenerationStore(), on: 1, hook: func() { id.set("user-b") }}
	mgr.SetRefreshGenerationStore(gens)

	refresh, err := mgr.GenerateRefreshToken(&mutableIDUser{id: id})
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}
	if id.get() != "user-b" {
		t.Fatal("test setup error: the identifier did not change during the generation read")
	}
	assertOneIdentity(t, mgr, refresh, "user-a")
}

// panickyJSONID is an identifier whose JSON form panics.
type panickyJSONID struct{}

func (panickyJSONID) String() string               { return "user-a" }
func (panickyJSONID) MarshalJSON() ([]byte, error) { panic("identifier cannot be encoded") }

type panickyJSONUser struct{}

func (panickyJSONUser) GetAuthIdentifier() interface{} { return panickyJSONID{} }
func (panickyJSONUser) GetAuthPassword() string        { return "" }
func (panickyJSONUser) GetRememberToken() string       { return "" }
func (panickyJSONUser) SetRememberToken(string)        {}

// An identifier whose JSON form panics is unreadable: minting returns the
// error and the panic does not reach the caller.
func TestJWT_GenerateToken_IdentifierJSONPanics_Unreadable(t *testing.T) {
	mgr := newConsumeManager(t, NewInMemoryBlacklistStore(), true)
	for name, mint := range map[string]func() (string, error){
		"access":  func() (string, error) { return mgr.GenerateToken(panickyJSONUser{}) },
		"refresh": func() (string, error) { return mgr.GenerateRefreshToken(panickyJSONUser{}) },
	} {
		token, err := mint()
		if token != "" || !errors.Is(err, ErrIdentifierUnreadable) {
			t.Errorf("%s: mint = (%q, %v), want ErrIdentifierUnreadable", name, token, err)
		}
	}
}
