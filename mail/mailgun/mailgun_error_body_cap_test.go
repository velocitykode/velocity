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

// TestMailgunDriverErrorBodyCappedAtPreview verifies that an oversized
// error response body is read at most up to the preview cap and that the
// returned error carries the truncation marker. Without the cap a
// misbehaving Mailgun proxy could exhaust memory by streaming an
// unbounded body that is then concatenated into the returned error.
func TestMailgunDriverErrorBodyCappedAtPreview(t *testing.T) {
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

	msg := mail.NewMessage().
		To("to@example.com").
		Subject("Subject").
		TextBody("body")

	err := driver.Send(context.Background(), msg)
	if err == nil {
		t.Fatalf("expected error from non-200 response, got nil")
	}

	errStr := err.Error()
	// The status code must still surface so callers can tell what happened.
	if !strings.Contains(errStr, "500") {
		t.Errorf("expected status code 500 in error, got: %q", errStr)
	}
	// The truncation marker must appear when the body exceeds the cap.
	if !strings.Contains(errStr, "...(truncated)") {
		t.Errorf("expected truncation marker in error, got: %q", errStr)
	}
	// The error message must not contain anywhere near the full body. We
	// allow some overhead (status code wrapping, marker, prefix) but assert
	// the total error is well under bodySize.
	const overhead = 4096 // ample room for prefix + marker + preview cap
	if len(errStr) > mailgunErrorPreview+overhead {
		t.Errorf("error length %d exceeds cap+overhead %d; body cap not enforced",
			len(errStr), mailgunErrorPreview+overhead)
	}
}

// TestMailgunDriverErrorBodySmallBodyPreserved verifies that small error
// bodies (well under the cap) flow through verbatim with no truncation
// marker. The cap must not change behaviour on the common path.
func TestMailgunDriverErrorBodySmallBodyPreserved(t *testing.T) {
	const small = `{"message":"Domain not found: mg.example.com"}`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(small))
	}))
	defer ts.Close()

	driver := newMailgunDriverAgainstServer(t, ts)

	msg := mail.NewMessage().
		To("to@example.com").
		Subject("Subject").
		TextBody("body")

	err := driver.Send(context.Background(), msg)
	if err == nil {
		t.Fatalf("expected error from non-200 response, got nil")
	}

	errStr := err.Error()
	if !strings.Contains(errStr, small) {
		t.Errorf("expected full small body to appear in error, got: %q", errStr)
	}
	if strings.Contains(errStr, "...(truncated)") {
		t.Errorf("small body should not be marked truncated, got: %q", errStr)
	}
}

// TestMailgunDriverErrorBodyAtExactCap verifies the boundary condition: a
// body whose size equals the cap exactly is preserved verbatim and not
// marked truncated. Only bodies strictly larger than the cap trigger the
// marker.
func TestMailgunDriverErrorBodyAtExactCap(t *testing.T) {
	body := strings.Repeat("X", mailgunErrorPreview)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(body))
	}))
	defer ts.Close()

	driver := newMailgunDriverAgainstServer(t, ts)

	msg := mail.NewMessage().
		To("to@example.com").
		Subject("Subject").
		TextBody("body")

	err := driver.Send(context.Background(), msg)
	if err == nil {
		t.Fatalf("expected error from non-200 response, got nil")
	}
	if strings.Contains(err.Error(), "...(truncated)") {
		t.Errorf("body of exactly the cap size should not be marked truncated")
	}
}
