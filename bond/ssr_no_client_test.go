package bond

import (
	"context"
	"errors"
	"testing"
)

// TestHTTPGateway_Dispatch_NoClient verifies that a zero-value HTTPGateway
// (Client == nil) refuses to dispatch rather than silently falling back to
// http.DefaultClient, which would bypass the TLS/redirect/SSRF hardening
// applied to httpclient elsewhere.
func TestHTTPGateway_Dispatch_NoClient(t *testing.T) {
	g := &HTTPGateway{URL: "http://127.0.0.1:1/render", ThrowOnError: true}

	page := Page{Component: "Test"}
	_, err := g.Dispatch(context.Background(), page)
	if !errors.Is(err, ErrNoClient) {
		t.Fatalf("Dispatch with nil Client: err = %v, want ErrNoClient", err)
	}
}

func TestHTTPGateway_IsHealthy_NoClient(t *testing.T) {
	g := &HTTPGateway{URL: "http://127.0.0.1:1/render"}

	ok, err := g.IsHealthy(context.Background())
	if !errors.Is(err, ErrNoClient) {
		t.Fatalf("IsHealthy with nil Client: err = %v, want ErrNoClient", err)
	}
	if ok {
		t.Fatalf("IsHealthy with nil Client: ok = true, want false")
	}
}

// TestNewHTTPGateway_SetsClient sanity-checks that the constructor installs
// a non-nil *http.Client, so ErrNoClient is only ever seen by callers who
// build HTTPGateway{} as a struct literal.
func TestNewHTTPGateway_SetsClient(t *testing.T) {
	g := NewHTTPGateway("http://127.0.0.1:13714/render")
	if g.Client == nil {
		t.Fatal("NewHTTPGateway produced a gateway with nil Client")
	}
}
