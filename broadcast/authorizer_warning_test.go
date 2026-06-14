package broadcast

import (
	"strings"
	"sync"
	"testing"
)

// TestSetAuthorizer_WarnsWithoutSecret verifies the fail-loud behaviour for the
// authorizer-without-verifier misconfiguration: installing a non-deny
// authorizer while no auth secret is configured emits a one-time warning, and
// the warning is suppressed for the secure default and when a secret is set.
func TestSetAuthorizer_WarnsWithoutSecret(t *testing.T) {
	allow := func(channel string, user interface{}) bool { return true }

	tests := []struct {
		name      string
		configure func(b *BroadcastManager)
		wantWarn  int
	}{
		{
			name:      "custom authorizer without secret warns once",
			configure: func(b *BroadcastManager) { b.SetAuthorizer(allow) },
			wantWarn:  1,
		},
		{
			name:      "secure deny-all default never warns",
			configure: func(b *BroadcastManager) {},
			wantWarn:  0,
		},
		{
			name:      "nil authorizer does not warn",
			configure: func(b *BroadcastManager) { b.SetAuthorizer(nil) },
			wantWarn:  0,
		},
		{
			name: "secret set before authorizer does not warn",
			configure: func(b *BroadcastManager) {
				b.SetAuthSecret([]byte("super-secret-key"))
				b.SetAuthorizer(allow)
			},
			wantWarn: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := New(NewMockDriver())

			var mu sync.Mutex
			var msgs []string
			b.SetLogger(func(msg string) {
				mu.Lock()
				msgs = append(msgs, msg)
				mu.Unlock()
			})

			tt.configure(b)

			mu.Lock()
			got := len(msgs)
			mu.Unlock()

			if got != tt.wantWarn {
				t.Fatalf("warning count: got %d, want %d (msgs=%v)", got, tt.wantWarn, msgs)
			}
			if tt.wantWarn > 0 && !strings.Contains(msgs[0], "SetAuthSecret") {
				t.Errorf("warning should mention SetAuthSecret, got %q", msgs[0])
			}
		})
	}
}

// TestSetAuthSecret_WarnsWhenClearedWithCustomAuthorizer covers the config-reload
// regression: a secret is set, a custom authorizer installed (no warn so far),
// then SetAuthSecret(nil) clears the secret while the authorizer stays. That
// transition silently re-opens the unauthenticated-subscribe gap, so it must emit the warning.
func TestSetAuthSecret_WarnsWhenClearedWithCustomAuthorizer(t *testing.T) {
	allow := func(channel string, user interface{}) bool { return true }

	tests := []struct {
		name      string
		configure func(b *BroadcastManager)
		wantWarn  int
	}{
		{
			name: "clearing secret while custom authorizer installed warns",
			configure: func(b *BroadcastManager) {
				b.SetAuthSecret([]byte("super-secret-key"))
				b.SetAuthorizer(allow)
				b.SetAuthSecret(nil)
			},
			wantWarn: 1,
		},
		{
			name: "clearing secret with deny-all default does not warn",
			configure: func(b *BroadcastManager) {
				b.SetAuthSecret([]byte("super-secret-key"))
				b.SetAuthSecret(nil)
			},
			wantWarn: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := New(NewMockDriver())

			var mu sync.Mutex
			var msgs []string
			b.SetLogger(func(msg string) {
				mu.Lock()
				msgs = append(msgs, msg)
				mu.Unlock()
			})

			tt.configure(b)

			mu.Lock()
			got := len(msgs)
			mu.Unlock()

			if got != tt.wantWarn {
				t.Fatalf("warning count: got %d, want %d (msgs=%v)", got, tt.wantWarn, msgs)
			}
			if tt.wantWarn > 0 && !strings.Contains(msgs[0], "SetAuthSecret") {
				t.Errorf("warning should mention SetAuthSecret, got %q", msgs[0])
			}
		})
	}
}

// TestSetAuthorizer_WarnsAtMostOnce ensures a hot reconfigure loop that
// repeatedly installs authorizers without a secret does not spam the log.
func TestSetAuthorizer_WarnsAtMostOnce(t *testing.T) {
	b := New(NewMockDriver())

	var count int
	b.SetLogger(func(string) { count++ })

	allow := func(channel string, user interface{}) bool { return true }
	for i := 0; i < 5; i++ {
		b.SetAuthorizer(allow)
	}

	if count != 1 {
		t.Fatalf("warning fired %d times, want exactly 1", count)
	}
}

// TestSetAuthorizer_WarnConcurrent exercises the warning path under -race with
// concurrent SetAuthorizer / SetLogger / SetAuthSecret callers.
func TestSetAuthorizer_WarnConcurrent(t *testing.T) {
	b := New(NewMockDriver())
	allow := func(channel string, user interface{}) bool { return true }

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.SetLogger(func(string) {})
			b.SetAuthorizer(allow)
			b.SetAuthSecret([]byte("k"))
		}()
	}
	wg.Wait()
}
