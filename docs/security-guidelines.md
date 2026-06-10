# Security Guidelines

Operational guidance for deploying Velocity applications securely. This is
the companion to [`SECURITY.md`](../SECURITY.md) (vulnerability reporting)
and [`docs/configuration.md`](configuration.md) (environment-variable
surface). Finding IDs (V2-xx) reference the OWASP v2 review report,
`docs/owasp-claude-v2.md`. Entries here close findings that need operator
awareness rather than a code change.

## Signed URLs: always set an expiry (V2-24)

`SignedURL` accepts a zero `expiresAt` (`time.Time{}`) and mints a URL with
no `expires` parameter. Such a URL is a permanent capability: it stays valid
until `APP_KEY` is rotated, and revoking one URL means revoking every signed
URL in the application. Prefer `TemporarySignedURL` with an explicit TTL for
every user-facing link (password reset, unsubscribe, downloads), and adopt a
team-wide maximum TTL (hours to days, not weeks). Reserve zero-expiry URLs
for cases where permanence is the documented intent. See `router/signed.go`.

## Queue: never accept unsigned payloads on shared stores (V2-26)

`QUEUE_ACCEPT_UNSIGNED=true` (and the dev/test profile's implicit unsigned
acceptance) disables HMAC verification of queue payloads. A worker running
unsigned will deserialize and execute any payload present in the store, so
anyone with write access to the queue backend (Redis, database) can
instantiate every registered job, command, or listener type with
attacker-controlled fields. Never set it where the queue store is shared
with other tenants or reachable from production-like networks; it exists
only for key-rotation migrations and isolated local development. The boot
warning in `queue/signing.go` is the operational signal that a fleet is
running unsigned.

## Queue: signing key practice (V2-13)

Set a dedicated `QUEUE_SIGNING_KEY` in production rather than relying on
the `APP_KEY` fallback (the fallback HKDF-derives a queue-specific subkey,
but a dedicated key rotates independently). The key is currently used
as raw HMAC-SHA256 material with no minimum-length enforcement
(`queue/signing.go`); provision at least 32 bytes of randomness yourself.
Pending change (owasp-v2-async-stores initiative, not yet landed): a
32-byte key floor and a `QUEUE_ENCRYPT` option for payload encryption.
Update this section when those land.

## HTTP client events carry full URLs (V2-25f)

`httpclient` dispatches `RequestSent` / `RequestFailed` events whose `URL`
field is the complete request URL including path and query
(`httpclient/client.go:531`). Webhook endpoints, presigned URLs, and APIs
that pass tokens in the query string therefore leak secrets into any
listener that logs the event. Treat event URLs as sensitive: redact query
strings (or known-sensitive parameters) before logging, or log only
scheme, host, and path.

## TLS terminates at the proxy (V2-25g)

The framework serves plaintext HTTP by design: `Serve` calls
`http.Server.ListenAndServe` (`serve.go:114`) and offers no TLS listener.
The deployment assumption is a TLS-terminating reverse proxy (Caddy, nginx,
a load balancer) in front of the app. Exposing the app port directly does
not fail loudly; it silently ships `Secure` cookies over plaintext, where
they are never sent back by browsers, and strips transport encryption.
Bind the app to localhost or a private interface, and configure HSTS at
the proxy.

## AES-GCM nonce bounds and key rotation (V2-25h)

The GCM encrypter generates a random 96-bit nonce per message
(`crypto/drivers/aes.go`). Random nonces carry a birthday bound: after
about 2^32 messages under one key, nonce collision probability becomes
non-negligible, and a collision breaks confidentiality and authenticity
for the affected messages. This is unreachable for typical session and
cookie workloads. Deployments encrypting at extreme volume (billions of
messages, e.g. bulk queue or cache encryption) should rotate `APP_KEY` or
the relevant subsystem key well before that bound, on a scheduled cadence.

## Closed by the OWASP v2 remediation wave

Findings from the same review that were fixed in code rather than
documented here, with their land commits (platform-policy wave):

- `a0d3eb3` console: `--force` required for destructive db commands in production
- `e492327` console: identifier charset validation on all `make:*` inputs
- `80c82ba` mail: Mailgun API error bodies redacted to match Postmark posture
- `505afee` storage/orm: S3 CopySource encoding, full-read MIME sniff, quoted DDL defaults
- `9f5e897` exceptions: app logger wired into default error reporting
- `acfc9fc` crypto: AAD bound into CBC HMAC, flash-cookie domain separation
- `d92c233` orm: deny-by-default mass assignment for map-based writes

The web-core and async-stores initiative waves track their own findings;
see [`docs/owasp-claude-v2.md`](owasp-claude-v2.md).
