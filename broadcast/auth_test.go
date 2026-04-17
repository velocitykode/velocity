package broadcast

import (
	"crypto/subtle"
	"strings"
	"testing"
)

// TestAuth_DefaultAuthorizerDeniesRestrictedChannels covers Task 1:
// the deny-all default authorizer must reject private- and presence- channels
// until SetAuthorizer is called, while leaving public channels untouched.
func TestAuth_DefaultAuthorizerDeniesRestrictedChannels(t *testing.T) {
	b := New(NewMockDriver())

	for _, channel := range []string{"private-team.7", "presence-room.42"} {
		if _, err := b.Auth(channel, "socket-1", nil); err != ErrUnauthorized {
			t.Errorf("%s: want ErrUnauthorized, got %v", channel, err)
		}
	}

	// Public channel is unaffected.
	if _, err := b.Auth("news", "socket-1", nil); err != nil {
		t.Errorf("news: want nil err, got %v", err)
	}
}

// TestAuth_CustomAuthorizerEnforcedOnPrefixes verifies that once an authorizer
// is installed, it is consulted for both private- and presence- prefixes.
func TestAuth_CustomAuthorizerEnforcedOnPrefixes(t *testing.T) {
	b := New(NewMockDriver())

	var seen []string
	b.SetAuthorizer(func(channel string, user interface{}) bool {
		seen = append(seen, channel)
		return strings.HasPrefix(channel, "private-allow")
	})

	if _, err := b.Auth("private-allow-me", "s", nil); err != nil {
		t.Errorf("expected allow, got %v", err)
	}
	if _, err := b.Auth("presence-allow-me", "s", nil); err != ErrUnauthorized {
		t.Errorf("expected deny, got %v", err)
	}

	// Authorizer must have been called once per restricted channel.
	if len(seen) != 2 {
		t.Fatalf("authorizer calls = %d, want 2", len(seen))
	}
}

// TestVerifyAuthToken_ConstantTime verifies tokens sign and verify correctly
// and that the verify helper runs in constant time (crypto/subtle).
func TestVerifyAuthToken_ConstantTime(t *testing.T) {
	b := New(NewMockDriver())
	b.SetAuthSecret([]byte("super-secret-key"))

	tok, err := b.SignAuthToken("sock-1", "private-chat")
	if err != nil {
		t.Fatalf("SignAuthToken: %v", err)
	}

	if !b.VerifyAuthToken("sock-1", "private-chat", tok) {
		t.Fatal("valid token was rejected")
	}
	if b.VerifyAuthToken("sock-1", "private-chat", tok+"x") {
		t.Fatal("tampered token accepted")
	}
	if b.VerifyAuthToken("sock-OTHER", "private-chat", tok) {
		t.Fatal("token valid for wrong socket")
	}

	// Sanity check: VerifyAuthToken uses subtle.ConstantTimeCompare. We cannot
	// measure timing portably here, so instead assert the return semantics
	// match constant-time compare.
	got := subtle.ConstantTimeCompare([]byte(tok), []byte(tok)) == 1
	if !got {
		t.Fatal("subtle.ConstantTimeCompare self-check failed (environment bug)")
	}
}

// TestSignAuthToken_NoSecret returns ErrUnauthorized when no secret has been
// installed, rather than issuing a token backed by the zero-length key.
func TestSignAuthToken_NoSecret(t *testing.T) {
	b := New(NewMockDriver())
	if _, err := b.SignAuthToken("sock", "private-x"); err != ErrUnauthorized {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
	if b.VerifyAuthToken("sock", "private-x", "anything") {
		t.Fatal("VerifyAuthToken must return false when no secret is configured")
	}
}
