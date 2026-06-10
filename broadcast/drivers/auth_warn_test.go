package drivers

import (
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/websocket"
)

// captureLogger records Warn calls for assertion. Satisfies the driver's
// Logger interface.
type captureLogger struct {
	mu    sync.Mutex
	warns []string
}

func (l *captureLogger) Info(msg string, kvs ...any)  {}
func (l *captureLogger) Error(msg string, kvs ...any) {}
func (l *captureLogger) Warn(msg string, kvs ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, msg)
}

func (l *captureLogger) warnCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.warns)
}

func subscribeMsg(channel string, token string) websocket.Message {
	data := map[string]interface{}{"channel": channel}
	if token != "" {
		data["auth"] = token
	}
	return websocket.Message{Type: "subscribe", Data: data}
}

// TestHandleSubscribe_AuthorizerWithoutVerifier_WarnsOnce covers audit V2-17:
// installing a channel authorizer without an auth secret / token verifier
// must emit a one-shot WARN on restricted subscribes, because the authorizer
// alone gates access with no cryptographic user binding. The warning must
// fire exactly once across repeated subscribes.
func TestHandleSubscribe_AuthorizerWithoutVerifier_WarnsOnce(t *testing.T) {
	d := &WebSocketDriver{
		channels:   make(map[string]map[string]*websocket.Client),
		authorizer: denyAllChannelAuthorizer,
	}
	logger := &captureLogger{}
	d.SetLogger(logger)
	d.SetAuthorizer(func(client *websocket.Client, channel string) bool { return true })

	client := createTestClient("warn-1")
	for _, ch := range []string{"private-a", "presence-b"} {
		if err := d.handleSubscribe(client, subscribeMsg(ch, "")); err != nil {
			t.Fatalf("subscribe to %s: %v", ch, err)
		}
	}

	if got := logger.warnCount(); got != 1 {
		t.Fatalf("expected exactly 1 warning, got %d: %v", got, logger.warns)
	}
	if !strings.Contains(logger.warns[0], "no auth-token verifier") {
		t.Fatalf("warning text missing verifier mention: %q", logger.warns[0])
	}
}

// TestHandleSubscribe_AuthorizerWithVerifier_NoWarn: when a token verifier is
// installed (the SetAuthSecret-wired path), the V2-17 warning must not fire.
func TestHandleSubscribe_AuthorizerWithVerifier_NoWarn(t *testing.T) {
	d := &WebSocketDriver{
		channels:   make(map[string]map[string]*websocket.Client),
		authorizer: denyAllChannelAuthorizer,
	}
	logger := &captureLogger{}
	d.SetLogger(logger)
	d.SetAuthorizer(func(client *websocket.Client, channel string) bool { return true })
	d.SetTokenVerifier(func(socketID, channel, token string) bool { return token == "valid" })

	client := createTestClient("warn-2")
	if err := d.handleSubscribe(client, subscribeMsg("private-a", "valid")); err != nil {
		t.Fatalf("subscribe with valid token: %v", err)
	}

	if got := logger.warnCount(); got != 0 {
		t.Fatalf("expected no warnings, got %d: %v", got, logger.warns)
	}
}

// TestHandleSubscribe_DefaultDeny_NoWarn: with no application authorizer
// installed, the deny-all default still rejects restricted subscribes and the
// V2-17 warning stays silent (there is no permissive-authorizer gap to flag).
func TestHandleSubscribe_DefaultDeny_NoWarn(t *testing.T) {
	d := &WebSocketDriver{
		channels:   make(map[string]map[string]*websocket.Client),
		authorizer: denyAllChannelAuthorizer,
	}
	logger := &captureLogger{}
	d.SetLogger(logger)

	client := createTestClient("warn-3")
	err := d.handleSubscribe(client, subscribeMsg("private-a", ""))
	if err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("expected unauthorized error, got %v", err)
	}

	if got := logger.warnCount(); got != 0 {
		t.Fatalf("expected no warnings, got %d: %v", got, logger.warns)
	}
}

// TestHandleSubscribe_VerifierClearedAfterSecretRemoval_Warns: clearing the
// verifier (SetAuthSecret with empty secret wires SetTokenVerifier(nil))
// reopens the V2-17 gap; the next restricted subscribe must warn.
func TestHandleSubscribe_VerifierClearedAfterSecretRemoval_Warns(t *testing.T) {
	d := &WebSocketDriver{
		channels:   make(map[string]map[string]*websocket.Client),
		authorizer: denyAllChannelAuthorizer,
	}
	logger := &captureLogger{}
	d.SetLogger(logger)
	d.SetAuthorizer(func(client *websocket.Client, channel string) bool { return true })
	d.SetTokenVerifier(func(socketID, channel, token string) bool { return true })

	client := createTestClient("warn-4")
	if err := d.handleSubscribe(client, subscribeMsg("private-a", "tok")); err != nil {
		t.Fatalf("subscribe with verifier: %v", err)
	}
	if got := logger.warnCount(); got != 0 {
		t.Fatalf("expected no warnings while verifier installed, got %d", got)
	}

	d.SetTokenVerifier(nil)
	if err := d.handleSubscribe(client, subscribeMsg("private-b", "")); err != nil {
		t.Fatalf("subscribe after verifier cleared: %v", err)
	}
	if got := logger.warnCount(); got != 1 {
		t.Fatalf("expected 1 warning after verifier cleared, got %d: %v", got, logger.warns)
	}
}
