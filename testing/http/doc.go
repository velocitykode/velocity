// Package http provides HTTP testing helpers: a router-driven TestClient with
// fluent response assertions, validation assertions, and multipart upload
// builders.
//
// # Naming debt (acknowledged, deferred to v1.0)
//
// The package name "http" collides with the standard library's net/http.
// Consumers import it with an alias, conventionally:
//
//	velhttp "github.com/velocitykode/velocity/testing/http"
//
// The consumer-facing entry point for the testing toolkit is the velocitytest
// package; this package is one of its building blocks. The sibling directory
// testing/ is package testsync (see testing/sync.go), another deliberate
// non-matching directory/package name. Both names are recognized pre-1.0
// naming debt and any rename is deferred to the v1.0 boundary so it lands as a
// single intentional break.
//
// # TestingT vs testing.TB
//
// Helpers here accept the minimal TestingT interface (Helper + Errorf, defined
// in upload.go) rather than the full testing.TB. This keeps the helpers usable
// from mock recorders in this package's own failure-path tests and avoids
// coupling callers to the entire testing.TB surface. Widening TestingT to
// testing.TB is likewise deferred to v1.0.
package http
