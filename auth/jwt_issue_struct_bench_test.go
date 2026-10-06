package auth

import (
	"context"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// structBenchID is an identifier that is not a basic value: minting
// encodes it to JSON.
type structBenchID struct {
	Tenant string `json:"tenant"`
	N      int    `json:"n"`
}

type structBenchUser struct{ id structBenchID }

func (u *structBenchUser) GetAuthIdentifier() interface{} { return u.id }
func (u *structBenchUser) GetAuthPassword() string        { return "" }
func (u *structBenchUser) GetRememberToken() string       { return "" }
func (u *structBenchUser) SetRememberToken(string)        {}

// oneUserBenchStore returns one user for every lookup.
type oneUserBenchStore struct{ user contract.Authenticatable }

func (p *oneUserBenchStore) FindByID(interface{}) (contract.Authenticatable, error) {
	return p.user, nil
}
func (p *oneUserBenchStore) FindByIDCtx(context.Context, interface{}) (contract.Authenticatable, error) {
	return p.user, nil
}
func (p *oneUserBenchStore) FindByCredentials(map[string]interface{}) (contract.Authenticatable, error) {
	return p.user, nil
}
func (p *oneUserBenchStore) FindByCredentialsCtx(context.Context, map[string]interface{}) (contract.Authenticatable, error) {
	return p.user, nil
}
func (p *oneUserBenchStore) ValidateCredentials(contract.Authenticatable, map[string]interface{}) bool {
	return true
}
func (p *oneUserBenchStore) ValidateCredentialsCtx(context.Context, contract.Authenticatable, map[string]interface{}) bool {
	return true
}
func (p *oneUserBenchStore) UpdateRememberToken(contract.Authenticatable, string) error { return nil }
func (p *oneUserBenchStore) UpdateRememberTokenCtx(context.Context, contract.Authenticatable, string) error {
	return nil
}

// BenchmarkJWTManager_IssueStructID measures minting for an identifier
// that takes the JSON-encoding path, and one refresh for it.
func BenchmarkJWTManager_IssueStructID(b *testing.B) {
	user := &structBenchUser{id: structBenchID{Tenant: "acme", N: 42}}
	b.Run("access", func(b *testing.B) {
		mgr := benchJWTManager(b, false)
		b.ReportAllocs()
		for b.Loop() {
			if _, err := mgr.GenerateToken(user); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("refresh", func(b *testing.B) {
		mgr := benchJWTManager(b, false)
		b.ReportAllocs()
		for b.Loop() {
			if _, err := mgr.GenerateRefreshToken(user); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("refresh_token", func(b *testing.B) {
		mgr := benchJWTManager(b, false)
		users := &oneUserBenchStore{user: user}
		refresh, err := mgr.GenerateRefreshToken(user)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for b.Loop() {
			if _, err := mgr.RefreshToken(refresh, users); err != nil {
				b.Fatal(err)
			}
		}
	})
}
