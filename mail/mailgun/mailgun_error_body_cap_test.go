package mailgun

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/mail"
)

// newMailgunDriverAgainstServer constructs a driver pointing at the given
// test server. The driver is configured with an https-looking endpoint and
// then mutated to use the test server's URL because NewMailgunDriver
// rejects http endpoints by design.
func newMailgunDriverAgainstServer(t *testing.T, ts *httptest.Server) *MailgunDriver {
	t.Helper()
	driver, err := NewMailgunDriver(mail.MailgunConfig{
		Domain:   "mg.example.com",
		Secret:   "key",
		Endpoint: "https://api.mailgun.net/v3",
	}, "from@example.com", "From Name")
	if err != nil {
		t.Fatalf("NewMailgunDriver: %v", err)
	}
	// Repoint at the test server. The https constraint is only enforced at
	// construction, not when Send dials.
	driver.endpoint = ts.URL
	return driver
}

func sendTestMessage(t *testing.T, driver *MailgunDriver) error {
	t.Helper()
	msg := mail.NewMessage().
		To("to@example.com").
		Subject("Subject").
		TextBody("body")
	return driver.Send(context.Background(), msg)
}

// TestMailgunDriverErrorBodyRedacted verifies that a JSON error response
// body is never echoed into the returned error. Mailgun error bodies can
// carry sensitive detail (domain names, account identifiers, upstream
// diagnostics); only the HTTP status code may surface.
func TestMailgunDriverErrorBodyRedacted(t *testing.T) {
	const secret = "sk-super-secret-token-do-not-leak"
	body := `{"message":"auth failed for key ` + secret + `"}`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(body))
	}))
	defer ts.Close()

	driver := newMailgunDriverAgainstServer(t, ts)

	err := sendTestMessage(t, driver)
	if err == nil {
		t.Fatalf("expected error from non-200 response, got nil")
	}

	errStr := err.Error()
	// The status code must still surface so callers can tell what happened.
	if !strings.Contains(errStr, "401") {
		t.Errorf("expected status code 401 in error, got: %q", errStr)
	}
	// No fragment of the response body may leak into the error.
	if strings.Contains(errStr, secret) {
		t.Errorf("error leaked secret from response body: %q", errStr)
	}
	if strings.Contains(errStr, "auth failed") {
		t.Errorf("error leaked message text from response body: %q", errStr)
	}
}

// TestMailgunDriverErrorBodyNonJSONStatusOnly verifies that a malformed
// (non-JSON) error body degrades to a status-only error with none of the
// body content included.
func TestMailgunDriverErrorBodyNonJSONStatusOnly(t *testing.T) {
	const body = "<html>internal gateway secret: hunter2</html>"

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(body))
	}))
	defer ts.Close()

	driver := newMailgunDriverAgainstServer(t, ts)

	err := sendTestMessage(t, driver)
	if err == nil {
		t.Fatalf("expected error from non-200 response, got nil")
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "502") {
		t.Errorf("expected status code 502 in error, got: %q", errStr)
	}
	if strings.Contains(errStr, "hunter2") || strings.Contains(errStr, "<html>") {
		t.Errorf("error leaked non-JSON response body: %q", errStr)
	}
}

// TestMailgunDriverErrorBodyOversizedBounded verifies that an oversized
// error response body is read at most up to the preview cap and that the
// returned error stays small and body-free. Without the cap a misbehaving
// Mailgun proxy could exhaust memory by streaming an unbounded body.
func TestMailgunDriverErrorBodyOversizedBounded(t *testing.T) {
	// 1 MiB of 'A' is dramatically larger than the cap and would have been
	// fully read+stringified by the old io.ReadAll(resp.Body) call.
	const bodySize = 1 << 20
	huge := strings.Repeat("A", bodySize)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(huge))
	}))
	defer ts.Close()

	driver := newMailgunDriverAgainstServer(t, ts)

	err := sendTestMessage(t, driver)
	if err == nil {
		t.Fatalf("expected error from non-200 response, got nil")
	}

	errStr := err.Error()
	if !strings.Contains(errStr, "500") {
		t.Errorf("expected status code 500 in error, got: %q", errStr)
	}
	if strings.Contains(errStr, "AAAA") {
		t.Errorf("error leaked oversized response body content: %q", errStr)
	}
	// The error must be a short fixed-shape message, nowhere near body size.
	if len(errStr) > 256 {
		t.Errorf("error length %d unexpectedly large; body content may have leaked", len(errStr))
	}
}
