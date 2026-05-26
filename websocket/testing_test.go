package websocket

import "net/http"

// originHeader returns the http.Header a test should pass to
// gorilla/websocket's Dial so the upgrade survives the post-H-24 same-origin
// gate. The default dialer omits Origin, which the gate (correctly) rejects;
// real browsers always send Origin, so tests must mirror that to exercise
// behaviour unrelated to origin policy.
//
// Pass an httptest.NewServer URL ("http://127.0.0.1:NNNN"); the header's
// Origin value is set to the same URL so the host matches the request
// authority.
func originHeader(httpURL string) http.Header {
	h := http.Header{}
	h.Set("Origin", httpURL)
	return h
}
