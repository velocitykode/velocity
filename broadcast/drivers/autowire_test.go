package drivers

import (
	"testing"

	"github.com/velocitykode/velocity/broadcast"
	"github.com/velocitykode/velocity/websocket"
)

// TestAutoWire_VerifierInstalledOnSetAuthSecret covers followup F1: the
// canonical construction pattern (broadcast.New(driver); b.SetAuthSecret(s))
// must by itself protect private/presence subscribes. Before F1 a consumer
// who set the secret but forgot the separate driver.SetTokenVerifier call
// left handleSubscribe accepting subscribes with no auth field.
func TestAutoWire_VerifierInstalledOnSetAuthSecret(t *testing.T) {
	// Build the driver without calling NewWebSocketDriver because that
	// starts the underlying websocket server. We only exercise
	// handleSubscribe, which operates on the in-memory channels map.
	driver := &WebSocketDriver{
		channels:   make(map[string]map[string]*websocket.Client),
		authorizer: func(*websocket.Client, string) bool { return true },
	}

	b := broadcast.New(driver)
	b.SetAuthorizer(func(channel string, user interface{}) bool { return true })
	b.SetAuthSecret([]byte("super-secret-key"))

	// Subscribing to a private channel WITHOUT an auth field must now be
	// rejected even though no explicit driver.SetTokenVerifier call was
	// made. The verifier must have been auto-wired by SetAuthSecret.
	client := createTestClient("client-1")
	err := driver.handleSubscribe(client, websocket.Message{
		Type: "subscribe",
		Data: map[string]interface{}{"channel": "private-room"},
	})
	if err == nil {
		t.Fatal("subscribe without auth must be rejected after SetAuthSecret auto-wires the verifier")
	}

	// Authorized auth-token subscribe must succeed. Token comes from the
	// same BroadcastManager so it carries the matching HMAC.
	token, sErr := b.SignAuthToken("client-1", "private-room")
	if sErr != nil {
		t.Fatalf("SignAuthToken: %v", sErr)
	}
	client = createTestClient("client-1")
	err = driver.handleSubscribe(client, websocket.Message{
		Type: "subscribe",
		Data: map[string]interface{}{
			"channel": "private-room",
			"auth":    token,
		},
	})
	if err != nil {
		t.Fatalf("subscribe with valid auto-wired token must succeed: %v", err)
	}
}

// TestAutoWire_VerifierClearedWhenSecretRemoved verifies that calling
// SetAuthSecret with an empty key removes the auto-wired verifier so the
// driver returns to authorizer-only mode (used during deliberate
// rollbacks or in tests that swap configurations).
func TestAutoWire_VerifierClearedWhenSecretRemoved(t *testing.T) {
	driver := &WebSocketDriver{
		channels:   make(map[string]map[string]*websocket.Client),
		authorizer: func(*websocket.Client, string) bool { return true },
	}

	b := broadcast.New(driver)
	b.SetAuthorizer(func(channel string, user interface{}) bool { return true })
	b.SetAuthSecret([]byte("k"))
	b.SetAuthSecret(nil)

	// With verifier cleared, subscribing without auth must once again be
	// accepted (authorizer-only legacy path).
	client := createTestClient("client-1")
	err := driver.handleSubscribe(client, websocket.Message{
		Type: "subscribe",
		Data: map[string]interface{}{"channel": "private-room"},
	})
	if err != nil {
		t.Fatalf("authorizer-only subscribe after secret cleared: %v", err)
	}
}

// TestAutoWire_SecretRotation verifies that rotating the secret via a
// subsequent SetAuthSecret invalidates tokens minted under the old key.
// The closure stored on the driver MUST resolve b.authSecret each call
// rather than capturing the old value.
func TestAutoWire_SecretRotation(t *testing.T) {
	driver := &WebSocketDriver{
		channels:   make(map[string]map[string]*websocket.Client),
		authorizer: func(*websocket.Client, string) bool { return true },
	}

	b := broadcast.New(driver)
	b.SetAuthorizer(func(channel string, user interface{}) bool { return true })

	b.SetAuthSecret([]byte("key-v1"))
	oldToken, err := b.SignAuthToken("client-1", "private-room")
	if err != nil {
		t.Fatalf("SignAuthToken under v1: %v", err)
	}

	b.SetAuthSecret([]byte("key-v2"))

	// Old token should no longer verify after rotation.
	client := createTestClient("client-1")
	err = driver.handleSubscribe(client, websocket.Message{
		Type: "subscribe",
		Data: map[string]interface{}{
			"channel": "private-room",
			"auth":    oldToken,
		},
	})
	if err == nil {
		t.Fatal("old token must be rejected after SetAuthSecret rotation")
	}

	// Fresh token under v2 should verify.
	newToken, err := b.SignAuthToken("client-1", "private-room")
	if err != nil {
		t.Fatalf("SignAuthToken under v2: %v", err)
	}
	client = createTestClient("client-1")
	err = driver.handleSubscribe(client, websocket.Message{
		Type: "subscribe",
		Data: map[string]interface{}{
			"channel": "private-room",
			"auth":    newToken,
		},
	})
	if err != nil {
		t.Fatalf("v2 token rejected: %v", err)
	}
}
