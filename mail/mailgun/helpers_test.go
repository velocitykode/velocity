package mailgun

import "net/http"

// newTestDriver builds a fully-initialised MailgunDriver pointed at the given
// endpoint and HTTP client. Every field is set in the constructing literal so
// the driver is never mutated after construction, matching the production
// invariant that a MailgunDriver is immutable once built. Passing the test
// server's endpoint/client here sidesteps NewMailgunDriver's https-only
// constraint without a post-construction field write.
func newTestDriver(endpoint string, client *http.Client) *MailgunDriver {
	if client == nil {
		client = &http.Client{}
	}
	return &MailgunDriver{
		domain:   "mg.example.com",
		apiKey:   "test-key",
		endpoint: endpoint,
		fromAddr: "from@example.com",
		fromName: "From",
		client:   client,
	}
}
