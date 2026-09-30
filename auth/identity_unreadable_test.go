package auth

import (
	"errors"
	"testing"
)

// panickingID is an identifier whose String panics.
type panickingID struct{}

func (panickingID) String() string { panic("String called") }

// unreadableJWTUser is a user whose identifier cannot be read: its
// GetAuthIdentifier panics, or returns an identifier whose String does.
type unreadableJWTUser struct{ getterPanics bool }

func (u unreadableJWTUser) GetAuthIdentifier() interface{} {
	if u.getterPanics {
		panic("GetAuthIdentifier called")
	}
	return panickingID{}
}
func (unreadableJWTUser) GetAuthPassword() string  { return "" }
func (unreadableJWTUser) GetRememberToken() string { return "" }
func (unreadableJWTUser) SetRememberToken(string)  {}

// countingGenerations counts the refresh-generation lookups.
type countingGenerations struct {
	RefreshGenerationStore
	keys []string
}

func (c *countingGenerations) Current(userID string) (int64, error) {
	c.keys = append(c.keys, userID)
	return c.RefreshGenerationStore.Current(userID)
}

// Issuing a token for a user whose identifier cannot be read fails with
// ErrIdentifierUnreadable: no token carries a placeholder subject, and no
// refresh generation is keyed by one.
func TestJWTManager_UnreadableIdentifierIssuesNoToken(t *testing.T) {
	for _, getterPanics := range []bool{true, false} {
		name := "String panics"
		if getterPanics {
			name = "GetAuthIdentifier panics"
		}
		t.Run(name, func(t *testing.T) {
			gens := &countingGenerations{RefreshGenerationStore: NewInMemoryRefreshGenerationStore()}
			m, err := NewJWTManager(JWTConfig{
				Secret:                 "test-secret-key-for-jwt-signing-minimum-length",
				Algorithm:              "HS256",
				TTL:                    60,
				RefreshTTL:             1440,
				RefreshGenerationStore: gens,
			})
			if err != nil {
				t.Fatalf("NewJWTManager: %v", err)
			}
			user := unreadableJWTUser{getterPanics: getterPanics}
			for _, issue := range []struct {
				name string
				fn   func() (string, error)
			}{
				{"GenerateToken", func() (string, error) { return m.GenerateToken(user) }},
				{"GenerateRefreshToken", func() (string, error) { return m.GenerateRefreshToken(user) }},
			} {
				var tok string
				func() {
					defer func() {
						if p := recover(); p != nil {
							t.Fatalf("%s: a panic escaped: %v", issue.name, p)
						}
					}()
					tok, err = issue.fn()
				}()
				if !errors.Is(err, ErrIdentifierUnreadable) || tok != "" {
					t.Errorf("%s = %q, %v; want \"\", ErrIdentifierUnreadable", issue.name, tok, err)
				}
			}
			if len(gens.keys) != 0 {
				t.Errorf("refresh generations looked up under %q, want none", gens.keys)
			}
		})
	}
}
