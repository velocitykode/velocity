package channels

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/httpclient"
	"github.com/velocitykode/velocity/notification"
)

func TestValidateWebhookURL_AllowsPublicHost(t *testing.T) {
	// hooks.slack.com resolves to public IPs; skip if DNS is offline.
	if err := validateWebhookURL("https://hooks.slack.com/services/TTT/BBB/XXX"); err != nil {
		if strings.Contains(err.Error(), "resolve") {
			t.Skip("DNS unavailable, skipping public-host check")
		}
		t.Errorf("public webhook rejected: %v", err)
	}
}

func TestValidateWebhookURL_BlocksLoopback(t *testing.T) {
	for _, u := range []string{
		"http://127.0.0.1/hook",
		"http://localhost/hook",
		"http://[::1]/hook",
	} {
		if err := validateWebhookURL(u); err == nil {
			t.Errorf("%s must be rejected", u)
		}
	}
}

func TestValidateWebhookURL_BlocksMetadataIP(t *testing.T) {
	if err := validateWebhookURL("http://169.254.169.254/latest/meta-data/"); err == nil {
		t.Fatal("metadata IP must be rejected")
	}
}

func TestValidateWebhookURL_BlocksPrivateRanges(t *testing.T) {
	for _, u := range []string{
		"http://10.0.0.1/hook",
		"http://172.16.0.1/hook",
		"http://192.168.1.1/hook",
		"http://100.64.0.1/hook",
		"http://[fc00::1]/hook",
	} {
		if err := validateWebhookURL(u); err == nil {
			t.Errorf("%s must be rejected", u)
		}
	}
}

func TestValidateWebhookURL_BlocksBadScheme(t *testing.T) {
	if err := validateWebhookURL("file:///etc/passwd"); err == nil {
		t.Fatal("file scheme must be rejected")
	}
	if err := validateWebhookURL("ftp://example.com/"); err == nil {
		t.Fatal("ftp scheme must be rejected")
	}
}

// slackRebindNotifiable lets tests inject the webhook URL directly while
// still satisfying the Notifiable contract.
type slackRebindNotifiable struct {
	webhook string
}

func (n *slackRebindNotifiable) NotificationRoute(channel string) string {
	if channel == "slack" {
		return n.webhook
	}
	return ""
}

type slackPingNotification struct{}

func (slackPingNotification) Via(_ interface{}) []string { return []string{"slack"} }

func (slackPingNotification) ToSlack(_ interface{}) *notification.SlackMessage {
	return notification.NewSlackMessage().Content("ping")
}

// TestSlackChannel_DialGuard_RefusesLoopbackAfterValidateBypass proves the
// SSRF defence is layered. validateWebhookURL is the first guard and
// catches loopback URLs at validation time. The httpclient.Client behind
// the channel is the second guard: even if a hostname resolved to a
// public IP at validation time and later flipped to 127.0.0.1 (DNS
// rebinding), the dial would still be refused because the guard reruns
// on every connect and pins the resolved IP. We exercise the dial layer
// directly here so the test fails if a future change downgrades the
// underlying client to a bare http.Client (the M-24 regression).
func TestSlackChannel_DialGuard_RefusesLoopbackAfterValidateBypass(t *testing.T) {
	var hit atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	ch := NewSlackChannel()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, backend.URL+"/hook", bytes.NewBufferString("{}"))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := ch.client.Do(context.Background(), req)
	if err == nil {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatal("expected SSRF guard to refuse loopback dial; request reached backend")
	}
	if hit.Load() {
		t.Fatal("guard let the loopback request through")
	}
	// The wrapped error must mention the private destination so operators
	// can distinguish SSRF block from a generic transport failure.
	if !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "internal") {
		t.Errorf("expected private/internal error, got %v", err)
	}
}

// TestSlackChannel_Send_PublicWebhookAllowed proves the channel still
// works when the destination is on the SSRF allowlist. The httptest
// server binds to 127.0.0.1, so we swap the channel client for one that
// whitelists loopback (mirroring how a httpclient.New(WithAllowedHosts)
// caller would explicitly permit a known internal target).
func TestSlackChannel_Send_PublicWebhookAllowed(t *testing.T) {
	var hit atomic.Bool
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	ch := NewSlackChannel()
	ch.client = httpclient.New(httpclient.WithAllowedHosts("127.0.0.1"))

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, backend.URL+"/hook", bytes.NewBufferString("{}"))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := ch.client.Do(context.Background(), req)
	if err != nil {
		t.Fatalf("expected allowlisted dial to succeed, got %v", err)
	}
	defer resp.Body.Close()
	if !hit.Load() {
		t.Fatal("backend was not reached even with allowlist")
	}
}

// TestSlackChannel_Send_RejectsPrivateWebhookURL is a behavior-level
// regression on the public Send path: a SlackNotification routed to a
// loopback webhook must be refused before any HTTP machinery fires.
func TestSlackChannel_Send_RejectsPrivateWebhookURL(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("loopback backend must not be reached")
	}))
	defer backend.Close()

	ch := NewSlackChannel()
	err := ch.Send(context.Background(), &slackRebindNotifiable{webhook: backend.URL + "/hook"}, slackPingNotification{})
	if err == nil {
		t.Fatal("expected loopback webhook to be refused")
	}
	if !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "internal") {
		t.Errorf("expected private/internal error, got %v", err)
	}
}

// TestSlackChannel_Send_RejectsMetadataIP pins the cloud-metadata exfil
// vector at the channel level (AWS/GCP IMDS endpoint).
func TestSlackChannel_Send_RejectsMetadataIP(t *testing.T) {
	ch := NewSlackChannel()
	err := ch.Send(context.Background(), &slackRebindNotifiable{webhook: "http://169.254.169.254/latest/meta-data/iam/security-credentials/"}, slackPingNotification{})
	if err == nil {
		t.Fatal("expected metadata IP to be refused")
	}
}

// TestSlackChannel_Send_NoWebhookURL pins the "notifiable has no slack
// route" error path so refactors do not silently swallow it.
func TestSlackChannel_Send_NoWebhookURL(t *testing.T) {
	ch := NewSlackChannel()
	err := ch.Send(context.Background(), &slackRebindNotifiable{webhook: ""}, slackPingNotification{})
	if err == nil {
		t.Fatal("expected missing webhook URL to be refused")
	}
	if !strings.Contains(err.Error(), "webhook URL") {
		t.Errorf("expected webhook URL error, got %v", err)
	}
}

// TestSlackChannel_DialGuard_PrivateLiteralBlocked exercises the most
// realistic DNS-rebinding path the channel can hit in production: the
// resolver returns a private address for what looked like a public host.
// We bypass validateWebhookURL by passing an IP literal directly to the
// dial layer; every literal in disallowed space must be refused.
func TestSlackChannel_DialGuard_PrivateLiteralBlocked(t *testing.T) {
	for _, addr := range []string{
		"http://127.0.0.1:1/",
		"http://10.0.0.1:1/",
		"http://192.168.1.1:1/",
		"http://169.254.169.254/",
	} {
		ch := NewSlackChannel()
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, addr, nil)
		if err != nil {
			t.Fatalf("build %s: %v", addr, err)
		}
		_, err = ch.client.Do(context.Background(), req)
		if err == nil {
			t.Errorf("dial to %s must be refused", addr)
			continue
		}
		if !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "internal") {
			t.Errorf("dial %s: expected private/internal error, got %v", addr, err)
		}
	}
}
