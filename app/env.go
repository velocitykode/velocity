// Package app - APP_ENV classification.
//
// Every "is this production?" check in the framework must route through the
// helpers defined here (or the contract package equivalents) so a single
// change to the environment-name vocabulary reaches every gate at once. The
// value classified is always the booted one (Config.Env, Services.Env):
// nothing here reads the process environment.
//
// The classification logic lives in the contract package (leaf, stdlib-only)
// so subsystems that cannot import app because of cycles (problem, auth,
// csrf, scheduler, grpc, ...) call contract.IsProductionEnv directly. This
// file re-exports the helpers for callers that already depend on app.
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
