package session

import (
	"errors"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
)

type nilCache struct{ contract.Cache }

type nilEncryptor struct{ crypto.Encryptor }

type nilRecords struct{ auth.ServerSessionStore }

// The store constructors refuse a typed-nil collaborator as they refuse nil.
func TestConstructorsRefuseATypedNil(t *testing.T) {
	var (
		backend   *nilCache
		encryptor *nilEncryptor
		records   *nilRecords
	)
	if _, err := NewCacheStore(backend); !errors.Is(err, ErrCacheStoreNilBackend) {
		t.Errorf("NewCacheStore(typed nil) = %v, want ErrCacheStoreNilBackend", err)
	}
	if _, err := NewCookieStore(auth.SessionConfig{}, encryptor); err == nil {
		t.Error("NewCookieStore(typed nil encryptor) = nil error, want one")
	}
	if _, err := NewServerStore(auth.SessionConfig{}, records); !errors.Is(err, auth.ErrNoServerSessionStore) {
		t.Errorf("NewServerStore(typed nil) = %v, want auth.ErrNoServerSessionStore", err)
	}
}
