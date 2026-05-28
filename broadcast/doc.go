// Package broadcast provides real-time channel broadcasting (WebSocket
// and Pusher-style backends) configured via the BROADCAST_DRIVER
// environment variable.
//
// # Optional driver capabilities
//
// Drivers MAY implement any of these to opt into framework features:
//
//	TokenVerifierSetter   Receives the framework-built HMAC token
//	                      verifier so private/presence subscribes
//	                      authenticate against the configured auth
//	                      secret. The BroadcastManager auto-installs
//	                      the verifier on any driver that implements
//	                      this when an auth secret is configured.
//	                      Closes the H-25 gap where a consumer that
//	                      configured a secret but forgot to wire the
//	                      driver would silently accept unsigned
//	                      subscribes.
//
// Capability detection is a plain type assertion at the call site.
package broadcast
