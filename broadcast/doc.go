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
// # Lifecycle hooks
//
// Cross-cutting lifecycle hooks (contract.ShutdownAware) are defined in
// the contract package and apply uniformly to every Velocity manager
// that holds background resources; they are not duplicated in each
// package's capability table.
//
// Capability detection is a plain type assertion at the call site.
package broadcast
