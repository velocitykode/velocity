// Package webhook provides primitives for signing, verifying, and retrying
// outbound webhook deliveries. The package is a leaf: it has no dependency
// on other framework packages and only uses the Go standard library so
// consumers can compose it freely with their own queue, cache, or transport
// of choice.
//
// Three primitives ship in this package:
//
//   - Signer:      computes a Stripe-style "t=<unix>,v1=<hex>" header value
//     over a payload using a pluggable Algorithm (HMAC-SHA256
//     by default).
//   - Verifier:    parses the same header, recompares the MAC in constant
//     time, enforces a timestamp Tolerance, and (optionally)
//     rejects replays via a NonceStore.
//   - RetryPolicy: returns the next retry delay using exponential backoff
//     with bounded uniform jitter and a hard MaxAttempts cap.
//
// Errors returned from Verify are sentinel values declared in this file.
// They never embed the payload, secret, or computed MAC. Callers are
// expected to log only the error and return a generic failure to the
// remote peer.
package webhook

import "errors"

// ErrSignatureMismatch is returned by Verifier.Verify when the recomputed
// MAC does not match the supplied signature. The compare is constant-time
// to avoid leaking timing information about the secret.
var ErrSignatureMismatch = errors.New("webhook: signature mismatch")

// ErrTimestampOutOfTolerance is returned when the timestamp embedded in the
// signature header is older (or further in the future) than Verifier.Tolerance
// from the current wall-clock time.
var ErrTimestampOutOfTolerance = errors.New("webhook: timestamp out of tolerance")

// ErrReplay is returned when a NonceStore is configured on the Verifier and
// the nonce derived from the signature has already been observed.
var ErrReplay = errors.New("webhook: replay detected")

// ErrMalformedHeader is returned when the signature header cannot be parsed
// (missing fields, wrong format, non-numeric timestamp, non-hex signature).
// The error never echoes the offending header value to avoid log-injection.
var ErrMalformedHeader = errors.New("webhook: malformed signature header")

// ErrMissingSecret is returned when Sign or Verify is called on a Signer
// or Verifier whose Secret is nil or empty.
var ErrMissingSecret = errors.New("webhook: missing secret")

// ErrNoAlgorithm is returned when Sign or Verify is called on a Signer or
// Verifier whose Algorithm is nil.
var ErrNoAlgorithm = errors.New("webhook: no algorithm configured")
