package schemes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/crypto"
)

// TestSessionScheme_SetUserStore_RaceWithAttempt is the H-10 regression test
// for SessionScheme. Concurrent SetUserStore + Attempt calls used to race on
// the bare interface field; the atomic.Pointer fix removes the data race.
// Run with `go test -race -count=1` so the race detector flags any
// regression.
func TestSessionScheme_SetUserStore_RaceWithAttempt(t *testing.T) {
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	scheme, err := NewSessionScheme(&mockSessionSchemeUserStore{}, auth.SessionConfig{
		Name:     "vel_session",
		Lifetime: 60,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}, enc)
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}
	scheme.SetAttemptFloor(-1) // bypass the 200ms timebox

	const iterations = 200
	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Writer: rotates the user store in a tight loop.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				scheme.SetUserStore(&mockSessionSchemeUserStore{})
			}
		}
	}()

	// Reader: calls Attempt repeatedly. The race detector flags any
	// torn read of provider/throttler under -race.
	for i := 0; i < iterations; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		_, _ = scheme.Attempt(w, r, map[string]interface{}{
			"email":    "ghost@example.com",
			"password": "x",
		})
	}

	close(stop)
	wg.Wait()
}

// TestSessionScheme_SetThrottler_RaceWithAttempt is the H-10 regression
// test for the throttler swap path.
func TestSessionScheme_SetThrottler_RaceWithAttempt(t *testing.T) {
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    strings.Repeat("k", 32),
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	scheme, err := NewSessionScheme(&mockSessionSchemeUserStore{}, auth.SessionConfig{
		Name:     "vel_session",
		Lifetime: 60,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}, enc)
	if err != nil {
		t.Fatalf("NewSessionScheme: %v", err)
	}
	scheme.SetAttemptFloor(-1)

	const iterations = 200
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				scheme.SetLoginThrottler(auth.NoopLoginThrottler{})
			}
		}
	}()

	for i := 0; i < iterations; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		_, _ = scheme.Attempt(w, r, map[string]interface{}{
			"email":    "ghost@example.com",
			"password": "x",
		})
	}

	close(stop)
	wg.Wait()
}

// TestJWTScheme_SetUserStore_RaceWithAttempt mirrors the SessionScheme race
// test on the JWT scheme surface.
func TestJWTScheme_SetUserStore_RaceWithAttempt(t *testing.T) {
	scheme, err := NewJWTScheme(&mockSessionSchemeUserStore{}, auth.JWTConfig{
		Secret:    strings.Repeat("s", 64),
		Algorithm: "HS256",
		TTL:       60,
	})
	if err != nil {
		t.Fatalf("NewJWTScheme: %v", err)
	}
	scheme.SetAttemptFloor(-1)

	const iterations = 200
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				scheme.SetUserStore(&mockSessionSchemeUserStore{})
			}
		}
	}()

	for i := 0; i < iterations; i++ {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/login", nil)
		_, _ = scheme.Attempt(w, r, map[string]interface{}{
			"email":    "ghost@example.com",
			"password": "x",
		})
	}

	close(stop)
	wg.Wait()
}
