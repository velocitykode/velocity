// Package app - APP_ENV canonical reader.
//
// Every "is this production?" check in the framework must route through the
// helpers defined here (or the contract package equivalents) so a single
// change to the environment-name vocabulary reaches every gate at once.
//
// The classification logic lives in the contract package (leaf, stdlib-only)
// so subsystems that cannot import app because of cycles (exceptions, auth,
// csrf, scheduler, grpc, ...) call contract.IsProductionEnv directly. This
// file re-exports the helpers for callers that already depend on app and
// adds the process-level Env() reader.
//
// The vocabulary is final for the 1.0 surface: no new names will be added.
package app

import (
	"github.com/velocitykode/velocity/contract"
)

// EnvVar is the canonical environment-variable name for the application
// environment selector. Exported so test fixtures and external probes
// reference one constant instead of repeating the literal string.
// Delegates to contract.EnvVar so the literal "APP_ENV" lives in
// exactly one file.
const EnvVar = contract.EnvVar

// Env returns the normalized APP_ENV value: lowercased, surrounding
// whitespace trimmed. Empty input is returned as "" so callers can
// distinguish "unset" from "explicitly set to production".
//
// Re-exports contract.GetEnv. Use whichever entry point is closer to
// the call site's existing imports: callers already on the app import
// graph use this, leaf packages call contract.GetEnv directly.
func Env() string {
	return contract.GetEnv()
}

// IsProduction reports whether the current APP_ENV names a production-class
// environment. Staging is folded into production so security gates that
// must be enabled in pre-production rollouts fire on the same code path as
// real production. Anything other than the recognised dev/test names is
// treated as production: fail-secure when an operator typos the value.
func IsProduction() bool {
	return contract.IsProductionEnv(Env())
}

// IsTesting reports whether the current APP_ENV names a test environment.
// Test environments relax fail-closed defaults (APP_KEY missing, Secure=false
// cookies, unsigned queue payloads) so unit tests do not have to wire a full
// secret bundle.
func IsTesting() bool {
	return contract.IsTestingEnv(Env())
}

// IsProductionEnv is the parameterised variant of IsProduction for packages
// that receive the env name explicitly. Delegates to contract.IsProductionEnv
// so the vocabulary lives in exactly one place.
func IsProductionEnv(env string) bool {
	return contract.IsProductionEnv(env)
}

// IsTestingEnv is the parameterised variant of IsTesting.
func IsTestingEnv(env string) bool {
	return contract.IsTestingEnv(env)
}

// IsDevelopmentEnv reports whether env names a development environment.
// Useful for emitting dev-only warnings without folding in test profiles.
func IsDevelopmentEnv(env string) bool {
	return contract.IsDevelopmentEnv(env)
}

// IsDevOrTestEnv reports whether env names a development or test profile.
// Used by config validators that relax Secure=false / HttpOnly=false /
// unsigned-payload defaults only when the operator has explicitly opted into
// a non-production environment. "local" is included so the queue-signing
// gate and this helper share a single vocabulary.
func IsDevOrTestEnv(env string) bool {
	return contract.IsDevOrTestEnv(env)
}
