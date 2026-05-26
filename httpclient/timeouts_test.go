package httpclient

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestTransportTimeouts_DefaultsPinned proves the M-45 framework
// defaults land on the default-construction transport. The stdlib
// DefaultTransport omits ResponseHeaderTimeout, so the test would also
// fail under the pre-M-45 build (which is the slowloris vector being
// closed).
func TestTransportTimeouts_DefaultsPinned(t *testing.T) {
	c := New()
	tr, ok := c.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", c.client.Transport)
	}
	if tr.TLSHandshakeTimeout != defaultTLSHandshakeTimeout {
		t.Errorf("TLSHandshakeTimeout = %v, want %v", tr.TLSHandshakeTimeout, defaultTLSHandshakeTimeout)
	}
	if tr.ResponseHeaderTimeout != defaultResponseHeaderTimeout {
		t.Errorf("ResponseHeaderTimeout = %v, want %v", tr.ResponseHeaderTimeout, defaultResponseHeaderTimeout)
	}
	if tr.IdleConnTimeout != defaultIdleConnTimeout {
		t.Errorf("IdleConnTimeout = %v, want %v", tr.IdleConnTimeout, defaultIdleConnTimeout)
	}
	if tr.ExpectContinueTimeout != defaultExpectContinueTimeout {
		t.Errorf("ExpectContinueTimeout = %v, want %v", tr.ExpectContinueTimeout, defaultExpectContinueTimeout)
	}
}

// TestWithResponseHeaderTimeout_Override pins the per-option override.
func TestWithResponseHeaderTimeout_Override(t *testing.T) {
	c := New(WithResponseHeaderTimeout(7 * time.Second))
	tr := c.client.Transport.(*http.Transport)
	if tr.ResponseHeaderTimeout != 7*time.Second {
		t.Errorf("ResponseHeaderTimeout = %v, want 7s", tr.ResponseHeaderTimeout)
	}
}

// TestWithResponseHeaderTimeout_ZeroDisables proves the operator can
// switch the per-stage cap off (pointer indirection distinguishes
// "unset" from "explicit zero").
func TestWithResponseHeaderTimeout_ZeroDisables(t *testing.T) {
	c := New(WithResponseHeaderTimeout(0))
	tr := c.client.Transport.(*http.Transport)
	if tr.ResponseHeaderTimeout != 0 {
		t.Errorf("ResponseHeaderTimeout = %v, want 0 (disabled)", tr.ResponseHeaderTimeout)
	}
}

// TestWithTLSHandshakeTimeout_Override pins the per-option override.
func TestWithTLSHandshakeTimeout_Override(t *testing.T) {
	c := New(WithTLSHandshakeTimeout(3 * time.Second))
	tr := c.client.Transport.(*http.Transport)
	if tr.TLSHandshakeTimeout != 3*time.Second {
		t.Errorf("TLSHandshakeTimeout = %v, want 3s", tr.TLSHandshakeTimeout)
	}
}

// TestWithIdleConnTimeout_Override pins the per-option override.
func TestWithIdleConnTimeout_Override(t *testing.T) {
	c := New(WithIdleConnTimeout(45 * time.Second))
	tr := c.client.Transport.(*http.Transport)
	if tr.IdleConnTimeout != 45*time.Second {
		t.Errorf("IdleConnTimeout = %v, want 45s", tr.IdleConnTimeout)
	}
}

// TestWithExpectContinueTimeout_Override pins the per-option override.
func TestWithExpectContinueTimeout_Override(t *testing.T) {
	c := New(WithExpectContinueTimeout(2 * time.Second))
	tr := c.client.Transport.(*http.Transport)
	if tr.ExpectContinueTimeout != 2*time.Second {
		t.Errorf("ExpectContinueTimeout = %v, want 2s", tr.ExpectContinueTimeout)
	}
}

// TestCustomTransport_TimeoutsUntouched confirms WithHTTPClient
// preserves the caller's transport fields when no With*Timeout override
// is set, matching the documented "your transport, your fields"
// contract.
func TestCustomTransport_TimeoutsUntouched(t *testing.T) {
	custom := &http.Transport{
		TLSHandshakeTimeout:   12345 * time.Millisecond,
		ResponseHeaderTimeout: 0,
		IdleConnTimeout:       4 * time.Minute,
		ExpectContinueTimeout: 250 * time.Millisecond,
	}
	c := New(WithHTTPClient(&http.Client{Transport: custom}))
	tr := c.client.Transport.(*http.Transport)
	if tr.TLSHandshakeTimeout != 12345*time.Millisecond {
		t.Errorf("TLSHandshakeTimeout overwritten: got %v", tr.TLSHandshakeTimeout)
	}
	if tr.ResponseHeaderTimeout != 0 {
		t.Errorf("ResponseHeaderTimeout overwritten: got %v", tr.ResponseHeaderTimeout)
	}
	if tr.IdleConnTimeout != 4*time.Minute {
		t.Errorf("IdleConnTimeout overwritten: got %v", tr.IdleConnTimeout)
	}
	if tr.ExpectContinueTimeout != 250*time.Millisecond {
		t.Errorf("ExpectContinueTimeout overwritten: got %v", tr.ExpectContinueTimeout)
	}
}

// TestCustomTransport_TimeoutsHonourExplicitOverride confirms an
// operator who *does* pass a With*Timeout option together with a custom
// transport still gets the override applied. The override is the
// explicit ask; the no-override branch leaves the field alone.
func TestCustomTransport_TimeoutsHonourExplicitOverride(t *testing.T) {
	custom := &http.Transport{ResponseHeaderTimeout: 1 * time.Hour}
	c := New(
		WithHTTPClient(&http.Client{Transport: custom}),
		WithResponseHeaderTimeout(5*time.Second),
	)
	tr := c.client.Transport.(*http.Transport)
	if tr.ResponseHeaderTimeout != 5*time.Second {
		t.Errorf("ResponseHeaderTimeout = %v, want 5s (override should win)", tr.ResponseHeaderTimeout)
	}
}

// TestSlowHeaderServer_TripsResponseHeaderTimeout is the end-to-end
// behavioural proof of the M-45 fix. A backend opens the TCP
// connection, accepts the request, then dribbles header bytes; the
// per-stage timeout must fire well before Client.Timeout (which we
// leave at 30s default) does.
func TestSlowHeaderServer_TripsResponseHeaderTimeout(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer listener.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Read the request bytes off the wire so the client's write
		// completes; then sit on the response without writing any
		// header byte. The transport's ResponseHeaderTimeout must trip.
		buf := make([]byte, 4096)
		_, _ = conn.Read(buf)
		time.Sleep(2 * time.Second)
	}()

	c := New(
		WithoutPrivateIPDeny(),
		WithResponseHeaderTimeout(150*time.Millisecond),
	)

	start := time.Now()
	_, err = c.Get(context.Background(), "http://"+listener.Addr().String()+"/")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected ResponseHeaderTimeout to fire on slow-header server")
	}
	// net.Error timeout flag is the canonical signal from the
	// transport's per-stage timeout.
	var ne net.Error
	if errors.As(err, &ne) {
		if !ne.Timeout() {
			t.Errorf("error reports Timeout()=false: %v", err)
		}
	} else if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error does not look like a timeout: %v", err)
	}
	// 150ms cap should fire well inside 1s; a generous slack guards
	// against scheduler jitter on busy CI hosts.
	if elapsed > time.Second {
		t.Errorf("elapsed = %v, want < 1s (cap fired too late)", elapsed)
	}

	<-done
}

// TestNormalServer_NotAffectedByDefaults confirms the framework default
// budgets do not break legitimate fast responses; this is the
// regression guard against an over-tight default.
func TestNormalServer_NotAffectedByDefaults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	c := New(WithoutPrivateIPDeny())
	resp, err := c.Get(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}
