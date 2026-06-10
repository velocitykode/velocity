package guards

import (
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/auth"
)

// countingReader counts how many bytes have been consumed from the
// underlying reader, so tests can prove the form-token extractor never
// slurps an oversized body.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// B17: an oversized urlencoded POST body must not be read past the cap,
// and must yield no token even when a token field sits beyond the cap.
func TestJWTGuard_FormToken_OversizedBodyNotSlurped(t *testing.T) {
	guard := mustNewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())

	// Padding pushes the token field past the 1 MiB cap.
	padding := strings.Repeat("x", jwtMaxFormBodyBytes+1024)
	body := "pad=" + padding + "&token=should-never-be-seen"
	counter := &countingReader{r: strings.NewReader(body)}

	r := httptest.NewRequest("POST", "/protected", counter)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if got := guard.getTokenFromRequest(r); got != "" {
		t.Fatalf("expected no token from oversized body, got %q", got)
	}
	if counter.n > jwtMaxFormBodyBytes+1 {
		t.Errorf("extractor read %d bytes; cap is %d (+1 sentinel)", counter.n, jwtMaxFormBodyBytes)
	}
}

// B17: within the cap, the token is extracted AND the downstream handler
// can still read the full body afterwards (the old r.FormValue path
// consumed the body).
func TestJWTGuard_FormToken_BodyRemainsReadable(t *testing.T) {
	guard := mustNewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())

	const body = "token=tok123&foo=bar"
	r := httptest.NewRequest("POST", "/protected", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if got := guard.getTokenFromRequest(r); got != "tok123" {
		t.Fatalf("token = %q, want %q", got, "tok123")
	}

	// A second extraction (e.g. Check then User on the same request)
	// still works because the body is restored after every read.
	if got := guard.getTokenFromRequest(r); got != "tok123" {
		t.Errorf("second extraction = %q, want %q", got, "tok123")
	}

	// Handler-side read sees the original payload.
	rest, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read restored body: %v", err)
	}
	if string(rest) != body {
		t.Errorf("restored body = %q, want %q", rest, body)
	}
}

// B17: non-urlencoded bodies (multipart and friends) are never parsed and
// never consumed by token extraction.
func TestJWTGuard_FormToken_NonFormContentTypeUntouched(t *testing.T) {
	guard := mustNewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())

	const body = `{"token":"not-a-form"}`
	counter := &countingReader{r: strings.NewReader(body)}
	r := httptest.NewRequest("POST", "/protected", counter)
	r.Header.Set("Content-Type", "application/json")

	if got := guard.getTokenFromRequest(r); got != "" {
		t.Fatalf("expected no token from JSON body, got %q", got)
	}
	if counter.n != 0 {
		t.Errorf("extractor consumed %d bytes of a non-form body", counter.n)
	}
}

// B18: LoginByID with a provider returning (nil, nil) must yield
// ErrUserNotFound, not a panic inside Login/GenerateToken.
func TestJWTGuard_LoginByID_NilUserReturnsErrUserNotFound(t *testing.T) {
	provider := &mockJWTUserProvider{
		findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
			return nil, nil // not found, per UserProvider contract
		},
	}
	guard := mustNewJWTGuard(provider, newTestJWTConfig())

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/login", nil)

	err := guard.LoginByID(w, r, "missing-user")
	if !errors.Is(err, auth.ErrUserNotFound) {
		t.Fatalf("LoginByID error = %v, want ErrUserNotFound", err)
	}
}

// B18: User() with a provider returning (nil, nil) must return nil and
// must not cache the nil as an authenticated entry.
func TestJWTGuard_User_NilUserNoPanic(t *testing.T) {
	provider := &mockJWTUserProvider{
		findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
			return nil, nil
		},
	}
	guard := mustNewJWTGuard(provider, newTestJWTConfig())

	token, err := guard.GenerateToken(&mockJWTUser{id: "user123"})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	r := httptest.NewRequest("GET", "/me", nil)
	r.Header.Set("Authorization", "Bearer "+token)

	if user := guard.User(r); user != nil {
		t.Fatalf("User = %v, want nil for not-found provider result", user)
	}
	if _, ok := guard.getCachedUser(token); ok {
		t.Error("nil user was cached as an authenticated entry")
	}
}

// B18: JWTManager.RefreshToken (driven via the guard) with a provider
// returning (nil, nil) must yield ErrUserNotFound, not a panic in
// GenerateToken.
func TestJWTGuard_RefreshToken_NilUserReturnsErrUserNotFound(t *testing.T) {
	provider := &mockJWTUserProvider{
		findByIDFunc: func(id interface{}) (auth.Authenticatable, error) {
			return nil, nil
		},
	}
	guard := mustNewJWTGuard(provider, newTestJWTConfig())

	refresh, err := guard.GenerateRefreshToken(&mockJWTUser{id: "user123"})
	if err != nil {
		t.Fatalf("GenerateRefreshToken: %v", err)
	}

	_, err = guard.RefreshToken(refresh)
	if !errors.Is(err, auth.ErrUserNotFound) {
		t.Fatalf("RefreshToken error = %v, want ErrUserNotFound", err)
	}
}

// StopCleanup race: concurrent callers must not double-close the stop
// channel. Run with -race.
func TestJWTGuard_StopCleanup_Concurrent(t *testing.T) {
	guard := mustNewJWTGuard(&mockJWTUserProvider{}, newTestJWTConfig())
	guard.Start()

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			guard.StopCleanup()
		}()
	}
	wg.Wait()
}

// ctxCapturingProvider records the context FindByIDCtx receives so the
// test can prove the guard threads the request context through instead
// of the deprecated context.Background() shim.
type ctxCapturingProvider struct {
	mockJWTUserProvider
	gotCtx context.Context
}

func (p *ctxCapturingProvider) FindByIDCtx(ctx context.Context, id interface{}) (auth.Authenticatable, error) {
	p.gotCtx = ctx
	return &mockJWTUser{id: id, password: "hashedpassword"}, nil
}

type ctxProbeKey struct{}

func TestJWTGuard_User_PropagatesRequestContext(t *testing.T) {
	provider := &ctxCapturingProvider{}
	guard := mustNewJWTGuard(provider, newTestJWTConfig())

	token, err := guard.GenerateToken(&mockJWTUser{id: "user123"})
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	r := httptest.NewRequest("GET", "/me", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxProbeKey{}, "probe"))
	r.Header.Set("Authorization", "Bearer "+token)

	if user := guard.User(r); user == nil {
		t.Fatal("User returned nil")
	}
	if provider.gotCtx == nil {
		t.Fatal("FindByIDCtx never called")
	}
	if got, _ := provider.gotCtx.Value(ctxProbeKey{}).(string); got != "probe" {
		t.Errorf("FindByIDCtx received context without request value; got %q", got)
	}
}
