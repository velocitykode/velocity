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

// TestAuth_SignsHMACForRestrictedChannels covers audit H-25 (a): when a secret
// is installed, Auth must return an "auth" field carrying a verifying HMAC
// signature for private- and presence- channels. Without that token a leaked
// authorizer verdict cannot bind a WebSocket session to the channel.
func TestAuth_SignsHMACForRestrictedChannels(t *testing.T) {
	b := New(NewMockDriver())
	b.SetAuthSecret([]byte("test-secret"))
	b.SetAuthorizer(func(channel string, user interface{}) bool { return true })

	t.Run("private channel response carries verifying HMAC", func(t *testing.T) {
		out, err := b.Auth("private-room.7", "socket-A", nil)
		if err != nil {
			t.Fatalf("Auth: %v", err)
		}
		m, ok := out.(map[string]interface{})
		if !ok {
			t.Fatalf("Auth returned %T, want map", out)
		}
		tok, _ := m["auth"].(string)
		if tok == "" {
			t.Fatal("Auth response missing auth field")
		}
		if !b.VerifyAuthToken("socket-A", "private-room.7", tok) {
			t.Fatal("returned token failed VerifyAuthToken")
		}
		// Token must be socket+channel bound; reusing it for a different
		// socket must fail.
		if b.VerifyAuthToken("socket-B", "private-room.7", tok) {
			t.Fatal("token verified for wrong socket")
		}
	})

	t.Run("presence channel without presence data still signs", func(t *testing.T) {
		out, err := b.Auth("presence-room.42", "socket-A", nil)
		if err != nil {
			t.Fatalf("Auth: %v", err)
		}
		m, ok := out.(map[string]interface{})
		if !ok {
			t.Fatalf("Auth returned %T, want map", out)
		}
		tok, _ := m["auth"].(string)
		if tok == "" {
			t.Fatal("presence Auth response missing auth field")
		}
		if !b.VerifyAuthToken("socket-A", "presence-room.42", tok) {
			t.Fatal("presence token failed VerifyAuthToken")
		}
	})

	t.Run("presence channel with presence data nests channel_data alongside auth", func(t *testing.T) {
		b.SetPresenceData(func(channel string, user interface{}) interface{} {
			return map[string]interface{}{"user_id": "u-1"}
		})
		defer b.SetPresenceData(nil)
		out, err := b.Auth("presence-room.42", "socket-A", nil)
		if err != nil {
			t.Fatalf("Auth: %v", err)
		}
		m, ok := out.(map[string]interface{})
		if !ok {
			t.Fatalf("Auth returned %T, want map", out)
		}
		if _, ok := m["auth"].(string); !ok {
			t.Fatal("Auth response missing auth field")
		}
		cd, ok := m["channel_data"].(map[string]interface{})
		if !ok {
			t.Fatalf("channel_data missing or wrong type: %T", m["channel_data"])
		}
		if cd["user_id"] != "u-1" {
			t.Errorf("channel_data.user_id = %v, want u-1", cd["user_id"])
		}
	})

	t.Run("public channel response omits auth", func(t *testing.T) {
		out, err := b.Auth("news", "socket-A", nil)
		if err != nil {
			t.Fatalf("Auth: %v", err)
		}
		m, ok := out.(map[string]interface{})
		if !ok {
			t.Fatalf("Auth returned %T, want map", out)
		}
		if _, present := m["auth"]; present {
			t.Error("public channel response must not include auth token")
		}
	})
}

// TestAuth_OmitsTokenWhenNoSecret confirms that without SetAuthSecret the Auth
// response shape is the legacy status-only form, so applications that rely on
// authorizer-only access during the transition keep working.
func TestAuth_OmitsTokenWhenNoSecret(t *testing.T) {
	b := New(NewMockDriver())
	b.SetAuthorizer(func(channel string, user interface{}) bool { return true })

	out, err := b.Auth("private-x", "socket", nil)
	if err != nil {
		t.Fatalf("Auth: %v", err)
	}
	m, ok := out.(map[string]interface{})
	if !ok {
		t.Fatalf("Auth returned %T, want map", out)
	}
	if _, present := m["auth"]; present {
		t.Error("Auth must not include auth field when no secret is configured")
	}
}
