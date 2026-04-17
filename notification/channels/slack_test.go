package channels

import (
	"strings"
	"testing"
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
