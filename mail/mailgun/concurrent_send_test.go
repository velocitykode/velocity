package mailgun

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/mail"
)

// TestMailgunConcurrentSend exercises many simultaneous Send calls against a
// single driver. The driver holds no per-Send mutable state, so this must be
// safe without serialisation; run under -race to catch any reintroduced
// shared mutable field.
func TestMailgunConcurrentSend(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// Construct against the test server's endpoint and client (the latter trusts
	// its self-signed cert) entirely through the constructor; no field is
	// mutated after the driver is built.
	driver := newTestDriver(ts.URL, ts.Client())

	const goroutines = 8
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			msg := mail.NewMessage().
				To("to@example.com", "To Name").
				Subject("hello").
				Body("body")
			errs[idx] = driver.Send(context.Background(), msg)
		}(i)
	}
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Errorf("goroutine %d: Send failed: %v", i, e)
		}
	}
}
